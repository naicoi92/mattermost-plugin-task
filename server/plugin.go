package main

import (
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/mattermost/mattermost/server/public/pluginapi/cluster"
	"github.com/pkg/errors"

	"github.com/naicoi92/mattermost-plugin-task/server/command"
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

	p.kvstore = kvstore.NewKVStore(p.client)

	p.taskService = task.NewService(p.kvstore)

	p.commandClient = command.NewCommandHandler(p.client, p.taskService)

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
