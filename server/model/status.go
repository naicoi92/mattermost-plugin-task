package model

// Task status constants. These are the only values the Status column on
// task_tasks may hold; IsValidStatus rejects anything else so the namespace
// stays controlled and the UI can render every status.
const (
	StatusTodo       = "todo"
	StatusInProgress = "in_progress"
	StatusDone       = "done"
	StatusCancelled  = "cancelled"
)

// IsValidStatus reports whether s is one of the recognized Status* constants.
// The store and service layers reject anything else so a typo can't persist an
// unknown status that the UI later can't render.
func IsValidStatus(s string) bool {
	switch s {
	case StatusTodo, StatusInProgress, StatusDone, StatusCancelled:
		return true
	default:
		return false
	}
}

// IsTerminalStatus reports whether s is a status from which a task does not
// transition further on its own (done/cancelled). Used by the reminder and
// subtask-cascade logic to skip terminal tasks.
func IsTerminalStatus(s string) bool {
	return s == StatusDone || s == StatusCancelled
}
