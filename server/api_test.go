package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/task"
)

// fakeTaskStore is a minimal in-memory store for HTTP handler tests. It only
// implements the subset the task.Service touches; the service is the unit under
// indirect test, so a thin store is enough to drive the HTTP layer end-to-end.
type fakeTaskStore struct {
	tasks   map[string]model.Task
	indexes map[string]struct{}
	subs    map[string]map[string]struct{}
}

func newFakeTaskStore() *fakeTaskStore {
	return &fakeTaskStore{
		tasks:   map[string]model.Task{},
		indexes: map[string]struct{}{},
		subs:    map[string]map[string]struct{}{},
	}
}

func (f *fakeTaskStore) GetTask(id string) (*model.Task, error) {
	t, ok := f.tasks[id]
	if !ok {
		return nil, nil
	}
	return &t, nil
}
func (f *fakeTaskStore) SaveTask(t model.Task) error { f.tasks[t.ID] = t; return nil }
func (f *fakeTaskStore) DeleteTask(id string) error  { delete(f.tasks, id); return nil }
func (f *fakeTaskStore) SaveIndex(key string) error  { f.indexes[key] = struct{}{}; return nil }
func (f *fakeTaskStore) DeleteIndex(key string) error {
	delete(f.indexes, key)
	return nil
}

func (f *fakeTaskStore) SaveSubtask(parentID, taskID string) error {
	if f.subs[parentID] == nil {
		f.subs[parentID] = map[string]struct{}{}
	}
	f.subs[parentID][taskID] = struct{}{}
	return nil
}
func (f *fakeTaskStore) GetSubtaskIDs(parentID string) ([]string, error) { return nil, nil }
func (f *fakeTaskStore) SaveComment(string, model.Comment) error         { return nil }
func (f *fakeTaskStore) GetCommentIDs(string) ([]string, error)          { return nil, nil }
func (f *fakeTaskStore) SaveReminder(string, model.ReminderMetadata) error {
	return nil
}
func (f *fakeTaskStore) GetReminder(string) (*model.ReminderMetadata, error) { return nil, nil }
func (f *fakeTaskStore) DeleteReminder(string) error                         { return nil }
func (f *fakeTaskStore) ListReminderKeys() ([]string, error)                 { return nil, nil }
func (f *fakeTaskStore) ListTaskIDsByPrefix(prefix string) ([]string, error) {
	var ids []string
	for k := range f.indexes {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			ids = append(ids, k[len(prefix):])
		}
	}
	return ids, nil
}

func (f *fakeTaskStore) ListUserAssignedTaskIDs(userID string) ([]string, error) {
	return f.ListTaskIDsByPrefix("idx:u:" + userID + ":assigned:")
}

func (f *fakeTaskStore) ListUserCreatedTaskIDs(userID string) ([]string, error) {
	return f.ListTaskIDsByPrefix("idx:u:" + userID + ":created:")
}

func (f *fakeTaskStore) ListChannelTaskIDs(channelID string) ([]string, error) {
	return f.ListTaskIDsByPrefix("idx:ch:" + channelID + ":task:")
}

func (f *fakeTaskStore) ListAllTaskIDs() ([]string, error) {
	return f.ListTaskIDsByPrefix("idx:all:task:")
}

func (f *fakeTaskStore) SetAtomicWithRetries(string, func([]byte) (any, error)) error {
	return nil
}

// newTestPlugin wires a Plugin with a router and a task.Service backed by a
// fresh fake store.
func newTestPlugin() (*Plugin, *fakeTaskStore) {
	store := newFakeTaskStore()
	p := &Plugin{
		taskService: task.NewService(store),
	}
	p.router = p.initRouter()
	return p, store
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

func TestCreateTask_Endpoint(t *testing.T) {
	p, _ := newTestPlugin()

	body := `{"summary":"Review PR","channel_id":"ch1","assignee_id":"u2"}`
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks", body, "u1"))

	require.Equal(t, http.StatusCreated, w.Code)
	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "Review PR", got.Summary)
	assert.Equal(t, "u1", got.CreatorID)
	assert.Equal(t, "u2", got.AssigneeID)
	assert.Equal(t, "ch1", got.ChannelID)
	assert.Equal(t, model.StatusTodo, got.Status)
	assert.NotEmpty(t, got.ID)
}

func TestCreateTask_RequiresSummary(t *testing.T) {
	p, _ := newTestPlugin()
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks", `{}`, "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAuthorization_Required(t *testing.T) {
	p, _ := newTestPlugin()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil) // no Mattermost-User-ID
	p.ServeHTTP(nil, w, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestGetTask_NotFound(t *testing.T) {
	p, _ := newTestPlugin()
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/missing", "", "u1"))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGetTask_Existing(t *testing.T) {
	p, _ := newTestPlugin()
	// Seed via service.
	created, err := p.taskService.Create(task.CreateInput{Summary: "x", CreatorID: "u1"})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID, "", "u1"))
	require.Equal(t, http.StatusOK, w.Code)
	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, created.ID, got.ID)
}

func TestPatchTask_Partial(t *testing.T) {
	p, _ := newTestPlugin()
	created, err := p.taskService.Create(task.CreateInput{Summary: "old", CreatorID: "u1"})
	require.NoError(t, err)

	body := `{"update_fields":["summary"],"summary":"new"}`
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch, "/api/v1/tasks/"+created.ID, body, "u1"))
	require.Equal(t, http.StatusOK, w.Code)
	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "new", got.Summary)
}

func TestDeleteTask_Cascade(t *testing.T) {
	p, _ := newTestPlugin()
	created, err := p.taskService.Create(task.CreateInput{Summary: "x", CreatorID: "u1"})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodDelete, "/api/v1/tasks/"+created.ID, "", "u1"))
	assert.Equal(t, http.StatusNoContent, w.Code)

	w2 := httptest.NewRecorder()
	p.ServeHTTP(nil, w2, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID, "", "u1"))
	assert.Equal(t, http.StatusNotFound, w2.Code)
}

func TestListTasks_ScopeChannel_RequiresChannelID(t *testing.T) {
	p, _ := newTestPlugin()
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks?scope=channel", "", "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestListTasks_BadStatus(t *testing.T) {
	p, _ := newTestPlugin()
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks?status=invalid", "", "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestListTasks_ReturnsMine(t *testing.T) {
	p, _ := newTestPlugin()
	_, err := p.taskService.Create(task.CreateInput{Summary: "a", CreatorID: "u1", AssigneeID: "u2"})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks?scope=mine", "", "u2"))
	require.Equal(t, http.StatusOK, w.Code)
	var got []model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "a", got[0].Summary)
}

func TestHelloWorld_StillWorks(t *testing.T) {
	p, _ := newTestPlugin()
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/hello", "", "u1"))
	body, _ := io.ReadAll(w.Result().Body)
	assert.Equal(t, "Hello, world!", string(body))
}

func TestPatchTaskStatus_Endpoint(t *testing.T) {
	p, _ := newTestPlugin()
	created, err := p.taskService.Create(task.CreateInput{Summary: "x", CreatorID: "u1"})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch,
		"/api/v1/tasks/"+created.ID+"/status", `{"status":"done"}`, "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, model.StatusDone, got.Status)
	require.NotNil(t, got.CompletedAt)
}

func TestPatchTaskStatus_Invalid(t *testing.T) {
	p, _ := newTestPlugin()
	created, err := p.taskService.Create(task.CreateInput{Summary: "x", CreatorID: "u1"})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch,
		"/api/v1/tasks/"+created.ID+"/status", `{"status":"bogus"}`, "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPatchTaskStatus_NotFound(t *testing.T) {
	p, _ := newTestPlugin()
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch,
		"/api/v1/tasks/missing/status", `{"status":"done"}`, "u1"))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestPatchTaskStatus_BadJSON(t *testing.T) {
	p, _ := newTestPlugin()
	created, err := p.taskService.Create(task.CreateInput{Summary: "x", CreatorID: "u1"})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch,
		"/api/v1/tasks/"+created.ID+"/status", `{"status":"done"`, "u1")) // malformed JSON
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSetReminder_Endpoint(t *testing.T) {
	p, _ := newTestPlugin()
	due := int64(100_000)
	created, err := p.taskService.Create(task.CreateInput{Summary: "x", CreatorID: "u1", Due: &due})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost,
		"/api/v1/tasks/"+created.ID+"/reminder", `{"offset_ms":60000}`, "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.NotNil(t, got.ReminderOffset)
	assert.Equal(t, int64(60_000), *got.ReminderOffset)
}

func TestSetReminder_NeedsDue(t *testing.T) {
	p, _ := newTestPlugin()
	created, err := p.taskService.Create(task.CreateInput{Summary: "x", CreatorID: "u1"})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost,
		"/api/v1/tasks/"+created.ID+"/reminder", `{"offset_ms":60000}`, "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSetReminder_InvalidOffset(t *testing.T) {
	p, _ := newTestPlugin()
	due := int64(100_000)
	created, err := p.taskService.Create(task.CreateInput{Summary: "x", CreatorID: "u1", Due: &due})
	require.NoError(t, err)

	// offset_ms <= 0 is a client error.
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost,
		"/api/v1/tasks/"+created.ID+"/reminder", `{"offset_ms":0}`, "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSetReminder_InternalErrorDoesNotLeak(t *testing.T) {
	// Point the service at a store seeded with a due-dated task, where
	// SaveReminder fails. The handler must respond 500 and must NOT echo the
	// raw error text.
	due := int64(100_000)
	store := &failingReminderStore{fakeTaskStore: newFakeTaskStore()}
	store.tasks["T1"] = model.Task{ID: "T1", CreatorID: "u1", Due: &due, Status: model.StatusTodo}
	api := &plugintest.API{}
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	p := &Plugin{
		taskService: task.NewService(store),
	}
	p.SetAPI(api)
	p.router = p.initRouter()

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost,
		"/api/v1/tasks/T1/reminder", `{"offset_ms":60000}`, "u1"))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), "boom-internal", "raw error must not leak")
}

func TestDeleteReminder_Endpoint(t *testing.T) {
	p, _ := newTestPlugin()
	due := int64(100_000)
	offset := int64(60_000)
	created, err := p.taskService.Create(task.CreateInput{Summary: "x", CreatorID: "u1", Due: &due, ReminderOffset: &offset})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodDelete,
		"/api/v1/tasks/"+created.ID+"/reminder", "", "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Nil(t, got.ReminderOffset)
}

func TestDeleteReminder_InternalErrorDoesNotLeak(t *testing.T) {
	// ClearReminder fails via a store whose DeleteReminder errors. The handler
	// must respond 500 and must NOT echo the raw error text.
	due := int64(100_000)
	offset := int64(60_000)
	store := &failingReminderStore{fakeTaskStore: newFakeTaskStore()}
	store.tasks["T1"] = model.Task{ID: "T1", CreatorID: "u1", Due: &due, ReminderOffset: &offset, Status: model.StatusTodo}
	api := &plugintest.API{}
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	p := &Plugin{
		taskService: task.NewService(store),
	}
	p.SetAPI(api)
	p.router = p.initRouter()

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodDelete,
		"/api/v1/tasks/T1/reminder", "", "u1"))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), "boom-internal", "raw error must not leak")
}

// failingReminderStore wraps a fakeTaskStore (seeded with a task that has a due
// date so SetReminder reaches SaveReminder) but fails SaveReminder, to exercise
// the setReminder handler's internal-error path.
type failingReminderStore struct {
	*fakeTaskStore
}

func (f *failingReminderStore) SaveReminder(string, model.ReminderMetadata) error {
	return errInternalFake
}

// DeleteReminder also fails, to exercise the deleteReminder handler's
// internal-error path.
func (f *failingReminderStore) DeleteReminder(string) error {
	return errInternalFake
}

type errInternalFakeType struct{}

func (errInternalFakeType) Error() string { return "boom-internal" }

var errInternalFake errInternalFakeType

func TestSetAssignee_Endpoint(t *testing.T) {
	p, _ := newTestPlugin()
	created, err := p.taskService.Create(task.CreateInput{Summary: "x", CreatorID: "u1", AssigneeID: "u-old"})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost,
		"/api/v1/tasks/"+created.ID+"/assignee", `{"user_id":"u-new"}`, "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "u-new", got.AssigneeID)
}

func TestSetAssignee_RequiresUserID(t *testing.T) {
	p, _ := newTestPlugin()
	created, err := p.taskService.Create(task.CreateInput{Summary: "x", CreatorID: "u1"})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost,
		"/api/v1/tasks/"+created.ID+"/assignee", `{"user_id":""}`, "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSetAssignee_NotFound(t *testing.T) {
	p, _ := newTestPlugin()
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost,
		"/api/v1/tasks/missing/assignee", `{"user_id":"u-new"}`, "u1"))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDeleteAssignee_Endpoint(t *testing.T) {
	p, _ := newTestPlugin()
	created, err := p.taskService.Create(task.CreateInput{Summary: "x", CreatorID: "u1", AssigneeID: "u-old"})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodDelete,
		"/api/v1/tasks/"+created.ID+"/assignee", "", "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Empty(t, got.AssigneeID)
}
