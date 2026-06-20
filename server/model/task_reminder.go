package model

// TaskReminder is one row of the task_reminders table. Each reminder records
// how many ms before the task's due_at it should fire, plus a fired_at
// timestamp (nil = pending). The MVP enforces one reminder per task at the
// application layer; the schema is ready for multi-reminder per task without
// a later migration.
type TaskReminder struct {
	// ID is the ULID primary key (each reminder has its own id, distinct from
	// the task id).
	ID string `json:"id" db:"id"`
	// TaskID is the task this reminder belongs to.
	TaskID string `json:"task_id" db:"task_id"`
	// OffsetMS is how many milliseconds before due_at the reminder fires.
	OffsetMS int64 `json:"offset_ms" db:"offset_ms"`
	// FiredAt is the ms-UTC timestamp the reminder fired; nil means pending.
	FiredAt *int64 `json:"fired_at,omitempty" db:"fired_at"`
	// CreatedAt is the ms-UTC timestamp the reminder was set.
	CreatedAt int64 `json:"created_at" db:"created_at"`
}

// DueReminder is the JOIN result returned by ListDueReminders: everything the
// scheduler needs to fire one reminder's DM in a single query, without a
// follow-up GetTask / GetMemberByRole round-trip per reminder.
type DueReminder struct {
	// ReminderID is the task_reminders.id of the due reminder.
	ReminderID string `json:"reminder_id" db:"id"`
	// TaskID is the task the reminder belongs to.
	TaskID string `json:"task_id" db:"task_id"`
	// DueMS is the task's due_at (ms UTC), joined from task_tasks.
	DueMS int64 `json:"due_ms" db:"due_at"`
	// OffsetMS is how many ms before due the reminder fires.
	OffsetMS int64 `json:"offset_ms" db:"offset_ms"`
	// AssigneeID is the user to DM (joined from task_members role=assignee).
	// May be empty if the task has no assignee at fire time; the scheduler
	// skips firing in that case.
	AssigneeID string `json:"assignee_id" db:"assignee_id"`
}
