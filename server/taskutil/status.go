package taskutil

import "github.com/naicoi92/mattermost-plugin-task/server/model"

// ApplyStatus transitions task to newStatus according to the plugin's canonical
// status state machine, stamps UpdatedAt with now (ms since epoch), and returns
// the same task pointer for chaining.
//
// Timestamp rules:
//
//	todo / in_progress: clear CompletedAt and CancelledAt (task is active again)
//	done:               set CompletedAt = now, clear CancelledAt
//	cancelled:          set CancelledAt = now, clear CompletedAt
//
// now is taken as a parameter so the function is pure and testable; callers
// (command/REST handlers, reminder jobs) supply the current time. Status
// validation is the caller's responsibility (see model.IsValidStatus).
func ApplyStatus(task *model.Task, newStatus string, now int64) *model.Task {
	task.Status = newStatus
	task.UpdatedAt = now
	switch newStatus {
	case model.StatusTodo, model.StatusInProgress:
		task.CompletedAt = nil
		task.CancelledAt = nil
	case model.StatusDone:
		task.CompletedAt = &now
		task.CancelledAt = nil
	case model.StatusCancelled:
		task.CancelledAt = &now
		task.CompletedAt = nil
	}
	return task
}
