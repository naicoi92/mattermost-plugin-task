package task

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/naicoi92/mattermost-plugin-task/server/store"
)

// TestReminderJob_FullCycle exercises the complete reminder-job flow that
// runReminderJob (server/job.go) drives: a task with a due date + reminder
// becomes due, FireReadyReminders returns it, MarkReminderFired stamps it, and
// a second tick does NOT re-fire it. This pins the M4-3 contract end-to-end
// against the real SQL store (no job.go plugin-API plumbing needed — the job
// is a thin loop over these two service calls).
func TestReminderJob_FullCycle(t *testing.T) {
	svc, s := newTestService(t)
	ctx := context.Background()

	// Create a task due 1 minute from now with a 2-minute reminder offset, so
	// its fire time (due - offset) is 1 minute in the PAST at "now".
	now := nowFunc()
	due := now + 60_000
	task := mustCreateTask(t, svc, CreateInput{
		Summary:        "due soon",
		CreatorID:      "u-c",
		AssigneeID:     "u-me",
		DueAt:          &due,
		ReminderOffset: ptrInt64(120_000),
	})

	// Tick 1: the reminder is due → FireReadyReminders returns it.
	due1, err := svc.FireReadyReminders(now, 5*time.Minute)
	require.NoError(t, err)
	require.Len(t, due1, 1, "first tick fires the due reminder")
	assert.Equal(t, task.ID, due1[0].TaskID)
	assert.Equal(t, "u-me", due1[0].AssigneeID, "assignee JOINed for the DM")
	assert.NotEmpty(t, due1[0].ReminderID, "reminder has its own id")

	// The job would DM here (fireReminderDM) — skip the plugin-API call; the
	// contract under test is the mark-fired step. Mark it fired.
	require.NoError(t, svc.MarkReminderFired(due1[0].ReminderID, due1[0].TaskID))

	// Tick 2: the reminder is now fired (fired_at set) → must NOT re-fire.
	due2, err := svc.FireReadyReminders(now, 5*time.Minute)
	require.NoError(t, err)
	assert.Empty(t, due2, "second tick must not re-fire a fired reminder")

	// Verify the reminder row's fired_at is set.
	reminders, err := s.ListReminders(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, reminders, 1)
	require.NotNil(t, reminders[0].FiredAt, "fired_at stamped")
}

// TestReminderJob_UnassignedTaskSurfacesButSkipsDM pins that an unassigned
// task still surfaces in ListDueReminders (LEFT JOIN yields empty assignee) so
// the job can decide to skip the DM. The mark-fired step is the job's call.
func TestReminderJob_UnassignedTaskSurfacesWithEmptyAssignee(t *testing.T) {
	svc, _ := newTestService(t)
	now := nowFunc()
	due := now + 60_000
	mustCreateTask(t, svc, CreateInput{
		Summary:        "no assignee",
		CreatorID:      "u-c",
		DueAt:          &due,
		ReminderOffset: ptrInt64(120_000),
	})

	due1, err := svc.FireReadyReminders(now, 5*time.Minute)
	require.NoError(t, err)
	require.Len(t, due1, 1)
	assert.Equal(t, "", due1[0].AssigneeID, "unassigned task surfaces with empty assignee")
}

// TestReminderJob_TerminalTaskExcluded pins that a done/cancelled task's
// reminder is not surfaced (the JOIN filters on active status). This is the
// SQL replacement for the KV store's self-heal of stale reminder edges.
func TestReminderJob_TerminalTaskExcluded(t *testing.T) {
	svc, _ := newTestService(t)
	now := nowFunc()
	due := now + 60_000
	task := mustCreateTask(t, svc, CreateInput{
		Summary:        "done task",
		CreatorID:      "u-c",
		AssigneeID:     "u-me",
		DueAt:          &due,
		ReminderOffset: ptrInt64(120_000),
	})
	_, err := svc.SetStatus("u-c", task.ID, "done")
	require.NoError(t, err)

	due1, err := svc.FireReadyReminders(now, 5*time.Minute)
	require.NoError(t, err)
	assert.Empty(t, due1, "terminal task's reminder must not surface")
}

// keep store import referenced.
var _ = store.ErrTaskNotFound
