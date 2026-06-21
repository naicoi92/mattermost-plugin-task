package taskutil

import (
	"testing"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
)

func TestApplyStatus_StateMachine(t *testing.T) {
	const now int64 = 5_000
	old := int64(1_000) // var, not const: we take its address below

	cases := []struct {
		name        string
		toStatus    string
		wantSetDone bool // expect CompletedAt set to now?
		wantSetCanc bool // expect CancelledAt set to now?
	}{
		{name: "todo clears both", toStatus: model.StatusTodo},
		{name: "in_progress clears both", toStatus: model.StatusInProgress},
		{name: "done sets completed only", toStatus: model.StatusDone, wantSetDone: true},
		{name: "cancelled sets cancelled only", toStatus: model.StatusCancelled, wantSetCanc: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Start from a task where both timestamps are already set, so we
			// can assert they get cleared (not just left zero).
			task := &model.TaskRow{
				Status:      model.StatusDone,
				CompletedAt: &old,
				CancelledAt: &old,
			}

			got := ApplyStatus(task, tc.toStatus, now)

			if got.Status != tc.toStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.toStatus)
			}
			if got.UpdatedAt != now {
				t.Errorf("UpdatedAt = %d, want %d", got.UpdatedAt, now)
			}
			checkTS(t, "CompletedAt", got.CompletedAt, tc.wantSetDone, now)
			checkTS(t, "CancelledAt", got.CancelledAt, tc.wantSetCanc, now)
		})
	}
}

func TestApplyStatus_ReStampsUpdatedAt(t *testing.T) {
	// Transitioning to the same status must still refresh UpdatedAt (e.g. a
	// "done -> done" re-confirmation re-stamps the completion time).
	const now int64 = 999
	old := int64(100) // var, not const: we take its address below
	row := &model.TaskRow{Status: model.StatusDone, CompletedAt: &old, UpdatedAt: old}

	got := ApplyStatus(row, model.StatusDone, now)

	if got.UpdatedAt != now {
		t.Errorf("UpdatedAt = %d, want %d", got.UpdatedAt, now)
	}
	if got.CompletedAt == nil || *got.CompletedAt != now {
		t.Errorf("CompletedAt = %v, want %d", got.CompletedAt, now)
	}
}

func TestApplyStatus_ReturnsSamePointer(t *testing.T) {
	row := &model.TaskRow{Status: model.StatusTodo}
	if ApplyStatus(row, model.StatusDone, 1) != row {
		t.Error("ApplyStatus did not return the same row pointer")
	}
}

// checkTS asserts an optional timestamp field: when wantSet is true the field
// must be non-nil and equal expected; otherwise it must be nil.
func checkTS(t *testing.T, field string, got *int64, wantSet bool, expected int64) {
	t.Helper()
	if wantSet {
		if got == nil {
			t.Errorf("%s = nil, want %d", field, expected)
			return
		}
		if *got != expected {
			t.Errorf("%s = %d, want %d", field, *got, expected)
		}
		return
	}
	if got != nil {
		t.Errorf("%s = %d, want nil", field, *got)
	}
}
