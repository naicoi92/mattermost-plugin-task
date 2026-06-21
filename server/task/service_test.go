package task

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/store"
	"github.com/naicoi92/mattermost-plugin-task/server/store/sqlstore"

	_ "modernc.org/sqlite"
)

// testDBCounter allocates a unique in-memory sqlite DB name per test.
var testDBCounter atomic.Int64

// newTestStore opens an isolated in-memory sqlite SQLStore with migrations
// applied. The service under test wraps this real store so WithTx atomicity is
// exercised truthfully rather than against a hand-rolled fake that would drift.
func newTestStore(t *testing.T) store.Store {
	t.Helper()
	id := testDBCounter.Add(1)
	dsn := fmt.Sprintf("file:svctestdb%d?mode=memory&cache=shared&_pragma=foreign_keys(1)", id)
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	s, err := sqlstore.New(db, sqlstore.DialectSQLite, "")
	require.NoError(t, err)
	require.NoError(t, s.RunMigrations(nil))
	return s
}

func newTestService(t *testing.T) (*Service, store.Store) {
	t.Helper()
	st := newTestStore(t)
	return NewService(st), st
}

var ulidCounter atomic.Int64

func nextTestULID() string {
	return fmt.Sprintf("TEST%020d", ulidCounter.Add(1))
}

func createTaskRow(t *testing.T, s store.Store, id, orderKey string, overrides ...func(*model.TaskRow)) model.TaskRow {
	t.Helper()
	ctx := context.Background()
	row := model.TaskRow{
		ID: id, Summary: "task " + id, Status: model.StatusTodo,
		OrderKey: orderKey, CreatedAt: 1_000, UpdatedAt: 1_000,
	}
	for _, o := range overrides {
		o(&row)
	}
	created, err := s.CreateTask(ctx, row)
	require.NoError(t, err)
	return created
}

func mustCreateTask(t *testing.T, svc *Service, in CreateInput) *model.Task {
	t.Helper()
	task, err := svc.Create(in)
	require.NoError(t, err)
	require.NotNil(t, task)
	return task
}

func ptrInt64(v int64) *int64 { return &v }

// --- Create ---

func TestCreate_Validation(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Create(CreateInput{})
	require.Error(t, err)
	_, err = svc.Create(CreateInput{Summary: "x"})
	require.Error(t, err)
}

func TestCreate_PersistsTaskAndCreatorMember(t *testing.T) {
	svc, s := newTestService(t)
	ctx := context.Background()
	task := mustCreateTask(t, svc, CreateInput{Summary: "Ship MVP", ChannelID: "ch1", CreatorID: "u1"})
	assert.Equal(t, "u1", task.CreatorID)
	creator, err := s.GetMemberByRole(ctx, task.ID, model.MemberRoleCreator)
	require.NoError(t, err)
	assert.Equal(t, "u1", creator)
}

func TestCreate_PersonalTaskHasEmptyChannel(t *testing.T) {
	svc, _ := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "personal", CreatorID: "u1"})
	assert.Equal(t, "", task.ChannelID)
}

func TestCreate_SubtaskInheritsChannelAndAssignee(t *testing.T) {
	svc, s := newTestService(t)
	ctx := context.Background()
	parent := mustCreateTask(t, svc, CreateInput{Summary: "parent", ChannelID: "ch-parent", CreatorID: "u-c", AssigneeID: "u-a"})
	sub := mustCreateTask(t, svc, CreateInput{Summary: "sub", CreatorID: "u-c", ParentTaskID: parent.ID})
	assert.Equal(t, "ch-parent", sub.ChannelID)
	assert.Equal(t, "u-a", sub.AssigneeID)
	subRow, err := s.GetTask(ctx, sub.ID)
	require.NoError(t, err)
	assert.Equal(t, parent.ID, subRow.ParentTaskID)
}

func TestCreate_SubtaskExplicitAssigneeOverridesInherited(t *testing.T) {
	svc, _ := newTestService(t)
	parent := mustCreateTask(t, svc, CreateInput{Summary: "p", CreatorID: "u-c", AssigneeID: "u-inherited"})
	sub := mustCreateTask(t, svc, CreateInput{Summary: "s", CreatorID: "u-c", AssigneeID: "u-explicit", ParentTaskID: parent.ID})
	assert.Equal(t, "u-explicit", sub.AssigneeID)
}

func TestCreate_SubtaskMissingParentRejected(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Create(CreateInput{Summary: "orphan", CreatorID: "u-c", ParentTaskID: "ghost"})
	require.ErrorIs(t, err, ErrParentNotFound)
}

func TestCreate_SubtaskDepthCapRejected(t *testing.T) {
	svc, _ := newTestService(t)
	// Build a chain of maxSubtaskDepth tasks (depth exactly at the cap, still
	// allowed). One more would exceed it.
	var parentID string
	for range maxSubtaskDepth {
		in := CreateInput{Summary: "n", CreatorID: "u-c"}
		if parentID != "" {
			in.ParentTaskID = parentID
		}
		parentID = mustCreateTask(t, svc, in).ID
	}
	// parentID is now at depth maxSubtaskDepth-1 (0-indexed root); adding a
	// child makes depth maxSubtaskDepth. That is still within the cap, so it
	// should succeed. To exceed, we chain one more first.
	parentID = mustCreateTask(t, svc, CreateInput{Summary: "edge", CreatorID: "u-c", ParentTaskID: parentID}).ID
	_, err := svc.Create(CreateInput{Summary: "too deep", CreatorID: "u-c", ParentTaskID: parentID})
	require.ErrorIs(t, err, ErrSubtaskCycle)
}

func TestCreate_SeedsReminderWhenDueAndOffset(t *testing.T) {
	svc, s := newTestService(t)
	ctx := context.Background()
	due := int64(2_000_000)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c", DueAt: &due, ReminderOffset: ptrInt64(60_000)})
	reminders, err := s.ListReminders(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, reminders, 1)
	assert.Equal(t, int64(60_000), reminders[0].OffsetMS)
}

func TestCreate_NoReminderWhenNoDue(t *testing.T) {
	svc, s := newTestService(t)
	ctx := context.Background()
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c", ReminderOffset: ptrInt64(60_000)})
	reminders, err := s.ListReminders(ctx, task.ID)
	require.NoError(t, err)
	assert.Empty(t, reminders)
}

// --- Get ---

func TestGet_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Get("ghost")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestGet_AssemblesRelations(t *testing.T) {
	svc, s := newTestService(t)
	ctx := context.Background()
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-creator", AssigneeID: "u-assignee", ChannelID: "ch1"})
	require.NoError(t, s.AddPost(ctx, nextTestULID(), task.ID, "post-ch", model.PostKindChannel))
	got, err := svc.Get(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "u-creator", got.CreatorID)
	assert.Equal(t, "u-assignee", got.AssigneeID)
	assert.Equal(t, "post-ch", got.ChannelPostID)
}

// --- ListSubtasks ---

func TestListSubtasks_ReturnsDirectSubtasks(t *testing.T) {
	svc, _ := newTestService(t)
	parent := mustCreateTask(t, svc, CreateInput{Summary: "p", CreatorID: "u-c"})
	mustCreateTask(t, svc, CreateInput{Summary: "s1", CreatorID: "u-c", ParentTaskID: parent.ID})
	mustCreateTask(t, svc, CreateInput{Summary: "s2", CreatorID: "u-c", ParentTaskID: parent.ID})
	subs, err := svc.ListSubtasks(parent.ID)
	require.NoError(t, err)
	assert.Len(t, subs, 2)
}

// --- Patch ---

func TestPatch_PartialUpdate(t *testing.T) {
	svc, _ := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "orig", Description: "d", CreatorID: "u-c"})
	newSummary := "renamed"
	patched, err := svc.Patch("u-actor", task.ID, PatchInput{UpdateFields: []string{"summary"}, Summary: &newSummary})
	require.NoError(t, err)
	assert.Equal(t, "renamed", patched.Summary)
	assert.Equal(t, "d", patched.Description)
}

func TestPatch_ClearDue(t *testing.T) {
	svc, _ := newTestService(t)
	due := int64(5_000_000)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c", DueAt: &due})
	patched, err := svc.Patch("u-actor", task.ID, PatchInput{UpdateFields: []string{"due"}, DueAt: nil})
	require.NoError(t, err)
	assert.Nil(t, patched.DueAt)
}

func TestPatch_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Patch("u-actor", "ghost", PatchInput{})
	require.ErrorIs(t, err, ErrNotFound)
}

// --- Delete ---

func TestDelete_CascadeRemovesDependents(t *testing.T) {
	svc, s := newTestService(t)
	ctx := context.Background()
	parent := mustCreateTask(t, svc, CreateInput{Summary: "p", CreatorID: "u-c"})
	sub := mustCreateTask(t, svc, CreateInput{Summary: "s", CreatorID: "u-c", ParentTaskID: parent.ID})
	require.NoError(t, svc.Delete("u-actor", parent.ID))
	_, err := svc.Get(parent.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.GetTask(ctx, sub.ID)
	require.ErrorIs(t, err, store.ErrTaskNotFound)
	_, err = s.GetMemberByRole(ctx, parent.ID, model.MemberRoleCreator)
	require.ErrorIs(t, err, store.ErrMemberNotFound)
}

func TestDelete_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	err := svc.Delete("u-actor", "ghost")
	require.ErrorIs(t, err, ErrNotFound)
}

// --- List ---

func TestList_ScopeDirectReturnsSharedTasks(t *testing.T) {
	svc, _ := newTestService(t)
	// u-me + u-partner are the DM pair. Only tasks where BOTH are members are
	// returned (mutual-membership). A task created by u-c and assigned to u-me
	// has u-c + u-me as members — neither u-me+u-partner both — so it's hidden.
	// We construct a task where u-me is the creator and u-partner the assignee.
	t1 := mustCreateTask(t, svc, CreateInput{Summary: "shared", CreatorID: "u-me", AssigneeID: "u-partner"})
	_ = t1
	// Unrelated task: neither u-me nor u-partner is a member.
	mustCreateTask(t, svc, CreateInput{Summary: "other", CreatorID: "u-c", AssigneeID: "u-third"})
	got, err := svc.List(ListQuery{Scope: ScopeDirect, UserID: "u-me", PartnerID: "u-partner", Limit: 50})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "shared", got[0].Summary)
}

func TestList_StatusFilter(t *testing.T) {
	svc, _ := newTestService(t)
	mustCreateTask(t, svc, CreateInput{Summary: "todo", CreatorID: "u-c", ChannelID: "ch1", AssigneeID: "u-me"})
	t2 := mustCreateTask(t, svc, CreateInput{Summary: "done", CreatorID: "u-c", ChannelID: "ch1", AssigneeID: "u-me"})
	_, _ = svc.SetStatus("u-actor", t2.ID, model.StatusDone)
	got, err := svc.List(ListQuery{Scope: ScopeChannel, ChannelID: "ch1", Status: model.StatusDone, Limit: 50})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "done", got[0].Summary)
}

func TestList_CursorPagination(t *testing.T) {
	svc, _ := newTestService(t)
	mustCreateTask(t, svc, CreateInput{Summary: "a", CreatorID: "u-c", ChannelID: "ch1"})
	mustCreateTask(t, svc, CreateInput{Summary: "b", CreatorID: "u-c", ChannelID: "ch1"})
	c := mustCreateTask(t, svc, CreateInput{Summary: "c", CreatorID: "u-c", ChannelID: "ch1"})
	page1, err := svc.List(ListQuery{Scope: ScopeChannel, ChannelID: "ch1", Limit: 2})
	require.NoError(t, err)
	assert.Len(t, page1, 2)
	page2, err := svc.List(ListQuery{Scope: ScopeChannel, ChannelID: "ch1", Limit: 2, AfterOrderKey: page1[1].OrderKey})
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, c.ID, page2[0].ID)
}

// --- Search ---

func TestSearch_MatchesSummaryOrDescription(t *testing.T) {
	svc, _ := newTestService(t)
	mustCreateTask(t, svc, CreateInput{Summary: "Fix login bug", CreatorID: "u-c"})
	mustCreateTask(t, svc, CreateInput{Summary: "other", Description: "Refers to LOGIN flow", CreatorID: "u-c"})
	mustCreateTask(t, svc, CreateInput{Summary: "unrelated", CreatorID: "u-c"})
	got, err := svc.Search("login", 10)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

// --- SetStatus ---

func TestSetStatus_InvalidStatus(t *testing.T) {
	svc, _ := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c"})
	_, err := svc.SetStatus("u-actor", task.ID, "paused")
	require.ErrorIs(t, err, ErrInvalidStatus)
}

func TestSetStatus_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.SetStatus("u-actor", "ghost", model.StatusDone)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestSetStatus_DoneStampsCompletedAt(t *testing.T) {
	svc, _ := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c"})
	got, err := svc.SetStatus("u-actor", task.ID, model.StatusDone)
	require.NoError(t, err)
	require.NotNil(t, got.CompletedAt)
	assert.Nil(t, got.CancelledAt)
}

func TestSetStatus_CancelStampsCancelledAt(t *testing.T) {
	svc, _ := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c"})
	got, err := svc.SetStatus("u-actor", task.ID, model.StatusCancelled)
	require.NoError(t, err)
	require.NotNil(t, got.CancelledAt)
	assert.Nil(t, got.CompletedAt)
}

func TestSetStatus_TodoClearsTimestamps(t *testing.T) {
	svc, _ := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c"})
	_, _ = svc.SetStatus("u-actor", task.ID, model.StatusDone)
	got, err := svc.SetStatus("u-actor", task.ID, model.StatusTodo)
	require.NoError(t, err)
	assert.Nil(t, got.CompletedAt)
	assert.Nil(t, got.CancelledAt)
}

func TestSetStatus_ParentDoneBlockedByOpenSubtask(t *testing.T) {
	svc, _ := newTestService(t)
	parent := mustCreateTask(t, svc, CreateInput{Summary: "p", CreatorID: "u-c"})
	mustCreateTask(t, svc, CreateInput{Summary: "open sub", CreatorID: "u-c", ParentTaskID: parent.ID})
	_, err := svc.SetStatus("u-actor", parent.ID, model.StatusDone)
	require.Error(t, err)
	var blocked ErrOpenSubtasks
	require.ErrorAs(t, err, &blocked)
	assert.NotEmpty(t, blocked.Open)
}

func TestSetStatus_ParentDoneAllowedWhenSubtasksTerminal(t *testing.T) {
	svc, _ := newTestService(t)
	parent := mustCreateTask(t, svc, CreateInput{Summary: "p", CreatorID: "u-c"})
	sub := mustCreateTask(t, svc, CreateInput{Summary: "s", CreatorID: "u-c", ParentTaskID: parent.ID})
	_, _ = svc.SetStatus("u-actor", sub.ID, model.StatusDone)
	_, err := svc.SetStatus("u-actor", parent.ID, model.StatusDone)
	require.NoError(t, err)
}

func TestSetStatus_CancelCascadesToOpenSubtasks(t *testing.T) {
	svc, s := newTestService(t)
	ctx := context.Background()
	parent := mustCreateTask(t, svc, CreateInput{Summary: "p", CreatorID: "u-c"})
	sub := mustCreateTask(t, svc, CreateInput{Summary: "s", CreatorID: "u-c", ParentTaskID: parent.ID})
	_, err := svc.SetStatus("u-actor", parent.ID, model.StatusCancelled)
	require.NoError(t, err)
	subRow, err := s.GetTask(ctx, sub.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, subRow.Status)
}

func TestSetStatus_NoOpWhenUnchanged(t *testing.T) {
	svc, _ := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c"})
	got, err := svc.SetStatus("u-actor", task.ID, model.StatusTodo)
	require.NoError(t, err)
	assert.Equal(t, model.StatusTodo, got.Status)
}

func TestSetStatus_TerminalClearsReminder(t *testing.T) {
	svc, s := newTestService(t)
	ctx := context.Background()
	due := int64(2_000_000)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c", DueAt: &due, ReminderOffset: ptrInt64(60_000)})
	reminders, err := s.ListReminders(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, reminders, 1)
	_, _ = svc.SetStatus("u-actor", task.ID, model.StatusDone)
	reminders, err = s.ListReminders(ctx, task.ID)
	require.NoError(t, err)
	assert.Empty(t, reminders)
}

// --- Assign ---

func TestAssign_SwapsAssignee(t *testing.T) {
	svc, _ := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c", AssigneeID: "u-old"})
	got, ev, err := svc.Assign("u-actor", task.ID, "u-new")
	require.NoError(t, err)
	assert.Equal(t, "u-new", got.AssigneeID)
	assert.Equal(t, "u-old", ev.OldAssigneeID)
}

func TestAssign_FromUnassigned(t *testing.T) {
	svc, _ := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c"})
	got, _, err := svc.Assign("u-actor", task.ID, "u-new")
	require.NoError(t, err)
	assert.Equal(t, "u-new", got.AssigneeID)
}

func TestAssign_NoOpSameAssignee(t *testing.T) {
	svc, _ := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c", AssigneeID: "u-same"})
	got, _, err := svc.Assign("u-actor", task.ID, "u-same")
	require.NoError(t, err)
	assert.Equal(t, "u-same", got.AssigneeID)
}

func TestAssign_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	_, _, err := svc.Assign("u-actor", "ghost", "u-x")
	require.ErrorIs(t, err, ErrNotFound)
}

// --- Reminders ---

func TestSetReminder_BuildsRow(t *testing.T) {
	svc, s := newTestService(t)
	ctx := context.Background()
	due := int64(2_000_000)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c", DueAt: &due})
	_, err := svc.SetReminder("u-actor", task.ID, 30_000)
	require.NoError(t, err)
	reminders, err := s.ListReminders(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, reminders, 1)
	assert.Equal(t, int64(30_000), reminders[0].OffsetMS)
}

func TestSetReminder_RequiresDue(t *testing.T) {
	svc, _ := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c"})
	_, err := svc.SetReminder("u-actor", task.ID, 30_000)
	require.ErrorIs(t, err, ErrReminderNeedsDue)
}

func TestSetReminder_InvalidOffset(t *testing.T) {
	svc, _ := newTestService(t)
	due := int64(2_000_000)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c", DueAt: &due})
	_, err := svc.SetReminder("u-actor", task.ID, 0)
	require.Error(t, err)
}

func TestClearReminder_RemovesRow(t *testing.T) {
	svc, s := newTestService(t)
	ctx := context.Background()
	due := int64(2_000_000)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c", DueAt: &due, ReminderOffset: ptrInt64(60_000)})
	_, err := svc.ClearReminder("u-actor", task.ID)
	require.NoError(t, err)
	reminders, err := s.ListReminders(ctx, task.ID)
	require.NoError(t, err)
	assert.Empty(t, reminders)
}

func TestFireReadyReminders_WithinWindow(t *testing.T) {
	svc, _ := newTestService(t)
	now := nowFunc()
	due := now + 60_000
	mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c", AssigneeID: "u-me", DueAt: &due, ReminderOffset: ptrInt64(120_000)})
	due2, err := svc.FireReadyReminders(now, 5*time.Minute)
	require.NoError(t, err)
	assert.NotEmpty(t, due2)
}

func TestFireReadyReminders_NotYetDue(t *testing.T) {
	svc, _ := newTestService(t)
	now := nowFunc()
	due := now + 10_000_000
	mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c", AssigneeID: "u-me", DueAt: &due, ReminderOffset: ptrInt64(60_000)})
	due2, err := svc.FireReadyReminders(now, 5*time.Minute)
	require.NoError(t, err)
	assert.Empty(t, due2)
}

// --- Comments ---

func TestLinkComment_PersistsAndReturnsEvent(t *testing.T) {
	svc, s := newTestService(t)
	ctx := context.Background()
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-creator", AssigneeID: "u-assignee"})
	c, ev, err := svc.LinkComment(task.ID, "post-1", "u-commenter")
	require.NoError(t, err)
	assert.Equal(t, "post-1", c.PostID)
	assert.Equal(t, "u-commenter", ev.AuthorID)
	assert.Equal(t, "u-creator", ev.CreatorID)
	comments, err := s.ListComments(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "post-1", comments[0].PostID)
}

func TestLinkComment_TaskNotFound(t *testing.T) {
	svc, _ := newTestService(t)
	_, _, err := svc.LinkComment("ghost", "post-1", "u-x")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestListComments_EmptyWhenNone(t *testing.T) {
	svc, _ := newTestService(t)
	mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c"})
	comments, err := svc.ListComments("no-comments-task")
	require.NoError(t, err)
	assert.Empty(t, comments)
}

// --- SubtaskProgress ---

func TestSubtaskProgress(t *testing.T) {
	svc, _ := newTestService(t)
	parent := mustCreateTask(t, svc, CreateInput{Summary: "p", CreatorID: "u-c"})
	s1 := mustCreateTask(t, svc, CreateInput{Summary: "s1", CreatorID: "u-c", ParentTaskID: parent.ID})
	s2 := mustCreateTask(t, svc, CreateInput{Summary: "s2", CreatorID: "u-c", ParentTaskID: parent.ID})
	_, _ = svc.SetStatus("u-actor", s1.ID, model.StatusDone)
	_, _ = svc.SetStatus("u-actor", s2.ID, model.StatusDone)
	done, total, err := svc.SubtaskProgress(parent.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, done)
	assert.Equal(t, 2, total)
}

// --- assertNoCycle ---

func TestAssertNoCycle_DetectsLoop(t *testing.T) {
	svc, s := newTestService(t)
	ctx := context.Background()
	a := createTaskRow(t, s, "A", "k1")
	b := createTaskRow(t, s, "B", "k2", func(r *model.TaskRow) { r.ParentTaskID = "A" })
	_, err := s.UpdateTask(ctx, model.TaskRow{
		ID: a.ID, Summary: a.Summary, Status: a.Status, OrderKey: a.OrderKey,
		ParentTaskID: b.ID, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	})
	require.NoError(t, err)
	err = svc.assertNoCycle(ctx, "A", maxSubtaskDepth)
	require.ErrorIs(t, err, ErrSubtaskCycle)
}

var _ = errors.New
