package sqlstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/store"
)

// tasksTestStore returns a SQLStore over a fresh in-memory sqlite DB with the
// 000001 bootstrap + 000002_tasks migrations already applied.
func tasksTestStore(t *testing.T) *SQLStore {
	t.Helper()
	s := newSQLiteTestStore(t)
	runMigrationsSilent(t, s)
	return s
}

// ptr is a tiny helper for building *int64 timestamps in test fixtures.
func ptr[T any](v T) *T { return &v }

// mustCreate inserts a task fixture and fails the test on error. CreateTask
// returns (TaskRow, error); tests only care about success, so this absorbs
// the returned row.
func mustCreate(t *testing.T, s *SQLStore, ctx context.Context, task model.TaskRow) {
	t.Helper()
	_, err := s.CreateTask(ctx, task)
	require.NoError(t, err)
}

// fixture builds a TaskRow with sensible defaults; overrides mutate it.
func fixture(id, orderKey string, overrides ...func(*model.TaskRow)) model.TaskRow {
	row := model.TaskRow{
		ID:        id,
		Summary:   "summary " + id,
		Status:    model.StatusTodo,
		OrderKey:  orderKey,
		CreatedAt: 1_700_000_000_000,
		UpdatedAt: 1_700_000_000_000,
	}
	for _, o := range overrides {
		o(&row)
	}
	return row
}

func withChannel(ch string) func(*model.TaskRow) {
	return func(t *model.TaskRow) { t.ChannelID = ch }
}

func withStatus(st string) func(*model.TaskRow) {
	return func(t *model.TaskRow) { t.Status = st }
}

func withParent(p string) func(*model.TaskRow) {
	return func(t *model.TaskRow) { t.ParentTaskID = p }
}

func withDue(ms int64) func(*model.TaskRow) {
	return func(t *model.TaskRow) { t.DueAt = &ms }
}

func TestCreateTask_InsertsAndRoundTrips(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	due := int64(1_700_000_036_000)

	// Parent must exist before the child because FK enforcement is on
	// (matches postgres production behaviour).
	mustCreate(t, s, ctx, fixture("01HXYZPARENT0001", "k0"))

	created, err := s.CreateTask(ctx, fixture("01HXYZTASK0001", "k1", func(t *model.TaskRow) {
		t.ChannelID = "ch1"
		t.ParentTaskID = "01HXYZPARENT0001"
		t.DueAt = &due
		t.IsAllDay = true
	}))
	require.NoError(t, err)
	assert.Equal(t, "01HXYZTASK0001", created.ID)

	got, err := s.GetTask(ctx, "01HXYZTASK0001")
	require.NoError(t, err)
	assert.Equal(t, "01HXYZTASK0001", got.ID)
	assert.Equal(t, "ch1", got.ChannelID)
	assert.Equal(t, "01HXYZPARENT0001", got.ParentTaskID)
	require.NotNil(t, got.DueAt)
	assert.Equal(t, due, *got.DueAt)
	assert.True(t, got.IsAllDay)
}

func TestCreateTask_RequiresID(t *testing.T) {
	s := tasksTestStore(t)
	_, err := s.CreateTask(context.Background(), model.TaskRow{OrderKey: "k1"})
	require.Error(t, err)
}

func TestGetTask_NotFound(t *testing.T) {
	s := tasksTestStore(t)
	_, err := s.GetTask(context.Background(), "missing")
	require.ErrorIs(t, err, store.ErrTaskNotFound)
}

func TestUpdateTask_ReturningReflectsWrite(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("01HXYZUPD000001", "k1"))

	updated, err := s.UpdateTask(ctx, fixture("01HXYZUPD000001", "k1", func(t *model.TaskRow) {
		t.Summary = "renamed"
		t.Status = model.StatusDone
		t.CompletedAt = ptr(int64(1_700_000_500_000))
		t.UpdatedAt = 1_700_000_500_000
	}))
	require.NoError(t, err)
	assert.Equal(t, "renamed", updated.Summary)
	assert.Equal(t, model.StatusDone, updated.Status)
	require.NotNil(t, updated.CompletedAt)
	assert.Equal(t, int64(1_700_000_500_000), *updated.CompletedAt)

	// Re-read to confirm RETURNING matches persisted state.
	got, err := s.GetTask(ctx, "01HXYZUPD000001")
	require.NoError(t, err)
	assert.Equal(t, "renamed", got.Summary)
	assert.Equal(t, model.StatusDone, got.Status)
}

func TestUpdateTask_NotFound(t *testing.T) {
	s := tasksTestStore(t)
	_, err := s.UpdateTask(context.Background(), model.TaskRow{ID: "ghost", OrderKey: "k1"})
	require.ErrorIs(t, err, store.ErrTaskNotFound)
}

func TestTouchTaskUpdatedAt_MonotonicAndNotFound(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("01HXYZTOU000001", "k1", func(t *model.TaskRow) {
		t.UpdatedAt = 1_000
	}))

	t.Run("newer timestamp advances updated_at", func(t *testing.T) {
		require.NoError(t, s.TouchTaskUpdatedAt(ctx, "01HXYZTOU000001", 5_000))
		got, err := s.GetTask(ctx, "01HXYZTOU000001")
		require.NoError(t, err)
		assert.Equal(t, int64(5_000), got.UpdatedAt)
	})

	t.Run("older timestamp does not regress updated_at", func(t *testing.T) {
		require.NoError(t, s.TouchTaskUpdatedAt(ctx, "01HXYZTOU000001", 2_000))
		got, err := s.GetTask(ctx, "01HXYZTOU000001")
		require.NoError(t, err)
		assert.Equal(t, int64(5_000), got.UpdatedAt, "GREATEST must keep the higher value")
	})

	t.Run("missing task yields store.ErrTaskNotFound", func(t *testing.T) {
		err := s.TouchTaskUpdatedAt(ctx, "ghost", 9_000)
		require.ErrorIs(t, err, store.ErrTaskNotFound)
	})
}

func TestDeleteTask_CascadesSubtasks(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("01HXYZPAR000001", "k1"))
	mustCreate(t, s, ctx, fixture("01HXYZSUB000001", "k2", withParent("01HXYZPAR000001")))
	mustCreate(t, s, ctx, fixture("01HXYZSUB000002", "k3", withParent("01HXYZPAR000001")))

	require.NoError(t, s.DeleteTask(ctx, "01HXYZPAR000001"))

	// Parent gone.
	_, err := s.GetTask(ctx, "01HXYZPAR000001")
	require.ErrorIs(t, err, store.ErrTaskNotFound)

	// Children cascade-deleted via FK ON DELETE CASCADE.
	subs, err := s.ListSubtasks(ctx, "01HXYZPAR000001")
	require.NoError(t, err)
	assert.Empty(t, subs)
}

func TestDeleteTask_NotFound(t *testing.T) {
	s := tasksTestStore(t)
	err := s.DeleteTask(context.Background(), "ghost")
	require.ErrorIs(t, err, store.ErrTaskNotFound)
}

func TestListTasks_ScopeChannelStatusAndPagination(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	// 3 tasks in ch1 (2 todo, 1 done), 1 in ch2, ordered by order_key k1..k4.
	mustCreate(t, s, ctx, fixture("T1", "k1", withChannel("ch1"), withStatus(model.StatusTodo)))
	mustCreate(t, s, ctx, fixture("T2", "k2", withChannel("ch1"), withStatus(model.StatusTodo)))
	mustCreate(t, s, ctx, fixture("T3", "k3", withChannel("ch1"), withStatus(model.StatusDone)))
	mustCreate(t, s, ctx, fixture("T4", "k4", withChannel("ch2"), withStatus(model.StatusTodo)))

	t.Run("channel scope returns only that channel", func(t *testing.T) {
		page, err := s.ListTasks(ctx, store.ListQuery{Scope: store.ScopeChannel, ChannelID: "ch1", Limit: 10})
		require.NoError(t, err)
		assert.Equal(t, 3, page.Total)
		assert.False(t, page.HasMore)
		assert.Len(t, page.Items, 3)
		// Ordered by order_key ASC.
		assert.Equal(t, "T1", page.Items[0].(*model.TaskRow).ID)
		assert.Equal(t, "T3", page.Items[2].(*model.TaskRow).ID)
	})

	t.Run("status filter narrows within scope", func(t *testing.T) {
		page, err := s.ListTasks(ctx, store.ListQuery{
			Scope: store.ScopeChannel, ChannelID: "ch1", Status: model.StatusDone, Limit: 10,
		})
		require.NoError(t, err)
		assert.Equal(t, 1, page.Total)
		assert.Equal(t, "T3", page.Items[0].(*model.TaskRow).ID)
	})

	t.Run("pagination with has_more", func(t *testing.T) {
		// Limit 2 of 3 in ch1.
		page, err := s.ListTasks(ctx, store.ListQuery{Scope: store.ScopeChannel, ChannelID: "ch1", Limit: 2})
		require.NoError(t, err)
		assert.True(t, page.HasMore)
		assert.Len(t, page.Items, 2)
		assert.Equal(t, 3, page.Total)

		// Next page via cursor.
		page2, err := s.ListTasks(ctx, store.ListQuery{
			Scope: store.ScopeChannel, ChannelID: "ch1", AfterOrderKey: "k2", Limit: 2,
		})
		require.NoError(t, err)
		assert.False(t, page2.HasMore)
		assert.Len(t, page2.Items, 1)
		assert.Equal(t, "T3", page2.Items[0].(*model.TaskRow).ID)
	})
}

func TestListTasks_AllScope(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	mustCreate(t, s, ctx, fixture("T2", "k2", withChannel("ch1")))

	page, err := s.ListTasks(ctx, store.ListQuery{Scope: store.ScopeAll, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 2, page.Total)
}

func TestCountTasksByStatus_GroupsKanbanProgress(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1", withChannel("ch1"), withStatus(model.StatusTodo)))
	mustCreate(t, s, ctx, fixture("T2", "k2", withChannel("ch1"), withStatus(model.StatusTodo)))
	mustCreate(t, s, ctx, fixture("T3", "k3", withChannel("ch1"), withStatus(model.StatusInProgress)))
	mustCreate(t, s, ctx, fixture("T4", "k4", withChannel("ch1"), withStatus(model.StatusDone)))

	counts, err := s.CountTasksByStatus(ctx, store.ListQuery{Scope: store.ScopeChannel, ChannelID: "ch1"})
	require.NoError(t, err)
	assert.Equal(t, 2, counts[model.StatusTodo])
	assert.Equal(t, 1, counts[model.StatusInProgress])
	assert.Equal(t, 1, counts[model.StatusDone])
	assert.Equal(t, 0, counts[model.StatusCancelled])
}

func TestSearchTasks_ILikeOrLike(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1", func(t *model.TaskRow) { t.Summary = "Fix login bug" }))
	mustCreate(t, s, ctx, fixture("T2", "k2", func(t *model.TaskRow) {
		t.Summary = "Unrelated"
		t.Description = "Refers to LOGIN flow"
	}))
	mustCreate(t, s, ctx, fixture("T3", "k3", func(t *model.TaskRow) { t.Summary = "Nothing relevant" }))

	// Case-insensitive substring match across summary + description.
	got, err := s.SearchTasks(ctx, "login", 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	// Assert identity, not just count, so a regression that returns the
	// wrong rows is caught.
	ids := []string{got[0].ID, got[1].ID}
	assert.ElementsMatch(t, []string{"T1", "T2"}, ids)
}

func TestSearchTasks_EscapesWildcards(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1", func(t *model.TaskRow) { t.Summary = "100% done" }))
	// A literal % in the keyword must not be treated as a wildcard; we expect
	// exactly the row whose summary contains "100% done".
	got, err := s.SearchTasks(ctx, "100%", 10)
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, "T1", got[0].ID)
}

func TestSubtaskProgress_CountsDoneAndTotal(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("P1", "k1"))
	mustCreate(t, s, ctx, fixture("S1", "k2", withParent("P1"), withStatus(model.StatusTodo)))
	mustCreate(t, s, ctx, fixture("S2", "k3", withParent("P1"), withStatus(model.StatusDone)))
	mustCreate(t, s, ctx, fixture("S3", "k4", withParent("P1"), withStatus(model.StatusDone)))

	done, total, err := s.SubtaskProgress(ctx, "P1")
	require.NoError(t, err)
	assert.Equal(t, 2, done)
	assert.Equal(t, 3, total)
}

func TestSubtaskProgress_NoSubtasks(t *testing.T) {
	s := tasksTestStore(t)
	done, total, err := s.SubtaskProgress(context.Background(), "01HXYZNOSUB0001")
	require.NoError(t, err)
	assert.Equal(t, 0, done)
	assert.Equal(t, 0, total)
}

func TestNextGlobalOrderKey(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()

	t.Run("empty table returns empty string", func(t *testing.T) {
		got, err := s.NextGlobalOrderKey(ctx)
		require.NoError(t, err)
		assert.Equal(t, "", got)
	})

	mustCreate(t, s, ctx, fixture("T1", "k005"))
	mustCreate(t, s, ctx, fixture("T2", "k009"))
	mustCreate(t, s, ctx, fixture("T3", "k007"))

	t.Run("returns the maximum order_key", func(t *testing.T) {
		got, err := s.NextGlobalOrderKey(ctx)
		require.NoError(t, err)
		assert.Equal(t, "k009", got)
	})
}

func TestDueWindow_Buckets(t *testing.T) {
	// Fixed reference: 2026-06-20T10:30:00Z.
	ref := time.Date(2026, 6, 20, 10, 30, 0, 0, time.UTC).UnixMilli()
	startOfToday := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC).UnixMilli()

	t.Run("overdue window", func(t *testing.T) {
		start, end, ok := dueWindow(store.DueOverdue, ref)
		require.True(t, ok)
		assert.Equal(t, int64(0), start)
		assert.Equal(t, startOfToday, end)
	})
	t.Run("today window is one day", func(t *testing.T) {
		start, end, ok := dueWindow(store.DueToday, ref)
		require.True(t, ok)
		assert.Equal(t, startOfToday, start)
		assert.Equal(t, startOfToday+dayMs, end)
	})
	t.Run("week window is seven days", func(t *testing.T) {
		start, end, ok := dueWindow(store.DueWeek, ref)
		require.True(t, ok)
		assert.Equal(t, startOfToday, start)
		assert.Equal(t, startOfToday+7*dayMs, end)
	})
	t.Run("unknown filter returns ok=false", func(t *testing.T) {
		_, _, ok := dueWindow("bogus", ref)
		assert.False(t, ok)
	})
}

func TestListTasks_DueTodayFilter(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	ref := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC).UnixMilli()
	startOfToday := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC).UnixMilli()

	// due today, due in 30 days, due yesterday (overdue), no due.
	mustCreate(t, s, ctx, fixture("T1", "k1", withChannel("ch1"), withDue(startOfToday+3_600_000)))
	mustCreate(t, s, ctx, fixture("T2", "k2", withChannel("ch1"), withDue(startOfToday+30*dayMs)))
	mustCreate(t, s, ctx, fixture("T3", "k3", withChannel("ch1"), withDue(startOfToday-dayMs), withStatus(model.StatusTodo)))
	mustCreate(t, s, ctx, fixture("T4", "k4", withChannel("ch1")))

	page, err := s.ListTasks(ctx, store.ListQuery{
		Scope: store.ScopeChannel, ChannelID: "ch1", Due: store.DueToday, DueAsOf: ref, Limit: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, page.Total)
	assert.Equal(t, "T1", page.Items[0].(*model.TaskRow).ID)
}

func TestNullableString_EmptyBecomesNil(t *testing.T) {
	assert.Nil(t, nullableString(""))
	assert.Equal(t, "x", nullableString("x"))
}
