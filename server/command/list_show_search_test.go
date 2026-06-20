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

// fakeQuickListOpener records the last OpenQuickList call (#97).
type fakeQuickListOpener struct {
	triggerID string
	userID    string
	scope     string
	channelID string
	status    string
	due       string
	called    bool
	opened    bool
}

func (f *fakeQuickListOpener) OpenQuickList(triggerID, userID, scope, channelID, status, due string) bool {
	f.called = true
	f.triggerID = triggerID
	f.userID = userID
	f.scope = scope
	f.channelID = channelID
	f.status = status
	f.due = due
	return f.opened
}

// fakeTaskDetailOpener records the last OpenTaskDetail call (#97).
type fakeTaskDetailOpener struct {
	triggerID string
	taskID    string
	called    bool
	opened    bool
}

func (f *fakeTaskDetailOpener) OpenTaskDetail(triggerID, taskID string) bool {
	f.called = true
	f.triggerID = triggerID
	f.taskID = taskID
	return f.opened
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

// /task new mirrors the desktop "New Task" button: it opens the dialog even
// with NO summary (blank dialog). This is the primary create entry point on
// mobile (#107), where there is no header button.
func TestHandleNew_OpensBlankDialogWithoutSummary(t *testing.T) {
	svc := &fakeStatusService{createTask: &taskmodel.Task{ID: "t1"}}
	h := newTestHandler(svc)
	opener := &fakeNewTaskOpener{opened: true}
	h.newTaskOpener = opener

	resp, err := h.Handle(argsWithTrigger("u1", "/task new", "trig-1"))
	require.NoError(t, err)

	assert.True(t, opener.called, "opener should be invoked")
	assert.Equal(t, "", opener.prefillSummary, "dialog opens blank with no summary")
	assert.Equal(t, "ch1", opener.channelID, "channel scope passed through")
	assert.Empty(t, svc.createInput.Summary, "task is NOT created — the dialog is the form")
	assert.Empty(t, resp.Text, "no ephemeral text — the dialog is the feedback")
}

// /task new "<summary>" pre-fills the dialog, like the button opened from a
// message (#16). It still does NOT create the task immediately.
func TestHandleNew_PrefillsDialogWithSummary(t *testing.T) {
	svc := &fakeStatusService{createTask: &taskmodel.Task{ID: "t1"}}
	h := newTestHandler(svc)
	opener := &fakeNewTaskOpener{opened: true}
	h.newTaskOpener = opener

	_, err := h.Handle(argsWithTrigger("u1", "/task new \"fix the bug\"", "trig-1"))
	require.NoError(t, err)

	assert.Equal(t, "fix the bug", opener.prefillSummary, "dialog is prefilled")
	assert.Empty(t, svc.createInput.Summary, "task is NOT created until the dialog submits")
}

// When the opener fails, /task new does NOT fall back to immediate create
// (an empty task is meaningless); it surfaces an actionable hint instead.
func TestHandleNew_HintsInsteadOfFallingBackWhenOpenerFails(t *testing.T) {
	svc := &fakeStatusService{createTask: &taskmodel.Task{ID: "t1"}}
	h := newTestHandler(svc)
	h.newTaskOpener = &fakeNewTaskOpener{opened: false} // opener fails

	resp, err := h.Handle(argsWithTrigger("u1", "/task new \"x\"", "trig-1"))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "/task add", "should point the user to /task add")
	assert.Empty(t, svc.createInput.Summary, "must NOT create an immediate task")
}

// Without an opener (or no trigger id), /task new cannot open a dialog and
// must NOT create an empty task. It returns an actionable hint.
func TestHandleNew_HintsWhenNoOpenerOrTrigger(t *testing.T) {
	svc := &fakeStatusService{createTask: &taskmodel.Task{ID: "t1"}}
	h := newTestHandler(svc) // newTaskOpener is nil

	resp, err := h.Handle(argsWithTrigger("u1", "/task new", "trig-1"))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "/task add", "should point the user to /task add")
	assert.Empty(t, svc.createInput.Summary, "must NOT create an immediate task")
}

// With an opener wired but no trigger id (e.g. an API-driven call with no
// interactive context), /task new cannot open a dialog either — it surfaces the
// same /task add hint instead of creating an empty task. Covers the TriggerId
// half of the `newTaskOpener != nil && args.TriggerId != ""` guard.
func TestHandleNew_HintsWhenTriggerIDEmpty(t *testing.T) {
	svc := &fakeStatusService{createTask: &taskmodel.Task{ID: "t1"}}
	h := newTestHandler(svc)
	h.newTaskOpener = &fakeNewTaskOpener{opened: true} // opener available

	resp, err := h.Handle(argsWithTrigger("u1", "/task new", ""))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "/task add", "should point the user to /task add")
	assert.Empty(t, svc.createInput.Summary, "must NOT create an immediate task")
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

// When a trigger id and a Quick List opener that succeeds are present,
// /task list opens the Interactive Dialog instead of returning the text list
// (#97). Scope/status/due filters pass through to the opener.
func TestHandleList_OpensDialogWhenTriggerAndOpener(t *testing.T) {
	svc := &fakeStatusService{listResult: []taskmodel.Task{
		{ID: "t1", Summary: "first", Status: taskmodel.StatusTodo},
	}}
	h := newTestHandler(svc)
	opener := &fakeQuickListOpener{opened: true}
	h.quickListOpener = opener

	resp, err := h.Handle(argsWithTrigger("u1", "/task list mine done overdue", "trig-1"))
	require.NoError(t, err)

	assert.True(t, opener.called, "opener invoked when a trigger id is present")
	assert.Equal(t, "trig-1", opener.triggerID)
	assert.Equal(t, "u1", opener.userID)
	assert.Equal(t, "mine", opener.scope, "scope parsed and passed")
	assert.Equal(t, "done", opener.status, "status filter passed")
	assert.Equal(t, "overdue", opener.due, "due filter passed")
	assert.Empty(t, resp.Text, "no ephemeral text — the dialog is the feedback")
	assert.Equal(t, task.Scope(""), svc.listQuery.Scope, "List NOT called when the dialog opens")
}

// /task list channel passes the context channel id through to the opener.
func TestHandleList_ChannelScopePassesChannelID(t *testing.T) {
	svc := &fakeStatusService{}
	h := newTestHandler(svc)
	opener := &fakeQuickListOpener{opened: true}
	h.quickListOpener = opener

	_, err := h.Handle(argsWithTrigger("u1", "/task list channel", "trig-1"))
	require.NoError(t, err)
	assert.Equal(t, "channel", opener.scope)
	assert.Equal(t, "ch1", opener.channelID, "context channel id passed for channel scope")
}

// When the opener reports failure, /task list falls back to the text list.
func TestHandleList_FallsBackToTextWhenOpenerFails(t *testing.T) {
	svc := &fakeStatusService{listResult: []taskmodel.Task{
		{ID: "t1", Summary: "first", Status: taskmodel.StatusTodo},
	}}
	h := newTestHandler(svc)
	h.quickListOpener = &fakeQuickListOpener{opened: false} // opener fails

	resp, err := h.Handle(argsWithTrigger("u1", "/task list mine", "trig-1"))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "first", "fallback returns the text list")
}

// Without an opener wired, /task list always returns the text list even with a
// trigger id.
func TestHandleList_ReturnsTextWithoutOpener(t *testing.T) {
	svc := &fakeStatusService{listResult: []taskmodel.Task{
		{ID: "t1", Summary: "first", Status: taskmodel.StatusTodo},
	}}
	h := newTestHandler(svc) // quickListOpener is nil

	resp, err := h.Handle(argsWithTrigger("u1", "/task list mine", "trig-1"))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "first")
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

// When a trigger id and a Task Detail opener that succeeds are present,
// /task show opens the Interactive Dialog instead of returning the text card
// (#97). The task id passes through to the opener.
func TestHandleShow_OpensDialogWhenTriggerAndOpener(t *testing.T) {
	svc := &fakeStatusService{getResult: &taskmodel.Task{ID: "t1", Summary: "Review"}}
	h := newTestHandler(svc)
	opener := &fakeTaskDetailOpener{opened: true}
	h.taskDetailOpener = opener

	resp, err := h.Handle(argsWithTrigger("u1", "/task show t1", "trig-1"))
	require.NoError(t, err)

	assert.True(t, opener.called, "opener invoked when a trigger id is present")
	assert.Equal(t, "trig-1", opener.triggerID)
	assert.Equal(t, "t1", opener.taskID, "task id passed to the opener")
	assert.Empty(t, resp.Text, "no ephemeral text — the dialog is the feedback")
}

// When the opener reports failure, /task show falls back to the text card.
func TestHandleShow_FallsBackToTextWhenOpenerFails(t *testing.T) {
	svc := &fakeStatusService{getResult: &taskmodel.Task{ID: "t1", Summary: "Review"}}
	h := newTestHandler(svc)
	h.taskDetailOpener = &fakeTaskDetailOpener{opened: false} // opener fails

	resp, err := h.Handle(argsWithTrigger("u1", "/task show t1", "trig-1"))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Review", "fallback returns the text card")
}

// Without an opener wired, /task show always returns the text card even with a
// trigger id.
func TestHandleShow_ReturnsTextWithoutOpener(t *testing.T) {
	svc := &fakeStatusService{getResult: &taskmodel.Task{ID: "t1", Summary: "Review"}}
	h := newTestHandler(svc) // taskDetailOpener is nil

	resp, err := h.Handle(argsWithTrigger("u1", "/task show t1", "trig-1"))
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Review")
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
