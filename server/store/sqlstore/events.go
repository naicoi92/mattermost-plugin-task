package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	sq "github.com/Masterminds/squirrel"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
)

// eventsTableShort is the short (unprefixed) name of the task_events table.
const eventsTableShort = "events"

// eventColumns lists every column of task_events in scan order.
var eventColumns = []string{"id", "task_id", "actor_id", "event_type", "from_value", "to_value", "created_at"}

// AppendTaskEvent inserts one audit-log row. The service layer calls this in
// the SAME transaction as the change it records (M3-3 wires the atomicity via
// WithTx), so a failed change can never leave an audit event behind, and a
// successful change can never be missing its audit entry.
//
// EventType is validated against the Event* constants so an unknown type
// fails loudly rather than landing as an un-renderable row.
func (s *SQLStore) AppendTaskEvent(ctx context.Context, e model.TaskEvent) error {
	if e.ID == "" {
		return errors.New("append event: id is required")
	}
	if e.TaskID == "" {
		return errors.New("append event: task id is required")
	}
	if e.ActorID == "" {
		return errors.New("append event: actor id is required")
	}
	if !model.IsValidEventType(e.EventType) {
		return fmt.Errorf("append event: invalid event type %q", e.EventType)
	}
	_, err := s.builder().
		Insert(s.tableName(eventsTableShort)).
		Columns(eventColumns...).
		Values(e.ID, e.TaskID, e.ActorID, e.EventType, e.FromValue, e.ToValue, e.CreatedAt).
		ExecContext(ctx)
	if err != nil {
		return fmt.Errorf("append event %s/%s: %w", e.TaskID, e.EventType, err)
	}
	return nil
}

// ListTaskEvents returns the most recent audit events for a task, newest first
// (the index idx_events_task is (task_id, created_at DESC) so this is an index
// scan). limit caps the page; <=0 falls back to a sane default.
func (s *SQLStore) ListTaskEvents(ctx context.Context, taskID string, limit int) ([]model.TaskEvent, error) {
	if taskID == "" {
		return nil, errors.New("list events: task id is required")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.builder().
		Select(eventColumns...).
		From(s.tableName(eventsTableShort)).
		Where(sq.Eq{"task_id": taskID}).
		OrderByClause(s.escapeField("created_at") + " DESC, " + s.escapeField("id") + " DESC").
		Limit(toUint64(limit)).
		QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list events %s: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()

	var events []model.TaskEvent
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("list events %s: %w", taskID, err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list events %s: %w", taskID, err)
	}
	return events, nil
}

// scanEvent scans one task_events row from a scanner (*sql.Rows or *sql.Row).
// from_value / to_value are nullable TEXT, so they scan into sql.NullString and
// unwrap to *string (nil when the column is NULL).
func scanEvent(r scanner) (model.TaskEvent, error) {
	var e model.TaskEvent
	var from, to sql.NullString
	if err := r.Scan(&e.ID, &e.TaskID, &e.ActorID, &e.EventType, &from, &to, &e.CreatedAt); err != nil {
		return model.TaskEvent{}, err
	}
	if from.Valid {
		fv := from.String
		e.FromValue = &fv
	}
	if to.Valid {
		tv := to.String
		e.ToValue = &tv
	}
	return e, nil
}
