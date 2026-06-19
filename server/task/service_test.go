package task

import (
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/store/kvstore"
)

// fakeStore is an in-memory KVStore implementation for service tests. It tracks
// task/comment/subtask/index records and the reminder edge so the service can be
// exercised end-to-end without the Mattermost pluginapi.
type fakeStore struct {
	tasks        map[string]model.Task
	comments     map[string][]model.Comment // taskID -> comments
	subtasks     map[string]map[string]struct{}
	indexes      map[string]struct{}
	reminders    map[string]model.ReminderMetadata
	skipComments map[string]struct{} // comment ids to treat as corrupt (defensive skip)
	corruptErr   map[string]error    // comment ids whose GetComment returns a decode error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		tasks:        map[string]model.Task{},
		comments:     map[string][]model.Comment{},
		subtasks:     map[string]map[string]struct{}{},
		indexes:      map[string]struct{}{},
		reminders:    map[string]model.ReminderMetadata{},
		skipComments: map[string]struct{}{},
		corruptErr:   map[string]error{},
	}
}

func (f *fakeStore) GetTask(id string) (*model.Task, error) {
	t, ok := f.tasks[id]
	if !ok {
		return nil, nil
	}
	return &t, nil
}

func (f *fakeStore) SaveTask(task model.Task) error {
	f.tasks[task.ID] = task
	return nil
}

func (f *fakeStore) DeleteTask(id string) error {
	delete(f.tasks, id)
	return nil
}

func (f *fakeStore) SaveIndex(key string) error {
	f.indexes[key] = struct{}{}
	return nil
}

func (f *fakeStore) DeleteIndex(key string) error {
	delete(f.indexes, key)
	// Comments are stored as entity keys t:{taskID}:c:{commentID}; mirror the
	// production KVStore by dropping them from the comments map when removed.
	if commentID, taskID, ok := parseCommentKey(key); ok {
		f.comments[taskID] = removeComment(f.comments[taskID], commentID)
	}
	return nil
}

func removeComment(in []model.Comment, id string) []model.Comment {
	out := in[:0]
	for _, c := range in {
		if c.ID != id {
			out = append(out, c)
		}
	}
	return out
}

// parseCommentKey decodes a t:{taskID}:c:{commentID} key. The ":c:" separator
// matches kvstore.keyCommentPrefix.
func parseCommentKey(key string) (commentID, taskID string, ok bool) {
	idx := lastIndex(key, ":c:")
	if idx < 0 || !startsWith(key, "t:") {
		return "", "", false
	}
	taskID = key[2:idx]
	commentID = key[idx+3:]
	if taskID == "" || commentID == "" {
		return "", "", false
	}
	return commentID, taskID, true
}

func lastIndex(s, sep string) int {
	for i := len(s) - len(sep); i >= 0; i-- {
		if s[i:i+len(sep)] == sep {
			return i
		}
	}
	return -1
}

func (f *fakeStore) SaveSubtask(parentID, taskID string) error {
	if f.subtasks[parentID] == nil {
		f.subtasks[parentID] = map[string]struct{}{}
	}
	f.subtasks[parentID][taskID] = struct{}{}
	return nil
}

func (f *fakeStore) GetSubtaskIDs(parentID string) ([]string, error) {
	var ids []string
	for id := range f.subtasks[parentID] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (f *fakeStore) SaveComment(taskID string, comment model.Comment) error {
	f.comments[taskID] = append(f.comments[taskID], comment)
	return nil
}

// GetComment returns the comment with commentID on taskID, or nil. The fake
// store keeps comments in memory so JSON never fails to decode; this method
// still mirrors the production read so ListComments is exercised faithfully.
// An id listed in skipComments simulates a corrupt payload (returns nil) so the
// service's defensive skip can be tested.
func (f *fakeStore) GetComment(taskID, commentID string) (*model.Comment, error) {
	if err, corrupt := f.corruptErr[commentID]; corrupt {
		return nil, err
	}
	if _, skip := f.skipComments[commentID]; skip {
		return nil, nil
	}
	for _, c := range f.comments[taskID] {
		if c.ID == commentID {
			out := c
			return &out, nil
		}
	}
	return nil, nil
}

func (f *fakeStore) GetCommentIDs(taskID string) ([]string, error) {
	var ids []string
	for _, c := range f.comments[taskID] {
		ids = append(ids, c.ID)
	}
	return ids, nil
}

func (f *fakeStore) SaveReminder(taskID string, value model.ReminderMetadata) error {
	f.reminders[taskID] = value
	return nil
}

func (f *fakeStore) GetReminder(taskID string) (*model.ReminderMetadata, error) {
	r, ok := f.reminders[taskID]
	if !ok {
		return nil, nil
	}
	return &r, nil
}

func (f *fakeStore) DeleteReminder(taskID string) error {
	delete(f.reminders, taskID)
	return nil
}

func (f *fakeStore) ListReminderKeys() ([]string, error) {
	var keys []string
	for k := range f.reminders {
		keys = append(keys, kvstore.ReminderKey(k))
	}
	return keys, nil
}

// ListTaskIDsByPrefix mimics the production store by scanning the global index
// and decoding task ids from the index keys. It supports the prefixes the
// service uses (idx:u:{u}:assigned:, idx:u:{u}:created:, idx:ch:{c}:task:,
// idx:all:task:, idx:t:{p}:sub:).
func (f *fakeStore) ListTaskIDsByPrefix(prefix string) ([]string, error) {
	var ids []string
	for key := range f.indexes {
		if !startsWith(key, prefix) {
			continue
		}
		id := key[len(prefix):]
		if id == "" {
			continue
		}
		// Self-heal: skip stale markers pointing to deleted tasks.
		if _, ok := f.tasks[id]; !ok {
			delete(f.indexes, key)
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (f *fakeStore) ListUserAssignedTaskIDs(userID string) ([]string, error) {
	return f.ListTaskIDsByPrefix("idx:u:" + userID + ":assigned:")
}

func (f *fakeStore) ListUserCreatedTaskIDs(userID string) ([]string, error) {
	return f.ListTaskIDsByPrefix("idx:u:" + userID + ":created:")
}

func (f *fakeStore) ListChannelTaskIDs(channelID string) ([]string, error) {
	return f.ListTaskIDsByPrefix("idx:ch:" + channelID + ":task:")
}

func (f *fakeStore) ListAllTaskIDs() ([]string, error) {
	return f.ListTaskIDsByPrefix("idx:all:task:")
}

func (f *fakeStore) SetAtomicWithRetries(key string, update func(old []byte) (any, error)) error {
	return nil // not exercised by the task service
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// fixedNow seeds nowFunc for deterministic timestamps.
func fixedNow(ms int64) func() int64 { return func() int64 { return ms } }

func TestCreate_Validation(t *testing.T) {
	svc := NewService(newFakeStore())

	_, err := svc.Create(CreateInput{CreatorID: "u1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "summary is required")

	_, err = svc.Create(CreateInput{Summary: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creator id is required")
}

func TestCreate_WritesIndexes(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	origNow := nowFunc
	nowFunc = fixedNow(1_700_000_000_000)
	defer func() { nowFunc = origNow }()

	due := int64(1_700_010_000_000)
	created, err := svc.Create(CreateInput{
		Summary:    "Review PR",
		ChannelID:  "ch1",
		CreatorID:  "u1",
		AssigneeID: "u2",
		Due:        &due,
	})
	require.NoError(t, err)

	// Entity persisted.
	got, err := svc.Get(created.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Review PR", got.Summary)
	assert.Equal(t, model.StatusTodo, got.Status)
	assert.NotEmpty(t, got.OrderKey)
	assert.Equal(t, int64(1_700_000_000_000), got.CreatedAt)

	// All four indexes present.
	assert.Contains(t, store.indexes, kvstore.UserAssignedKey("u2", created.ID))
	assert.Contains(t, store.indexes, kvstore.UserCreatedKey("u1", created.ID))
	assert.Contains(t, store.indexes, kvstore.ChannelTaskKey("ch1", created.ID))
	assert.Contains(t, store.indexes, kvstore.AllTasksKey(created.ID))
}

func TestCreate_PersonalTask_NoChannelIndex(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	created, err := svc.Create(CreateInput{Summary: "Personal", CreatorID: "u1"})
	require.NoError(t, err)

	assert.NotContains(t, store.indexes, kvstore.ChannelTaskKey("", created.ID))
	assert.Contains(t, store.indexes, kvstore.AllTasksKey(created.ID))
}

func TestCreate_SubtaskWritesMembershipAndInheritsNothing(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	parent, err := svc.Create(CreateInput{Summary: "parent", CreatorID: "u1"})
	require.NoError(t, err)

	child, err := svc.Create(CreateInput{Summary: "child", CreatorID: "u1", ParentTaskID: parent.ID})
	require.NoError(t, err)

	assert.Contains(t, store.subtasks[parent.ID], child.ID)
}

// Issue #20: a subtask inherits ChannelID and the parent's assignee as its
// default assignee. The subtask membership index edge is also written.
func TestCreate_SubtaskInheritsChannelAndAssignee(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	parent, err := svc.Create(CreateInput{
		Summary: "parent", CreatorID: "u1", AssigneeID: "u2", ChannelID: "ch1",
	})
	require.NoError(t, err)

	child, err := svc.Create(CreateInput{
		Summary: "child", CreatorID: "u3", ParentTaskID: parent.ID,
	})
	require.NoError(t, err)
	assert.Equal(t, "ch1", child.ChannelID, "subtask inherits parent ChannelID")
	assert.Equal(t, "u2", child.AssigneeID, "subtask default assignee is parent's assignee")
	assert.Equal(t, parent.ID, child.ParentTaskID)
	assert.Contains(t, store.subtasks[parent.ID], child.ID)
}

// Issue #20: an explicit assignee on subtask creation overrides the inherited
// default.
func TestCreate_SubtaskExplicitAssigneeOverridesInherited(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	parent, err := svc.Create(CreateInput{
		Summary: "parent", CreatorID: "u1", AssigneeID: "u2", ChannelID: "ch1",
	})
	require.NoError(t, err)

	child, err := svc.Create(CreateInput{
		Summary: "child", CreatorID: "u3", AssigneeID: "u9", ParentTaskID: parent.ID,
	})
	require.NoError(t, err)
	assert.Equal(t, "u9", child.AssigneeID, "explicit assignee wins over inherited")
	assert.Equal(t, "ch1", child.ChannelID, "channel still inherited")
}

// Issue #20: a subtask referencing a non-existent parent is rejected.
func TestCreate_SubtaskMissingParentRejected(t *testing.T) {
	svc := NewService(newFakeStore())
	_, err := svc.Create(CreateInput{Summary: "orphan", CreatorID: "u1", ParentTaskID: "ghost"})
	assert.ErrorIs(t, err, ErrParentNotFound)
}

// Issue #21: CreateSubtask is a thin wrapper over Create with inheritance.
func TestCreateSubtask_InheritsFromParent(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	parent, err := svc.Create(CreateInput{
		Summary: "parent", CreatorID: "u1", AssigneeID: "u2", ChannelID: "ch1",
	})
	require.NoError(t, err)

	child, err := svc.CreateSubtask(parent.ID, "u3", "child", "", nil)
	require.NoError(t, err)
	assert.Equal(t, parent.ID, child.ParentTaskID)
	assert.Equal(t, "ch1", child.ChannelID)
	assert.Equal(t, "u2", child.AssigneeID, "inherits parent assignee")
}

func TestCreateSubtask_MissingParentRejected(t *testing.T) {
	svc := NewService(newFakeStore())
	_, err := svc.CreateSubtask("ghost", "u1", "child", "", nil)
	assert.ErrorIs(t, err, ErrParentNotFound)
}

// Issue #21: ListSubtasks returns direct subtasks in creation order.
func TestListSubtasks_ReturnsDirectSubtasksInOrder(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	parent, err := svc.Create(CreateInput{Summary: "parent", CreatorID: "u1"})
	require.NoError(t, err)
	c1, err := svc.CreateSubtask(parent.ID, "u1", "first", "", nil)
	require.NoError(t, err)
	c2, err := svc.CreateSubtask(parent.ID, "u1", "second", "", nil)
	require.NoError(t, err)

	subs, err := svc.ListSubtasks(parent.ID)
	require.NoError(t, err)
	require.Len(t, subs, 2)
	assert.Equal(t, c1.ID, subs[0].ID)
	assert.Equal(t, c2.ID, subs[1].ID)

	// A task with no subtasks returns an empty list.
	empty, err := svc.ListSubtasks(c1.ID)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestPatch_PartialUpdate(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	origNow := nowFunc
	nowFunc = fixedNow(1_000)
	defer func() { nowFunc = origNow }()

	created, err := svc.Create(CreateInput{Summary: "old", Description: "d", CreatorID: "u1"})
	require.NoError(t, err)

	newSummary := "new summary"
	patched, err := svc.Patch(created.ID, PatchInput{
		UpdateFields: []string{"summary"},
		Summary:      &newSummary,
	})
	require.NoError(t, err)
	assert.Equal(t, "new summary", patched.Summary)
	assert.Equal(t, "d", patched.Description, "untouched field preserved")
	assert.True(t, patched.UpdatedAt >= 1000)
}

func TestPatch_ClearDue(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	due := int64(123)
	created, err := svc.Create(CreateInput{Summary: "x", CreatorID: "u1", Due: &due})
	require.NoError(t, err)
	require.NotNil(t, created.Due)

	patched, err := svc.Patch(created.ID, PatchInput{UpdateFields: []string{"due"}, Due: nil})
	require.NoError(t, err)
	assert.Nil(t, patched.Due)
}

func TestPatch_NotFound(t *testing.T) {
	svc := NewService(newFakeStore())
	_, err := svc.Patch("nope", PatchInput{UpdateFields: []string{"summary"}})
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestDelete_CascadeRemovesSubtasksCommentsIndexes(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	parent, err := svc.Create(CreateInput{Summary: "p", CreatorID: "u1", AssigneeID: "u2", ChannelID: "ch1"})
	require.NoError(t, err)
	child, err := svc.Create(CreateInput{Summary: "c", CreatorID: "u1", ParentTaskID: parent.ID})
	require.NoError(t, err)
	require.NoError(t, store.SaveComment(parent.ID, model.Comment{ID: "cmt1", Content: "hi"}))

	require.NoError(t, svc.Delete(parent.ID))

	// Parent gone.
	got, err := svc.Get(parent.ID)
	require.NoError(t, err)
	assert.Nil(t, got)
	// Child gone too.
	gotChild, err := svc.Get(child.ID)
	require.NoError(t, err)
	assert.Nil(t, gotChild)
	// Indexes removed by full known key.
	assert.NotContains(t, store.indexes, kvstore.AllTasksKey(parent.ID))
	assert.NotContains(t, store.indexes, kvstore.UserAssignedKey("u2", parent.ID))
	assert.NotContains(t, store.indexes, kvstore.UserCreatedKey("u1", parent.ID))
	assert.NotContains(t, store.indexes, kvstore.ChannelTaskKey("ch1", parent.ID))
	// Comments removed.
	assert.Empty(t, store.comments[parent.ID])
}

func TestDelete_NotFound(t *testing.T) {
	svc := NewService(newFakeStore())
	err := svc.Delete("nope")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestList_ScopeMine(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	// u2 owns two tasks; u3 owns one.
	_, err := svc.Create(CreateInput{Summary: "a", CreatorID: "u1", AssigneeID: "u2"})
	require.NoError(t, err)
	_, err = svc.Create(CreateInput{Summary: "b", CreatorID: "u1", AssigneeID: "u2"})
	require.NoError(t, err)
	_, err = svc.Create(CreateInput{Summary: "c", CreatorID: "u1", AssigneeID: "u3"})
	require.NoError(t, err)

	tasks, err := svc.List(ListQuery{Scope: ScopeMine, UserID: "u2"})
	require.NoError(t, err)
	assert.Len(t, tasks, 2)
}

func TestList_StatusFilter(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	t1, err := svc.Create(CreateInput{Summary: "t1", CreatorID: "u1", AssigneeID: "u2"})
	require.NoError(t, err)
	t2, err := svc.Create(CreateInput{Summary: "t2", CreatorID: "u1", AssigneeID: "u2"})
	require.NoError(t, err)
	require.NotNil(t, t1)

	// Mark t2 done by editing the stored task directly.
	stored := store.tasks[t2.ID]
	stored.Status = model.StatusDone
	stored.CompletedAt = ptrInt64(1)
	store.tasks[t2.ID] = stored

	tasks, err := svc.List(ListQuery{Scope: ScopeMine, UserID: "u2", Status: model.StatusDone})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, t2.ID, tasks[0].ID)
}

func TestList_CursorPagination(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	// Create 3 tasks; their OrderKeys are n, n0, n00 (monotonic).
	a, err := svc.Create(CreateInput{Summary: "a", CreatorID: "u1", AssigneeID: "u2"})
	require.NoError(t, err)
	b, err := svc.Create(CreateInput{Summary: "b", CreatorID: "u1", AssigneeID: "u2"})
	require.NoError(t, err)
	c, err := svc.Create(CreateInput{Summary: "c", CreatorID: "u1", AssigneeID: "u2"})
	require.NoError(t, err)

	// Page 1: limit 2 -> a, b.
	page1, err := svc.List(ListQuery{Scope: ScopeMine, UserID: "u2", Limit: 2})
	require.NoError(t, err)
	require.Len(t, page1, 2)
	assert.Equal(t, a.ID, page1[0].ID)
	assert.Equal(t, b.ID, page1[1].ID)

	// Page 2: after b's order key -> c.
	page2, err := svc.List(ListQuery{Scope: ScopeMine, UserID: "u2", Limit: 2, AfterOrderKey: page1[1].OrderKey})
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, c.ID, page2[0].ID)
}

func TestSearch_MatchesSummaryOrDescription(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	_, err := svc.Create(CreateInput{Summary: "Fix login bug", CreatorID: "u1"})
	require.NoError(t, err)
	_, err = svc.Create(CreateInput{Summary: "unrelated", Description: "discuss the LOGIN flow", CreatorID: "u1"})
	require.NoError(t, err)
	_, err = svc.Create(CreateInput{Summary: "no match", CreatorID: "u1"})
	require.NoError(t, err)

	results, err := svc.Search("login", 10)
	require.NoError(t, err)
	require.Len(t, results, 2)

	empty, err := svc.Search("", 10)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestSetStatus_InvalidStatus(t *testing.T) {
	svc := NewService(newFakeStore())
	_, err := svc.SetStatus("any", "bogus")
	assert.ErrorIs(t, err, ErrInvalidStatus)
}

func TestSetStatus_NotFound(t *testing.T) {
	svc := NewService(newFakeStore())
	_, err := svc.SetStatus("nope", model.StatusDone)
	assert.ErrorIs(t, err, ErrNotFound)
}

// Issue #22: a parent cannot be marked done while it has open subtasks.
func TestSetStatus_ParentDoneBlockedByOpenSubtask(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	parent, err := svc.Create(CreateInput{Summary: "parent", CreatorID: "u1"})
	require.NoError(t, err)
	openSub, err := svc.Create(CreateInput{Summary: "still open", CreatorID: "u1", ParentTaskID: parent.ID})
	require.NoError(t, err)
	_ = openSub

	_, err = svc.SetStatus(parent.ID, model.StatusDone)
	require.Error(t, err)
	var blocked ErrOpenSubtasks
	require.ErrorAs(t, err, &blocked)
	require.Len(t, blocked.Open, 1)
	assert.Equal(t, "still open", blocked.Open[0])
	assert.Contains(t, err.Error(), "still open", "error lists the open subtask")

	// Parent status unchanged after the rejection.
	got, _ := svc.Get(parent.ID)
	assert.Equal(t, model.StatusTodo, got.Status)
}

// Issue #22: once all subtasks are terminal, the parent can be marked done.
func TestSetStatus_ParentDoneAllowedWhenSubtasksTerminal(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	parent, err := svc.Create(CreateInput{Summary: "parent", CreatorID: "u1"})
	require.NoError(t, err)
	doneSub, err := svc.Create(CreateInput{Summary: "done", CreatorID: "u1", ParentTaskID: parent.ID})
	require.NoError(t, err)
	cancelledSub, err := svc.Create(CreateInput{Summary: "cancelled", CreatorID: "u1", ParentTaskID: parent.ID})
	require.NoError(t, err)
	_, err = svc.SetStatus(doneSub.ID, model.StatusDone)
	require.NoError(t, err)
	_, err = svc.SetStatus(cancelledSub.ID, model.StatusCancelled)
	require.NoError(t, err)

	updated, err := svc.SetStatus(parent.ID, model.StatusDone)
	require.NoError(t, err)
	assert.Equal(t, model.StatusDone, updated.Status)
}

// Issue #22: cancelling a parent cascade-cancels open subtasks (covered by an
// existing test); this one asserts the notification contract is the caller's
// job — the service sends no per-subtask notification by itself.
func TestSetStatus_CancelCascadeIsSilentInService(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	parent, err := svc.Create(CreateInput{Summary: "parent", CreatorID: "u1"})
	require.NoError(t, err)
	open, err := svc.Create(CreateInput{Summary: "open", CreatorID: "u1", ParentTaskID: parent.ID})
	require.NoError(t, err)

	updated, err := svc.SetStatus(parent.ID, model.StatusCancelled)
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, updated.Status)
	// The open subtask is cascade-cancelled.
	got, _ := svc.Get(open.ID)
	assert.Equal(t, model.StatusCancelled, got.Status)
}

// Issue #22: nesting beyond maxSubtaskDepth is rejected (cycle/depth guard).
func TestCreate_SubtaskDepthCapRejected(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	// Build a chain at the depth cap: root <- c1 <- ... <- cN.
	root, err := svc.Create(CreateInput{Summary: "root", CreatorID: "u1"})
	require.NoError(t, err)
	cur := root
	for i := 0; i < maxSubtaskDepth; i++ {
		child, err := svc.Create(CreateInput{Summary: "child", CreatorID: "u1", ParentTaskID: cur.ID})
		require.NoErrorf(t, err, "nest level %d should be allowed", i+1)
		cur = child
	}
	// One more level exceeds the cap → rejected as a cycle/depth error.
	_, err = svc.Create(CreateInput{Summary: "too deep", CreatorID: "u1", ParentTaskID: cur.ID})
	assert.ErrorIs(t, err, ErrSubtaskCycle)
}

// Issue #22: an explicit cycle in the stored chain is detected by assertNoCycle.
func TestAssertNoCycle_DetectsLoop(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	// Manually craft a cycle A -> B -> A at the store level.
	store.tasks["A"] = model.Task{ID: "A", ParentTaskID: "B"}
	store.tasks["B"] = model.Task{ID: "B", ParentTaskID: "A"}
	err := svc.assertNoCycle("A", maxSubtaskDepth)
	assert.ErrorIs(t, err, ErrSubtaskCycle)
}

func TestSetStatus_DoneStampsCompletedAtClearsCancelled(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	origNow := nowFunc
	nowFunc = fixedNow(5_000)
	defer func() { nowFunc = origNow }()

	created, err := svc.Create(CreateInput{Summary: "x", CreatorID: "u1"})
	require.NoError(t, err)

	// Pre-set CancelledAt to confirm it gets cleared on done.
	stored := store.tasks[created.ID]
	stored.CancelledAt = ptrInt64(1)
	store.tasks[created.ID] = stored

	nowFunc = fixedNow(9_000)
	updated, err := svc.SetStatus(created.ID, model.StatusDone)
	require.NoError(t, err)
	require.NotNil(t, updated.CompletedAt)
	assert.Nil(t, updated.CancelledAt)
	assert.Equal(t, int64(9_000), *updated.CompletedAt)
	assert.Equal(t, model.StatusDone, updated.Status)
}

func TestSetStatus_TodoClearsTimestamps(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	created, err := svc.Create(CreateInput{Summary: "x", CreatorID: "u1"})
	require.NoError(t, err)
	_, err = svc.SetStatus(created.ID, model.StatusDone)
	require.NoError(t, err)

	updated, err := svc.SetStatus(created.ID, model.StatusTodo)
	require.NoError(t, err)
	assert.Nil(t, updated.CompletedAt)
	assert.Nil(t, updated.CancelledAt)
	assert.Equal(t, model.StatusTodo, updated.Status)
}

func TestSetStatus_TerminalStopsReminder(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	created, err := svc.Create(CreateInput{Summary: "x", CreatorID: "u1"})
	require.NoError(t, err)
	require.NoError(t, store.SaveReminder(created.ID, model.ReminderMetadata{DueMS: 1_000_000, OffsetMS: 60_000, AssigneeID: "u1"}))
	require.Contains(t, store.reminders, created.ID)

	_, err = svc.SetStatus(created.ID, model.StatusDone)
	require.NoError(t, err)
	assert.NotContains(t, store.reminders, created.ID, "done removes reminder edge")

	require.NoError(t, store.SaveReminder(created.ID, model.ReminderMetadata{DueMS: 1_000_000, OffsetMS: 60_000, AssigneeID: "u1"}))
	_, err = svc.SetStatus(created.ID, model.StatusCancelled)
	require.NoError(t, err)
	assert.NotContains(t, store.reminders, created.ID, "cancelled removes reminder edge")
}

func TestSetStatus_CancelCascadesToOpenSubtasks(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	parent, err := svc.Create(CreateInput{Summary: "parent", CreatorID: "u1"})
	require.NoError(t, err)
	openSub, err := svc.Create(CreateInput{Summary: "open", CreatorID: "u1", ParentTaskID: parent.ID})
	require.NoError(t, err)
	doneSub, err := svc.Create(CreateInput{Summary: "done", CreatorID: "u1", ParentTaskID: parent.ID})
	require.NoError(t, err)
	// Mark one subtask already done — it must not be re-cancelled.
	_, err = svc.SetStatus(doneSub.ID, model.StatusDone)
	require.NoError(t, err)

	_, err = svc.SetStatus(parent.ID, model.StatusCancelled)
	require.NoError(t, err)

	open, _ := svc.Get(openSub.ID)
	require.NotNil(t, open)
	assert.Equal(t, model.StatusCancelled, open.Status, "open subtask cascade-cancelled")

	done, _ := svc.Get(doneSub.ID)
	require.NotNil(t, done)
	assert.Equal(t, model.StatusDone, done.Status, "already-done subtask left untouched")
}

func TestSetStatus_NoOpWhenUnchanged(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	created, err := svc.Create(CreateInput{Summary: "x", CreatorID: "u1"})
	require.NoError(t, err)

	updated, err := svc.SetStatus(created.ID, model.StatusTodo)
	require.NoError(t, err)
	assert.Equal(t, created.UpdatedAt, updated.UpdatedAt, "no rewrite when status unchanged")
}

func TestAssign_SwapsIndexEdges(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	created, err := svc.Create(CreateInput{Summary: "x", CreatorID: "u1", AssigneeID: "u-old"})
	require.NoError(t, err)
	assert.Contains(t, store.indexes, kvstore.UserAssignedKey("u-old", created.ID))

	updated, ev, err := svc.Assign(created.ID, "u-new")
	require.NoError(t, err)
	assert.Equal(t, "u-new", updated.AssigneeID)
	assert.Equal(t, "u-old", ev.OldAssigneeID)
	assert.Equal(t, "u-new", ev.NewAssigneeID)

	// Old edge removed, new edge added.
	assert.NotContains(t, store.indexes, kvstore.UserAssignedKey("u-old", created.ID))
	assert.Contains(t, store.indexes, kvstore.UserAssignedKey("u-new", created.ID))
}

func TestAssign_FromUnassigned(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	created, err := svc.Create(CreateInput{Summary: "x", CreatorID: "u1"})
	require.NoError(t, err)
	require.Empty(t, created.AssigneeID)

	_, ev, err := svc.Assign(created.ID, "u-new")
	require.NoError(t, err)
	assert.Empty(t, ev.OldAssigneeID)
	assert.Equal(t, "u-new", ev.NewAssigneeID)
	assert.Contains(t, store.indexes, kvstore.UserAssignedKey("u-new", created.ID))
}

func TestAssign_ClearRemovesEdge(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	created, err := svc.Create(CreateInput{Summary: "x", CreatorID: "u1", AssigneeID: "u-old"})
	require.NoError(t, err)

	updated, _, err := svc.Assign(created.ID, "")
	require.NoError(t, err)
	assert.Empty(t, updated.AssigneeID)
	assert.NotContains(t, store.indexes, kvstore.UserAssignedKey("u-old", created.ID))
}

func TestAssign_NoOpSameAssignee(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	created, err := svc.Create(CreateInput{Summary: "x", CreatorID: "u1", AssigneeID: "u-old"})
	require.NoError(t, err)
	originalUpdated := store.tasks[created.ID].UpdatedAt

	_, ev, err := svc.Assign(created.ID, "u-old")
	require.NoError(t, err)
	assert.Equal(t, "u-old", ev.OldAssigneeID)
	assert.Equal(t, "u-old", ev.NewAssigneeID)
	// Index untouched, UpdatedAt unchanged.
	assert.Contains(t, store.indexes, kvstore.UserAssignedKey("u-old", created.ID))
	assert.Equal(t, originalUpdated, store.tasks[created.ID].UpdatedAt)
}

func TestAssign_NotFound(t *testing.T) {
	svc := NewService(newFakeStore())
	_, _, err := svc.Assign("nope", "u-new")
	assert.ErrorIs(t, err, ErrNotFound)
}

func ptrInt64(v int64) *int64 { return &v }

// --- Comments (issue #23) ---

func TestListComments_ReturnsCommentsInCreationOrder(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	require.NoError(t, store.SaveComment("t1", model.Comment{ID: "01A", UserID: "u1", Content: "first"}))
	require.NoError(t, store.SaveComment("t1", model.Comment{ID: "02B", UserID: "u2", Content: "second"}))

	comments, err := svc.ListComments("t1")
	require.NoError(t, err)
	require.Len(t, comments, 2)
	assert.Equal(t, "01A", comments[0].ID)
	assert.Equal(t, "02B", comments[1].ID)
}

// Issue #23: a comment whose stored JSON fails to deserialize is skipped rather
// than failing the whole list. Simulates a corrupt payload by making the store
// return a decode error for that id (matching what the production store returns
// when KV.Get can't unmarshal the bytes).
func TestListComments_SkipsCorruptComment(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	require.NoError(t, store.SaveComment("t1", model.Comment{ID: "01A", Content: "good"}))
	require.NoError(t, store.SaveComment("t1", model.Comment{ID: "02B", Content: "corrupt"}))
	require.NoError(t, store.SaveComment("t1", model.Comment{ID: "03C", Content: "also good"}))
	// Mark 02B as corrupt: GetComment returns a decode error for it.
	store.corruptErr["02B"] = errors.New("invalid character 'z' looking for beginning of value")

	comments, err := svc.ListComments("t1")
	require.NoError(t, err)
	require.Len(t, comments, 2, "corrupt comment skipped, not fatal")
	assert.Equal(t, "01A", comments[0].ID)
	assert.Equal(t, "03C", comments[1].ID)
}

func TestListComments_EmptyWhenNone(t *testing.T) {
	svc := NewService(newFakeStore())
	comments, err := svc.ListComments("none")
	require.NoError(t, err)
	assert.Empty(t, comments)
}

// Issue #24: AddComment persists a comment and returns a notification event.
func TestAddComment_PersistsAndReturnsEvent(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	parent, err := svc.Create(CreateInput{Summary: "task", CreatorID: "u1", AssigneeID: "u2"})
	require.NoError(t, err)

	comment, ev, err := svc.AddComment(parent.ID, "u3", "nice work")
	require.NoError(t, err)
	assert.Equal(t, "nice work", comment.Content)
	assert.Equal(t, "u3", comment.UserID)
	assert.NotEmpty(t, comment.ID)
	assert.Equal(t, parent.ID, ev.TaskID)
	assert.Equal(t, "u1", ev.CreatorID)
	assert.Equal(t, "u2", ev.AssigneeID, "event carries participants for notification")

	// Stored and visible via ListComments.
	comments, err := svc.ListComments(parent.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, comment.ID, comments[0].ID)
}

func TestAddComment_EmptyContentRejected(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	parent, err := svc.Create(CreateInput{Summary: "task", CreatorID: "u1"})
	require.NoError(t, err)

	_, _, err = svc.AddComment(parent.ID, "u1", "   ")
	assert.Error(t, err)
}

func TestAddComment_TaskNotFound(t *testing.T) {
	svc := NewService(newFakeStore())
	_, _, err := svc.AddComment("ghost", "u1", "hi")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestSubtaskProgress(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	parent, err := svc.Create(CreateInput{Summary: "parent", CreatorID: "u1"})
	require.NoError(t, err)
	open, err := svc.Create(CreateInput{Summary: "open", CreatorID: "u1", ParentTaskID: parent.ID})
	require.NoError(t, err)
	done, err := svc.Create(CreateInput{Summary: "done", CreatorID: "u1", ParentTaskID: parent.ID})
	require.NoError(t, err)
	cancelled, err := svc.Create(CreateInput{Summary: "cancelled", CreatorID: "u1", ParentTaskID: parent.ID})
	require.NoError(t, err)

	_, err = svc.SetStatus(done.ID, model.StatusDone)
	require.NoError(t, err)
	_, err = svc.SetStatus(cancelled.ID, model.StatusCancelled)
	require.NoError(t, err)

	d, total, err := svc.SubtaskProgress(parent.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, d, "done+cancelled are terminal")
	assert.Equal(t, 3, total)

	// A task with no subtasks reports 0/0.
	d2, total2, err := svc.SubtaskProgress(open.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, d2)
	assert.Equal(t, 0, total2)
}

// --- Reminder subsystem ---

func TestCreate_SeedsReminderIndexWhenDueAndOffset(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	origNow := nowFunc
	nowFunc = fixedNow(1_000)
	defer func() { nowFunc = origNow }()

	due := int64(100_000)
	offset := int64(60_000)
	created, err := svc.Create(CreateInput{
		Summary: "x", CreatorID: "u1", AssigneeID: "u2",
		Due: &due, ReminderOffset: &offset,
	})
	require.NoError(t, err)

	meta, err := store.GetReminder(created.ID)
	require.NoError(t, err)
	require.NotNil(t, meta)
	assert.Equal(t, due, meta.DueMS)
	assert.Equal(t, offset, meta.OffsetMS)
	assert.Equal(t, "u2", meta.AssigneeID)
}

func TestCreate_NoReminderWhenNoDue(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	offset := int64(60_000)
	created, err := svc.Create(CreateInput{Summary: "x", CreatorID: "u1", ReminderOffset: &offset})
	require.NoError(t, err)

	meta, err := store.GetReminder(created.ID)
	require.NoError(t, err)
	assert.Nil(t, meta, "no reminder without a due date")
}

func TestSetReminder_BuildsIndex(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	due := int64(100_000)
	created, err := svc.Create(CreateInput{Summary: "x", CreatorID: "u1", AssigneeID: "u2", Due: &due})
	require.NoError(t, err)

	updated, err := svc.SetReminder(created.ID, 30_000)
	require.NoError(t, err)
	require.NotNil(t, updated.ReminderOffset)
	assert.Equal(t, int64(30_000), *updated.ReminderOffset)

	meta, err := store.GetReminder(created.ID)
	require.NoError(t, err)
	require.NotNil(t, meta)
	assert.Equal(t, int64(30_000), meta.OffsetMS)
}

func TestSetReminder_RequiresDue(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	created, err := svc.Create(CreateInput{Summary: "x", CreatorID: "u1"})
	require.NoError(t, err)

	_, err = svc.SetReminder(created.ID, 30_000)
	assert.ErrorIs(t, err, ErrReminderNeedsDue)
}

func TestSetReminder_InvalidOffset(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	due := int64(100_000)
	created, err := svc.Create(CreateInput{Summary: "x", CreatorID: "u1", Due: &due})
	require.NoError(t, err)

	_, err = svc.SetReminder(created.ID, 0)
	assert.Error(t, err)
}

func TestClearReminder_RemovesIndex(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	due := int64(100_000)
	offset := int64(60_000)
	created, err := svc.Create(CreateInput{Summary: "x", CreatorID: "u1", Due: &due, ReminderOffset: &offset})
	require.NoError(t, err)
	require.NoError(t, store.SaveReminder(created.ID, model.ReminderMetadata{DueMS: due, OffsetMS: offset, AssigneeID: "u2"}))

	updated, err := svc.ClearReminder(created.ID)
	require.NoError(t, err)
	assert.Nil(t, updated.ReminderOffset)

	meta, err := store.GetReminder(created.ID)
	require.NoError(t, err)
	assert.Nil(t, meta)
}

func TestSetStatus_DoneDropsReminder_TodoRebuilds(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	due := int64(100_000)
	offset := int64(60_000)
	created, err := svc.Create(CreateInput{Summary: "x", CreatorID: "u1", Due: &due, ReminderOffset: &offset})
	require.NoError(t, err)

	// done -> reminder dropped.
	_, err = svc.SetStatus(created.ID, model.StatusDone)
	require.NoError(t, err)
	meta, _ := store.GetReminder(created.ID)
	assert.Nil(t, meta)

	// Reopen -> reminder rebuilt (still has due+offset, not fired).
	_, err = svc.SetStatus(created.ID, model.StatusTodo)
	require.NoError(t, err)
	meta, _ = store.GetReminder(created.ID)
	require.NotNil(t, meta)
}

func TestFireReadyReminders_WithinWindow(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	// due=100000, offset=60000 -> fires at 40000.
	require.NoError(t, store.SaveTask(model.Task{ID: "T1", Status: model.StatusTodo}))
	require.NoError(t, store.SaveReminder("T1", model.ReminderMetadata{
		DueMS: 100_000, OffsetMS: 60_000, AssigneeID: "u1",
	}))

	due, err := svc.FireReadyReminders(50_000, time.Minute)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, "T1", due[0].TaskID)
	assert.Equal(t, "u1", due[0].AssigneeID)
}

func TestFireReadyReminders_NotYetDue(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	require.NoError(t, store.SaveTask(model.Task{ID: "T1", Status: model.StatusTodo}))
	require.NoError(t, store.SaveReminder("T1", model.ReminderMetadata{
		DueMS: 100_000, OffsetMS: 60_000, AssigneeID: "u1",
	}))

	due, err := svc.FireReadyReminders(10_000, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, due)
}

func TestFireReadyReminders_SelfHealsTerminalAndOrphanEdges(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)

	t.Run("terminal task edge dropped without firing", func(t *testing.T) {
		require.NoError(t, store.SaveTask(model.Task{ID: "T1", Status: model.StatusDone}))
		require.NoError(t, store.SaveReminder("T1", model.ReminderMetadata{
			DueMS: 100_000, OffsetMS: 60_000, AssigneeID: "u1",
		}))
		due, err := svc.FireReadyReminders(50_000, time.Minute)
		require.NoError(t, err)
		assert.Empty(t, due, "terminal task must not fire")
		meta, _ := store.GetReminder("T1")
		assert.Nil(t, meta, "stale edge cleaned up")
	})

	t.Run("orphan edge (task gone) dropped", func(t *testing.T) {
		require.NoError(t, store.SaveReminder("T2", model.ReminderMetadata{
			DueMS: 100_000, OffsetMS: 60_000, AssigneeID: "u1",
		}))
		due, err := svc.FireReadyReminders(50_000, time.Minute)
		require.NoError(t, err)
		assert.Empty(t, due)
		meta, _ := store.GetReminder("T2")
		assert.Nil(t, meta, "orphan edge cleaned up")
	})
}

func TestFireReadyReminders_PastGraceDroppedAndMarkedFired(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	// Seed a task entity so MarkReminderFired can stamp it.
	require.NoError(t, store.SaveTask(model.Task{ID: "T1", Status: model.StatusTodo}))

	require.NoError(t, store.SaveReminder("T1", model.ReminderMetadata{
		DueMS: 100_000, OffsetMS: 60_000, AssigneeID: "u1",
	}))

	// now well beyond due+grace (100000 + 60000ms).
	due, err := svc.FireReadyReminders(200_000, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, due, "missed reminder dropped, not fired")

	meta, _ := store.GetReminder("T1")
	assert.Nil(t, meta, "edge dropped")
	task, _ := store.GetTask("T1")
	require.NotNil(t, task)
	assert.True(t, task.ReminderFired)
}

func TestFireReadyReminders_NoAssigneeSkipped(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store)
	require.NoError(t, store.SaveTask(model.Task{ID: "T1", Status: model.StatusTodo}))
	require.NoError(t, store.SaveReminder("T1", model.ReminderMetadata{
		DueMS: 100_000, OffsetMS: 60_000, AssigneeID: "",
	}))

	due, err := svc.FireReadyReminders(50_000, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, due)
}
