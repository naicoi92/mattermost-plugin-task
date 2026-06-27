package model

// TaskRow is one row of the task_tasks table: the core 1:1 attributes only.
// Relations (members, reminders, posts, comments, events) live in their own
// tables and are assembled into a Task by the service's assembleTask helper.
//
// The field set is exactly the task_tasks columns; CreateTask accepts a
// TaskRow (not a Task) so a caller can't accidentally pass AssigneeID /
// ChannelPostID expecting them to be persisted — those require their own
// AddMember / AddPost calls. Field order is not load-bearing; repositories
// select columns by name.
type TaskRow struct {
	// ID is the ULID primary key.
	ID string `json:"id" db:"id"`
	// Summary is the one-line task title.
	Summary string `json:"summary" db:"summary"`
	// Description is the long-form body (markdown). Empty string, not nil.
	Description string `json:"description" db:"description"`
	// ChannelID scopes the task to a channel (team channel, DM, or self-DM).
	// Required and non-empty under the all-channel model.
	ChannelID string `json:"channel_id" db:"channel_id"`
	// ChannelPostID is the Mattermost post id of the task's home-channel card
	// (NULL until a card is posted). Stored inline on task_tasks under the
	// all-channel model; the legacy task_posts table has been collapsed.
	ChannelPostID *string `json:"channel_post_id,omitempty" db:"channel_post_id"`
	// ParentTaskID is the ULID of the parent task for subtasks; "" (stored as
	// NULL) means a top-level task.
	ParentTaskID string `json:"parent_task_id,omitempty" db:"parent_task_id"`
	// Status is one of the Status* constants (todo/in_progress/done/cancelled).
	Status string `json:"status" db:"status"`
	// Priority is one of the Priority* constants
	// (standard/important/urgent). Mirrors the Mattermost message-priority enum.
	Priority string `json:"priority" db:"priority"`
	// OrderKey is the global fractional-index rank used for Kanban ordering.
	OrderKey string `json:"order_key" db:"order_key"`
	// IsAllDay marks the due date as a date (no time component) for rendering.
	IsAllDay bool `json:"is_all_day" db:"is_all_day"`
	// DueAt is the due timestamp in ms UTC; nil means no due date. The json tag
	// is "due" to match the webapp's REST contract; the db tag is "due_at"
	// matching the column name. The field name follows the *At convention of
	// the sibling timestamp fields (CreatedAt, UpdatedAt, CompletedAt).
	DueAt *int64 `json:"due,omitempty" db:"due_at"`
	// CompletedAt is set when the task transitions to done; nil otherwise.
	CompletedAt *int64 `json:"completed_at,omitempty" db:"completed_at"`
	// CancelledAt is set when the task transitions to cancelled; nil otherwise.
	CancelledAt *int64 `json:"cancelled_at,omitempty" db:"cancelled_at"`
	// CreatedAt is the creation timestamp in ms UTC.
	CreatedAt int64 `json:"created_at" db:"created_at"`
	// UpdatedAt is the last-modification timestamp in ms UTC, bumped on every
	// transition so the WebSocket sequence advances monotonically.
	UpdatedAt int64 `json:"updated_at" db:"updated_at"`
	// LastOverdueSentAt is stamped with now-ms (UTC) each time the overdue
	// notification scheduler sends a DM for this task. The job skips any task
	// whose stamp already falls within the current UTC day, so a task gets at
	// most one overdue DM per calendar day. NULL means "never sent".
	LastOverdueSentAt *int64 `json:"last_overdue_sent_at,omitempty" db:"last_overdue_sent_at"`
}

// Task is the task entity returned to REST/WS consumers. It embeds the core
// TaskRow (id, summary, status, due, ...) and denormalizes the relations back
// into the flat JSON shape the webapp expects: creator_id and assignee_id
// (from task_members), the card post id (now inline on task_tasks), the
// reminder state (from task_reminders), plus subtask progress and comment
// count aggregates.
//
// The storage layer is fully normalized (6 tables); Task is the assembled
// projection consumers see. It is built by assembleTask at read time, never
// written directly — writes go through the repository methods on each table.
// (The posts table was collapsed into an inline channel_post_id column; see
// migration 000010.)
type Task struct {
	TaskRow
	// CreatorID is the user who created the task (task_members role=creator).
	CreatorID string `json:"creator_id"`
	// AssigneeID is the user assigned to the task (task_members role=assignee).
	// "" means unassigned.
	AssigneeID string `json:"assignee_id"`
	// ReminderOffset is how many ms before due the reminder fires; nil means
	// no reminder. Assembled from task_reminders.
	ReminderOffset *int64 `json:"reminder_offset,omitempty"`
	// ReminderFired is true once the reminder has fired. Assembled from
	// task_reminders.fired_at.
	ReminderFired bool `json:"reminder_fired"`
	// SubtaskDone / SubtaskTotal give Kanban progress (e.g. "5/12 done").
	// Zero/zero when the task has no subtasks.
	SubtaskDone int `json:"subtask_done"`
	// SubtaskTotal is the count of direct subtasks.
	SubtaskTotal int `json:"subtask_total"`
	// CommentCount is the number of thread replies linked to this task.
	CommentCount int `json:"comment_count"`
}
