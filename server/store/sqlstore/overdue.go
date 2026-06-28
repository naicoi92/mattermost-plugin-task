package sqlstore

import (
	"context"
	"errors"
	"fmt"

	sq "github.com/Masterminds/squirrel"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/store"
)

// ListOverdueTasks returns every task whose due_at is in the past (due_at <
// nowMs) and whose status is NOT terminal (done/cancelled), ordered by
// order_key. The daily overdue notification job calls this to decide whom to
// DM. Per-day dedupe (last_overdue_sent_at) is NOT applied here — the caller
// checks the stamp so the query stays simple and unit-testable on its own
// (change notification-overdue-and-context, design D3).
func (s *SQLStore) ListOverdueTasks(ctx context.Context, nowMs int64) ([]model.TaskRow, error) {
	qb := s.builder().
		Select(taskColumns...).
		From(s.tableName(taskTableShort)).
		Where(sq.NotEq{"due_at": nil}).
		Where(sq.Lt{"due_at": nowMs}).
		Where(sq.NotEq{"status": []string{model.StatusDone, model.StatusCancelled}}).
		OrderBy("order_key ASC")
	rows, err := qb.QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list overdue tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanTaskRows(rows)
}

// MarkOverdueSent stamps last_overdue_sent_at = ms (UTC) on a task so the daily
// overdue job can dedupe: a task already stamped within the current UTC day is
// skipped on the next scan. Returns store.ErrTaskNotFound when no row matches
// the id (design D2).
// ClaimOverdueSent atomically stamps last_overdue_sent_at = ms ONLY IF the row's
// current stamp is older than claimAfterMs (or NULL). Returns (claimed, err):
// claimed=true means this caller won the race and should send the DM; claimed=false
// means another runner already claimed the task for this dedupe window.
// This moves the "not sent today" check into the write path so two plugin
// instances can't both read a stale stamp and both send a DM (CodeRabbit review).
func (s *SQLStore) ClaimOverdueSent(ctx context.Context, taskID string, ms, claimAfterMs int64) (bool, error) {
	if taskID == "" {
		return false, errors.New("claim overdue sent: id is required")
	}
	res, err := s.builder().
		Update(s.tableName(taskTableShort)).
		Set("last_overdue_sent_at", ms).
		Where(sq.Eq{"id": taskID}).
		Where(sq.Or{
			sq.Lt{"last_overdue_sent_at": claimAfterMs},
			sq.Eq{"last_overdue_sent_at": nil},
		}).
		ExecContext(ctx)
	if err != nil {
		return false, fmt.Errorf("claim overdue sent %s: %w", taskID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim overdue sent %s: rows affected: %w", taskID, err)
	}
	if rows == 0 {
		// Either the task doesn't exist OR another runner already claimed it.
		if _, err := s.GetTask(ctx, taskID); err != nil {
			return false, store.ErrTaskNotFound
		}
		return false, nil
	}
	return true, nil
}

func (s *SQLStore) MarkOverdueSent(ctx context.Context, taskID string, ms int64) error {
	if taskID == "" {
		return errors.New("mark overdue sent: id is required")
	}
	res, err := s.builder().
		Update(s.tableName(taskTableShort)).
		Set("last_overdue_sent_at", ms).
		Where(sq.Eq{"id": taskID}).
		ExecContext(ctx)
	if err != nil {
		return fmt.Errorf("mark overdue sent %s: %w", taskID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark overdue sent %s: rows affected: %w", taskID, err)
	}
	if rows == 0 {
		return store.ErrTaskNotFound
	}
	return nil
}
