package command

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/task"
)

// mockAnything is a short alias for mock.Anything used in permissive stubs.
func mockAnything() any { return mock.Anything }

type env struct {
	client *pluginapi.Client
	api    *plugintest.API
}

func setupTest() *env {
	api := &plugintest.API{}
	driver := &plugintest.Driver{}
	client := pluginapi.NewClient(api, driver)

	return &env{
		client: client,
		api:    api,
	}
}

// fakeStatusService is a StatusService fake for command tests.
type fakeStatusService struct {
	lastID     string
	lastStatus string
	result     *taskmodel.Task
	err        error
}

func (f *fakeStatusService) SetStatus(id, status string) (*taskmodel.Task, error) {
	f.lastID = id
	f.lastStatus = status
	return f.result, f.err
}

func TestHelloCommand(t *testing.T) {
	assert := assert.New(t)
	env := setupTest()

	env.api.On("RegisterCommand", &model.Command{
		Trigger:          helloCommandTrigger,
		AutoComplete:     true,
		AutoCompleteDesc: "Say hello to someone",
		AutoCompleteHint: "[@username]",
		AutocompleteData: model.NewAutocompleteData("hello", "[@username]", "Username to say hello to"),
	}).Return(nil)
	// /task registration also happens; stub it permissively.
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	cmdHandler := NewCommandHandler(env.client, &fakeStatusService{})

	args := &model.CommandArgs{
		Command: "/hello world",
	}
	response, err := cmdHandler.Handle(args)
	assert.Nil(err)
	assert.Equal("Hello, world", response.Text)
}

func TestTaskStatus_Command(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{result: &taskmodel.Task{ID: "T1", Summary: "Review PR"}}
	handler := NewCommandHandler(env.client, svc)

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task status T1 done"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "now **done**")
	assert.Equal(t, "T1", svc.lastID)
	assert.Equal(t, taskmodel.StatusDone, svc.lastStatus)
}

func TestTaskDone_Shortcut(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{result: &taskmodel.Task{ID: "T1", Summary: "x"}}
	handler := NewCommandHandler(env.client, svc)

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task done T1"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "done")
	assert.Equal(t, taskmodel.StatusDone, svc.lastStatus)
}

func TestTaskCancel_Shortcut(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{result: &taskmodel.Task{ID: "T1", Summary: "x"}}
	handler := NewCommandHandler(env.client, svc)

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task cancel T1"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "cancelled")
	assert.Equal(t, taskmodel.StatusCancelled, svc.lastStatus)
}

func TestTaskStatus_InvalidStatus(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	handler := NewCommandHandler(env.client, &fakeStatusService{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task status T1 bogus"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Invalid status")
}

func TestTaskStatus_NotFound(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{err: task.ErrNotFound}
	handler := NewCommandHandler(env.client, svc)

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task status T1 done"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "not found")
}

func TestTaskStatus_UsageErrors(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	handler := NewCommandHandler(env.client, &fakeStatusService{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task status"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Usage")

	resp, err = handler.Handle(&model.CommandArgs{Command: "/task done"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Usage")

	resp, err = handler.Handle(&model.CommandArgs{Command: "/task cancel"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Usage")
}

func TestTaskHelp(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	handler := NewCommandHandler(env.client, &fakeStatusService{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task help"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "/task status")
	assert.Contains(t, resp.Text, "/task done")
}

func TestTaskStatus_InternalError(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	// LogError is variadic (message + key/value...); match anything.
	env.api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	svc := &fakeStatusService{err: errors.New("boom")}
	handler := NewCommandHandler(env.client, svc)

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task status T1 done"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Failed to update")
}
