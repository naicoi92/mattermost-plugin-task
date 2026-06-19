package model

// ReminderMetadata is the structured value stored under the
// idx:reminder:{taskID} index key. It carries everything the reminder scheduler
// job needs to decide whether to fire, without re-reading the task entity: the
// absolute due time, the offset (how many ms before due the reminder fires),
// and the assignee to DM.
//
// Storing a snapshot here (rather than just an offset) lets the per-minute job
// scan idx:reminder: keys alone and fire in O(N) where N is the number of
// pending reminders, instead of loading every task.
type ReminderMetadata struct {
	// DueMS is the task's due date as a millisecond epoch timestamp.
	DueMS int64 `json:"due_ms"`
	// OffsetMS is how many milliseconds before DueMS the reminder should fire.
	OffsetMS int64 `json:"offset_ms"`
	// AssigneeID is the user to DM when the reminder fires. May be empty if the
	// task has no assignee at the time the index was built; the scheduler skips
	// firing in that case.
	AssigneeID string `json:"assignee_id"`
}

// FireMS returns the absolute millisecond timestamp at which the reminder should
// fire, i.e. DueMS - OffsetMS. If OffsetMS is zero or negative the reminder
// fires at the due time.
func (r ReminderMetadata) FireMS() int64 {
	if r.OffsetMS <= 0 {
		return r.DueMS
	}
	return r.DueMS - r.OffsetMS
}
