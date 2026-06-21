package taskutil

import "github.com/naicoi92/mattermost-plugin-task/server/model"

// ApplyStatus transitions row to newStatus according to the plugin's canonical
// status state machine, stamps UpdatedAt with now (ms since epoch), and returns
// the same row pointer for chaining.
//
// Timestamp rules:
//
//	todo / in_progress: clear CompletedAt and CancelledAt (task is active again)
//	done:               set CompletedAt = now, clear CancelledAt
//	cancelled:          set CancelledAt = now, clear CompletedAt
//
// The argument is a *model.TaskRow (the storage row) rather than the assembled
// *model.Task: the service reads the current row from the DB, applies this
// transition to it in memory, then persists the whole row via store.UpdateTask.
// Mutating the assembled entity instead would change only the in-memory copy
// and silently drop the transition.
//
// now is taken as a parameter so the function is pure and testable; callers
// (command/REST handlers, reminder jobs) supply the current time. Status
// validation is the caller's responsibility (see model.IsValidStatus).
func ApplyStatus(row *model.TaskRow, newStatus string, now int64) *model.TaskRow {
	row.Status = newStatus
	row.UpdatedAt = now
	switch newStatus {
	case model.StatusTodo, model.StatusInProgress:
		row.CompletedAt = nil
		row.CancelledAt = nil
	case model.StatusDone:
		row.CompletedAt = &now
		row.CancelledAt = nil
	case model.StatusCancelled:
		row.CancelledAt = &now
		row.CompletedAt = nil
	}
	return row
}
