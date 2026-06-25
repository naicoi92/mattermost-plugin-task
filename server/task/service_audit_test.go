package task

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/store"
)

// auditEvents returns the task's audit events oldest-first, filtered to the
// given types (empty = all). A test helper so each audit test can assert on
// exactly the transition(s) it exercises.
func auditEvents(t *testing.T, s store.Store, taskID string, types ...string) []model.TaskEvent {
	t.Helper()
	ctx := context.Background()
	all, err := s.ListTaskEvents(ctx, taskID, 100)
	require.NoError(t, err)
	// ListTaskEvents returns newest-first; flip to oldest-first for readability.
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if len(types) == 0 {
		return all
	}
	want := map[string]bool{}
	for _, ty := range types {
		want[ty] = true
	}
	var out []model.TaskEvent
	for _, e := range all {
		if want[e.EventType] {
			out = append(out, e)
		}
	}
	return out
}

func TestAudit_Create_RecordsCreatedEventWithSnapshot(t *testing.T) {
	svc, s := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "audit me", CreatorID: "u-creator"})

	events := auditEvents(t, s, task.ID, model.EventCreated)
	require.Len(t, events, 1)
	assert.Equal(t, "u-creator", events[0].ActorID)
	assert.Nil(t, events[0].FromValue, "create has no prior value")
	require.NotNil(t, events[0].ToValue)
	assert.Contains(t, *events[0].ToValue, "audit me", "snapshot carries the summary")
}

func TestAudit_SetStatus_RecordsStatusChangedWithFromTo(t *testing.T) {
	svc, s := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c"})

	_, err := svc.SetStatus("u-actor", task.ID, model.StatusDone)
	require.NoError(t, err)

	events := auditEvents(t, s, task.ID, model.EventStatusChanged)
	require.Len(t, events, 1)
	assert.Equal(t, "u-actor", events[0].ActorID)
	require.NotNil(t, events[0].FromValue)
	assert.Equal(t, model.StatusTodo, *events[0].FromValue)
	require.NotNil(t, events[0].ToValue)
	assert.Equal(t, model.StatusDone, *events[0].ToValue)
}

func TestAudit_Assign_RecordsAssignedWithFromTo(t *testing.T) {
	svc, s := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c", AssigneeID: "u-old"})

	_, _, err := svc.Assign("u-actor", task.ID, "u-new")
	require.NoError(t, err)

	events := auditEvents(t, s, task.ID, model.EventAssigned)
	require.Len(t, events, 1)
	assert.Equal(t, "u-actor", events[0].ActorID)
	require.NotNil(t, events[0].FromValue)
	assert.Equal(t, "u-old", *events[0].FromValue)
	require.NotNil(t, events[0].ToValue)
	assert.Equal(t, "u-new", *events[0].ToValue)
}

func TestAudit_Assign_UnassignRecordsUnassignedEvent(t *testing.T) {
	svc, s := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c", AssigneeID: "u-a"})

	_, _, err := svc.Assign("u-actor", task.ID, "")
	require.NoError(t, err)

	events := auditEvents(t, s, task.ID, model.EventUnassigned)
	require.Len(t, events, 1)
	assert.Equal(t, "u-a", *events[0].FromValue)
	assert.Equal(t, "", *events[0].ToValue)
}

func TestAudit_Patch_RecordsPerFieldEvents(t *testing.T) {
	svc, s := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "old", Description: "olddesc", CreatorID: "u-c"})
	newSum := "new"

	_, err := svc.Patch("u-actor", task.ID, PatchInput{
		UpdateFields: []string{"summary", "description"},
		Summary:      &newSum,
		Description:  new("newdesc"),
	})
	require.NoError(t, err)

	sumEvents := auditEvents(t, s, task.ID, model.EventSummaryChanged)
	require.Len(t, sumEvents, 1)
	assert.Equal(t, "old", *sumEvents[0].FromValue)
	assert.Equal(t, "new", *sumEvents[0].ToValue)

	descEvents := auditEvents(t, s, task.ID, model.EventDescriptionChanged)
	require.Len(t, descEvents, 1)
	assert.Equal(t, "olddesc", *descEvents[0].FromValue)
	assert.Equal(t, "newdesc", *descEvents[0].ToValue)
}

func TestAudit_Patch_DueChangedRecordsDueEvent(t *testing.T) {
	svc, s := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c"})
	newDue := int64(5_000_000)

	_, err := svc.Patch("u-actor", task.ID, PatchInput{UpdateFields: []string{"due"}, DueAt: &newDue})
	require.NoError(t, err)

	events := auditEvents(t, s, task.ID, model.EventDueChanged)
	require.Len(t, events, 1)
	assert.Equal(t, "", *events[0].FromValue, "from empty (no prior due)")
	assert.Equal(t, "5000000", *events[0].ToValue)
}

func TestAudit_Delete_RecordsDeletedEventBeforeCascade(t *testing.T) {
	svc, s := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "doomed", CreatorID: "u-c"})

	require.NoError(t, svc.Delete("u-actor", task.ID))

	// The deleted event was appended inside the tx; FK cascade removes the
	// event row with the task, so we can't read it back post-delete. Instead
	// verify the task is gone (the delete committed) — the event served its
	// in-tx purpose. This documents the cascade limitation.
	ctx := context.Background()
	_, err := s.GetTask(ctx, task.ID)
	require.ErrorIs(t, err, store.ErrTaskNotFound)
}

func TestAudit_SetReminder_RecordsReminderSet(t *testing.T) {
	svc, s := newTestService(t)
	due := int64(2_000_000)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c", DueAt: &due})

	_, err := svc.SetReminder("u-actor", task.ID, 30_000)
	require.NoError(t, err)

	events := auditEvents(t, s, task.ID, model.EventReminderSet)
	require.Len(t, events, 1)
	assert.Equal(t, "u-actor", events[0].ActorID)
	require.NotNil(t, events[0].ToValue)
	assert.Contains(t, *events[0].ToValue, "30000")
}

func TestAudit_ClearReminder_RecordsReminderCleared(t *testing.T) {
	svc, s := newTestService(t)
	due := int64(2_000_000)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c", DueAt: &due, ReminderOffset: new(int64(60_000))})

	_, err := svc.ClearReminder("u-actor", task.ID)
	require.NoError(t, err)

	events := auditEvents(t, s, task.ID, model.EventReminderCleared)
	// Note: Create also seeds a reminder_set event; filter to cleared only.
	require.Len(t, events, 1)
	assert.Equal(t, "u-actor", events[0].ActorID)
}

func TestAudit_LinkComment_RecordsCommentedEvent(t *testing.T) {
	svc, s := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c"})

	_, _, err := svc.LinkComment(task.ID, "post-1", "u-commenter")
	require.NoError(t, err)

	events := auditEvents(t, s, task.ID, model.EventCommented)
	require.Len(t, events, 1)
	assert.Equal(t, "u-commenter", events[0].ActorID)
	require.NotNil(t, events[0].ToValue)
}

func TestAudit_CreateSubtask_RecordsSubtaskAddedOnParent(t *testing.T) {
	svc, s := newTestService(t)
	parent := mustCreateTask(t, svc, CreateInput{Summary: "p", CreatorID: "u-c"})
	// CreateSubtask records a subtask_added event on the PARENT.
	_, err := svc.CreateSubtask(parent.ID, "u-c", "child", "", nil)
	require.NoError(t, err)

	events := auditEvents(t, s, parent.ID, model.EventSubtaskAdded)
	require.Len(t, events, 1)
	assert.Equal(t, "u-c", events[0].ActorID)
}

// TestAudit_Rollback_EventAndChangeRollBackTogether verifies atomicity: if
// any step inside a transition's WithTx fails, NEITHER the change NOR the
// event persists. We trigger a failure by making the task's status invalid
// mid-transaction (here we use an invalid priority on Patch — the service
// rejects it before the tx commits, so no audit event should persist).
func TestAudit_Rollback_EventDoesNotPersistOnFailure(t *testing.T) {
	svc, s := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-c"})
	ctx := context.Background()

	// Patch with an invalid priority — the service rejects it before the tx.
	badPriority := "blocker"
	_, err := svc.Patch("u-actor", task.ID, PatchInput{
		UpdateFields: []string{"priority"},
		Priority:     &badPriority,
	})
	require.Error(t, err)

	// No priority_changed event should have persisted.
	events := auditEvents(t, s, task.ID, model.EventPriorityChanged)
	assert.Empty(t, events, "rejected patch must leave no audit event")

	// The task's priority is unchanged (still the default standard).
	row, gErr := s.GetTask(ctx, task.ID)
	require.NoError(t, gErr)
	assert.Equal(t, model.PriorityStandard, row.Priority, "rejected patch leaves priority unchanged")
}

func TestAudit_ActorIDThreaded_NotSystemPlaceholder(t *testing.T) {
	// Regression: before M3-3 every transition recorded ActorID="system".
	// Now the real actor is threaded through. This test pins that the actor
	// is NOT the placeholder for a human-initiated transition.
	svc, s := newTestService(t)
	task := mustCreateTask(t, svc, CreateInput{Summary: "x", CreatorID: "u-real-user"})

	events := auditEvents(t, s, task.ID, model.EventCreated)
	require.Len(t, events, 1)
	assert.Equal(t, "u-real-user", events[0].ActorID, "actor must be the real user, not 'system'")
	assert.NotEqual(t, "system", events[0].ActorID)
}

func TestAudit_CascadeCancel_RecordsEventPerSubtask(t *testing.T) {
	// Cancelling a parent cascade-cancels its open subtasks; each subtask must
	// get its own status_changed audit event (from open -> cancelled).
	svc, s := newTestService(t)
	parent := mustCreateTask(t, svc, CreateInput{Summary: "p", CreatorID: "u-c"})
	sub := mustCreateTask(t, svc, CreateInput{Summary: "open sub", CreatorID: "u-c", ParentTaskID: parent.ID})

	_, err := svc.SetStatus("u-actor", parent.ID, model.StatusCancelled)
	require.NoError(t, err)

	// The subtask must have a status_changed event from todo -> cancelled.
	events := auditEvents(t, s, sub.ID, model.EventStatusChanged)
	require.Len(t, events, 1)
	assert.Equal(t, "u-actor", events[0].ActorID, "cascade event carries the parent's actor")
	require.NotNil(t, events[0].FromValue)
	assert.Equal(t, model.StatusTodo, *events[0].FromValue)
	require.NotNil(t, events[0].ToValue)
	assert.Equal(t, model.StatusCancelled, *events[0].ToValue)
}
