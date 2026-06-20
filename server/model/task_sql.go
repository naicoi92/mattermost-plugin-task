package model

// This file holds the SQL-store data types introduced by the KV → SQL
// migration (milestone 12). They are intentionally separate from the legacy
// KV-backed model.Task / model.Comment / model.ReminderMetadata until M5-1
// removes the KV layer: the legacy types stay usable for the live service,
// while the SQL repositories operate on the normalized rows below. M3-2 swaps
// the service over to these types and M5-1 deletes the legacy ones.

// TaskRow is the normalized row of the task_tasks table. It carries only the
// core 1:1 attributes; relations (members, reminders, posts, comments, events)
// live in their own tables and are assembled into TaskView by GetTaskView.
//
// Field order is not load-bearing — repositories select columns by name.
type TaskRow struct {
	// ID is the ULID primary key.
	ID string `json:"id" db:"id"`
	// Summary is the one-line task title.
	Summary string `json:"summary" db:"summary"`
	// Description is the long-form body (markdown). Empty string, not nil.
	Description string `json:"description" db:"description"`
	// ChannelID scopes the task; "" means a personal task.
	ChannelID string `json:"channel_id" db:"channel_id"`
	// ParentTaskID is the ULID of the parent task for subtasks; "" (stored as
	// NULL) means a top-level task.
	ParentTaskID string `json:"parent_task_id,omitempty" db:"parent_task_id"`
	// Status is one of the Status* constants (todo/in_progress/done/cancelled).
	Status string `json:"status" db:"status"`
	// OrderKey is the global fractional-index rank used for Kanban ordering.
	OrderKey string `json:"order_key" db:"order_key"`
	// IsAllDay marks due_at as a date (no time component) for rendering.
	IsAllDay bool `json:"is_all_day" db:"is_all_day"`
	// DueAt is the due timestamp in ms UTC; nil means no due date.
	DueAt *int64 `json:"due_at,omitempty" db:"due_at"`
	// CompletedAt is set when the task transitions to done; nil otherwise.
	CompletedAt *int64 `json:"completed_at,omitempty" db:"completed_at"`
	// CancelledAt is set when the task transitions to cancelled; nil otherwise.
	CancelledAt *int64 `json:"cancelled_at,omitempty" db:"cancelled_at"`
	// CreatedAt is the creation timestamp in ms UTC.
	CreatedAt int64 `json:"created_at" db:"created_at"`
	// UpdatedAt is the last-modification timestamp in ms UTC, bumped on every
	// transition so the WebSocket sequence advances monotonically.
	UpdatedAt int64 `json:"updated_at" db:"updated_at"`
}

// TaskView is the assembled DTO returned by REST/WS. It embeds the core
// TaskRow and denormalizes the relations back into the flat shape the webapp
// and legacy REST contract expect (creator_id, assignee_id, post ids,
// reminder fields). This keeps the JSON response stable across the migration
// while the storage layer is fully normalized underneath.
type TaskView struct {
	TaskRow
	// CreatorID is the user who created the task (task_members role=creator).
	CreatorID string `json:"creator_id"`
	// AssigneeID is the user assigned to the task (task_members role=assignee).
	// "" means unassigned.
	AssigneeID string `json:"assignee_id"`
	// ChannelPostID is the card root-post id in the channel, if any. Tracked
	// via task_posts kind=channel.
	ChannelPostID string `json:"channel_post_id,omitempty"`
	// DMPostID is the card post id in the assignee's DM, if any. Tracked via
	// task_posts kind=dm.
	DMPostID string `json:"dm_post_id,omitempty"`
	// ReminderOffset is how many ms before due the reminder fires; nil means
	// no reminder. Assembled from task_reminders.
	ReminderOffset *int64 `json:"reminder_offset,omitempty"`
	// ReminderFired is true once the reminder has fired. Assembled from
	// task_reminders.fired_at.
	ReminderFired bool `json:"reminder_fired"`
	// SubtaskDone / SubtaskTotal give Kanban progress (e.g. "5/12 done").
	// Populated by GetTaskView; zero/zero when the task has no subtasks.
	SubtaskDone  int `json:"subtask_done"`
	SubtaskTotal int `json:"subtask_total"`
	// CommentCount is the number of thread replies linked to this task.
	// Populated by GetTaskView.
	CommentCount int `json:"comment_count"`
}
