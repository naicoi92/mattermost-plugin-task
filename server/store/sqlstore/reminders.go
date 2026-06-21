package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"

	"github.com/naicoi92/mattermost-plugin-task/server/store"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
)

// remindersTableShort is the short (unprefixed) name of the task_reminders table.
const remindersTableShort = "reminders"

// reminderColumns lists every column of task_reminders in scan order.
var reminderColumns = []string{"id", "task_id", "offset_ms", "fired_at", "created_at"}

// matching reminder row exists.

// SetReminder sets the reminder for a task. The MVP enforces one reminder per
// task, backed by the UNIQUE(task_id) constraint: the INSERT uses ON CONFLICT
// (task_id) DO UPDATE so a concurrent set can't create a duplicate and the
// existing row's id/created_at are replaced atomically in a single statement.
// When multi-reminder support is enabled later, drop the UNIQUE constraint and
// this DO UPDATE clause.
//
// id is the caller-assigned ULID for the new reminder row (the service layer
// owns id allocation, matching CreateTask). offsetMS is how many ms before
// due_at the reminder fires; the CHECK(offset_ms >= 0) constraint rejects
// negatives at the DB.
func (s *SQLStore) SetReminder(ctx context.Context, id, taskID string, offsetMS int64) (model.TaskReminder, error) {
	if id == "" {
		return model.TaskReminder{}, errors.New("set reminder: id is required")
	}
	if taskID == "" {
		return model.TaskReminder{}, errors.New("set reminder: task id is required")
	}

	r := model.TaskReminder{
		ID:        id,
		TaskID:    taskID,
		OffsetMS:  offsetMS,
		CreatedAt: s.nowMilli(),
	}
	// ON CONFLICT (task_id) DO UPDATE: if a reminder already exists for this
	// task, replace its id/offset_ms/fired_at/created_at in place. This is a
	// single atomic statement, so two concurrent SetReminder calls can't
	// leave two rows. (task_id, fired_at reset to NULL so a rescheduled
	// reminder fires fresh.)
	_, err := s.builder().
		Insert(s.tableName(remindersTableShort)).
		Columns(reminderColumns...).
		Values(r.ID, r.TaskID, r.OffsetMS, r.FiredAt, r.CreatedAt).
		Suffix("ON CONFLICT (task_id) DO UPDATE SET " +
			"id = EXCLUDED.id, offset_ms = EXCLUDED.offset_ms, " +
			"fired_at = NULL, created_at = EXCLUDED.created_at").
		ExecContext(ctx)
	if err != nil {
		return model.TaskReminder{}, fmt.Errorf("set reminder %s: %w", taskID, err)
	}
	return r, nil
}

// ClearReminder removes the reminder(s) for a task. Idempotent: returns nil if
// the task had no reminder.
func (s *SQLStore) ClearReminder(ctx context.Context, taskID string) error {
	if taskID == "" {
		return errors.New("clear reminder: task id is required")
	}
	if _, err := s.builder().
		Delete(s.tableName(remindersTableShort)).
		Where(sq.Eq{"task_id": taskID}).
		ExecContext(ctx); err != nil {
		return fmt.Errorf("clear reminder %s: %w", taskID, err)
	}
	return nil
}

// ListReminders returns every reminder row for a task. The MVP has at most one,
// but the method returns a slice so multi-reminder support needs no signature
// change later.
func (s *SQLStore) ListReminders(ctx context.Context, taskID string) ([]model.TaskReminder, error) {
	if taskID == "" {
		return nil, errors.New("list reminders: task id is required")
	}
	rows, err := s.builder().
		Select(reminderColumns...).
		From(s.tableName(remindersTableShort)).
		Where(sq.Eq{"task_id": taskID}).
		OrderByClause(s.escapeField("created_at") + " ASC").
		QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list reminders %s: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()

	var reminders []model.TaskReminder
	for rows.Next() {
		r, err := scanReminder(rows)
		if err != nil {
			return nil, fmt.Errorf("list reminders %s: %w", taskID, err)
		}
		reminders = append(reminders, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list reminders %s: %w", taskID, err)
	}
	return reminders, nil
}

// ListDueReminders returns every pending reminder that should fire now, in a
// single 3-table JOIN query. This replaces the KV store's ListReminderKeys +
// per-reminder GetReminder + GetTask N+1 pattern with one round-trip.
//
// A reminder is "due" when:
//   - fired_at IS NULL (still pending)
//   - the task is active (status todo or in_progress)
//   - the task has a due_at
//   - the fire time (due_at - offset_ms) is now or in the past
//   - the due_at is within graceMs of now (so a long-overdue task whose
//     reminder was missed doesn't fire stale — it fires once when re-enabled)
//
// The assignee is LEFT-joined so an unassigned task still surfaces (the
// scheduler skips firing when AssigneeID is empty).
func (s *SQLStore) ListDueReminders(ctx context.Context, nowMs, graceMs int64) ([]model.DueReminder, error) {
	// Use single-letter aliases (r/t/m) for the three joined tables so the
	// SELECT/WHERE column references are short and unambiguous. The full
	// prefixed table names appear only in FROM/JOIN.
	rTable := s.tableName(remindersTableShort)
	tTable := s.tableName(taskTableShort)
	mTable := s.tableName(membersTableShort)

	// assignee_id from the LEFT JOIN is nullable when the task has no
	// assignee; COALESCE to '' so the scan target is always a string.
	qb := s.builder().
		Select(
			"r.id", "r.task_id", "r.offset_ms",
			"t.due_at",
			"COALESCE(m.user_id, '') AS assignee_id",
		).
		From(rTable + " AS r").
		Join(tTable + " AS t ON t.id = r.task_id").
		LeftJoin(mTable + " AS m ON m.task_id = r.task_id AND m.role = '" + model.MemberRoleAssignee + "'").
		Where(sq.And{
			sq.Eq{"r.fired_at": nil},
			sq.Eq{"t.status": []string{model.StatusTodo, model.StatusInProgress}},
			sq.NotEq{"t.due_at": nil},
			// fire time (due_at - offset_ms) <= now
			sq.Expr("(t.due_at - r.offset_ms) <= ?", nowMs),
			// due_at >= now - graceMs (skip long-overdue, already-passed tasks)
			sq.GtOrEq{"t.due_at": nowMs - graceMs},
		}).
		OrderByClause("r.id ASC")

	rows, err := qb.QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list due reminders: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var due []model.DueReminder
	for rows.Next() {
		var d model.DueReminder
		if err := rows.Scan(&d.ReminderID, &d.TaskID, &d.OffsetMS, &d.DueAt, &d.AssigneeID); err != nil {
			return nil, fmt.Errorf("list due reminders: %w", err)
		}
		due = append(due, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list due reminders: %w", err)
	}
	return due, nil
}

// MarkReminderFired records that a reminder fired, so the next scheduler tick
// doesn't fire it again. firedAt is the ms-UTC timestamp to record.
func (s *SQLStore) MarkReminderFired(ctx context.Context, reminderID string, firedAt int64) error {
	if reminderID == "" {
		return errors.New("mark reminder fired: reminder id is required")
	}
	res, err := s.builder().
		Update(s.tableName(remindersTableShort)).
		Set("fired_at", firedAt).
		Where(sq.Eq{"id": reminderID}).
		ExecContext(ctx)
	if err != nil {
		return fmt.Errorf("mark reminder fired %s: %w", reminderID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark reminder fired %s: rows affected: %w", reminderID, err)
	}
	if rows == 0 {
		return store.ErrReminderNotFound
	}
	return nil
}

// scanReminder scans one task_reminders row. fired_at is nullable, so it scans
// into sql.NullInt64 and unwraps to *int64 (nil when pending).
func scanReminder(r scanner) (model.TaskReminder, error) {
	var rem model.TaskReminder
	var firedAt sql.NullInt64
	if err := r.Scan(&rem.ID, &rem.TaskID, &rem.OffsetMS, &firedAt, &rem.CreatedAt); err != nil {
		return model.TaskReminder{}, err
	}
	if firedAt.Valid {
		fa := firedAt.Int64
		rem.FiredAt = &fa
	}
	return rem, nil
}

// nowMilli returns the current time as ms UTC. A method on the store so tests
// could substitute a clock via a field override if deterministic time is
// needed; today it just wraps the package-level timeNowMilli.
func (s *SQLStore) nowMilli() int64 {
	return timeNowMilli()
}

// timeNowMilli is the package-level clock. Indirected as a var so tests can
// swap it for a deterministic value when a reminder's created_at needs to be
// assertable.
var timeNowMilli = func() int64 {
	return time.Now().UnixMilli()
}
