package sqlstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
)

// TestListOverdueTasks covers the overdue scan query (change
// notification-overdue-and-context, design D3). A task is "overdue" when it
// has a due_at that is in the past AND it has NOT reached a terminal status
// (done/cancelled). The job uses this to decide whom to DM.
func TestListOverdueTasks(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	now := int64(1_700_000_000_000)
	past := now - 1_000 // any value strictly before now
	future := now + 1_000

	// Overdue + non-terminal (todo) → MUST be returned.
	mustCreate(t, s, ctx, fixture("01HXYZOVERDUE001", "k1", func(r *model.TaskRow) {
		r.DueAt = &past
		r.Status = model.StatusTodo
	}))
	// Overdue + in_progress → MUST be returned (non-terminal).
	mustCreate(t, s, ctx, fixture("01HXYZOVERDUE002", "k2", func(r *model.TaskRow) {
		r.DueAt = &past
		r.Status = model.StatusInProgress
	}))
	// Overdue but DONE → MUST be excluded (terminal).
	mustCreate(t, s, ctx, fixture("01HXYZDONE0000001", "k3", func(r *model.TaskRow) {
		r.DueAt = &past
		r.Status = model.StatusDone
	}))
	// Overdue but CANCELLED → MUST be excluded (terminal).
	mustCreate(t, s, ctx, fixture("01HXYZCAN00000001", "k4", func(r *model.TaskRow) {
		r.DueAt = &past
		r.Status = model.StatusCancelled
	}))
	// No due date → MUST be excluded (never overdue).
	mustCreate(t, s, ctx, fixture("01HXYZNODUE00001", "k5"))
	// Due in the future → MUST be excluded (not yet overdue).
	mustCreate(t, s, ctx, fixture("01HXYZFUTURE00001", "k6", func(r *model.TaskRow) {
		r.DueAt = &future
	}))

	got, err := s.ListOverdueTasks(ctx, now)
	require.NoError(t, err)
	ids := taskIDs(got)
	require.ElementsMatch(t, []string{"01HXYZOVERDUE001", "01HXYZOVERDUE002"}, ids,
		"only past-due non-terminal tasks are overdue")
}

// TestMarkOverdueSent stamps last_overdue_sent_at on a task so the daily job
// can dedupe (design D2): a task stamped within the current UTC day is skipped
// on the next scan.
func TestMarkOverdueSent(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("01HXYZTASK0001", "k1"))

	// Fresh task: no stamp yet.
	before, err := s.GetTask(ctx, "01HXYZTASK0001")
	require.NoError(t, err)
	require.Nil(t, before.LastOverdueSentAt, "new task has no overdue stamp")

	// Stamp it.
	stamp := int64(1_700_000_036_000)
	require.NoError(t, s.MarkOverdueSent(ctx, "01HXYZTASK0001", stamp))

	after, err := s.GetTask(ctx, "01HXYZTASK0001")
	require.NoError(t, err)
	require.NotNil(t, after.LastOverdueSentAt)
	require.Equal(t, stamp, *after.LastOverdueSentAt)
}

func taskIDs(rows []model.TaskRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// TestListDueSoonTasks covers the due-soon scan window [fromMs, toMs) for open
// non-terminal tasks (change due-color-and-scheduled-notify, design D6).
func TestListDueSoonTasks(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	now := int64(1_700_000_000_000)
	const hour = int64(60 * 60 * 1000)

	// due in 12h → in window → MUST be returned.
	mustCreate(t, s, ctx, fixture("01HXYZDUESOON001", "k1", func(r *model.TaskRow) {
		r.DueAt = ptrInt64Row(now + (12 * hour))
		r.Status = model.StatusTodo
	}))
	// due in 48h → OUT of window (>24h) → MUST be excluded.
	mustCreate(t, s, ctx, fixture("01HXYZDUEDISTANT", "k2", func(r *model.TaskRow) {
		r.DueAt = ptrInt64Row(now + (48 * hour))
		r.Status = model.StatusTodo
	}))
	// due 5h ago (overdue) → OUT of window (<fromMs) → MUST be excluded.
	mustCreate(t, s, ctx, fixture("01HXYZOVERDUE0001", "k3", func(r *model.TaskRow) {
		r.DueAt = ptrInt64Row(now - (5 * hour))
		r.Status = model.StatusTodo
	}))
	// due in 12h but done → excluded (terminal).
	mustCreate(t, s, ctx, fixture("01HXYZDUESOONDONE", "k4", func(r *model.TaskRow) {
		r.DueAt = ptrInt64Row(now + (12 * hour))
		r.Status = model.StatusDone
	}))

	got, err := s.ListDueSoonTasks(ctx, now, now+(24*hour))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"01HXYZDUESOON001"}, taskIDs(got))
}

// TestMarkDueSoonSent stamps last_due_soon_sent_at for dedupe.
func TestMarkDueSoonSent(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("01HXYZTASK0001", "k1"))

	stamp := int64(1_700_000_036_000)
	require.NoError(t, s.MarkDueSoonSent(ctx, "01HXYZTASK0001", stamp))

	after, err := s.GetTask(ctx, "01HXYZTASK0001")
	require.NoError(t, err)
	require.NotNil(t, after.LastDueSoonSentAt)
	require.Equal(t, stamp, *after.LastDueSoonSentAt)
}

func ptrInt64Row(v int64) *int64 {
	p := new(int64)
	*p = v
	return p
}
