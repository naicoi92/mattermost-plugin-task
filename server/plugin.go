package main

import (
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/mattermost/mattermost/server/public/pluginapi/cluster"
	"github.com/pkg/errors"

	"github.com/naicoi92/mattermost-plugin-task/server/command"
	"github.com/naicoi92/mattermost-plugin-task/server/notification"
	"github.com/naicoi92/mattermost-plugin-task/server/store/kvstore"
	"github.com/naicoi92/mattermost-plugin-task/server/task"
)

// Plugin implements the interface expected by the Mattermost server to communicate between the server and plugin processes.
type Plugin struct {
	plugin.MattermostPlugin

	// kvstore is the client used to read/write KV records for this plugin.
	kvstore kvstore.KVStore

	// client is the Mattermost server API client.
	client *pluginapi.Client

	// commandClient is the client used to register and execute slash commands.
	commandClient command.Command

	// taskService wraps the kvstore with task lifecycle business logic
	// (create/list/patch/delete cascade), shared by the REST API and slash
	// commands.
	taskService *task.Service

	// botUserID is the plugin bot's user id, used as the author of DM/card
	// posts. Ensured in OnActivate via EnsureBot.
	botUserID string

	// i18n is the server-side translation bundle used by notifications and
	// ephemeral responses.
	i18n *I18n

	// notifier sends task-event DMs from the bot in each recipient's locale.
	notifier *notification.Notifier

	// router is the HTTP router for handling API requests.
	router *mux.Router

	backgroundJob *cluster.Job

	// reminderJob is the cluster-scheduled job that fires due task reminders
	// once per minute on a single node.
	reminderJob *cluster.Job

	// configurationLock synchronizes access to the configuration.
	configurationLock sync.RWMutex

	// configuration is the active plugin configuration. Consult getConfiguration and
	// setConfiguration for usage.
	configuration *configuration
}

// OnActivate is invoked when the plugin is activated. If an error is returned, the plugin will be deactivated.
func (p *Plugin) OnActivate() error {
	p.client = pluginapi.NewClient(p.API, p.Driver)

	botID, err := p.ensureBot()
	if err != nil {
		return errors.Wrap(err, "failed to ensure bot")
	}
	p.botUserID = botID

	i18nBundle, err := NewI18n()
	if err != nil {
		return errors.Wrap(err, "failed to load i18n bundle")
	}
	p.i18n = i18nBundle
	p.notifier = notification.New(notifierAPI{api: p.API}, i18nBundle, p.botUserID)

	p.kvstore = kvstore.NewKVStore(p.client)

	p.taskService = task.NewService(p.kvstore)

	p.commandClient = command.NewCommandHandler(p.client, p.taskService, command.Options{
		Notifier:        commandNotifier{p.notifier},
		AssignNotifier:  commandAssignNotifier{p.notifier},
		CommentNotifier: commandCommentNotifier{p.notifier},
		Users:           userResolver{p.API},
	})

	p.router = p.initRouter()

	job, err := cluster.Schedule(
		p.API,
		"BackgroundJob",
		cluster.MakeWaitForRoundedInterval(1*time.Hour),
		p.runJob,
	)
	if err != nil {
		return errors.Wrap(err, "failed to schedule background job")
	}
	p.backgroundJob = job

	reminderJob, err := cluster.Schedule(
		p.API,
		"TaskReminderJob",
		cluster.MakeWaitForRoundedInterval(1*time.Minute),
		p.runReminderJob,
	)
	if err != nil {
		// OnActivate failed: OnDeactivate won't run, so clean up the already-
		// scheduled backgroundJob to avoid an orphaned job.
		if closeErr := p.backgroundJob.Close(); closeErr != nil {
			p.API.LogError("Failed to close background job during cleanup", "err", closeErr)
		}
		return errors.Wrap(err, "failed to schedule reminder job")
	}
	p.reminderJob = reminderJob

	return nil
}

// ensureBot creates or updates the plugin bot and returns its user id. The bot
// authors all DM/card posts so user notifications come from a single Task bot
// identity (see PLAN.md section 2.3 — EnsureBot in OnActivate, no manifest
// declaration).
func (p *Plugin) ensureBot() (string, error) {
	bot := &model.Bot{
		Username:    "task-bot",
		DisplayName: "Task",
		Description: "Bot created by the Task plugin for task notifications and DMs.",
	}
	ensured, err := p.client.Bot.EnsureBot(bot, pluginapi.ProfileImagePath(""))
	if err != nil {
		return "", err
	}
	return ensured, nil
}

// OnDeactivate is invoked when the plugin is deactivated.
func (p *Plugin) OnDeactivate() error {
	for _, job := range []*cluster.Job{p.backgroundJob, p.reminderJob} {
		if job != nil {
			if err := job.Close(); err != nil {
				p.API.LogError("Failed to close job", "err", err)
			}
		}
	}
	return nil
}

// This will execute the commands that were registered in the NewCommandHandler function.
func (p *Plugin) ExecuteCommand(c *plugin.Context, args *model.CommandArgs) (*model.CommandResponse, *model.AppError) {
	response, err := p.commandClient.Handle(args)
	if err != nil {
		return nil, model.NewAppError("ExecuteCommand", "plugin.command.execute_command.app_error", nil, err.Error(), http.StatusInternalServerError)
	}
	return response, nil
}

// See https://developers.mattermost.com/extend/plugins/server/reference/

// notifierAPI adapts plugin.API to notification.API. plugin.API returns
// *model.AppError (which implements error); the notifier works with plain
// errors, so this thin wrapper keeps the notification package decoupled from
// the plugin SDK while remaining fully testable.
type notifierAPI struct {
	api plugin.API
}

func (n notifierAPI) GetUser(userID string) (*model.User, error) {
	user, appErr := n.api.GetUser(userID)
	if appErr != nil {
		return nil, appErr
	}
	return user, nil
}

func (n notifierAPI) GetDirectChannel(userID1, userID2 string) (*model.Channel, error) {
	channel, appErr := n.api.GetDirectChannel(userID1, userID2)
	if appErr != nil {
		return nil, appErr
	}
	return channel, nil
}

func (n notifierAPI) CreatePost(post *model.Post) (*model.Post, error) {
	created, appErr := n.api.CreatePost(post)
	if appErr != nil {
		return nil, appErr
	}
	return created, nil
}

func (n notifierAPI) LogError(message string, keyValuePairs ...any) {
	n.api.LogError(message, keyValuePairs...)
}

// commandNotifier adapts the notification.Notifier to the command.TaskNotifier
// interface (TaskRef -> notification.TaskSummary). Kept nil-safe: when the
// underlying notifier is nil (e.g. activation race) all calls are no-ops.
type commandNotifier struct {
	n *notification.Notifier
}

func (c commandNotifier) NotifyCompleted(ref command.TaskRef, actorID, creatorID, assigneeID string) {
	if c.n == nil {
		return
	}
	c.n.NotifyCompleted(toSummary(ref), actorID, creatorID, assigneeID)
}

func (c commandNotifier) NotifyCancelled(ref command.TaskRef, actorID, creatorID, assigneeID string) {
	if c.n == nil {
		return
	}
	c.n.NotifyCancelled(toSummary(ref), actorID, creatorID, assigneeID)
}

func toSummary(ref command.TaskRef) notification.TaskSummary {
	return notification.TaskSummary{ID: ref.ID, Summary: ref.Summary}
}

// commandAssignNotifier adapts notification.Notifier.NotifyAssigned to the
// command.AssignNotifier interface. Nil-safe (no-op when notifier unset).
type commandAssignNotifier struct {
	n *notification.Notifier
}

func (c commandAssignNotifier) NotifyAssigned(assigneeID, creatorID string, ref command.AssignRef) {
	if c.n == nil {
		return
	}
	c.n.NotifyAssigned(assigneeID, creatorID, notification.TaskSummary{ID: ref.ID, Summary: ref.Summary})
}

// commandCommentNotifier adapts notification.Notifier.NotifyCommented to the
// command.CommentNotifier interface. Nil-safe (no-op when notifier unset).
type commandCommentNotifier struct {
	n *notification.Notifier
}

func (c commandCommentNotifier) NotifyCommented(ref command.TaskRef, actorID, creatorID, assigneeID string) {
	if c.n == nil {
		return
	}
	c.n.NotifyCommented(notification.TaskSummary{ID: ref.ID, Summary: ref.Summary}, actorID, creatorID, assigneeID)
}

// userResolver adapts plugin.API.GetUserByUsername to command.UserResolver,
// returning the user id ("" when not found / on error).
type userResolver struct {
	api plugin.API
}

func (u userResolver) UserIDByUsername(username string) string {
	user, appErr := u.api.GetUserByUsername(username)
	if appErr != nil || user == nil {
		return ""
	}
	return user.Id
}

// channelMembershipChecker adapts plugin.API to permission.ChannelMembershipChecker.
// It backs the view/comment permission rules for channel-scoped tasks. A nil api
// reports "not a member" for every check.
type channelMembershipChecker struct {
	api plugin.API
}

// IsChannelMember reports whether userID is any member of channelID.
func (c channelMembershipChecker) IsChannelMember(userID, channelID string) bool {
	if c.api == nil {
		return false
	}
	member, appErr := c.api.GetChannelMember(channelID, userID)
	if appErr != nil {
		return false
	}
	return member != nil
}

// IsChannelAdmin reports whether userID is a channel admin of channelID.
func (c channelMembershipChecker) IsChannelAdmin(userID, channelID string) bool {
	if c.api == nil {
		return false
	}
	member, appErr := c.api.GetChannelMember(channelID, userID)
	if appErr != nil || member == nil {
		return false
	}
	// Channel admins carry the "channel_admin" role in the member's role list.
	return slices.Contains(member.GetRoles(), "channel_admin")
}
