package main

import (
	"fmt"
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
	"github.com/naicoi92/mattermost-plugin-task/server/dialog"
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

	// router is the HTTP router for handling API requests.
	router *mux.Router

	backgroundJob *cluster.Job

	// configurationLock synchronizes access to the configuration.
	configurationLock sync.RWMutex

	// configuration is the active plugin configuration. Consult getConfiguration and
	// setConfiguration for usage.
	configuration *configuration
}

// OnActivate is invoked when the plugin is activated. If an error is returned, the plugin will be deactivated.
func (p *Plugin) OnActivate() error {
	p.client = pluginapi.NewClient(p.API, p.Driver)

	p.kvstore = kvstore.NewKVStore(p.client)

	p.taskService = task.NewService(p.kvstore)

	p.commandClient = command.NewCommandHandler(p.client, p.taskService, dialogOpener{p})

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

	return nil
}

// OnDeactivate is invoked when the plugin is deactivated.
func (p *Plugin) OnDeactivate() error {
	if p.backgroundJob != nil {
		if err := p.backgroundJob.Close(); err != nil {
			p.API.LogError("Failed to close background job", "err", err)
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

// dialogOpener adapts plugin.API.OpenInteractiveDialog to the command.DialogOpener
// interface. The plugin's site URL is needed to build the dialog submit callback
// URL; we resolve it lazily so activation order doesn't matter.
type dialogOpener struct {
	p *Plugin
}

func (d dialogOpener) OpenNewTaskDialog(triggerID, prefillSummary, channelID string) error {
	dialogDef := dialog.NewTaskDialog(prefillSummary, channelID)
	siteURL := ""
	if cfg := d.p.API.GetConfig(); cfg != nil && cfg.ServiceSettings.SiteURL != nil {
		siteURL = *cfg.ServiceSettings.SiteURL
	}
	request := model.OpenDialogRequest{
		TriggerId: triggerID,
		URL:       fmt.Sprintf("%s/plugins/%s/api/v1/dialogs/task/create", siteURL, manifestID),
		Dialog:    dialogDef,
	}
	if appErr := d.p.API.OpenInteractiveDialog(request); appErr != nil {
		return appErr
	}
	return nil
}

// manifestID is the plugin id from plugin.json, used to build callback URLs.
const manifestID = "com.mattermost.plugin-task"
