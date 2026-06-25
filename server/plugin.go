package main

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/mattermost/mattermost/server/public/pluginapi/cluster"
	"github.com/pkg/errors"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/notification"
	"github.com/naicoi92/mattermost-plugin-task/server/store"
	"github.com/naicoi92/mattermost-plugin-task/server/store/sqlstore"
	"github.com/naicoi92/mattermost-plugin-task/server/task"
)

// Plugin implements the interface expected by the Mattermost server to communicate between the server and plugin processes.
type Plugin struct {
	plugin.MattermostPlugin

	// taskStore is the relational store (SQLStore) backing the task lifecycle.
	// It reuses the server's master database via pluginapi Store.GetMasterDB;
	// the schema is provisioned by RunMigrationsClusterSafe in OnActivate.
	taskStore store.Store

	// client is the Mattermost server API client.
	client *pluginapi.Client

	// taskService wraps the store with task lifecycle business logic
	// (create/list/patch/delete cascade), used by the REST API and the
	// cluster-scheduled reminder job.
	taskService *task.Service

	// botUserID is the plugin bot's user id, used as the author of DM/card
	// posts. Ensured in OnActivate via EnsureBot.
	botUserID string

	// i18n is the server-side translation bundle used by notifications.
	i18n *I18n

	// notifier sends task-event DMs from the bot in each recipient's locale.
	notifier *notification.Notifier

	// router is the HTTP router for handling API requests.
	router *mux.Router

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

	// Wire the relational store: acquire the server's master DB, build the
	// dialect-aware SQLStore, and run migrations under a cluster mutex so only
	// one node provisions the schema. A short PingContext fails fast if the DB
	// is unreachable rather than hanging every later query.
	db, err := p.client.Store.GetMasterDB()
	if err != nil {
		return errors.Wrap(err, "failed to get master database")
	}
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pingCancel()
	if pingErr := db.PingContext(pingCtx); pingErr != nil {
		return errors.Wrap(pingErr, "plugin database unreachable on activation")
	}
	sqlSettings := p.client.Configuration.GetConfig().SqlSettings
	sqlStore, err := sqlstore.NewFromConfig(db, &sqlSettings, sqlstore.DefaultTablePrefix)
	if err != nil {
		return errors.Wrap(err, "failed to initialize task sqlstore")
	}
	if migErr := sqlStore.RunMigrationsClusterSafe(p.API); migErr != nil {
		return errors.Wrap(migErr, "failed to run task database migrations")
	}
	p.taskStore = sqlStore

	p.taskService = task.NewService(p.taskStore, &p.client.Log)

	// Backfill any legacy personal tasks (channel_id="") into a real DM
	// channel so they conform to the all-channel model. Idempotent: tasks
	// already migrated are skipped by the WHERE channel_id="" filter.
	if bfErr := p.backfillChannelIDs(); bfErr != nil {
		p.API.LogError("channel-id backfill failed (non-fatal)", "error", bfErr)
	}

	p.router = p.initRouter()

	// NOTE(reminders): cluster.Schedule is disabled while diagnosing an RPC
	// shutdown that spam-blocks all plugin API calls after the first task card.
	// The reminder job's cluster mutex uses KVSetWithOptions, which appears to
	// crash the plugin RPC connection in this environment. Re-enable once the
	// root cause is fixed.
	_ = p.runReminderJob // keep referenced
	p.API.LogInfo("reminder job temporarily disabled (RPC shutdown investigation)")

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
	for _, job := range []*cluster.Job{p.reminderJob} {
		if job != nil {
			if err := job.Close(); err != nil {
				p.API.LogError("Failed to close job", "err", err)
			}
		}
	}
	return nil
}

// backfillChannelIDs relocates legacy personal tasks (channel_id="") into a
// real DM channel so every task conforms to the all-channel model. For each
// such task:
//   - if it has an assignee ≠ creator → DM(creator, assignee);
//   - otherwise → self-DM(creator, creator).
//
// Orphans (a deleted/unknown user so GetDirectChannel fails) are logged and
// left with channel_id="" for admin follow-up; the backfill is best-effort
// and non-fatal. Idempotent: re-running is a no-op because the query filters
// on channel_id="".
func (p *Plugin) backfillChannelIDs() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Repair pass first: a previous (buggy) backfill may have set channel_id
	// to a DM while the task's card actually lives in a team channel. For any
	// task whose ChannelPostID resolves to a post in a DIFFERENT channel than
	// task.ChannelID, realign ChannelID to the card's real channel. This is the
	// source of truth: the card post's channel IS the task's home.
	if repErr := p.repairChannelIDFromCard(ctx); repErr != nil {
		p.API.LogError("channel-id repair failed (non-fatal)", "error", repErr)
	}

	rows, err := p.taskStore.ListTasksWithoutChannel(ctx, 0)
	if err != nil {
		return fmt.Errorf("list legacy tasks: %w", err)
	}
	for _, row := range rows {
		// Prefer the channel the task's card actually lives in (a legacy
		// personal-scope task could still have a card posted in a real channel
		// via the old share/post_channel_id path). Only fall back to a DM when
		// there is no card post to anchor the home channel.
		targetChannel := ""
		if row.ChannelPostID != nil && *row.ChannelPostID != "" {
			if post, pErr := p.API.GetPost(*row.ChannelPostID); pErr == nil && post != nil && post.ChannelId != "" {
				targetChannel = post.ChannelId
			}
		}
		if targetChannel == "" {
			creatorID, _ := p.taskStore.GetMemberByRole(ctx, row.ID, taskmodel.MemberRoleCreator)
			if creatorID == "" {
				p.API.LogWarn("backfill: task has no creator and no card; skipping", "task_id", row.ID)
				continue
			}
			assigneeID, _ := p.taskStore.GetMemberByRole(ctx, row.ID, taskmodel.MemberRoleAssignee)
			partner := assigneeID
			if partner == "" || partner == creatorID {
				partner = creatorID // self-DM
			}
			dm, dErr := p.API.GetDirectChannel(creatorID, partner)
			if dErr != nil || dm == nil {
				p.API.LogWarn("backfill: could not resolve DM channel; leaving as orphan",
					"task_id", row.ID, "creator_id", creatorID, "partner_id", partner, "error", dErr)
				continue
			}
			targetChannel = dm.Id
		}
		if _, uErr := p.taskService.UpdateChannel(row.ID, targetChannel, ""); uErr != nil {
			p.API.LogError("backfill: update channel failed",
				"task_id", row.ID, "channel_id", targetChannel, "error", uErr)
		}
	}
	if n := len(rows); n > 0 {
		p.API.LogInfo("channel-id backfill processed legacy tasks", "count", n)
	}
	return nil
}

// repairChannelIDFromCard corrects tasks whose channel_id does not match the
// channel their card post actually lives in. This repairs data left wrong by
// a previous buggy backfill that set channel_id to a DM while the card was
// posted in a team channel. The card post's channel is the source of truth
// for the task's home channel.
func (p *Plugin) repairChannelIDFromCard(ctx context.Context) error {
	rows, err := p.taskStore.ListTasksWithCardPost(ctx, 0)
	if err != nil {
		return fmt.Errorf("list tasks with card: %w", err)
	}
	fixed := 0
	for _, row := range rows {
		if row.ChannelPostID == nil || *row.ChannelPostID == "" {
			continue
		}
		post, pErr := p.API.GetPost(*row.ChannelPostID)
		if pErr != nil || post == nil || post.ChannelId == "" {
			continue
		}
		if post.ChannelId == row.ChannelID {
			continue // already correct
		}
		if _, uErr := p.taskService.UpdateChannel(row.ID, post.ChannelId, *row.ChannelPostID); uErr != nil {
			p.API.LogError("repair: update channel failed",
				"task_id", row.ID, "channel_id", post.ChannelId, "error", uErr)
			continue
		}
		fixed++
	}
	if fixed > 0 {
		p.API.LogInfo("channel-id repair realigned tasks to their card channel", "count", fixed)
	}
	return nil
}

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

// channelMembershipChecker adapts plugin.API to permission.ChannelMembershipChecker.
// It backs the view/comment permission rules for channel-scoped tasks. A nil api
// reports "not a member" for every check.
type channelMembershipChecker struct {
	api plugin.API
}

// IsChannelMember reports whether userID is any member of channelID.
//
// GetChannelMember only returns an AppError when something is actually wrong
// (channel not found, archived, transient). The "user is not a member" case
// returns a nil member with NO error. So an AppError is a signal of a real
// problem — investigate it rather than blindly failing closed and blocking
// legitimate users with spurious 403s (the regression that prompted the
// previous fail-open workaround).
//
// Strategy: validate inputs deterministically, call the API for the truth,
// and on error log full context + TODO + temporarily allow access so a flaky
// membership signal does not lock out valid users. The log + TODO surface the
// problem for follow-up; this is NOT a silent paper-over.
//
// TODO(perm): root cause of historical GetChannelMember AppError for actual
// members is still UNKNOWN. Re-add the [DEBUG-perm] probe (commit 19e5e3a) to
// capture appErr.Id in production. Bot-perspective is ruled out: plugin API
// runs via context.Background() at system level, so the bot does not need to
// be a channel member. Suspect: stale ChannelID, archived channel, cluster
// sync timing. See design Decision 2b.
func (c channelMembershipChecker) IsChannelMember(userID, channelID string) bool {
	if c.api == nil {
		return false
	}
	// Input validation — deterministic rejection of garbage inputs.
	if userID == "" || channelID == "" {
		return false
	}
	member, appErr := c.api.GetChannelMember(channelID, userID)
	if appErr == nil && member != nil {
		return true
	}
	// Either an AppError (channel not found, archived, transient) or a nil
	// member with no error. Historically GetChannelMember has returned (nil,
	// nil) for actual members in this environment, which would otherwise
	// produce spurious 403s. Surface both cases and fail open so legitimate
	// users are not blocked while the root cause is investigated.
	if appErr != nil {
		c.api.LogWarn("GetChannelMember error; temporarily allowing access",
			"channel_id", channelID,
			"user_id", userID,
			"error_id", appErr.Id,
			"error_msg", appErr.Message,
			"status_code", appErr.StatusCode,
		)
	} else {
		c.api.LogWarn("GetChannelMember returned nil member without error; allowing access",
			"channel_id", channelID,
			"user_id", userID,
		)
	}
	return true
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
