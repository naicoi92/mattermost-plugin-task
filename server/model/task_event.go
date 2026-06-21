package model

// TaskEvent types record what kind of transition an audit-log row represents.
// AppendTaskEvent validates against this set so a typo can't create an unknown
// event type that the audit-trail UI later can't render.
const (
	EventCreated            = "created"
	EventStatusChanged      = "status_changed"
	EventAssigned           = "assigned"
	EventUnassigned         = "unassigned"
	EventDueChanged         = "due_changed"
	EventSummaryChanged     = "summary_changed"
	EventDescriptionChanged = "description_changed"
	EventReminderSet        = "reminder_set"
	EventReminderFired      = "reminder_fired"
	EventReminderCleared    = "reminder_cleared"
	EventCommented          = "commented"
	EventSubtaskAdded       = "subtask_added"
	EventDeleted            = "deleted"
)

// TaskEvent is one row of the task_events audit log. Each state transition on
// a task appends exactly one row, in the same transaction as the change (M3-3),
// so the audit trail can never disagree with the data.
//
// FromValue/ToValue are JSON snapshots of the affected field (or the whole
// task for created/deleted); nil means "no prior" (create) or "no after"
// (delete). They are TEXT, not a typed column, so the audit payload can evolve
// without a schema change.
type TaskEvent struct {
	// ID is the ULID primary key (caller-assigned, matching CreateTask).
	ID string `json:"id" db:"id"`
	// TaskID is the task the event pertains to.
	TaskID string `json:"task_id" db:"task_id"`
	// ActorID is the user who triggered the transition.
	ActorID string `json:"actor_id" db:"actor_id"`
	// EventType is one of the Event* constants.
	EventType string `json:"event_type" db:"event_type"`
	// FromValue is the JSON snapshot of the prior value; nil for create.
	FromValue *string `json:"from_value,omitempty" db:"from_value"`
	// ToValue is the JSON snapshot of the new value; nil for delete.
	ToValue *string `json:"to_value,omitempty" db:"to_value"`
	// CreatedAt is the ms-UTC timestamp the event was recorded.
	CreatedAt int64 `json:"created_at" db:"created_at"`
}

// IsValidEventType reports whether t is one of the recognized Event*
// constants. The store rejects anything else so the event_type namespace stays
// controlled and the audit-trail UI can render every row.
func IsValidEventType(t string) bool {
	switch t {
	case EventCreated, EventStatusChanged, EventAssigned, EventUnassigned,
		EventDueChanged, EventSummaryChanged, EventDescriptionChanged,
		EventReminderSet, EventReminderFired, EventReminderCleared, EventCommented,
		EventSubtaskAdded, EventDeleted:
		return true
	default:
		return false
	}
}
