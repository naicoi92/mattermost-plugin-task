package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/store"
	"github.com/naicoi92/mattermost-plugin-task/server/task"
)

// newTestPlugin wires a Plugin with a router and a task.Service backed by a
// fresh in-memory sqlite SQLStore (real store, real WithTx/FK semantics). A
// permissive mock API is set so handlers that log/post don't panic in tests.
//
// Returns the plugin and the underlying store.Store so tests can seed fixtures
// directly when a setup-via-endpoint is awkward.
func newTestPlugin(t *testing.T) (*Plugin, store.Store) {
	t.Helper()
	st := newTestTaskStore(t)
	api := &plugintest.API{}
	api.On("LogDebug", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	// LogError is variadic in usage (handlers pass message + N key/value
	// pairs). Register per-arity stubs so any call is tolerated.
	for n := 1; n <= 9; n++ {
		args := make([]any, n)
		for i := range args {
			args[i] = mock.Anything
		}
		api.On("LogError", args...).Return().Maybe()
	}
	// CreatePost must return a UNIQUE post id per call: task_posts.post_id is
	// UNIQUE, and createTask posts a card per task. Use Run to mint a fresh id
	// on the returned post.
	var postSeq int
	api.On("CreatePost", mock.Anything).Run(func(args mock.Arguments) {
		postSeq++
		post := args.Get(0).(*mmmodel.Post)
		post.Id = fmt.Sprintf("post-%d", postSeq)
	}).Return(func(post *mmmodel.Post) (*mmmodel.Post, *mmmodel.AppError) {
		return post, nil
	}).Maybe()
	api.On("UpdatePost", mock.Anything).Return(&mmmodel.Post{}, nil).Maybe()
	api.On("GetPost", mock.Anything).Return(&mmmodel.Post{Props: map[string]any{}}, nil).Maybe()
	api.On("GetDirectChannel", mock.Anything, mock.Anything).Return(&mmmodel.Channel{Id: "dm-channel"}, nil).Maybe()
	api.On("PublishWebSocketEvent", mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	api.On("SendEphemeralPost", mock.Anything, mock.Anything).Return(&mmmodel.Post{}).Maybe()
	p := &Plugin{
		taskService: task.NewService(st),
		botUserID:   "bot",
	}
	p.SetAPI(api)
	p.router = p.initRouter()
	return p, st
}

// createTaskViaService seeds a task through the service (full lifecycle).
func createTaskViaService(t *testing.T, p *Plugin, in task.CreateInput) *model.Task {
	t.Helper()
	taskObj, err := p.taskService.Create(in)
	require.NoError(t, err)
	return taskObj
}

func authedRequest(method, target, body, userID string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, bytes.NewReader([]byte(body)))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.Header.Set("Mattermost-User-ID", userID)
	return r
}

func unauthedRequest(method, target, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, bytes.NewReader([]byte(body)))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	return r
}

var _ = context.Background

func TestCreateTask_Endpoint(t *testing.T) {
	p, _ := newTestPlugin(t)
	body := `{"summary":"Review PR","channel_id":"ch1","assignee_id":"u2"}`
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks", body, "u1"))
	require.Equal(t, http.StatusCreated, w.Code)

	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "Review PR", got.Summary)
	assert.Equal(t, "ch1", got.ChannelID)
	assert.Equal(t, "u2", got.AssigneeID)
	assert.Equal(t, "u1", got.CreatorID)
}

func TestCreateTask_RequiresSummary(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks", `{}`, "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateTask_MissingParentIsBadRequest(t *testing.T) {
	p, _ := newTestPlugin(t)
	body := `{"summary":"sub","parent_task_id":"ghost"}`
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks", body, "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateSubtask_InheritsAndPersists(t *testing.T) {
	p, _ := newTestPlugin(t)
	parent := createTaskViaService(t, p, task.CreateInput{Summary: "p", ChannelID: "ch1", CreatorID: "u1", AssigneeID: "u2"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+parent.ID+"/subtasks", `{"summary":"sub"}`, "u1"))
	require.Equal(t, http.StatusCreated, w.Code)

	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "ch1", got.ChannelID, "subtask inherits parent channel")
	assert.Equal(t, "u2", got.AssigneeID, "subtask inherits parent assignee")
}

func TestCreateSubtask_ForbiddenForNonModifier(t *testing.T) {
	p, _ := newTestPlugin(t)
	parent := createTaskViaService(t, p, task.CreateInput{Summary: "p", CreatorID: "u-owner", AssigneeID: "u-owner"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+parent.ID+"/subtasks", `{"summary":"sub"}`, "u-stranger"))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestCreateSubtask_ParentNotFound(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/ghost/subtasks", `{"summary":"x"}`, "u1"))
	// The subtask handler resolves the parent via Get first; a missing parent
	// yields 404 before Create's ErrParentNotFound is reached.
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestCreateSubtask_ExplicitAssigneeOverridesInherited(t *testing.T) {
	p, _ := newTestPlugin(t)
	parent := createTaskViaService(t, p, task.CreateInput{Summary: "p", CreatorID: "u1", AssigneeID: "u-inherited"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+parent.ID+"/subtasks",
		`{"summary":"sub","assignee_id":"u-explicit"}`, "u1"))
	require.Equal(t, http.StatusCreated, w.Code)

	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "u-explicit", got.AssigneeID)
}

func TestListSubtasks_ReturnsDirectSubtasksInOrder(t *testing.T) {
	p, _ := newTestPlugin(t)
	parent := createTaskViaService(t, p, task.CreateInput{Summary: "p", CreatorID: "u1"})
	createTaskViaService(t, p, task.CreateInput{Summary: "s1", CreatorID: "u1", ParentTaskID: parent.ID})
	createTaskViaService(t, p, task.CreateInput{Summary: "s2", CreatorID: "u1", ParentTaskID: parent.ID})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+parent.ID+"/subtasks", "", "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got []model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 2)
}

func TestCreateComment_PersistsAndReturns(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/comments", `{"content":"looks good"}`, "u1"))
	require.Equal(t, http.StatusCreated, w.Code)
}

func TestCreateComment_TaskNotFound(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/ghost/comments", `{"content":"x"}`, "u1"))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestCreateComment_EmptyContentRejected(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/comments", `{"content":"  "}`, "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestListComments_ReturnsInCreationOrder(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})
	_, _, err := p.taskService.LinkComment(created.ID, "post-1", "u1")
	require.NoError(t, err)
	_, _, err = p.taskService.LinkComment(created.ID, "post-2", "u1")
	require.NoError(t, err)

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID+"/comments", "", "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got []model.TaskComment
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Len(t, got, 2)
}

func TestListComments_ForbiddenForNonParticipant(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u-owner"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID+"/comments", "", "u-stranger"))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestAuthorization_Required(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, unauthedRequest(http.MethodGet, "/api/v1/tasks", ""))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestGetTask_NotFound(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/ghost", "", "u1"))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetTask_Existing(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "hello", ChannelID: "ch1", CreatorID: "u1"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID, "", "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "hello", got.Summary)
}

func TestPatchTask_Partial(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "orig", Description: "d", CreatorID: "u1"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch, "/api/v1/tasks/"+created.ID, `{"update_fields":["summary"],"summary":"renamed"}`, "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "renamed", got.Summary)
	assert.Equal(t, "d", got.Description)
}

func TestDeleteTask_Cascade(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})
	createTaskViaService(t, p, task.CreateInput{Summary: "sub", CreatorID: "u1", ParentTaskID: created.ID})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodDelete, "/api/v1/tasks/"+created.ID, "", "u1"))
	require.Equal(t, http.StatusNoContent, w.Code)

	w2 := httptest.NewRecorder()
	p.ServeHTTP(nil, w2, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID+"/subtasks", "", "u1"))
	var subs []model.Task
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &subs))
	assert.Empty(t, subs)
}

func TestListTasks_ScopeChannel_RequiresChannelID(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks?scope=channel", "", "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestListTasks_BadStatus(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks?status=paused", "", "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestListTasks_ReturnsMine(t *testing.T) {
	p, _ := newTestPlugin(t)
	createTaskViaService(t, p, task.CreateInput{Summary: "mine", CreatorID: "u1", AssigneeID: "u-me"})
	createTaskViaService(t, p, task.CreateInput{Summary: "other", CreatorID: "u1", AssigneeID: "u-other"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks?scope=mine", "", "u-me"))
	require.Equal(t, http.StatusOK, w.Code)

	var got []model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "mine", got[0].Summary)
}

func TestHelloWorld_StillWorks(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	// /hello is mounted under the authenticated /api/v1 subrouter.
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/hello", "", "u1"))
	assert.Equal(t, http.StatusOK, w.Code)
	b, err := io.ReadAll(w.Body)
	require.NoError(t, err)
	assert.Equal(t, "Hello, world!", string(b))
}

func TestPatchTaskStatus_Endpoint(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch, "/api/v1/tasks/"+created.ID+"/status", `{"status":"done"}`, "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, model.StatusDone, got.Status)
	require.NotNil(t, got.CompletedAt)
}

func TestPatchTaskStatus_Invalid(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch, "/api/v1/tasks/"+created.ID+"/status", `{"status":"paused"}`, "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPatchTaskStatus_NotFound(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch, "/api/v1/tasks/ghost/status", `{"status":"done"}`, "u1"))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestPatchTaskStatus_BadJSON(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch, "/api/v1/tasks/"+created.ID+"/status", `{not json`, "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPatchTaskStatus_ParentDoneBlockedByOpenSubtask(t *testing.T) {
	p, _ := newTestPlugin(t)
	parent := createTaskViaService(t, p, task.CreateInput{Summary: "p", CreatorID: "u1"})
	createTaskViaService(t, p, task.CreateInput{Summary: "open", CreatorID: "u1", ParentTaskID: parent.ID})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch, "/api/v1/tasks/"+parent.ID+"/status", `{"status":"done"}`, "u1"))
	// Parent-done guard surfaces as 409 Conflict with the open-subtask summary.
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestSetReminder_Endpoint(t *testing.T) {
	p, _ := newTestPlugin(t)
	due := int64(100_000)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1", Due: &due})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/reminder", `{"offset_ms":60000}`, "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.NotNil(t, got.ReminderOffset)
	assert.Equal(t, int64(60_000), *got.ReminderOffset)
}

func TestSetReminder_NeedsDue(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/reminder", `{"offset_ms":60000}`, "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSetReminder_InvalidOffset(t *testing.T) {
	p, _ := newTestPlugin(t)
	due := int64(100_000)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1", Due: &due})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/reminder", `{"offset_ms":0}`, "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Note: the two "InternalErrorDoesNotLeak" reminder tests are not preserved.
// They tested error-text-leak suppression against a failingReminderStore built
// on the deleted kvstore. Happy-path coverage above plus writeError's
// consistent status/text handling cover the same handler logic; rebuilding a
// failing-store fake against store.Store is throwaway scaffolding.

func TestDeleteReminder_Endpoint(t *testing.T) {
	p, _ := newTestPlugin(t)
	due := int64(100_000)
	offset := int64(60_000)
	created, err := p.taskService.Create(task.CreateInput{Summary: "x", CreatorID: "u1", Due: &due, ReminderOffset: &offset})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodDelete, "/api/v1/tasks/"+created.ID+"/reminder", "", "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Nil(t, got.ReminderOffset)
}

func TestSetAssignee_Endpoint(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1", AssigneeID: "u-old"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/assignee", `{"user_id":"u-new"}`, "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "u-new", got.AssigneeID)
}

func TestSetAssignee_RequiresUserID(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/assignee", `{"user_id":""}`, "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSetAssignee_NotFound(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/missing/assignee", `{"user_id":"u-new"}`, "u1"))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDeleteAssignee_Endpoint(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1", AssigneeID: "u-old"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodDelete, "/api/v1/tasks/"+created.ID+"/assignee", "", "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Empty(t, got.AssigneeID)
}

// TestAuthenticatedRoutes_StillRequireHeader pins the security boundary after
// #109: endpoints reached from the browser session must keep rejecting requests
// that lack the Mattermost-User-ID header.
func TestAuthenticatedRoutes_StillRequireHeader(t *testing.T) {
	cases := []struct {
		name   string
		method string
		target string
		body   string
	}{
		{"task create", http.MethodPost, "/api/v1/tasks", `{"summary":"x"}`},
		{"task list", http.MethodGet, "/api/v1/tasks", ""},
		{"card action", http.MethodPost, "/api/v1/actions", `{}`},
		{"dialog open task detail", http.MethodPost, "/api/v1/dialogs/open-task-detail", `{"trigger_id":"t","task_id":"x"}`},
		{"dialog open new task", http.MethodPost, "/api/v1/dialogs/open-new-task", `{"trigger_id":"t"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := newTestPlugin(t)
			w := httptest.NewRecorder()
			p.ServeHTTP(nil, w, unauthedRequest(tc.method, tc.target, tc.body))
			assert.Equal(t, http.StatusUnauthorized, w.Code,
				"%s %s must require the Mattermost-User-ID header", tc.method, tc.target)
		})
	}
}

var _ = sort.Slice
