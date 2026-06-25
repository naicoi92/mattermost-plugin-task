package sqlstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/naicoi92/mattermost-plugin-task/server/model"

	"github.com/naicoi92/mattermost-plugin-task/server/store"
)

func TestSetReminder_ReplacesExisting(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))

	// Set first reminder.
	r1, err := s.SetReminder(ctx, "R1", "T1", 3_600_000)
	require.NoError(t, err)
	assert.Equal(t, "R1", r1.ID)
	assert.Equal(t, int64(3_600_000), r1.OffsetMS)

	// Set a different offset; MVP enforces one reminder per task, so R1 must
	// be replaced.
	r2, err := s.SetReminder(ctx, "R2", "T1", 1_800_000)
	require.NoError(t, err)
	assert.Equal(t, "R2", r2.ID)

	got, err := s.ListReminders(ctx, "T1")
	require.NoError(t, err)
	require.Len(t, got, 1, "SetReminder must replace, not append")
	assert.Equal(t, "R2", got[0].ID)
	assert.Equal(t, int64(1_800_000), got[0].OffsetMS)
}

func TestSetReminder_RequiresFields(t *testing.T) {
	s := tasksTestStore(t)
	_, err := s.SetReminder(context.Background(), "", "T1", 1)
	require.Error(t, err)
	_, err = s.SetReminder(context.Background(), "R1", "", 1)
	require.Error(t, err)
}

func TestClearReminder_Idempotent(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))

	// Clearing a task with no reminder is a no-op.
	require.NoError(t, s.ClearReminder(ctx, "T1"))

	_, err := s.SetReminder(ctx, "R1", "T1", 3_600_000)
	require.NoError(t, err)
	require.NoError(t, s.ClearReminder(ctx, "T1"))

	got, err := s.ListReminders(ctx, "T1")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestListReminders_OrderedAndIsolated(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	mustCreate(t, s, ctx, fixture("T2", "k2"))
	// Note: SetReminder replaces per task, so use direct inserts via SetReminder
	// only once per task for this slice test. ListReminders returns a slice to
	// stay multi-reminder-ready; here we just verify isolation.
	_, err := s.SetReminder(ctx, "R1", "T1", 1_000)
	require.NoError(t, err)
	_, err = s.SetReminder(ctx, "R2", "T2", 2_000)
	require.NoError(t, err)

	got, err := s.ListReminders(ctx, "T1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "T1", got[0].TaskID)
}

func TestMarkReminderFired_SetsTimestamp(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	_, err := s.SetReminder(ctx, "R1", "T1", 1_000)
	require.NoError(t, err)

	require.NoError(t, s.MarkReminderFired(ctx, "R1", 5_000))
	got, err := s.ListReminders(ctx, "T1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].FiredAt)
	assert.Equal(t, int64(5_000), *got[0].FiredAt)
}

func TestMarkReminderFired_NotFound(t *testing.T) {
	s := tasksTestStore(t)
	err := s.MarkReminderFired(context.Background(), "ghost", 1)
	require.ErrorIs(t, err, store.ErrReminderNotFound)
}

func TestListDueReminders_FiresOnlyDuePendingAssigned(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()

	// now = 10_000. graceMs = 1_000.
	const now, grace = int64(10_000), int64(1_000)

	// T1: due at 11_000, offset 2_000 -> fire time 9_000 <= now. DUE.
	mustCreate(t, s, ctx, fixture("T1", "k1", func(t *model.TaskRow) { t.DueAt = new(int64(11_000)) }))
	require.NoError(t, s.AddMember(ctx, "T1", "u1", model.MemberRoleAssignee))
	_, err := s.SetReminder(ctx, "R1", "T1", 2_000)
	require.NoError(t, err)

	// T2: due at 20_000, offset 1_000 -> fire time 19_000 > now. NOT DUE YET.
	mustCreate(t, s, ctx, fixture("T2", "k2", func(t *model.TaskRow) { t.DueAt = new(int64(20_000)) }))
	require.NoError(t, s.AddMember(ctx, "T2", "u1", model.MemberRoleAssignee))
	_, err = s.SetReminder(ctx, "R2", "T2", 1_000)
	require.NoError(t, err)

	// T3: due at 11_000, offset 2_000 -> fire time 9_000 <= now, BUT status=done.
	mustCreate(t, s, ctx, fixture("T3", "k3", func(t *model.TaskRow) {
		t.DueAt = new(int64(11_000))
		t.Status = model.StatusDone
	}))
	require.NoError(t, s.AddMember(ctx, "T3", "u1", model.MemberRoleAssignee))
	_, err = s.SetReminder(ctx, "R3", "T3", 2_000)
	require.NoError(t, err)

	// T4: due at 11_000, offset 2_000 -> due, but already fired.
	mustCreate(t, s, ctx, fixture("T4", "k4", func(t *model.TaskRow) { t.DueAt = new(int64(11_000)) }))
	require.NoError(t, s.AddMember(ctx, "T4", "u1", model.MemberRoleAssignee))
	_, err = s.SetReminder(ctx, "R4", "T4", 2_000)
	require.NoError(t, err)
	require.NoError(t, s.MarkReminderFired(ctx, "R4", 9_500))

	// T5: due at 5_000, offset 1_000 -> fire time 4_000 <= now, BUT due_at < now-grace
	// (5_000 < 9_000) -> long-overdue, skipped.
	mustCreate(t, s, ctx, fixture("T5", "k5", func(t *model.TaskRow) { t.DueAt = new(int64(5_000)) }))
	require.NoError(t, s.AddMember(ctx, "T5", "u1", model.MemberRoleAssignee))
	_, err = s.SetReminder(ctx, "R5", "T5", 1_000)
	require.NoError(t, err)

	// T6: due at 11_000, offset 2_000 -> due, but NO assignee. Should still
	// surface (LEFT JOIN) with empty AssigneeID; scheduler decides to skip.
	mustCreate(t, s, ctx, fixture("T6", "k6", func(t *model.TaskRow) { t.DueAt = new(int64(11_000)) }))
	_, err = s.SetReminder(ctx, "R6", "T6", 2_000)
	require.NoError(t, err)

	due, err := s.ListDueReminders(ctx, now, grace)
	require.NoError(t, err)

	// Expect: R1 (due, assigned), R6 (due, unassigned). NOT R2 (future), R3
	// (done), R4 (fired), R5 (long-overdue past grace).
	ids := make([]string, 0, len(due))
	for _, d := range due {
		ids = append(ids, d.ReminderID)
	}
	assert.ElementsMatch(t, []string{"R1", "R6"}, ids)

	// Verify the JOIN carried the assignee + due + offset for R1.
	var r1 model.DueReminder
	for _, d := range due {
		if d.ReminderID == "R1" {
			r1 = d
		}
	}
	assert.Equal(t, "T1", r1.TaskID)
	assert.Equal(t, "u1", r1.AssigneeID, "assignee must be JOINed for the DM")
	assert.Equal(t, int64(11_000), r1.DueAt)
	assert.Equal(t, int64(2_000), r1.OffsetMS)

	// R6 has no assignee -> empty string (COALESCE).
	for _, d := range due {
		if d.ReminderID == "R6" {
			assert.Equal(t, "", d.AssigneeID, "unassigned task must surface with empty assignee")
		}
	}
}

func TestListDueReminders_EmptyWhenNoneDue(t *testing.T) {
	s := tasksTestStore(t)
	due, err := s.ListDueReminders(context.Background(), 10_000, 1_000)
	require.NoError(t, err)
	assert.Empty(t, due)
}

func TestReminders_FKCascadeOnTaskDelete(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	_, err := s.SetReminder(ctx, "R1", "T1", 1_000)
	require.NoError(t, err)

	require.NoError(t, s.DeleteTask(ctx, "T1"))
	got, err := s.ListReminders(ctx, "T1")
	require.NoError(t, err)
	assert.Empty(t, got, "FK ON DELETE CASCADE must remove reminders with the task")
}
