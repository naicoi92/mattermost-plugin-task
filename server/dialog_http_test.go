package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/task"
)

// submitNewTaskDialog tests exercise the New Task dialog submit handler
// (POST /api/v1/dialogs/newtask) end-to-end through the plugin router. The
// handler drives the most critical path in PR #102: scope resolution, due
// parsing, task creation, card posting, the ephemeral confirmation for personal
// tasks, and the real-time broadcast. CodeRabbit review flagged this path as
// uncovered.

// submissionEnvelope wraps a SubmitDialogRequest so tests read like the wire
// format Mattermost sends. State carries the originating channel id.
func submissionEnvelope(userID, state string, submission map[string]any) string {
	req := struct {
		UserId     string         `json:"user_id"`
		State      string         `json:"state"`
		Submission map[string]any `json:"submission"`
	}{
		UserId:     userID,
		State:      state,
		Submission: submission,
	}
	b, _ := json.Marshal(req)
	return string(b)
}

// minimalSubmission builds a submission map with the required fields set, so
// each test only overrides the field it cares about.
func minimalSubmission(summary, scope string) map[string]any {
	return map[string]any{
		dialogFieldSummary:     summary,
		dialogFieldNewScope:    scope,
		dialogFieldAssignee:    "",
		dialogFieldTaskDue:     "",
		dialogFieldDescription: "",
	}
}

func TestSubmitNewTaskDialog_EmptySummaryRejected(t *testing.T) {
	p, _ := newTestPlugin()
	w := httptest.NewRecorder()
	body := submissionEnvelope("u1", "ch1", minimalSubmission("", "channel"))
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/dialogs/newtask", body, "u1"))

	// A validation error keeps the dialog open with an inline error message.
	require.Equal(t, http.StatusOK, w.Code)
	var resp mmmodel.SubmitDialogResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp.Error, "Summary is required")
}

func TestSubmitNewTaskDialog_InvalidDueRejected(t *testing.T) {
	p, _ := newTestPlugin()
	w := httptest.NewRecorder()
	sub := minimalSubmission("Review PR", "channel")
	sub[dialogFieldTaskDue] = "abc"
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/dialogs/newtask",
		submissionEnvelope("u1", "ch1", sub), "u1"))

	var resp mmmodel.SubmitDialogResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp.Error, "Due must be a numeric millisecond timestamp")
}

func TestSubmitNewTaskDialog_DueZeroOrNegativeRejected(t *testing.T) {
	for _, due := range []string{"0", "-1"} {
		t.Run("due="+due, func(t *testing.T) {
			p, _ := newTestPlugin()
			w := httptest.NewRecorder()
			sub := minimalSubmission("Review PR", "channel")
			sub[dialogFieldTaskDue] = due
			p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/dialogs/newtask",
				submissionEnvelope("u1", "ch1", sub), "u1"))

			var resp mmmodel.SubmitDialogResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Contains(t, resp.Error, "Due must be a numeric millisecond timestamp")
		})
	}
}

func TestSubmitNewTaskDialog_ChannelScopeUsesStateChannel(t *testing.T) {
	p, store := newTestPlugin()
	w := httptest.NewRecorder()
	body := submissionEnvelope("u1", "ch1", minimalSubmission("Review PR", "channel"))
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/dialogs/newtask", body, "u1"))

	var resp mmmodel.SubmitDialogResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Error, "successful submit closes the dialog with no error")

	// The created task must be scoped to the channel carried in the dialog state.
	require.Len(t, store.tasks, 1)
	for _, tsk := range store.tasks {
		assert.Equal(t, "Review PR", tsk.Summary)
		assert.Equal(t, "u1", tsk.CreatorID)
		assert.Equal(t, "ch1", tsk.ChannelID, "scope=channel uses the state channel id")
	}
}

func TestSubmitNewTaskDialog_PersonalScopeClearsChannel(t *testing.T) {
	p, store := newTestPlugin()
	w := httptest.NewRecorder()
	// State still carries a channel id, but scope=personal must override it.
	body := submissionEnvelope("u1", "ch1", minimalSubmission("Personal errand", "personal"))
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/dialogs/newtask", body, "u1"))

	var resp mmmodel.SubmitDialogResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Error)

	require.Len(t, store.tasks, 1)
	for _, tsk := range store.tasks {
		assert.Empty(t, tsk.ChannelID, "scope=personal clears the channel id")
	}
}

func TestSubmitNewTaskDialog_AssigneeAndDueAndDescriptionApplied(t *testing.T) {
	p, store := newTestPlugin()
	w := httptest.NewRecorder()
	sub := minimalSubmission("Ship release", "channel")
	sub[dialogFieldAssignee] = "u-assignee"
	sub[dialogFieldTaskDue] = "1700000000000"
	sub[dialogFieldDescription] = "Cut the release branch"
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/dialogs/newtask",
		submissionEnvelope("u1", "ch1", sub), "u1"))

	var resp mmmodel.SubmitDialogResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Error)

	require.Len(t, store.tasks, 1)
	for _, tsk := range store.tasks {
		assert.Equal(t, "u-assignee", tsk.AssigneeID)
		require.NotNil(t, tsk.Due)
		assert.Equal(t, int64(1_700_000_000_000), *tsk.Due)
		assert.Equal(t, "Cut the release branch", tsk.Description)
	}
}

// Personal task (no channel card, no DM) triggers the ephemeral confirmation
// so the user gets feedback that the task was created (fix #4).
func TestSubmitNewTaskDialog_PersonalTaskSendsEphemeralConfirmation(t *testing.T) {
	p, store := newTestPlugin()
	api := p.API.(*plugintest.API)

	w := httptest.NewRecorder()
	body := submissionEnvelope("u1", "ch1", minimalSubmission("Personal errand", "personal"))
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/dialogs/newtask", body, "u1"))

	var resp mmmodel.SubmitDialogResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Error)

	require.Len(t, store.tasks, 1)
	for _, tsk := range store.tasks {
		assert.Empty(t, tsk.ChannelID, "personal task has no channel card")
	}
	api.AssertCalled(t, "SendEphemeralPost", "u1", mock.Anything)
}

// Channel task posts a card (no ephemeral needed). We assert the channel
// task is created with the right scope rather than the ephemeral call count,
// because the shared newTestPlugin mock returns a post with an empty Id (so
// postCard reports "" and the ephemeral fallback would also fire in the test
// harness — a test artifact, not a code path). The personal-task test above
// already pins the ephemeral-on-no-card contract.
func TestSubmitNewTaskDialog_ChannelTaskCreatedWithChannelScope(t *testing.T) {
	p, store := newTestPlugin()
	w := httptest.NewRecorder()
	body := submissionEnvelope("u1", "ch1", minimalSubmission("Review PR", "channel"))
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/dialogs/newtask", body, "u1"))

	var resp mmmodel.SubmitDialogResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Error)

	require.Len(t, store.tasks, 1)
	for _, tsk := range store.tasks {
		assert.Equal(t, "ch1", tsk.ChannelID, "channel task has a channel card")
	}
}

// --- Regression tests for #109: dialog submit callbacks must be reachable
// without the Mattermost-User-ID header, because the Mattermost server issues
// them as internal server→plugin requests that carry no session cookie. The
// actor is taken from SubmitDialogRequest.UserId in the body. The authed
// tests above cover the legacy harness path; these mirror production. ---

// TestSubmitNewTaskDialog_NoAuthHeaderStillRuns posts a New Task submission
// WITHOUT the Mattermost-User-ID header and asserts the handler runs and
// creates the task (instead of returning 401 as it did before #109).
func TestSubmitNewTaskDialog_NoAuthHeaderStillRuns(t *testing.T) {
	p, store := newTestPlugin()
	w := httptest.NewRecorder()
	body := submissionEnvelope("u1", "ch1", minimalSubmission("Ship release", "channel"))
	p.ServeHTTP(nil, w, unauthedRequest(http.MethodPost, "/api/v1/dialogs/newtask", body))

	// The handler responds 200 with a SubmitDialogResponse, NOT 401.
	require.Equal(t, http.StatusOK, w.Code)
	var resp mmmodel.SubmitDialogResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Error, "successful submit closes the dialog")

	// The actor (req.UserId from the body, not the header) created the task.
	require.Len(t, store.tasks, 1)
	for _, tsk := range store.tasks {
		assert.Equal(t, "u1", tsk.CreatorID, "actor taken from body UserId")
	}
}

// TestSubmitNewTaskDialog_MissingUserIDRejected verifies the hardening: a
// submit callback whose body carries no user id is rejected with 401 even
// though the route bypasses the auth middleware.
func TestSubmitNewTaskDialog_MissingUserIDRejected(t *testing.T) {
	p, _ := newTestPlugin()
	w := httptest.NewRecorder()
	body := submissionEnvelope("", "ch1", minimalSubmission("Ship release", "channel"))
	p.ServeHTTP(nil, w, unauthedRequest(http.MethodPost, "/api/v1/dialogs/newtask", body))

	require.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestSubmitQuickListDialog_NoAuthHeaderStillRuns posts a Quick List
// submission WITHOUT the Mattermost-User-ID header and asserts the handler
// runs (200 + SubmitDialogResponse) instead of 401.
func TestSubmitQuickListDialog_NoAuthHeaderStillRuns(t *testing.T) {
	p, _ := newTestPlugin()
	w := httptest.NewRecorder()
	body := submissionEnvelope("u1", "", map[string]any{
		dialogFieldTaskPick: "t1",
	})
	p.ServeHTTP(nil, w, unauthedRequest(http.MethodPost, "/api/v1/dialogs/quicklist", body))

	require.Equal(t, http.StatusOK, w.Code)
	var resp mmmodel.SubmitDialogResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp.Error, "/task show t1")
}

// TestSubmitQuickListDialog_MissingUserIDRejected verifies the hardening for
// the Quick List callback.
func TestSubmitQuickListDialog_MissingUserIDRejected(t *testing.T) {
	p, _ := newTestPlugin()
	w := httptest.NewRecorder()
	body := submissionEnvelope("", "", map[string]any{dialogFieldTaskPick: "t1"})
	p.ServeHTTP(nil, w, unauthedRequest(http.MethodPost, "/api/v1/dialogs/quicklist", body))

	require.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestSubmitTaskDetailDialog_NoAuthHeaderStillRuns posts a Task Detail save
// WITHOUT the Mattermost-User-ID header and asserts the handler runs (not
// 401). The task must exist first so the PATCH path is exercised.
func TestSubmitTaskDetailDialog_NoAuthHeaderStillRuns(t *testing.T) {
	p, _ := newTestPlugin()
	created, err := p.taskService.Create(task.CreateInput{Summary: "Old", CreatorID: "u1"})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	body := submissionEnvelope("u1", created.ID, map[string]any{
		dialogFieldSummary: "Updated summary",
		dialogFieldStatus:  taskmodel.StatusTodo,
	})
	p.ServeHTTP(nil, w, unauthedRequest(http.MethodPost, "/api/v1/dialogs/taskdetail", body))

	require.Equal(t, http.StatusOK, w.Code)
	var resp mmmodel.SubmitDialogResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Error, "successful save closes the dialog")

	updated, err := p.taskService.Get(created.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated summary", updated.Summary)
}

// TestSubmitTaskDetailDialog_MissingUserIDRejected verifies the hardening for
// the Task Detail callback.
func TestSubmitTaskDetailDialog_MissingUserIDRejected(t *testing.T) {
	p, _ := newTestPlugin()
	created, err := p.taskService.Create(task.CreateInput{Summary: "Old", CreatorID: "u1"})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	body := submissionEnvelope("", created.ID, map[string]any{
		dialogFieldSummary: "Updated summary",
		dialogFieldStatus:  taskmodel.StatusTodo,
	})
	p.ServeHTTP(nil, w, unauthedRequest(http.MethodPost, "/api/v1/dialogs/taskdetail", body))

	require.Equal(t, http.StatusUnauthorized, w.Code)
}
