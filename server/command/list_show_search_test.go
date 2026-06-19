package command

import (
	"testing"

	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/task"
)

// newTestHandler builds a Handler with a fake service and a no-op client for
// the list/show/search/add command tests.
func newTestHandler(svc *fakeStatusService) *Handler {
	return &Handler{
		client:      &pluginapi.Client{},
		taskService: svc,
	}
}

func args(userID, command string) *mmmodel.CommandArgs {
	return &mmmodel.CommandArgs{UserId: userID, Command: command, ChannelId: "ch1"}
}

func TestHandleAdd_RequiresSummary(t *testing.T) {
	h := newTestHandler(&fakeStatusService{})
	resp, err := h.Handle(args("u1", "/task add"))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "summary is required")
}

func TestHandleAdd_CreatesTaskInChannel(t *testing.T) {
	svc := &fakeStatusService{createTask: &taskmodel.Task{ID: "t1", Summary: "buy milk"}}
	h := newTestHandler(svc)
	resp, err := h.Handle(args("u1", "/task add \"buy milk\""))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Task created")
	assert.Equal(t, "buy milk", svc.createInput.Summary)
	assert.Equal(t, "u1", svc.createInput.CreatorID)
	assert.Equal(t, "ch1", svc.createInput.ChannelID, "default scope is the current channel")
}

func TestHandleAdd_StripsSurroundingQuotes(t *testing.T) {
	svc := &fakeStatusService{createTask: &taskmodel.Task{ID: "t1"}}
	h := newTestHandler(svc)
	_, err := h.Handle(args("u1", "/task add 'write docs'"))
	require.NoError(t, err)
	assert.Equal(t, "write docs", svc.createInput.Summary)
}

func TestHandleAdd_DefaultsToChannelScope(t *testing.T) {
	// isBotDM needs a resolvable DM channel via pluginapi; without a mock it
	// would panic on a nil client, so we leave botUserID empty (isBotDM returns
	// false early) and confirm the channel-scope default. The personal-scope
	// branch (bot DM) is exercised in the integration/E2E path.
	svc := &fakeStatusService{createTask: &taskmodel.Task{ID: "t1"}}
	h := newTestHandler(svc) // botUserID empty → isBotDM short-circuits
	_, err := h.Handle(args("u1", "/task add personal task"))
	require.NoError(t, err)
	assert.Equal(t, "ch1", svc.createInput.ChannelID, "defaults to the current channel scope")
}

// fakeNewTaskOpener records the last OpenNewTask call and reports whether the
// dialog was "opened". Returns the configured opened flag so a test can force
// the opener to fail (exercising the immediate-create fallback).
type fakeNewTaskOpener struct {
	triggerID      string
	prefillSummary string
	channelID      string
	called         bool
	opened         bool
}

func (f *fakeNewTaskOpener) OpenNewTask(triggerID, prefillSummary, channelID string) bool {
	f.called = true
	f.triggerID = triggerID
	f.prefillSummary = prefillSummary
	f.channelID = channelID
	return f.opened
}

// argsWithTrigger is args() plus a TriggerId so the dialog path is reachable.
func argsWithTrigger(userID, command, triggerID string) *mmmodel.CommandArgs {
	a := args(userID, command)
	a.TriggerId = triggerID
	return a
}

// When a trigger id and an opener that succeeds are present, /task add opens
// the New Task dialog prefilled with the summary instead of creating the task
// immediately (#95).
func TestHandleAdd_OpensDialogWhenTriggerAndOpener(t *testing.T) {
	svc := &fakeStatusService{createTask: &taskmodel.Task{ID: "t1"}}
	h := newTestHandler(svc)
	opener := &fakeNewTaskOpener{opened: true}
	h.newTaskOpener = opener

	resp, err := h.Handle(argsWithTrigger("u1", "/task add \"buy milk\"", "trig-1"))
	require.NoError(t, err)

	assert.True(t, opener.called, "opener should be invoked when a trigger id is present")
	assert.Equal(t, "trig-1", opener.triggerID)
	assert.Equal(t, "buy milk", opener.prefillSummary, "dialog is prefilled with the summary")
	assert.Equal(t, "ch1", opener.channelID, "channel scope passed through")
	assert.Empty(t, svc.createInput.Summary, "task is NOT created until the dialog submits")
	assert.Empty(t, resp.Text, "no ephemeral text needed — the dialog is the feedback")
}

// When the opener reports failure (e.g. OpenInteractiveDialog errored), the
// command falls back to creating the task immediately so the flow never
// dead-ends (#95).
func TestHandleAdd_FallsBackToCreateWhenOpenerFails(t *testing.T) {
	svc := &fakeStatusService{createTask: &taskmodel.Task{ID: "t1", Summary: "buy milk"}}
	h := newTestHandler(svc)
	h.newTaskOpener = &fakeNewTaskOpener{opened: false} // opener fails

	resp, err := h.Handle(argsWithTrigger("u1", "/task add \"buy milk\"", "trig-1"))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Task created")
	assert.Equal(t, "buy milk", svc.createInput.Summary, "fallback created the task")
}

// Without an opener wired (bare handler, e.g. unit tests), /task add always
// creates immediately even when a trigger id is present.
func TestHandleAdd_CreatesImmediatelyWithoutOpener(t *testing.T) {
	svc := &fakeStatusService{createTask: &taskmodel.Task{ID: "t1", Summary: "buy milk"}}
	h := newTestHandler(svc) // newTaskOpener is nil

	resp, err := h.Handle(argsWithTrigger("u1", "/task add \"buy milk\"", "trig-1"))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Task created")
	assert.Equal(t, "buy milk", svc.createInput.Summary)
}

func TestHandleList_EmptyReturnsNoTasks(t *testing.T) {
	h := newTestHandler(&fakeStatusService{})
	resp, err := h.Handle(args("u1", "/task list mine"))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "No tasks found")
}

func TestHandleList_ReturnsTasks(t *testing.T) {
	svc := &fakeStatusService{listResult: []taskmodel.Task{
		{ID: "t1", Summary: "first", Status: taskmodel.StatusTodo},
		{ID: "t2", Summary: "second", Status: taskmodel.StatusDone},
	}}
	h := newTestHandler(svc)
	resp, err := h.Handle(args("u1", "/task list"))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "first")
	assert.Contains(t, resp.Text, "second")
	assert.Equal(t, task.ScopeMine, svc.listQuery.Scope)
}

func TestHandleList_ParsesScopeAndStatusFilters(t *testing.T) {
	svc := &fakeStatusService{listResult: []taskmodel.Task{}}
	h := newTestHandler(svc)
	_, err := h.Handle(args("u1", "/task list channel done"))
	require.NoError(t, err)
	assert.Equal(t, task.ScopeChannel, svc.listQuery.Scope)
	assert.Equal(t, "ch1", svc.listQuery.ChannelID)
	assert.Equal(t, taskmodel.StatusDone, svc.listQuery.Status)
}

func TestHandleShow_RequiresID(t *testing.T) {
	h := newTestHandler(&fakeStatusService{})
	resp, err := h.Handle(args("u1", "/task show"))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Usage")
}

func TestHandleShow_TaskNotFound(t *testing.T) {
	svc := &fakeStatusService{getResult: nil}
	h := newTestHandler(svc)
	resp, err := h.Handle(args("u1", "/task show nope"))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "not found")
}

func TestHandleShow_RendersDetail(t *testing.T) {
	due := int64(1700000000000)
	svc := &fakeStatusService{getResult: &taskmodel.Task{
		ID: "t1", Summary: "Review", Status: taskmodel.StatusInProgress,
		Description: "needs review", AssigneeID: "u2", Due: &due,
	}}
	h := newTestHandler(svc)
	resp, err := h.Handle(args("u1", "/task show t1"))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Review")
	assert.Contains(t, resp.Text, "in_progress")
	assert.Contains(t, resp.Text, "needs review")
	assert.Contains(t, resp.Text, "u2")
}

func TestHandleSearch_RequiresKeyword(t *testing.T) {
	h := newTestHandler(&fakeStatusService{})
	resp, err := h.Handle(args("u1", "/task search"))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Usage")
}

func TestHandleSearch_NoMatches(t *testing.T) {
	svc := &fakeStatusService{searchResult: nil}
	h := newTestHandler(svc)
	resp, err := h.Handle(args("u1", "/task search nothing"))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "No tasks matching")
	assert.Equal(t, "nothing", svc.searchKeyword)
}

func TestHandleSearch_ReturnsMatches(t *testing.T) {
	svc := &fakeStatusService{searchResult: []taskmodel.Task{
		{ID: "t1", Summary: "buy milk", Status: taskmodel.StatusTodo},
	}}
	h := newTestHandler(svc)
	resp, err := h.Handle(args("u1", "/task search milk"))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "buy milk")
}

func TestStatusGlyph(t *testing.T) {
	assert.Equal(t, "✅", statusGlyph(taskmodel.StatusDone))
	assert.Equal(t, "🚫", statusGlyph(taskmodel.StatusCancelled))
	assert.Equal(t, "🔄", statusGlyph(taskmodel.StatusInProgress))
	assert.Equal(t, "◻️", statusGlyph(taskmodel.StatusTodo))
}

func TestFormatTaskDetail(t *testing.T) {
	t.Run("minimal task", func(t *testing.T) {
		s := formatTaskDetail(&taskmodel.Task{ID: "t1", Summary: "x", Status: taskmodel.StatusTodo})
		assert.Contains(t, s, "x")
		assert.Contains(t, s, "todo")
	})
	t.Run("full task", func(t *testing.T) {
		due := int64(0)
		s := formatTaskDetail(&taskmodel.Task{
			ID: "t1", Summary: "x", Status: taskmodel.StatusTodo,
			Description: "desc", AssigneeID: "u2", Due: &due,
		})
		assert.Contains(t, s, "desc")
		assert.Contains(t, s, "u2")
	})
}
