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

// fakeStatusService is a TaskService fake for command tests.
type fakeStatusService struct {
	lastID     string
	lastStatus string
	result     *taskmodel.Task
	err        error

	lastPatchID string
	lastPatch   task.PatchInput
	patchResult *taskmodel.Task
	patchErr    error

	reminderID      string
	reminderOffset  int64
	reminderCleared bool
	reminderResult  *taskmodel.Task
	reminderErr     error

	assignID     string
	assignUserID string
	assignResult *taskmodel.Task
	assignEvent  task.AssignEvent
	assignErr    error

	getResult *taskmodel.Task
	getErr    error

	subtaskParentID string
	subtaskCreator  string
	subtaskSummary  string
	subtaskResult   *taskmodel.Task
	subtaskErr      error

	commentTaskID string
	commentPostID string
	commentUserID string
	commentResult taskmodel.TaskComment
	commentEvent  task.CommentEvent
	commentErr    error

	listQuery  task.ListQuery
	listResult []*taskmodel.Task
	listErr    error

	searchKeyword string
	searchResult  []*taskmodel.Task
	searchErr     error

	createInput task.CreateInput
	createTask  *taskmodel.Task
	createErr   error
}

func (f *fakeStatusService) SetStatus(id, status string) (*taskmodel.Task, error) {
	f.lastID = id
	f.lastStatus = status
	return f.result, f.err
}

func (f *fakeStatusService) Patch(id string, in task.PatchInput) (*taskmodel.Task, error) {
	f.lastPatchID = id
	f.lastPatch = in
	return f.patchResult, f.patchErr
}

func (f *fakeStatusService) SetReminder(id string, offsetMS int64) (*taskmodel.Task, error) {
	f.reminderID = id
	f.reminderOffset = offsetMS
	f.reminderCleared = false
	return f.reminderResult, f.reminderErr
}

func (f *fakeStatusService) ClearReminder(id string) (*taskmodel.Task, error) {
	f.reminderID = id
	f.reminderCleared = true
	return f.reminderResult, f.reminderErr
}

func (f *fakeStatusService) Assign(id, newAssigneeID string) (*taskmodel.Task, task.AssignEvent, error) {
	f.assignID = id
	f.assignUserID = newAssigneeID
	return f.assignResult, f.assignEvent, f.assignErr
}

func (f *fakeStatusService) Get(id string) (*taskmodel.Task, error) {
	return f.getResult, f.getErr
}

func (f *fakeStatusService) CreateSubtask(parentID, creatorID, summary, assigneeID string, due *int64) (*taskmodel.Task, error) {
	f.subtaskParentID = parentID
	f.subtaskCreator = creatorID
	f.subtaskSummary = summary
	return f.subtaskResult, f.subtaskErr
}

func (f *fakeStatusService) LinkComment(taskID, postID, userID string) (taskmodel.TaskComment, task.CommentEvent, error) {
	f.commentTaskID = taskID
	f.commentPostID = postID
	f.commentUserID = userID
	return f.commentResult, f.commentEvent, f.commentErr
}

func (f *fakeStatusService) List(q task.ListQuery) ([]*taskmodel.Task, error) {
	f.listQuery = q
	return f.listResult, f.listErr
}

func (f *fakeStatusService) Search(keyword string, limit int) ([]*taskmodel.Task, error) {
	f.searchKeyword = keyword
	return f.searchResult, f.searchErr
}

func (f *fakeStatusService) Create(in task.CreateInput) (*taskmodel.Task, error) {
	f.createInput = in
	return f.createTask, f.createErr
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
	cmdHandler := NewCommandHandler(env.client, &fakeStatusService{}, Options{})

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
	svc := &fakeStatusService{result: &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "T1", Summary: "Review PR"}}}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task status T1 done"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "now **done**")
	assert.Equal(t, "T1", svc.lastID)
	assert.Equal(t, taskmodel.StatusDone, svc.lastStatus)
}

func TestTaskDone_Shortcut(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{result: &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "T1", Summary: "x"}}}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task done T1"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "done")
	assert.Equal(t, taskmodel.StatusDone, svc.lastStatus)
}

// Issue #22: /task done on a parent with open subtasks shows the clear,
// actionable blocking message (lists the open subtask).
func TestTaskDone_BlockedByOpenSubtask(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{err: task.ErrOpenSubtasks{Open: []string{"still open"}}}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task done P1"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "still open")
	assert.Contains(t, resp.Text, "cannot mark task done")
}

func TestTaskCancel_Shortcut(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{result: &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "T1", Summary: "x"}}}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task cancel T1"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "cancelled")
	assert.Equal(t, taskmodel.StatusCancelled, svc.lastStatus)
}

func TestTaskStatus_InvalidStatus(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	handler := NewCommandHandler(env.client, &fakeStatusService{}, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task status T1 bogus"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Invalid status")
}

func TestTaskStatus_NotFound(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{err: task.ErrNotFound}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task status T1 done"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "not found")
}

func TestTaskStatus_UsageErrors(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	handler := NewCommandHandler(env.client, &fakeStatusService{}, Options{})

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
	handler := NewCommandHandler(env.client, &fakeStatusService{}, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task help"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "/task status")
	assert.Contains(t, resp.Text, "/task done")
}

// /task subtask adds a subtask; summary may contain spaces.
func TestTaskSubtask_Command(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{
		getResult:     &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "P1", Summary: "parent"}, CreatorID: "u-me"},
		subtaskResult: &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "C1", Summary: "fix tests", ParentTaskID: "P1"}},
	}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task subtask P1 fix the tests", UserId: "u-me"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Subtask")
	assert.Equal(t, "P1", svc.subtaskParentID)
	assert.Equal(t, "u-me", svc.subtaskCreator)
	assert.Equal(t, "fix the tests", svc.subtaskSummary)
}

func TestTaskSubtask_RequiresModifyPermission(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	// Parent owned by u-other; actor u-stranger has no permission.
	svc := &fakeStatusService{
		getResult: &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "P1", Summary: "parent"}, CreatorID: "u-other"},
	}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task subtask P1 child", UserId: "u-stranger"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "permission")
}

func TestTaskSubtask_ParentNotFound(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{getResult: nil}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task subtask ghost child", UserId: "u-me"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "not found")
}

func TestTaskSubtask_Usage(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	handler := NewCommandHandler(env.client, &fakeStatusService{}, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task subtask P1", UserId: "u-me"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Usage")
}

// /task comment adds a comment; text may contain spaces.
func TestTaskComment_Command(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	// Comment-as-thread: the handler creates the reply post first (via the
	// plugin API), then links it. Stub CreatePost to return a post with an id.
	env.api.On("CreatePost", mockAnything()).Return(&model.Post{Id: "post-1"}, nil)
	svc := &fakeStatusService{
		getResult:     &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "T1", Summary: "task"}, CreatorID: "u-me"},
		commentResult: taskmodel.TaskComment{ID: "C1", PostID: "post-1"},
		commentEvent:  task.CommentEvent{TaskID: "T1", UserID: "u-me", CreatorID: "u-me"},
	}
	// CommentNotifier is wired to capture the call.
	notifier := &captureCommentNotifier{}
	handler := NewCommandHandler(env.client, svc, Options{CommentNotifier: notifier})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task comment T1 looks good to me", UserId: "u-me"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Comment added")
	assert.Equal(t, "T1", svc.commentTaskID)
	assert.Equal(t, "post-1", svc.commentPostID)
	assert.Equal(t, "u-me", svc.commentUserID)
	assert.Equal(t, "T1", notifier.taskID, "comment DM fired to participants")
}

func TestTaskComment_TaskNotFound(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{getResult: nil}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task comment ghost hi", UserId: "u-me"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "not found")
}

// Issue #24 (CodeRabbit): /task comment must enforce an authorization gate,
// mirroring the REST path. Without a channel-aware authorizer, the fallback is
// the personal-task co-owner rule (creator or assignee).
func TestTaskComment_RequiresPermission(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{
		getResult: &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "T1", Summary: "task"}, CreatorID: "u-owner"},
	}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task comment T1 hi", UserId: "u-stranger"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "permission")
	// Guard authorization order: AddComment must never be reached when denied.
	assert.Empty(t, svc.commentTaskID)
}

// When a CommentAuthorizer is wired, it gates commenting (covers channel members).
func TestTaskComment_UsesAuthorizer(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	env.api.On("CreatePost", mockAnything()).Return(&model.Post{Id: "post-1"}, nil)
	task := &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "T1", Summary: "task"}, CreatorID: "u-owner"}
	svc := &fakeStatusService{
		getResult:     task,
		commentResult: taskmodel.TaskComment{ID: "C1", PostID: "post-1"},
	}
	handler := NewCommandHandler(env.client, svc, Options{CommentAuthorizer: allowCommentAuthorizer{}})

	// A stranger is allowed by the stub authorizer.
	resp, err := handler.Handle(&model.CommandArgs{Command: "/task comment T1 hi", UserId: "u-stranger"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Comment added")
}

// allowCommentAuthorizer permits everyone; used to verify the authorizer path.
type allowCommentAuthorizer struct{}

func (allowCommentAuthorizer) CanComment(userID string, t *taskmodel.Task) bool { return true }

func TestTaskComment_Usage(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	handler := NewCommandHandler(env.client, &fakeStatusService{}, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task comment T1", UserId: "u-me"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Usage")
}

// captureCommentNotifier records the last NotifyCommented call.
type captureCommentNotifier struct {
	called bool
	taskID string
}

func (c *captureCommentNotifier) NotifyCommented(ref TaskRef, actorID, creatorID, assigneeID string) {
	c.called = true
	c.taskID = ref.ID
}

func TestTaskStatus_InternalError(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	// LogError is variadic (message + key/value...); match anything.
	env.api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	svc := &fakeStatusService{err: errors.New("boom")}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task status T1 done"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Failed to update")
}

func TestTaskEdit_Command(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	env.api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	svc := &fakeStatusService{patchResult: &taskmodel.Task{TaskRow: taskmodel.TaskRow{Summary: "new"}}}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task edit T1 summary=New title due=1700000000000"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "updated")

	require.Equal(t, "T1", svc.lastPatchID)
	assert.ElementsMatch(t, []string{"summary", "due"}, svc.lastPatch.UpdateFields)
	require.NotNil(t, svc.lastPatch.Summary)
	assert.Equal(t, "New title", *svc.lastPatch.Summary)
	require.NotNil(t, svc.lastPatch.Due)
	assert.Equal(t, int64(1700000000000), *svc.lastPatch.Due)
}

func TestTaskEdit_ClearDue(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{patchResult: &taskmodel.Task{TaskRow: taskmodel.TaskRow{Summary: "x"}}}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task edit T1 due=0"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "updated")
	assert.Nil(t, svc.lastPatch.Due)
}

func TestTaskEdit_DescriptionAlias(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{patchResult: &taskmodel.Task{TaskRow: taskmodel.TaskRow{Summary: "x"}}}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task edit T1 description=hello"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "updated")
	assert.Equal(t, []string{"description"}, svc.lastPatch.UpdateFields)
	require.NotNil(t, svc.lastPatch.Description)
	assert.Equal(t, "hello", *svc.lastPatch.Description)
}

func TestTaskEdit_BadDue(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	handler := NewCommandHandler(env.client, &fakeStatusService{}, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task edit T1 due=notanumber"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Could not parse")
}

func TestTaskEdit_NoFields(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	handler := NewCommandHandler(env.client, &fakeStatusService{}, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task edit T1"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Nothing to edit")
}

func TestTaskEdit_NotFound(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	env.api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	svc := &fakeStatusService{patchErr: task.ErrNotFound}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task edit T1 summary=x"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "not found")
}

func TestParseEditFields_UnknownKey(t *testing.T) {
	in, bad := parseEditFields([]string{"foo=bar"})
	assert.Equal(t, "foo=bar", bad)
	assert.Empty(t, in.UpdateFields)
}

func TestTaskRemind_SetOffset(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{reminderResult: &taskmodel.Task{TaskRow: taskmodel.TaskRow{Summary: "x"}}}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task remind T1 1h"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Reminder set")
	assert.Equal(t, "T1", svc.reminderID)
	assert.Equal(t, int64(60*60*1000), svc.reminderOffset)
	assert.False(t, svc.reminderCleared)
}

func TestTaskRemind_Off(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{reminderResult: &taskmodel.Task{TaskRow: taskmodel.TaskRow{Summary: "x"}}}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task remind T1 off"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "turned off")
	assert.True(t, svc.reminderCleared)
}

func TestTaskRemind_UnknownToken(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	handler := NewCommandHandler(env.client, &fakeStatusService{}, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task remind T1 soon"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Unknown reminder")
}

func TestTaskRemind_Usage(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	handler := NewCommandHandler(env.client, &fakeStatusService{}, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task remind T1"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Usage")
}

func TestTaskRemind_NeedsDue(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	env.api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	svc := &fakeStatusService{reminderErr: task.ErrReminderNeedsDue}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task remind T1 15m"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "no due date")
}

func TestTaskRemind_GenericErrorSanitized(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	// LogError is variadic; match any number of args.
	env.api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	svc := &fakeStatusService{reminderErr: errors.New("internal db explosion")}
	handler := NewCommandHandler(env.client, svc, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task remind T1 15m"})
	require.NoError(t, err)
	// The raw backend error must not leak to the user; only a safe message.
	assert.NotContains(t, resp.Text, "internal db explosion", "raw error must not leak")
	assert.Contains(t, resp.Text, "Failed")
}

func TestParseReminderOffset(t *testing.T) {
	cases := []struct {
		token string
		want  int64
		ok    bool
	}{
		{"15m", 15 * 60 * 1000, true},
		{"1h", 60 * 60 * 1000, true},
		{"1d", 24 * 60 * 60 * 1000, true},
		{"2h", 2 * 60 * 60 * 1000, true},
		{"off", 0, false},
		{"abc", 0, false},
		{"0m", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := parseReminderOffset(c.token)
		assert.Equal(t, c.ok, ok, "token %q ok mismatch", c.token)
		if ok {
			assert.Equal(t, c.want, got, "token %q offset", c.token)
		}
	}
}

// fakeNotifier records NotifyCompleted/NotifyCancelled calls.
type fakeNotifier struct {
	completed []recordedNotify
	cancelled []recordedNotify
	assigned  []recordedAssign
}

type recordedNotify struct {
	ref       TaskRef
	actorID   string
	creatorID string
	assignee  string
}

type recordedAssign struct {
	assigneeID string
	creatorID  string
	ref        AssignRef
}

func (f *fakeNotifier) NotifyCompleted(ref TaskRef, actorID, creatorID, assigneeID string) {
	f.completed = append(f.completed, recordedNotify{ref, actorID, creatorID, assigneeID})
}

func (f *fakeNotifier) NotifyAssigned(assigneeID, creatorID string, ref AssignRef) {
	f.assigned = append(f.assigned, recordedAssign{assigneeID, creatorID, ref})
}

func (f *fakeNotifier) NotifyCancelled(ref TaskRef, actorID, creatorID, assigneeID string) {
	f.cancelled = append(f.cancelled, recordedNotify{ref, actorID, creatorID, assigneeID})
}

func TestTaskStatus_DoneNotifies(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{result: &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "T1", Summary: "Ship"}, CreatorID: "creator", AssigneeID: "assignee"}}
	notif := &fakeNotifier{}
	handler := NewCommandHandler(env.client, svc, Options{Notifier: notif})

	_, err := handler.Handle(&model.CommandArgs{Command: "/task done T1", UserId: "actor"})
	require.NoError(t, err)
	require.Len(t, notif.completed, 1)
	assert.Equal(t, "Ship", notif.completed[0].ref.Summary)
	assert.Equal(t, "actor", notif.completed[0].actorID)
	assert.Empty(t, notif.cancelled)
}

func TestTaskStatus_CancelNotifies(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{result: &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "T1", Summary: "Drop"}, CreatorID: "creator", AssigneeID: "assignee"}}
	notif := &fakeNotifier{}
	handler := NewCommandHandler(env.client, svc, Options{Notifier: notif})

	_, err := handler.Handle(&model.CommandArgs{Command: "/task cancel T1", UserId: "actor"})
	require.NoError(t, err)
	require.Len(t, notif.cancelled, 1)
	assert.Empty(t, notif.completed)
}

func TestTaskStatus_TodoDoesNotNotify(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{result: &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "T1", Summary: "x"}}}
	notif := &fakeNotifier{}
	handler := NewCommandHandler(env.client, svc, Options{Notifier: notif})

	_, err := handler.Handle(&model.CommandArgs{Command: "/task status T1 todo"})
	require.NoError(t, err)
	assert.Empty(t, notif.completed)
	assert.Empty(t, notif.cancelled)
}

// fakeUsers is a UserResolver fake backed by a username->id map.
type fakeUsers struct {
	ids map[string]string
}

func (f fakeUsers) UserIDByUsername(username string) string { return f.ids[username] }

func TestTaskAssign_Command(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	env.api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	svc := &fakeStatusService{
		assignResult: &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "T1", Summary: "Ship"}, CreatorID: "creator"},
		assignEvent:  task.AssignEvent{NewAssigneeID: "u-bob", CreatorID: "creator"},
	}
	notif := &fakeNotifier{}
	handler := NewCommandHandler(env.client, svc, Options{
		AssignNotifier: notif,
		Users:          fakeUsers{ids: map[string]string{"bob": "u-bob"}},
	})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task assign T1 @bob"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "@bob")
	assert.Equal(t, "T1", svc.assignID)
	assert.Equal(t, "u-bob", svc.assignUserID)
	require.Len(t, notif.assigned, 1)
	assert.Equal(t, "u-bob", notif.assigned[0].assigneeID)
}

func TestTaskAssign_UserNotFound(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	handler := NewCommandHandler(env.client, &fakeStatusService{}, Options{
		Users: fakeUsers{ids: map[string]string{}},
	})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task assign T1 @ghost"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "not found")
}

func TestTaskAssign_NotAMention(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	handler := NewCommandHandler(env.client, &fakeStatusService{}, Options{Users: fakeUsers{}})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task assign T1 bob"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "@username")
}

func TestTaskAssign_Usage(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	handler := NewCommandHandler(env.client, &fakeStatusService{}, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task assign T1"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Usage")
}

func TestTaskAssign_NotFound(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	env.api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	svc := &fakeStatusService{assignErr: task.ErrNotFound}
	handler := NewCommandHandler(env.client, svc, Options{Users: fakeUsers{ids: map[string]string{"bob": "u-bob"}}})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task assign T1 @bob"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "not found")
}

func TestTaskUnassign_Command(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	svc := &fakeStatusService{assignResult: &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "T1", Summary: "Ship"}}}
	notif := &fakeNotifier{}
	handler := NewCommandHandler(env.client, svc, Options{AssignNotifier: notif})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task unassign T1"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "removed")
	assert.Equal(t, "", svc.assignUserID, "unassign passes empty user id")
	assert.Empty(t, notif.assigned, "no DM on unassign")
}

func TestTaskUnassign_Usage(t *testing.T) {
	env := setupTest()
	env.api.On("RegisterCommand", mockAnything()).Return(nil).Maybe()
	handler := NewCommandHandler(env.client, &fakeStatusService{}, Options{})

	resp, err := handler.Handle(&model.CommandArgs{Command: "/task unassign"})
	require.NoError(t, err)
	assert.Contains(t, resp.Text, "Usage")
}
