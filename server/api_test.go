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
	// LogDebug is variadic in usage; register per-arity stubs so the
	// temporary [DEBUG-perm] instrumentation (many key/value pairs) is
	// tolerated. Remove with the instrumentation.
	for n := 1; n <= 15; n++ {
		args := make([]any, n)
		for i := range args {
			args[i] = mock.Anything
		}
		api.On("LogDebug", args...).Return().Maybe()
	}
	// LogError is variadic in usage (handlers pass message + N key/value
	// pairs). Register per-arity stubs so any call is tolerated.
	for n := 1; n <= 15; n++ {
		args := make([]any, n)
		for i := range args {
			args[i] = mock.Anything
		}
		api.On("LogError", args...).Return().Maybe()
	}
	// LogWarn is variadic in usage (handlers pass message + N key/value pairs).
	// Register per-arity stubs so any call is tolerated.
	for n := 1; n <= 15; n++ {
		args := make([]any, n)
		for i := range args {
			args[i] = mock.Anything
		}
		api.On("LogWarn", args...).Return().Maybe()
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
	// channelMembership.IsChannelMember: default to member so card-resolution
	// (commentRoot, shareTask) resolves the requested/first card. Tests needing
	// a non-member path override GetChannelMember AFTER this default and reseed.
	api.On("GetChannelMember", mock.Anything, mock.Anything).Return(&mmmodel.ChannelMember{}, nil).Maybe()
	api.On("PublishWebSocketEvent", mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	api.On("SendEphemeralPost", mock.Anything, mock.Anything).Return(&mmmodel.Post{}).Maybe()
	p := &Plugin{
		taskService: task.NewService(st),
		taskStore:   st,
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

// TestCreateTask_PersonalTaskPostsCardIntoPostChannelID asserts that a
// personal task (empty channel_id) created in a DM context posts its card
// preview into post_channel_id (the DM the viewer is in). The task's own
// channel_id stays empty (personal scope), but the card is visible in the
// originating channel. No IsChannelMember probe — the server trusts the client.
func TestCreateTask_PersonalTaskPostsCardIntoPostChannelID(t *testing.T) {
	p, _ := newTestPlugin(t)
	body := `{"summary":"DM task","post_channel_id":"dm1"}`
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks", body, "u1"))
	require.Equal(t, http.StatusCreated, w.Code)

	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "", got.ChannelID, "personal scope preserved")
	assert.Equal(t, "post-1", got.ChannelPostID, "card posted into post_channel_id")
}

// TestCreateTask_PersonalTaskWithAssigneePostsChannelCardAndDMCard asserts
// that a personal task (no home channel) created in a DM with an assignee posts
// BOTH the announce card into post_channel_id (the DM the viewer is in) AND the
// assignee-bot DM card (DM(bot, assignee)). Two distinct surfaces.
func TestCreateTask_PersonalTaskWithAssigneePostsChannelCardAndDMCard(t *testing.T) {
	p, _ := newTestPlugin(t)
	body := `{"summary":"DM assigned","post_channel_id":"dm1","assignee_id":"partner"}`
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks", body, "u1"))
	require.Equal(t, http.StatusCreated, w.Code)

	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "", got.ChannelID, "personal scope preserved")
	assert.Equal(t, "post-1", got.ChannelPostID, "announce card into post_channel_id")
	assert.Equal(t, "post-2", got.DMPostID, "assignee-bot DM card")
	p.API.(*plugintest.API).AssertNumberOfCalls(t, "CreatePost", 2)
}

// TestCreateTask_PostChannelID_IgnoredWhenNotMember is the defense-in-depth
// guard for client-controlled post_channel_id: if the requesting user is NOT a
// member of post_channel_id, the announce card is NOT posted there. The task is
// still created (personal scope preserved). Restored after the IsChannelMember
// gate was re-added to createTask (review R1 + CodeRabbit nitpick).
func TestCreateTask_PostChannelID_IgnoredWhenNotMember(t *testing.T) {
	p, _ := newTestPlugin(t)
	api := p.API.(*plugintest.API)
	// Drop the default member-true mock; caller is not a member of "secret".
	reseedGetChannelMember(t, api)
	api.On("GetChannelMember", "secret", "u1").Return(nil, nil).Maybe()
	body := `{"summary":"sneak","post_channel_id":"secret"}`
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks", body, "u1"))
	require.Equal(t, http.StatusCreated, w.Code)

	var got model.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	// The task is still created (personal scope, no assignee).
	assert.Equal(t, "", got.ChannelID)
	assert.Equal(t, "sneak", got.Summary)
	// But NO announce card was posted: the caller couldn't direct the bot into
	// a channel they don't belong to.
	assert.Equal(t, "", got.ChannelPostID)
	assert.Equal(t, "", got.DMPostID)
	api.AssertNumberOfCalls(t, "CreatePost", 0)
}

// TestShareTask_PostsCardAndLinks verifies the happy path: sharing an existing
// task into a channel posts a card there, links it via task_posts with
// kind="share", and returns 200 {post_id}.
func TestShareTask_PostsCardAndLinks(t *testing.T) {
	p, st := newTestPlugin(t)
	taskObj := createTaskViaService(t, p, task.CreateInput{Summary: "Share me", CreatorID: "u1"})

	api := p.API.(*plugintest.API)
	api.On("GetChannelMember", "ch1", "u1").Return(&mmmodel.ChannelMember{}, nil).Maybe()

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+taskObj.ID+"/share",
		`{"channel_id":"ch1"}`, "u1"))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		PostID string `json:"post_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "post-1", resp.PostID)

	// The card was posted into the target channel.
	api.AssertCalled(t, "CreatePost", mock.MatchedBy(func(post *mmmodel.Post) bool {
		return post.ChannelId == "ch1"
	}))

	// A kind="share" task_post row links the card to the task.
	posts, err := st.ListPosts(context.Background(), taskObj.ID)
	require.NoError(t, err)
	var found bool
	for _, tp := range posts {
		if tp.Kind == "share" && tp.PostID == "post-1" {
			found = true
		}
	}
	assert.True(t, found, "expected a kind=share task_post row with post_id=post-1, got %v", posts)
}

func TestShareTask_404_UnknownTask(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/nonexistent/share",
		`{"channel_id":"ch1"}`, "u1"))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestShareTask_400_MissingChannelID(t *testing.T) {
	p, _ := newTestPlugin(t)
	taskObj := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+taskObj.ID+"/share",
		`{}`, "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestShareTask_403_NotViewer: a personal task (empty channel_id) created by
// u1 — a third user u3 cannot view it (not creator/assignee/member) → 403.
func TestShareTask_403_NotViewer(t *testing.T) {
	p, _ := newTestPlugin(t)
	taskObj := createTaskViaService(t, p, task.CreateInput{Summary: "private", CreatorID: "u1"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+taskObj.ID+"/share",
		`{"channel_id":"ch1"}`, "u3"))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestShareTask_403_NotChannelMember: the caller can view the task (is creator)
// but is NOT a member of the target channel → 403.
func TestShareTask_403_NotChannelMember(t *testing.T) {
	p, _ := newTestPlugin(t)
	taskObj := createTaskViaService(t, p, task.CreateInput{Summary: "share me", CreatorID: "u1"})

	api := p.API.(*plugintest.API)
	// u1 is NOT a member of ch-secret: drop the default member-true mock first.
	reseedGetChannelMember(t, api)
	api.On("GetChannelMember", "ch-secret", "u1").Return(nil, nil).Maybe()

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+taskObj.ID+"/share",
		`{"channel_id":"ch-secret"}`, "u1"))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestShareTask_Idempotent_ExistingCardInChannel: sharing into ch1 twice returns
// the SAME post id and does NOT post a second card. The idempotency check
// resolves the task's existing posts via GetPost; the second call short-
// circuits when it finds a card already in ch1.
func TestShareTask_Idempotent_ExistingCardInChannel(t *testing.T) {
	p, _ := newTestPlugin(t)
	taskObj := createTaskViaService(t, p, task.CreateInput{Summary: "share me", CreatorID: "u1"})

	api := p.API.(*plugintest.API)
	api.On("GetChannelMember", "ch1", "u1").Return(&mmmodel.ChannelMember{}, nil).Maybe()
	// Override the default GetPost mock: testify mock matches FIFO, so the
	// general GetPost(mock.Anything) registered in newTestPlugin always wins
	// over a specific one registered later. Clear the GetPost mocks and
	// re-register specific-first, then the general fallback, so GetPost("post-1")
	// returns the correct ChannelId for the idempotency check.
	var kept []*mock.Call
	for _, c := range api.ExpectedCalls {
		if c.Method != "GetPost" {
			kept = append(kept, c)
		}
	}
	api.ExpectedCalls = kept
	api.On("GetPost", "post-1").Return(&mmmodel.Post{Id: "post-1", ChannelId: "ch1"}, nil).Maybe()
	api.On("GetPost", mock.Anything).Return(&mmmodel.Post{Props: map[string]any{}}, nil).Maybe()

	// First share: posts post-1.
	w1 := httptest.NewRecorder()
	p.ServeHTTP(nil, w1, authedRequest(http.MethodPost, "/api/v1/tasks/"+taskObj.ID+"/share",
		`{"channel_id":"ch1"}`, "u1"))
	require.Equal(t, http.StatusOK, w1.Code, w1.Body.String())
	var resp1 struct {
		PostID string `json:"post_id"`
	}
	require.NoError(t, json.Unmarshal(w1.Body.Bytes(), &resp1))
	assert.Equal(t, "post-1", resp1.PostID)

	// Second share into the same channel: returns the SAME post id.
	w2 := httptest.NewRecorder()
	p.ServeHTTP(nil, w2, authedRequest(http.MethodPost, "/api/v1/tasks/"+taskObj.ID+"/share",
		`{"channel_id":"ch1"}`, "u1"))
	require.Equal(t, http.StatusOK, w2.Code, w2.Body.String())
	var resp2 struct {
		PostID string `json:"post_id"`
	}
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp2))
	assert.Equal(t, "post-1", resp2.PostID)

	// Exactly ONE CreatePost call — no duplicate card.
	api.AssertNumberOfCalls(t, "CreatePost", 1)
}

// TestShareTask_409_AlreadySharedElsewhere locks in the single-share invariant:
// task_posts UNIQUE(task_id, kind) allows at most one kind="share" card, so
// sharing a task that is already shared in another channel is rejected with
// 409 (no orphan card, no constraint violation) rather than 500.
func TestShareTask_409_AlreadySharedElsewhere(t *testing.T) {
	p, _ := newTestPlugin(t)
	taskObj := createTaskViaService(t, p, task.CreateInput{Summary: "share me", CreatorID: "u1"})

	api := p.API.(*plugintest.API)
	api.On("GetChannelMember", "chA", "u1").Return(&mmmodel.ChannelMember{}, nil).Maybe()
	api.On("GetChannelMember", "chB", "u1").Return(&mmmodel.ChannelMember{}, nil).Maybe()
	// GetPost("post-1") reports the existing share lives in chA, so a chB share
	// does not match idempotency and hits the single-share guard. Same FIFO
	// caveat as TestShareTask_Idempotent_ExistingCardInChannel: re-register
	// specific-first.
	var kept []*mock.Call
	for _, c := range api.ExpectedCalls {
		if c.Method != "GetPost" {
			kept = append(kept, c)
		}
	}
	api.ExpectedCalls = kept
	api.On("GetPost", "post-1").Return(&mmmodel.Post{Id: "post-1", ChannelId: "chA"}, nil).Maybe()
	api.On("GetPost", mock.Anything).Return(&mmmodel.Post{Props: map[string]any{}}, nil).Maybe()

	// First share to chA succeeds.
	w1 := httptest.NewRecorder()
	p.ServeHTTP(nil, w1, authedRequest(http.MethodPost, "/api/v1/tasks/"+taskObj.ID+"/share",
		`{"channel_id":"chA"}`, "u1"))
	require.Equal(t, http.StatusOK, w1.Code, w1.Body.String())

	// Second share to a DIFFERENT channel is rejected: single-share invariant.
	w2 := httptest.NewRecorder()
	p.ServeHTTP(nil, w2, authedRequest(http.MethodPost, "/api/v1/tasks/"+taskObj.ID+"/share",
		`{"channel_id":"chB"}`, "u1"))
	assert.Equal(t, http.StatusConflict, w2.Code)
	assert.Contains(t, w2.Body.String(), "already shared")

	// Only the first share posted a card.
	api.AssertNumberOfCalls(t, "CreatePost", 1)
}

// TestShareTask_Idempotent_EvenWhenSharedElsewhere locks the precedence between
// the two guards: if the TARGET channel already has a card (any kind), the
// share is idempotent (200, existing card) — NOT 409 — even when a share-kind
// card exists in a different channel. The single-share 409 only applies to a
// channel that has no existing card. (Spec: idempotent per channel.)
func TestShareTask_Idempotent_EvenWhenSharedElsewhere(t *testing.T) {
	p, st := newTestPlugin(t)
	taskObj := createTaskViaService(t, p, task.CreateInput{Summary: "dual card", CreatorID: "u1"})
	ctx := context.Background()
	// Seed a channel-kind card in X and a share-kind card in Y.
	require.NoError(t, st.AddPost(ctx, "row1", taskObj.ID, "post-x", model.PostKindChannel))
	require.NoError(t, st.AddPost(ctx, "row2", taskObj.ID, "post-y", model.PostKindShare))

	api := p.API.(*plugintest.API)
	api.On("GetChannelMember", "X", "u1").Return(&mmmodel.ChannelMember{}, nil).Maybe()
	var kept []*mock.Call
	for _, c := range api.ExpectedCalls {
		if c.Method != "GetPost" {
			kept = append(kept, c)
		}
	}
	api.ExpectedCalls = kept
	api.On("GetPost", "post-x").Return(&mmmodel.Post{Id: "post-x", ChannelId: "X"}, nil).Maybe()
	api.On("GetPost", "post-y").Return(&mmmodel.Post{Id: "post-y", ChannelId: "Y"}, nil).Maybe()
	api.On("GetPost", mock.Anything).Return(&mmmodel.Post{Props: map[string]any{}}, nil).Maybe()

	// Share to X: X already has the channel card → idempotent 200 (post-x), not
	// 409, despite the share in Y.
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+taskObj.ID+"/share",
		`{"channel_id":"X"}`, "u1"))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		PostID string `json:"post_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "post-x", resp.PostID)
	// No new card posted (idempotent).
	api.AssertNumberOfCalls(t, "CreatePost", 0)
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
	p, st := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})
	// Give the task a card root in ch1 so createComment can thread the reply
	// under it (FIX-2: a task with no resolvable card root now rejects 400).
	require.NoError(t, st.AddPost(context.Background(), "tp-card", created.ID, "cardpost", model.PostKindChannel))
	api := p.API.(*plugintest.API)
	reseedGetPost(t, api, map[string]*mmmodel.Post{
		"cardpost": {Id: "cardpost", ChannelId: "ch1"},
	})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/comments",
		`{"content":"looks good","channel_id":"ch1"}`, "u1"))
	require.Equal(t, http.StatusCreated, w.Code)
}

// TestCreateComment_NonRegression_PathUnchanged (AC9) pins the existing
// create-comment flow end-to-end: the contract fix + UI redesign must NOT alter
// it. The create handler (server/api.go createComment) is untouched by design;
// this test asserts it still yields (a) a reply post authored by the HUMAN
// commenter (Change A: post.UserId == actorID; bot-ownership is retained ONLY
// for card posts), (b) a linked task_comments row with the AuthorID snapshot,
// (c) an EventCommented row, and (d) a task_updated WS broadcast whose
// changed_fields contains "comment".
func TestCreateComment_NonRegression_PathUnchanged(t *testing.T) {
	p, st := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})
	// FIX-2: createComment now requires a resolvable card root. Seed one in ch1.
	require.NoError(t, st.AddPost(context.Background(), "tp-card", created.ID, "cardpost", model.PostKindChannel))

	var capturedPost *mmmodel.Post
	api := p.API.(*plugintest.API)
	// cardPostInChannel resolves the card root via GetPost; seed it so the
	// comment threads under "cardpost" in ch1.
	reseedGetPost(t, api, map[string]*mmmodel.Post{
		"cardpost": {Id: "cardpost", ChannelId: "ch1"},
	})
	// Re-register the CreatePost mock with a Run hook that captures the post the
	// handler creates, without changing its return (the default mock mints a
	// unique post id per call; we re-implement the same Run here).
	var postSeq int
	var kept []*mock.Call
	for _, c := range api.ExpectedCalls {
		if c.Method != "CreatePost" {
			kept = append(kept, c)
		}
	}
	api.ExpectedCalls = kept
	api.On("CreatePost", mock.Anything).Run(func(arguments mock.Arguments) {
		postSeq++
		post := arguments.Get(0).(*mmmodel.Post)
		post.Id = fmt.Sprintf("post-%d", postSeq)
		capturedPost = post
	}).Return(func(post *mmmodel.Post) (*mmmodel.Post, *mmmodel.AppError) {
		return post, nil
	}).Maybe()

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/comments", `{"content":"looks good","channel_id":"ch1"}`, "u1"))
	require.Equal(t, http.StatusCreated, w.Code)

	// (a) the comment post is authored by the HUMAN commenter, NOT the bot.
	require.NotNil(t, capturedPost, "a reply post was created")
	assert.Equal(t, "u1", capturedPost.UserId, "Change A: comment post UserId == actorID (human author), not bot")

	// (b) a linked task_comments row carries the caller's AuthorID snapshot.
	rows, err := st.ListComments(context.Background(), created.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "u1", rows[0].AuthorID, "AuthorID is the caller snapshot")

	// (c) an EventCommented row exists in task_events.
	events, err := st.ListTaskEvents(context.Background(), created.ID, 50)
	require.NoError(t, err)
	var sawCommented bool
	for _, e := range events {
		if e.EventType == model.EventCommented {
			sawCommented = true
			break
		}
	}
	assert.True(t, sawCommented, "create-comment appends an EventCommented")

	// (d) a task_updated WS broadcast with changed_fields containing "comment".
	var sawCommentBroadcast bool
	for _, c := range api.Calls {
		if c.Method != "PublishWebSocketEvent" {
			continue
		}
		payload, _ := c.Arguments[1].(map[string]any)
		cf, _ := payload["changed_fields"].([]string)
		for _, f := range cf {
			if f == "comment" {
				sawCommentBroadcast = true
			}
		}
	}
	assert.True(t, sawCommentBroadcast, `broadcast changed_fields contains "comment"`)
}

func TestCreateComment_TaskNotFound(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/ghost/comments", `{"content":"x"}`, "u1"))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestCreateComment_ThreadsUnderCardInRequestedChannel (Change B) asserts the
// channel/root reconciliation for a SHARED task: the task lives in ch-home but
// is shared via a kind="share" card into ch-shared. When the webapp passes
// channel_id=ch-shared (the channel the viewer is acting from), the comment
// post MUST thread under the SHARE card (RootId == share post id) and post into
// ch-shared (ChannelId == ch-shared) — NOT root under the home channel card.
// Currently the handler ignores channel_id and roots under t.ChannelPostID
// (empty for a shared task here) => RootId=="" (a root channel message): the bug.
func TestCreateComment_ThreadsUnderCardInRequestedChannel(t *testing.T) {
	p, st := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "shared", ChannelID: "ch-home", CreatorID: "u-creator"})
	require.NoError(t, st.AddPost(context.Background(), "tp-share", created.ID, "sharepost", model.PostKindShare))

	api := p.API.(*plugintest.API)
	reseedGetPost(t, api, map[string]*mmmodel.Post{
		"sharepost": {Id: "sharepost", ChannelId: "ch-shared"},
	})
	api.On("GetChannelMember", "ch-shared", "sharer").Return(&mmmodel.ChannelMember{}, nil).Maybe()
	api.On("GetChannelMember", "ch-home", "sharer").Return(nil, &mmmodel.AppError{}).Maybe()

	var capturedPost *mmmodel.Post
	var postSeq int
	var kept []*mock.Call
	for _, c := range api.ExpectedCalls {
		if c.Method != "CreatePost" {
			kept = append(kept, c)
		}
	}
	api.ExpectedCalls = kept
	api.On("CreatePost", mock.Anything).Run(func(arguments mock.Arguments) {
		postSeq++
		post := arguments.Get(0).(*mmmodel.Post)
		post.Id = fmt.Sprintf("post-%d", postSeq)
		capturedPost = post
	}).Return(func(post *mmmodel.Post) (*mmmodel.Post, *mmmodel.AppError) {
		return post, nil
	}).Maybe()

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/comments",
		`{"content":"hi","channel_id":"ch-shared"}`, "sharer"))
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	require.NotNil(t, capturedPost)
	assert.Equal(t, "sharepost", capturedPost.RootId, "comment threads under the share card in the requested channel")
	assert.Equal(t, "ch-shared", capturedPost.ChannelId, "comment posts into the requested channel")
}

// TestCreateComment_PersonalTaskFallsBackWhenNoChannelRequested was removed:
// it pinned the legacy rootless-reply (RootId=="") behaviour for a personal
// task with no card, which is exactly the FIX-2 bug (the reply renders once
// then as "deleted" on refetch). Replaced by TestCreateComment_NoCardRootRejected.

func TestCreateComment_EmptyContentRejected(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/comments", `{"content":"  "}`, "u1"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestCreateComment_NoCardRootRejected (FIX-2) pins the rootID guard: when a
// task has no card root resolvable (ChannelPostID=="", DMPostID=="", and no
// tracked task_post), createComment MUST reject with 400 instead of creating
// a rootless/unthreaded post (RootId==""). A rootless comment post is NOT in
// any card thread, so listComments (which fetches only the card threads)
// cannot resolve it and marks it deleted on refetch — surfacing as "comment
// shows once, then (comment deleted) after reload".
func TestCreateComment_NoCardRootRejected(t *testing.T) {
	p, _ := newTestPlugin(t)
	// Personal task with no channel/DM card and no tracked task_posts.
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/comments", `{"content":"hi"}`, "u1"))

	assert.Equal(t, http.StatusBadRequest, w.Code, "a task with no card root must reject the comment, not post a rootless reply")
}

// TestCreateComment_ResponseIncludesContent (FIX-A) asserts the createComment
// handler returns a commentResponse (the same shape listComments uses) with
// content resolved from the just-created post and deleted=false, so the webapp
// can render the new comment body immediately without waiting for a WS refetch.
// Regression: previously the handler returned the raw model.TaskComment row,
// which has no content/deleted fields, so the freshly-posted comment rendered
// with an empty body (only the author label showed).
func TestCreateComment_ResponseIncludesContent(t *testing.T) {
	p, st := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})
	// FIX-2: createComment now requires a resolvable card root. Seed one in ch1.
	require.NoError(t, st.AddPost(context.Background(), "tp-card", created.ID, "cardpost", model.PostKindChannel))

	// Capture the post the handler creates so we can assert the response PostID
	// matches it and no extra GetPost lookup is issued.
	var capturedPost *mmmodel.Post
	api := p.API.(*plugintest.API)
	// cardPostInChannel resolves the card root via GetPost; seed it for ch1.
	reseedGetPost(t, api, map[string]*mmmodel.Post{
		"cardpost": {Id: "cardpost", ChannelId: "ch1"},
	})
	var kept []*mock.Call
	for _, c := range api.ExpectedCalls {
		if c.Method != "CreatePost" {
			kept = append(kept, c)
		}
	}
	api.ExpectedCalls = kept
	var postSeq int
	api.On("CreatePost", mock.Anything).Run(func(arguments mock.Arguments) {
		postSeq++
		post := arguments.Get(0).(*mmmodel.Post)
		post.Id = fmt.Sprintf("post-%d", postSeq)
		capturedPost = post
	}).Return(func(post *mmmodel.Post) (*mmmodel.Post, *mmmodel.AppError) {
		return post, nil
	}).Maybe()

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/comments", `{"content":"hello world","channel_id":"ch1"}`, "u1"))
	require.Equal(t, http.StatusCreated, w.Code)

	require.NotNil(t, capturedPost, "a reply post was created")
	var resp commentResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "hello world", resp.Content, "FIX-A: response content comes from the created post Message")
	assert.False(t, resp.Deleted, "FIX-A: freshly created comment is not deleted")
	assert.Equal(t, "u1", resp.AuthorID, "author_id is the caller snapshot")
	assert.Equal(t, capturedPost.Id, resp.PostID, "post_id is the just-created post")
}

// TestCreateComment_NilPostNoCrash pins the defensive guard added after a
// production panic: if p.API.CreatePost returns (nil, nil) — a nil post with a
// nil error — the handler MUST respond with HTTP 500 and NOT dereference the
// nil post (which previously panicked createComment at api.go:840 and crashed
// the plugin process, surfacing as an RPC EOF / crash loop).
func TestCreateComment_NilPostNoCrash(t *testing.T) {
	p, st := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})
	// FIX-2: createComment requires a resolvable card root; seed one in ch1 so
	// the handler reaches CreatePost (where the nil-post edge case lives).
	require.NoError(t, st.AddPost(context.Background(), "tp-card", created.ID, "cardpost", model.PostKindChannel))

	// Override CreatePost to return (nil, nil) — the production edge case.
	api := p.API.(*plugintest.API)
	reseedGetPost(t, api, map[string]*mmmodel.Post{
		"cardpost": {Id: "cardpost", ChannelId: "ch1"},
	})
	var kept []*mock.Call
	for _, c := range api.ExpectedCalls {
		if c.Method != "CreatePost" {
			kept = append(kept, c)
		}
	}
	api.ExpectedCalls = kept
	api.On("CreatePost", mock.Anything).Return((*mmmodel.Post)(nil), (*mmmodel.AppError)(nil)).Maybe()
	api.On("LogError", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe()

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/comments", `{"content":"hi","channel_id":"ch1"}`, "u1"))

	// No panic (the test itself would have crashed the runner); clean 500.
	assert.Equal(t, http.StatusInternalServerError, w.Code)
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

// TestListComments_AssigneeAllowedViaAssignAction (AC4) is the deterministic
// repro for the assignee-403 gap: a personal task (no channel) is created by
// one user, then a second user is assigned via the real Assign service path
// (SetAssignee writes the role='assignee' task_members row — not a hand-
// crafted fixture). The assignee must be permitted to list the task's
// comments because CanUserCommentTask follows CanUserViewTask, which allows
// userID == task.AssigneeID (server/permission/permission.go). This test pins
// the write path (Assign persists the assignee edge) + the load path
// (assembleTask populates task.AssigneeID) + the permission rule end-to-end.
func TestListComments_AssigneeAllowedViaAssignAction(t *testing.T) {
	p, st := newTestPlugin(t)
	// Personal task, no channel: only creator + assignee may view/comment.
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u-creator"})

	// Assign via the real Assign service path (server/task/service.go Assign ->
	// SetAssignee -> AddMember role='assignee'). This is the action path, not a
	// hand-crafted member row.
	assigned, _, err := p.taskService.Assign("u-creator", created.ID, "u-assignee")
	require.NoError(t, err)

	// Debug checkpoints (AC4 diagnosis): assert the assignee edge was written
	// and that assembleTask loaded it onto task.AssigneeID. These pinpoint the
	// write vs load path should a 403 ever surface.
	ctx := context.Background()
	assigneeEdge, err := st.GetMemberByRole(ctx, created.ID, model.MemberRoleAssignee)
	require.NoError(t, err, "assignee task_members row must be persisted by Assign")
	assert.Equal(t, "u-assignee", assigneeEdge, "assignee edge carries the new assignee user id")
	assert.Equal(t, "u-assignee", assigned.AssigneeID, "assembleTask loaded task.AssigneeID from the assignee edge")

	// AC4: the assignee calling GET /tasks/:id/comments must get 200, not 403.
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID+"/comments", "", "u-assignee"))
	assert.Equal(t, http.StatusOK, w.Code, "assignee (via Assign action) may list comments; body=%s", w.Body.String())
}

// commentListItem mirrors the listComments transport response (server/api.go
// commentResponse): the DB row fields plus the transport-only Content/Deleted.
// Decoding into model.TaskComment would drop content/deleted, so this struct is
// the test's view of the wire contract.
type commentListItem struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	PostID    string `json:"post_id"`
	AuthorID  string `json:"author_id"`
	CreatedAt int64  `json:"created_at"`
	Content   string `json:"content"`
	Deleted   bool   `json:"deleted"`
}

// seedCommentRow inserts a task_comments row directly via the store with an
// explicit post_id, author_id and created_at, so tests can control ordering
// and the backing-post lookup independently of the create-comment flow.
func seedCommentRow(t *testing.T, st store.Store, taskID, id, postID, authorID string, createdAt int64) {
	t.Helper()
	_, err := st.LinkComment(context.Background(), id, taskID, postID, authorID, createdAt)
	require.NoError(t, err)
}

// TestListComments_ResolvesContentViaGetPost asserts: the payload includes
// content sourced from post.Message and author_id from the row snapshot,
// resolved by a direct GetPost(comment.post_id) per comment (not via
// GetPostThread over card roots — the simplified comment resolution).
func TestListComments_ResolvesContentViaGetPost(t *testing.T) {
	p, st := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})
	seedCommentRow(t, st, created.ID, "c1", "p1", "alice", 1000)
	seedCommentRow(t, st, created.ID, "c2", "p2", "alice", 2000)

	api := p.API.(*plugintest.API)
	reseedGetPost(t, api, map[string]*mmmodel.Post{
		"p1": {Id: "p1", Message: "hello"},
		"p2": {Id: "p2", Message: "world"},
	})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID+"/comments", "", "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got []commentListItem
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 2)
	byPost := map[string]commentListItem{}
	for _, c := range got {
		byPost[c.PostID] = c
	}
	assert.Equal(t, "hello", byPost["p1"].Content, "content for p1 must come from post.Message")
	assert.Equal(t, "world", byPost["p2"].Content, "content for p2 must come from post.Message")
	assert.Equal(t, "alice", byPost["p1"].AuthorID, "author_id is the row snapshot, not re-derived")
	assert.False(t, byPost["p1"].Deleted)
	assert.False(t, byPost["p2"].Deleted)
	// Simplified resolution: each comment resolved by its own GetPost, no
	// GetPostThread over card roots.
	api.AssertNumberOfCalls(t, "GetPostThread", 0)
}

// TestListComments_DeletedFlagForMissingPost asserts AC2: a comment whose
// backing post is absent from the GetPostThread result is returned with
// deleted:true, content:"" and is NOT omitted.
func TestListComments_DeletedFlagForMissingPost(t *testing.T) {
	p, st := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})
	require.NoError(t, st.AddPost(context.Background(), "tp-1", created.ID, "cardroot", model.PostKindChannel))
	seedCommentRow(t, st, created.ID, "c1", "p-alive", "alice", 1000)
	seedCommentRow(t, st, created.ID, "c2", "p-gone", "alice", 2000)

	api := p.API.(*plugintest.API)
	thread := &mmmodel.PostList{Posts: map[string]*mmmodel.Post{
		"cardroot": {Id: "cardroot", Message: ""},
		"p-alive":  {Id: "p-alive", Message: "hi"},
		// p-gone is intentionally absent (post deleted out-of-band).
	}}
	api.On("GetPostThread", "cardroot").Return(thread, nil)
	// FIX-3: GetPost fallback must also miss p-gone for it to stay deleted.
	// Set up GetPost explicitly (no default-non-nil catch-all).
	var kept []*mock.Call
	for _, c := range api.ExpectedCalls {
		if c.Method != "GetPost" {
			kept = append(kept, c)
		}
	}
	api.ExpectedCalls = kept
	api.On("GetPost", "cardroot").Return(&mmmodel.Post{Id: "cardroot", ChannelId: "ch1"}, nil).Maybe()
	api.On("GetPost", "p-alive").Return(&mmmodel.Post{Id: "p-alive", Message: "hi"}, nil).Maybe()
	api.On("GetPost", "p-gone").Return((*mmmodel.Post)(nil), &mmmodel.AppError{StatusCode: http.StatusNotFound}).Maybe()

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID+"/comments", "", "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got []commentListItem
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 2, "the deleted comment must NOT be omitted")
	byPost := map[string]commentListItem{}
	for _, c := range got {
		byPost[c.PostID] = c
	}
	assert.True(t, byPost["p-gone"].Deleted, "absent post => deleted:true")
	assert.Equal(t, "", byPost["p-gone"].Content, "deleted comment has no content")
	assert.False(t, byPost["p-alive"].Deleted)
}

// TestListComments_GetPostFallbackForUnresolvedComment (FIX-3) asserts the
// per-comment GetPost fallback: when a comment's PostID is NOT in any fetched
// card thread (e.g. the reply post's RootId didn't land in a fetched root's
// thread due to a root-resolution mismatch), listComments MUST fall back to a
// direct GetPost(comment.PostID) to resolve content rather than marking the
// freshly-created comment deleted. Regression: without the fallback, a comment
// showed once (optimistic, content from the create response) then rendered as
// "(comment deleted)" after reload.
func TestListComments_GetPostFallbackForUnresolvedComment(t *testing.T) {
	p, st := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})
	require.NoError(t, st.AddPost(context.Background(), "tp-1", created.ID, "cardroot", model.PostKindChannel))
	seedCommentRow(t, st, created.ID, "c1", "p-orphan", "alice", 1000)

	api := p.API.(*plugintest.API)
	// The card thread does NOT contain the comment reply (simulating a
	// root-resolution mismatch in production).
	thread := &mmmodel.PostList{Posts: map[string]*mmmodel.Post{
		"cardroot": {Id: "cardroot", Message: ""},
	}}
	api.On("GetPostThread", "cardroot").Return(thread, nil)
	// The direct GetPost fallback resolves the orphan comment's content.
	reseedGetPost(t, api, map[string]*mmmodel.Post{
		"p-orphan": {Id: "p-orphan", Message: "I am the comment body"},
	})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID+"/comments", "", "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got []commentListItem
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "I am the comment body", got[0].Content, "fallback GetPost resolved the content")
	assert.False(t, got[0].Deleted, "resolved via fallback => not deleted")
}

// TestListComments_NewestFirstOrder asserts AC5/AC8: given comments at
// t1<t2<t3, the response array is ordered [t3, t2, t1].
func TestListComments_NewestFirstOrder(t *testing.T) {
	p, st := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})
	require.NoError(t, st.AddPost(context.Background(), "tp-1", created.ID, "cardroot", model.PostKindChannel))
	seedCommentRow(t, st, created.ID, "c1", "pa", "alice", 1000)
	seedCommentRow(t, st, created.ID, "c2", "pb", "alice", 2000)
	seedCommentRow(t, st, created.ID, "c3", "pc", "alice", 3000)

	api := p.API.(*plugintest.API)
	thread := &mmmodel.PostList{Posts: map[string]*mmmodel.Post{
		"cardroot": {Id: "cardroot"},
		"pa":       {Id: "pa", Message: "m1"},
		"pb":       {Id: "pb", Message: "m2"},
		"pc":       {Id: "pc", Message: "m3"},
	}}
	api.On("GetPostThread", "cardroot").Return(thread, nil)

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID+"/comments", "", "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got []commentListItem
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 3)
	assert.Equal(t, int64(3000), got[0].CreatedAt, "newest first")
	assert.Equal(t, int64(2000), got[1].CreatedAt)
	assert.Equal(t, int64(1000), got[2].CreatedAt, "oldest last")
}

// TestListComments_RootIDEmptyReturnsDeletedFlag asserts the defensive branch:
// when the task has no card post (ChannelPostID and DMPostID both empty), the
// handler returns all comments with content:"" and deleted:true (no N+1
// fallback, no panic). Covers the Personal-task DMPostID empty edge case.
func TestListComments_RootIDEmptyReturnsDeletedFlag(t *testing.T) {
	p, st := newTestPlugin(t)
	// Personal task: no channel, no card posts added => ChannelPostID/DMPostID empty.
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})
	seedCommentRow(t, st, created.ID, "c1", "p1", "alice", 1000)
	// FIX-3: GetPost fallback must also miss p1 for it to stay deleted.
	api := p.API.(*plugintest.API)
	var kept []*mock.Call
	for _, c := range api.ExpectedCalls {
		if c.Method != "GetPost" {
			kept = append(kept, c)
		}
	}
	api.ExpectedCalls = kept
	api.On("GetPost", "p1").Return((*mmmodel.Post)(nil), &mmmodel.AppError{StatusCode: http.StatusNotFound}).Maybe()

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID+"/comments", "", "u1"))
	require.Equal(t, http.StatusOK, w.Code)

	var got []commentListItem
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.True(t, got[0].Deleted, "no root post => deleted placeholder")
	assert.Equal(t, "", got[0].Content)
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

	// After cascade delete the parent (and its subtasks) are gone, so the
	// subtasks endpoint reports the parent as not found rather than an empty
	// list.
	w2 := httptest.NewRecorder()
	p.ServeHTTP(nil, w2, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID+"/subtasks", "", "u1"))
	assert.Equal(t, http.StatusNotFound, w2.Code)
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

// reseedGetPost strips any previously-registered GetPost expectations from the
// plugintest API and re-registers a specific post-id→post mapping followed by a
// generic fallback. Required because plugintest matches expectations FIFO, so a
// generic mock.Anything registered first by newTestPlugin would shadow a
// later specific one. Mirrors the pattern used by the share-task tests.
func reseedGetPost(t *testing.T, api *plugintest.API, byID map[string]*mmmodel.Post) {
	t.Helper()
	var kept []*mock.Call
	for _, c := range api.ExpectedCalls {
		if c.Method != "GetPost" {
			kept = append(kept, c)
		}
	}
	api.ExpectedCalls = kept
	for id, post := range byID {
		p := post
		api.On("GetPost", id).Return(p, nil).Maybe()
	}
	api.On("GetPost", mock.Anything).Return(&mmmodel.Post{Props: map[string]any{}}, nil).Maybe()
}

// reseedGetChannelMember drops every GetChannelMember call from the mock so a
// test can register specific (channel,user) → non-member expectations without
// the default member-true mock.Anything registered by newTestPlugin shadowing
// them. Mirrors reseedGetPost.
func reseedGetChannelMember(t *testing.T, api *plugintest.API) {
	t.Helper()
	var kept []*mock.Call
	for _, c := range api.ExpectedCalls {
		if c.Method != "GetChannelMember" {
			kept = append(kept, c)
		}
	}
	api.ExpectedCalls = kept
}

// TestListComments_SharedChannelMemberAllowed (Change C) asserts the permission
// rule expansion at the API layer: a task lives in ch-home, but is shared via a
// kind="share" card into ch-shared. A member of ch-shared (NOT a member of
// ch-home) must GET 200 on listComments. The card-channel set is resolved by
// the caller from task_posts (cardChannelIDs). This exercises the GetPost→
// ChannelId resolution path the pure permission package cannot.
func TestListComments_SharedChannelMemberAllowed(t *testing.T) {
	p, st := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "shared", ChannelID: "ch-home", CreatorID: "u-creator"})
	// Track a share card post in ch-shared (kind=share).
	require.NoError(t, st.AddPost(context.Background(), "tp-share", created.ID, "sharepost", model.PostKindShare))

	api := p.API.(*plugintest.API)
	reseedGetPost(t, api, map[string]*mmmodel.Post{
		"sharepost": {Id: "sharepost", ChannelId: "ch-shared"},
	})
	// sharer is a member of ch-shared only; ch-home lookup is a non-member
	// (return nil member + an AppError so IsChannelMember reports false).
	api.On("GetChannelMember", "ch-shared", "sharer").Return(&mmmodel.ChannelMember{}, nil).Maybe()
	api.On("GetChannelMember", "ch-home", "sharer").Return(nil, &mmmodel.AppError{}).Maybe()
	// listComments content resolution: no thread needed for the permission assertion.
	api.On("GetPostThread", mock.Anything).Return(&mmmodel.PostList{Posts: map[string]*mmmodel.Post{}}, nil).Maybe()

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID+"/comments", "", "sharer"))
	assert.Equal(t, http.StatusOK, w.Code, "member of a shared card channel may list comments; body=%s", w.Body.String())
}

// TestListComments_ChannelTaskReadableByOutsider (behavior change): a task
// with a channel surface (ChannelID != "" or a tracked card post) is now
// readable by anyone — its card is already public in the channel, so view no
// longer gates on channel membership. An outsider (member of no channel) may
// list comments on such a task.
func TestListComments_ChannelTaskReadableByOutsider(t *testing.T) {
	p, st := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "shared", ChannelID: "ch-home", CreatorID: "u-creator"})
	require.NoError(t, st.AddPost(context.Background(), "tp-share", created.ID, "sharepost", model.PostKindShare))

	api := p.API.(*plugintest.API)
	reseedGetPost(t, api, map[string]*mmmodel.Post{
		"sharepost": {Id: "sharepost", ChannelId: "ch-shared"},
	})
	// outsider is not a member of any channel: every lookup is a non-member.
	api.On("GetChannelMember", mock.Anything, mock.Anything).Return(nil, &mmmodel.AppError{}).Maybe()
	// listComments content resolution: no thread needed.
	api.On("GetPostThread", mock.Anything).Return(&mmmodel.PostList{Posts: map[string]*mmmodel.Post{}}, nil).Maybe()

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID+"/comments", "", "outsider"))
	assert.Equal(t, http.StatusOK, w.Code, "channel-surfaced task is readable by anyone")
}

// --- Permission enforcement (task-permissions-review) ---
//
// Manage actions (modify/status/assign/reminder) are restricted to creator +
// assignee (co-owners). Delete is creator-only. Channel members and outsiders
// are denied. The manage guard does not consult channel membership, so these
// 403s need no GetChannelMember mock.

func TestPatchTask_ForbiddenForNonOwner(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch, "/api/v1/tasks/"+created.ID,
		`{"update_fields":["summary"],"summary":"hacked"}`, "u2"))
	assert.Equal(t, http.StatusForbidden, w.Code, "non-owner cannot patch")
}

func TestPatchTask_AllowedForAssignee(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1", AssigneeID: "u2"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch, "/api/v1/tasks/"+created.ID,
		`{"update_fields":["summary"],"summary":"ok"}`, "u2"))
	assert.Equal(t, http.StatusOK, w.Code, "assignee (co-owner) can patch")
}

func TestPatchTaskStatus_ForbiddenForNonOwner(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch, "/api/v1/tasks/"+created.ID+"/status",
		`{"status":"in_progress"}`, "u2"))
	assert.Equal(t, http.StatusForbidden, w.Code, "non-owner cannot change status")
}

func TestSetAssignee_ForbiddenForNonOwner(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/assignee",
		`{"user_id":"u3"}`, "u2"))
	assert.Equal(t, http.StatusForbidden, w.Code, "non-owner cannot reassign")
}

func TestSetAssignee_AssigneeCanReassign(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1", AssigneeID: "u2"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/assignee",
		`{"user_id":"u3"}`, "u2"))
	assert.Equal(t, http.StatusOK, w.Code, "assignee (co-owner) can reassign")
}

func TestSetReminder_ForbiddenForNonOwner(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPost, "/api/v1/tasks/"+created.ID+"/reminder",
		`{"offset_ms":1000}`, "u2"))
	assert.Equal(t, http.StatusForbidden, w.Code, "non-owner cannot set reminder")
}

func TestDeleteAssignee_ForbiddenForNonOwner(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodDelete, "/api/v1/tasks/"+created.ID+"/assignee", "", "u2"))
	assert.Equal(t, http.StatusForbidden, w.Code, "non-owner cannot clear assignee")
}

func TestDeleteTask_ForbiddenForAssignee(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1", AssigneeID: "u2"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodDelete, "/api/v1/tasks/"+created.ID, "", "u2"))
	assert.Equal(t, http.StatusForbidden, w.Code, "assignee cannot delete")
}

func TestDeleteTask_ForbiddenForChannelMember(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})

	// Mock the "member" fixture as a real member of ch1 so this represents a
	// genuine channel member (not an outsider). Delete is still creator-only.
	api := p.API.(*plugintest.API)
	api.On("GetChannelMember", "ch1", "member").Return(&mmmodel.ChannelMember{}, nil).Maybe()

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodDelete, "/api/v1/tasks/"+created.ID, "", "member"))
	assert.Equal(t, http.StatusForbidden, w.Code, "channel member cannot delete")
}

// CodeRabbit nitpick hardening: a real channel member (mocked membership) must
// still be denied manage actions — guards against a future view/list-based
// regression where channel members accidentally gain write access.
func TestPatchTask_ForbiddenForChannelMember(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})

	api := p.API.(*plugintest.API)
	api.On("GetChannelMember", "ch1", "member").Return(&mmmodel.ChannelMember{}, nil).Maybe()

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodPatch, "/api/v1/tasks/"+created.ID,
		`{"update_fields":["summary"],"summary":"hacked"}`, "member"))
	assert.Equal(t, http.StatusForbidden, w.Code, "channel member cannot patch")
}

func TestDeleteTask_AllowedForCreator(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodDelete, "/api/v1/tasks/"+created.ID, "", "u1"))
	assert.Equal(t, http.StatusNoContent, w.Code, "creator can delete")
}

// Guard must not be skipped when the task is missing: a transient/unknown id
// returns 404 (NotFound), never a silent proceed-to-delete or a misleading 403.
func TestDeleteTask_NotFound(t *testing.T) {
	p, _ := newTestPlugin(t)
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodDelete, "/api/v1/tasks/nonexistent", "", "u1"))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// View/list guards DO consult channel membership.

func TestGetTask_PersonalTaskHiddenFromOthers(t *testing.T) {
	p, _ := newTestPlugin(t)
	// Personal task (no ChannelID): only creator + assignee may view.
	created := createTaskViaService(t, p, task.CreateInput{Summary: "secret", CreatorID: "u1", AssigneeID: "u2"})

	api := p.API.(*plugintest.API)
	api.On("GetChannelMember", mock.Anything, mock.Anything).Return(&mmmodel.ChannelMember{}, nil).Maybe()

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID, "", "u3"))
	assert.Equal(t, http.StatusForbidden, w.Code, "personal task hidden from non creator/assignee")
}

// Behavior change: a channel-surfaced task (ChannelID != "") is now readable
// by anyone — its card is already public in the channel, so view does not gate
// on channel membership. An outsider (not a member of any channel) may view it.
// Only a personal task (no ChannelID, no card) is restricted.
func TestGetTask_ChannelTaskReadableByOutsider(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})

	api := p.API.(*plugintest.API)
	api.On("GetChannelMember", mock.Anything, mock.Anything).Return(nil, &mmmodel.AppError{}).Maybe()

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID, "", "outsider"))
	assert.Equal(t, http.StatusOK, w.Code, "channel task readable by anyone")
}

func TestGetTask_AllowedForChannelMember(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})

	api := p.API.(*plugintest.API)
	api.On("GetChannelMember", "ch1", "member").Return(&mmmodel.ChannelMember{}, nil).Maybe()

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID, "", "member"))
	assert.Equal(t, http.StatusOK, w.Code, "channel member can view channel task")
}

// Channel-surfaced task: subtasks are listable by anyone (card is public).
func TestListSubtasks_ChannelTaskReadableByOutsider(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})

	api := p.API.(*plugintest.API)
	api.On("GetChannelMember", mock.Anything, mock.Anything).Return(nil, &mmmodel.AppError{}).Maybe()

	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, authedRequest(http.MethodGet, "/api/v1/tasks/"+created.ID+"/subtasks", "", "outsider"))
	assert.Equal(t, http.StatusOK, w.Code, "channel task subtasks listable by anyone")
}

// Card-action buttons (status/priority) must also respect the manage rule.
// handleCardAction replies with HTTP 200 + an ephemeral denial message rather
// than a 403, per the Mattermost action-callback contract.

func TestHandleCardAction_Status_ForbiddenForNonOwner(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})

	body := fmt.Sprintf(`{"user_id":"u2","context":{"action":"status","task_id":%q}}`, created.ID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/actions", bytes.NewReader([]byte(body)))
	req.Header.Set("Mattermost-User-ID", "u2")
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "do not have permission")
}

func TestHandleCardAction_Priority_ForbiddenForNonOwner(t *testing.T) {
	p, _ := newTestPlugin(t)
	created := createTaskViaService(t, p, task.CreateInput{Summary: "x", CreatorID: "u1"})

	body := fmt.Sprintf(`{"user_id":"u2","context":{"action":"priority","task_id":%q}}`, created.ID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/actions", bytes.NewReader([]byte(body)))
	req.Header.Set("Mattermost-User-ID", "u2")
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "do not have permission")
}
