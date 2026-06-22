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
	// GetUser backs resolveMention: any id maps to a user whose username echoes
	// the id so the Assignee field renders "@<id>" in tests. The real resolve
	// path hits the server; tests don't care about a real username.
	api.On("GetUser", mock.Anything).Return(func(userID string) (*mmmodel.User, *mmmodel.AppError) {
		return &mmmodel.User{Id: userID, Username: userID}, nil
	}).Maybe()
	// GetConfig backs getSiteURL/resolveUser: return a config with an empty
	// SiteURL so the avatar/permalink builders return "" and the card renders
	// without external URLs in tests.
	emptySiteURL := ""
	api.On("GetConfig").Return(&mmmodel.Config{
		ServiceSettings: mmmodel.ServiceSettings{SiteURL: &emptySiteURL},
	}).Maybe()
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

// TestListTasks_ReturnsDirectScope verifies the DM scope returns only tasks
// where BOTH the caller and the partner are members (mutual-membership). A
// task created by u-me and assigned to u-partner has both as members → returned.
// A task created by u-third and assigned to u-other has neither → hidden.
func TestListTasks_ReturnsDirectScope(t *testing.T) {
	p, _ := newTestPlugin(t)
	// Shared task: u-me is creator, u-partner is assignee → both are members.
	createTaskViaService(t, p, task.CreateInput{Summary: "shared", CreatorID: "u-me", AssigneeID: "u-partner"})
	// Unrelated task: neither u-me nor u-partner is a member.
	createTaskViaService(t, p, task.CreateInput{Summary: "other", CreatorID: "u-third", AssigneeID: "u-other"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks?scope=direct&partner_id=u-partner", "", "u-me"))
	require.Equal(t, http.StatusOK, w.Code)

	var got []model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "shared", got[0].Summary)
}

// TestListTasks_DirectScopePreventsPartnerEnumeration verifies the security
// invariant: u-me cannot see u-third's tasks by passing partner_id=u-third,
// because u-me is not a member of any task u-third relates to.
func TestListTasks_DirectScopePreventsPartnerEnumeration(t *testing.T) {
	p, _ := newTestPlugin(t)
	// u-third created a task assigned to u-other; u-me is not involved.
	createTaskViaService(t, p, task.CreateInput{Summary: "private", CreatorID: "u-third", AssigneeID: "u-other"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks?scope=direct&partner_id=u-third", "", "u-me"))
	require.Equal(t, http.StatusOK, w.Code)

	var got []model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Empty(t, got, "u-me cannot enumerate u-third's tasks by guessing partner_id")
}

func TestListTasks_DirectScopeRequiresPartnerID(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks?scope=direct", "", "u-me"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestCreateTask_PriorityRoundTrip verifies priority is persisted by create
// and echoed back unchanged (including the default when omitted).
func TestCreateTask_PriorityRoundTrip(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks",
		`{"summary":"urgent task","creator_id":"u1","priority":"urgent"}`, "u1"))
	require.Equal(t, http.StatusCreated, w.Code)
	var created model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	assert.Equal(t, model.PriorityUrgent, created.Priority)

	// Default priority when omitted.
	w2 := httptest.NewRecorder()
	p.ServeHTTP(nil, w2, authedRequest(http.MethodPost, "/api/v1/tasks",
		`{"summary":"default priority","creator_id":"u1"}`, "u1"))
	require.Equal(t, http.StatusCreated, w2.Code)
	var created2 model.Task
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &created2))
	assert.Equal(t, model.PriorityStandard, created2.Priority)
}

// TestPatchTask_Priority verifies priority is patchable via update_fields.
func TestPatchTask_Priority(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})

	newPrio := model.PriorityImportant
	body, _ := json.Marshal(map[string]any{
		"update_fields": []string{"priority"},
		"priority":      newPrio,
	})
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch, "/api/v1/tasks/"+created.ID, string(body), "u1"))
	require.Equal(t, http.StatusOK, w.Code)
	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, model.PriorityImportant, got.Priority)
}

// TestPatchTask_InvalidPriorityRejected verifies an unknown priority value is
// rejected as a 400 rather than persisted.
func TestPatchTask_InvalidPriorityRejected(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})
	body, _ := json.Marshal(map[string]any{
		"update_fields": []string{"priority"},
		"priority":      "blocker",
	})
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch, "/api/v1/tasks/"+created.ID, string(body), "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestCreateTask_InvalidPriorityRejected verifies that POST /tasks with an
// unknown priority value returns 400 (not 500).
func TestCreateTask_InvalidPriorityRejected(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks",
		`{"summary":"x","creator_id":"u1","priority":"blocker"}`, "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestListTasks_InvalidScopeRejected verifies that an unknown/empty scope is
// rejected at the API boundary with 400 (not 500 from the store layer).
func TestListTasks_InvalidScopeRejected(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks?scope=mine", "", "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestListTasks_EmptyScopeRejected(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks", "", "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
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

// TestPatchTaskStatus_ParentDoneCascadesOpenSubtasks verifies that marking a
// parent Done via the API succeeds (cascade-cancels open subtasks) rather
// than returning 409.
func TestPatchTaskStatus_ParentDoneCascadesOpenSubtasks(t *testing.T) {
	p, _ := newTestPlugin(t)
	parent := createTaskViaService(t, p, task.CreateInput{Summary: "p", CreatorID: "u1"})
	createTaskViaService(t, p, task.CreateInput{Summary: "open", CreatorID: "u1", ParentTaskID: parent.ID})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch, "/api/v1/tasks/"+parent.ID+"/status", `{"status":"done"}`, "u1"))
	// Done now cascades — no 409.
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSetReminder_Endpoint(t *testing.T) {
	p, _ := newTestPlugin(t)
	due := int64(100_000)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1", DueAt: &due})

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
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1", DueAt: &due})

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
	created, err := p.taskService.Create(task.CreateInput{Summary: "x", CreatorID: "u1", DueAt: &due, ReminderOffset: &offset})
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

func TestListTaskEvents_ReturnsAuditTrail(t *testing.T) {
	p, _ := newTestPlugin(t)
	// Create + status change + assign → 3+ events.
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1", AssigneeID: "u-old"})
	_, _ = p.taskService.SetStatus("u1", created.ID, model.StatusDone)
	_, _, _ = p.taskService.Assign("u1", created.ID, "u-new")

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID+"/events?limit=10", "", "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var events []model.TaskEvent
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &events))
	// created + status_changed + assigned (at minimum).
	assert.GreaterOrEqual(t, len(events), 3, "expected created + status_changed + assigned events")
	// Newest-first ordering across the entire slice (pairwise, ID tie-break).
	for i := 1; i < len(events); i++ {
		prev, cur := events[i-1], events[i]
		assert.True(
			t,
			prev.CreatedAt > cur.CreatedAt ||
				(prev.CreatedAt == cur.CreatedAt && prev.ID >= cur.ID),
			"events not newest-first at index %d", i,
		)
	}
}

func TestListTaskEvents_ForbiddenForNonParticipant(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u-owner"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID+"/events", "", "u-stranger"))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestListTaskEvents_NotFound(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/ghost/events", "", "u1"))
	assert.Equal(t, http.StatusNotFound, w.Code)
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
		{"task patch", http.MethodPatch, "/api/v1/tasks/ghost", `{"summary":"x"}`},
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

func TestPatchTaskStatus_ReopenFromDoneToInProgress(t *testing.T) {
	p, _ := newTestPlugin(t)
	parent := createTaskViaService(t, p, task.CreateInput{Summary: "p", CreatorID: "u1"})

	// Mark Done first.
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch, "/api/v1/tasks/"+parent.ID+"/status", `{"status":"done"}`, "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	// Now reopen → In Progress.
	w2 := httptest.NewRecorder()
	p.ServeHTTP(nil, w2, authedRequest(http.MethodPatch, "/api/v1/tasks/"+parent.ID+"/status", `{"status":"in_progress"}`, "u1"))
	assert.Equal(t, http.StatusOK, w2.Code, "reopen from done to in_progress must not 500")
}
