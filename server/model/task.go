// Package model defines the core business-data types for the Task plugin.
// These types are pure data carriers: they hold no logic beyond their JSON tags.
// ULID generation and status transitions are implemented separately (see issue #5).
package model

// Task statuses, stored on Task.Status.
const (
	StatusTodo       = "todo"
	StatusInProgress = "in_progress"
	StatusDone       = "done"
	StatusCancelled  = "cancelled"
)

// Task is the core task entity. IDs are ULID strings (globally unique and
// time-sortable, so no sequence counter is needed).
type Task struct {
	ID             string `json:"id"`
	Summary        string `json:"summary"`
	Description    string `json:"description"`
	ChannelID      string `json:"channel_id"`
	CreatorID      string `json:"creator_id"`
	AssigneeID     string `json:"assignee_id"`
	ChannelPostID  string `json:"channel_post_id"`
	DMPostID       string `json:"dm_post_id"`
	Due            *int64 `json:"due,omitempty"` // due date as ms timestamp; nil means no due date
	IsAllDay       bool   `json:"is_all_day"`
	Status         string `json:"status"`    // one of the Status* constants
	OrderKey       string `json:"order_key"` // global fractional-index rank for Kanban ordering
	CompletedAt    *int64 `json:"completed_at,omitempty"`
	CancelledAt    *int64 `json:"cancelled_at,omitempty"`
	ParentTaskID   string `json:"parent_task_id"`            // non-empty for subtasks
	ReminderOffset *int64 `json:"reminder_offset,omitempty"` // ms before due; nil means no reminder
	ReminderFired  bool   `json:"reminder_fired"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

// IsValidStatus reports whether s is one of the valid Task status values.
func IsValidStatus(s string) bool {
	switch s {
	case StatusTodo, StatusInProgress, StatusDone, StatusCancelled:
		return true
	default:
		return false
	}
}
