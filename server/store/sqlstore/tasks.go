package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/store"
)

// taskTableShort is the short (unprefixed) name of the task_tasks table; the
// store's tableName() adds the configured prefix.
const taskTableShort = "task_tasks"

// taskColumns lists every column of task_tasks in the order scanRow reads
// them. Keeping it in one place means a missing column in a SELECT surfaces
// as a single compile-site edit rather than scattered magic numbers.
var taskColumns = []string{
	"id", "summary", "description", "channel_id", "parent_task_id",
	"status", "order_key", "is_all_day", "due_at", "completed_at",
	"cancelled_at", "created_at", "updated_at",
}

// ErrTaskNotFound is returned by GetTask/UpdateTask/DeleteTask/TouchTaskUpdatedAt
// when no row matches the given id. Service-layer code checks errors.Is to
// translate it into the appropriate not-found response.
var ErrTaskNotFound = errors.New("task not found")

// CreateTask inserts a task row and returns the stored row. The caller is
// responsible for assigning the ULID id and the initial order_key; the store
// does not generate them so the service layer controls ordering semantics
// (fractional indexing) and id allocation.
func (s *SQLStore) CreateTask(ctx context.Context, task model.TaskRow) (model.TaskRow, error) {
	if task.ID == "" {
		return model.TaskRow{}, errors.New("create task: id is required")
	}
	qb := s.builder().
		Insert(s.tableName(taskTableShort)).
		Columns(taskColumns...).
		Values(
			task.ID, task.Summary, task.Description, task.ChannelID,
			nullableString(task.ParentTaskID), task.Status, task.OrderKey,
			task.IsAllDay, task.DueAt, task.CompletedAt, task.CancelledAt,
			task.CreatedAt, task.UpdatedAt,
		)
	if _, err := qb.ExecContext(ctx); err != nil {
		return model.TaskRow{}, fmt.Errorf("create task %s: %w", task.ID, err)
	}
	return task, nil
}

// GetTask selects a single task by id. Returns ErrTaskNotFound when the row is
// absent so callers can distinguish "missing" from a genuine database error.
func (s *SQLStore) GetTask(ctx context.Context, id string) (*model.TaskRow, error) {
	qb := s.builder().
		Select(taskColumns...).
		From(s.tableName(taskTableShort)).
		Where(sq.Eq{"id": id})
	row := qb.QueryRowContext(ctx)
	tr, err := scanTaskRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTaskNotFound
		}
		return nil, fmt.Errorf("get task %s: %w", id, err)
	}
	return tr, nil
}

// UpdateTask writes every column of `task` back to its row and returns the
// stored row. It uses UPDATE ... RETURNING * so the result reflects any DB
// defaults or triggers (e.g. an updated_at set by a future trigger).
func (s *SQLStore) UpdateTask(ctx context.Context, task model.TaskRow) (model.TaskRow, error) {
	if task.ID == "" {
		return model.TaskRow{}, errors.New("update task: id is required")
	}
	// Column/value pairs excluding the PK; the PK is the WHERE target.
	updates := map[string]any{
		"summary":        task.Summary,
		"description":    task.Description,
		"channel_id":     task.ChannelID,
		"parent_task_id": nullableString(task.ParentTaskID),
		"status":         task.Status,
		"order_key":      task.OrderKey,
		"is_all_day":     task.IsAllDay,
		"due_at":         task.DueAt,
		"completed_at":   task.CompletedAt,
		"cancelled_at":   task.CancelledAt,
		"updated_at":     task.UpdatedAt,
	}
	// Postgres/mysql/sqlite all support RETURNING on UPDATE since sqlite 3.35
	// (2021) and the plugin's min sqlite is well past that; postgres has it
	// natively and mysql 8.0+ supports it.
	qb := s.builder().
		Update(s.tableName(taskTableShort)).
		SetMap(updates).
		Where(sq.Eq{"id": task.ID}).
		Suffix("RETURNING " + joinColumns(taskColumns))
	row := qb.QueryRowContext(ctx)
	updated, err := scanTaskRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.TaskRow{}, ErrTaskNotFound
		}
		return model.TaskRow{}, fmt.Errorf("update task %s: %w", task.ID, err)
	}
	return *updated, nil
}

// TouchTaskUpdatedAt advances updated_at to `ts` only when `ts` is greater
// than the current value, keeping the column monotonic so a stale update
// (e.g. a subtask whose CreatedAt predates a concurrent parent change) can
// never push the WebSocket sequence backward and drop newer events.
//
// The monotonic guard is implemented in portable SQL via a CASE expression
// rather than the GREATEST() function, because modernc.org/sqlite (the
// pure-Go driver used by the test suite) predates sqlite's GREATEST support
// and would otherwise fail the tests. CASE is supported by every dialect.
func (s *SQLStore) TouchTaskUpdatedAt(ctx context.Context, id string, ts int64) error {
	if id == "" {
		return errors.New("touch updated_at: id is required")
	}
	// updated_at = CASE WHEN updated_at < ? THEN ? ELSE updated_at END
	// keeps the larger of the two values, computed entirely in the database
	// so the read+write is atomic across concurrent touches on the same row.
	res, err := s.builder().
		Update(s.tableName(taskTableShort)).
		Set("updated_at", sq.Expr("CASE WHEN updated_at < ? THEN ? ELSE updated_at END", ts, ts)).
		Where(sq.Eq{"id": id}).
		ExecContext(ctx)
	if err != nil {
		return fmt.Errorf("touch updated_at %s: %w", id, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("touch updated_at %s: rows affected: %w", id, err)
	}
	if rows == 0 {
		return ErrTaskNotFound
	}
	return nil
}

// DeleteTask removes a task row. FK ON DELETE CASCADE on parent_task_id (and
// the child tables added by M2-2..M2-6) automatically removes dependents, so
// the service no longer needs the KV-style ListKeys + loop + index-delete.
func (s *SQLStore) DeleteTask(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("delete task: id is required")
	}
	res, err := s.builder().
		Delete(s.tableName(taskTableShort)).
		Where(sq.Eq{"id": id}).
		ExecContext(ctx)
	if err != nil {
		return fmt.Errorf("delete task %s: %w", id, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete task %s: rows affected: %w", id, err)
	}
	if rows == 0 {
		return ErrTaskNotFound
	}
	return nil
}

// ListTasks returns one page of tasks plus the full filtered total. It pushes
// WHERE/ORDER BY/LIMIT into the database (the KV store's core scaling problem)
// and runs a parallel COUNT(*) for the total. See store.ListQuery for the
// filter/pagination contract.
//
// The query fetches Limit+1 rows to compute HasMore without a second list
// query: if more than Limit rows come back, a further page exists.
func (s *SQLStore) ListTasks(ctx context.Context, q store.ListQuery) (store.PageResult, error) {
	if q.Limit <= 0 {
		return store.PageResult{}, errors.New("list tasks: limit must be > 0")
	}

	builder, err := s.applyTaskFilters(q, taskColumns...)
	if err != nil {
		return store.PageResult{}, err
	}

	// Keyset pagination on order_key (strictly greater than the cursor),
	// ordered ascending so pages flow in creation/kanban order.
	builder = builder.
		OrderByClause(s.escapeField("order_key") + " ASC").
		Limit(uint64(q.Limit) + 1) // +1 to detect HasMore.
	if q.AfterOrderKey != "" {
		builder = builder.Where(sq.Gt{"order_key": q.AfterOrderKey})
	}

	rows, err := builder.QueryContext(ctx)
	if err != nil {
		return store.PageResult{}, fmt.Errorf("list tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	tasks, err := scanTaskRows(rows)
	if err != nil {
		return store.PageResult{}, fmt.Errorf("list tasks: %w", err)
	}

	hasMore := len(tasks) > q.Limit
	if hasMore {
		// Trim the sentinel +1 row from the returned page.
		tasks = tasks[:q.Limit]
	}

	total, err := s.countTasks(ctx, q)
	if err != nil {
		return store.PageResult{}, err
	}

	items := make([]any, 0, len(tasks))
	for i := range tasks {
		// ListTasks returns TaskRow values; TaskView assembly (creator/
		// assignee/posts/reminder) is a separate GetTaskView call to keep the
		// hot list path a single join-free query.
		items = append(items, &tasks[i])
	}
	return store.PageResult{Items: items, Total: total, HasMore: hasMore}, nil
}

// CountTasksByStatus groups the filtered set by status and returns a map of
// status -> count. It powers the Kanban column headers ("todo 4 / in_progress
// 2 / done 7") with a single GROUP BY instead of loading every task.
func (s *SQLStore) CountTasksByStatus(ctx context.Context, q store.ListQuery) (map[string]int, error) {
	builder, err := s.applyTaskFilters(q, "status", "COUNT(*) AS cnt")
	if err != nil {
		return nil, err
	}
	builder = builder.GroupBy("status")

	rows, err := builder.QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("count tasks by status: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make(map[string]int)
	for rows.Next() {
		var status string
		var cnt int
		if err := rows.Scan(&status, &cnt); err != nil {
			return nil, fmt.Errorf("count tasks by status: %w", err)
		}
		result[status] = cnt
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count tasks by status: %w", err)
	}
	return result, nil
}

// SearchTasks performs a substring search on summary and description using the
// dialect's case-insensitive LIKE/ILIKE. The % wildcards and escape handling
// come from likePattern; callers must pair the predicate with ESCAPE '\' (see
// the likePattern doc). Results are capped at `limit`.
func (s *SQLStore) SearchTasks(ctx context.Context, keyword string, limit int) ([]model.TaskRow, error) {
	if keyword == "" {
		return nil, errors.New("search tasks: keyword is required")
	}
	if limit <= 0 {
		limit = 20
	}
	pattern := likePattern(keyword)
	op := "LIKE"
	if s.dbType == DialectPostgres {
		op = "ILIKE"
	}
	qb := s.builder().
		Select(taskColumns...).
		From(s.tableName(taskTableShort)).
		Where(sq.Or{
			sq.Expr(s.escapeField("summary")+" "+op+" ? ESCAPE '\\'", pattern),
			sq.Expr(s.escapeField("description")+" "+op+" ? ESCAPE '\\'", pattern),
		}).
		OrderByClause(s.escapeField("updated_at") + " DESC").
		Limit(uint64(limit))

	rows, err := qb.QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("search tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanTaskRows(rows)
}

// ListSubtasks returns the direct children of parentID, ordered by creation
// time. Parent/child is modelled via the self-FK parent_task_id.
func (s *SQLStore) ListSubtasks(ctx context.Context, parentID string) ([]model.TaskRow, error) {
	if parentID == "" {
		return nil, errors.New("list subtasks: parent id is required")
	}
	qb := s.builder().
		Select(taskColumns...).
		From(s.tableName(taskTableShort)).
		Where(sq.Eq{"parent_task_id": parentID}).
		OrderByClause(s.escapeField("created_at") + " ASC")
	rows, err := qb.QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list subtasks %s: %w", parentID, err)
	}
	defer func() { _ = rows.Close() }()
	return scanTaskRows(rows)
}

// SubtaskProgress returns (done, total) counts for a parent's children with a
// single GROUP BY query, instead of loading every subtask into Go. `done`
// counts children whose status is done.
func (s *SQLStore) SubtaskProgress(ctx context.Context, parentID string) (done, total int, err error) {
	if parentID == "" {
		return 0, 0, errors.New("subtask progress: parent id is required")
	}
	qb := s.builder().
		Select("COUNT(*)", fmt.Sprintf("SUM(CASE WHEN status = '%s' THEN 1 ELSE 0 END)", model.StatusDone)).
		From(s.tableName(taskTableShort)).
		Where(sq.Eq{"parent_task_id": parentID})
	var count int
	var sum sql.NullInt64
	if err := qb.QueryRowContext(ctx).Scan(&count, &sum); err != nil {
		return 0, 0, fmt.Errorf("subtask progress %s: %w", parentID, err)
	}
	if sum.Valid {
		done = int(sum.Int64)
	}
	return done, count, nil
}

// NextGlobalOrderKey returns the current maximum order_key across all tasks,
// or "" when the table is empty. The service uses it to assign the next task
// a higher key for Kanban append-to-end ordering.
func (s *SQLStore) NextGlobalOrderKey(ctx context.Context) (string, error) {
	var max sql.NullString
	err := s.builder().
		Select("MAX(order_key)").
		From(s.tableName(taskTableShort)).
		QueryRowContext(ctx).
		Scan(&max)
	if err != nil {
		return "", fmt.Errorf("next order key: %w", err)
	}
	if !max.Valid {
		return "", nil
	}
	return max.String, nil
}

// applyTaskFilters returns a SelectBuilder with the WHERE clauses implied by
// the ListQuery (scope, status, due). Columns selects what to project; both
// ListTasks and CountTasksByStatus use it so the WHERE stays identical.
func (s *SQLStore) applyTaskFilters(q store.ListQuery, columns ...string) (sq.SelectBuilder, error) {
	b := s.builder().Select(columns...).From(s.tableName(taskTableShort))

	switch q.Scope {
	case store.ScopeMine:
		// JOIN task_members role='assignee' for the user. task_members is
		// created in M2-2; until then this branch returns an error so a
		// misconfigured caller fails loudly instead of returning all rows.
		return b, errors.New("list tasks: scope=mine requires task_members (M2-2)")
	case store.ScopeChannel:
		if q.ChannelID == "" {
			return b, errors.New("list tasks: scope=channel requires ChannelID")
		}
		b = b.Where(sq.Eq{"channel_id": q.ChannelID})
	case store.ScopeAll:
		// no extra filter
	default:
		return b, fmt.Errorf("list tasks: unknown scope %q", q.Scope)
	}

	if q.Status != "" {
		b = b.Where(sq.Eq{"status": q.Status})
	}
	if q.Due != store.DueAny {
		if q.DueAsOf == 0 {
			return b, errors.New("list tasks: due filter requires DueAsOf")
		}
		start, end, ok := dueWindow(q.Due, q.DueAsOf)
		if !ok {
			return b, fmt.Errorf("list tasks: unknown due filter %q", q.Due)
		}
		// due_at IS NOT NULL is implied by any range filter.
		b = b.Where(sq.NotEq{"due_at": nil})
		if start > 0 {
			b = b.Where(sq.GtOrEq{"due_at": start})
		}
		if end > 0 {
			b = b.Where(sq.Lt{"due_at": end})
		}
		if q.Due == store.DueOverdue {
			// Overdue excludes done/cancelled tasks. NotEq with a slice
			// renders as "status NOT IN (?, ?)".
			b = b.Where(sq.NotEq{"status": []string{model.StatusDone, model.StatusCancelled}})
		}
	}
	return b, nil
}

// countTasks returns the filtered total via COUNT(*). It rebuilds the same
// WHERE (minus order_key cursor / limit) so the total matches the page set.
func (s *SQLStore) countTasks(ctx context.Context, q store.ListQuery) (int, error) {
	// Reuse applyTaskFilters with a single COUNT(*) projection; drop the
	// pagination cursor so the count covers the whole filtered set.
	countQuery := q
	countQuery.AfterOrderKey = ""
	b, err := s.applyTaskFilters(countQuery, "COUNT(*)")
	if err != nil {
		return 0, err
	}
	var total int
	if err := b.QueryRowContext(ctx).Scan(&total); err != nil {
		return 0, fmt.Errorf("count tasks: %w", err)
	}
	return total, nil
}

// dueWindow translates a DueFilter + reference timestamp into a [start, end)
// ms-UTC range. `ok` is false for an unrecognised filter. Overdue uses start=0
// (any past due) with no upper bound on the *due_at* value but a separate
// status exclusion applied by the caller.
func dueWindow(f store.DueFilter, nowMs int64) (start, end int64, ok bool) {
	now := time.UnixMilli(nowMs).UTC()
	switch f {
	case store.DueOverdue:
		// due_at < start-of-today AND not completed (status filter applied
		// separately). start = start of today; end unset.
		return 0, startOfDayMs(now), true
	case store.DueToday:
		s := startOfDayMs(now)
		return s, s + dayMs, true
	case store.DueWeek:
		s := startOfDayMs(now)
		return s, s + 7*dayMs, true
	default:
		return 0, 0, false
	}
}

const dayMs = int64(24 * time.Hour / time.Millisecond)

// startOfDayMs returns the ms-UTC timestamp of the start of the given day.
func startOfDayMs(t time.Time) int64 {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).UnixMilli()
}

// nullableString converts an empty string to a nil interface so the column is
// stored as NULL rather than the empty string. Used for parent_task_id which
// is NULL for top-level tasks (so the self-FK and NOT NULL constraints stay
// consistent).
func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// joinColumns renders a comma-separated column list for RETURNING clauses.
func joinColumns(cols []string) string {
	return strings.Join(cols, ", ")
}

// scanTaskRow scans a single task row from something with a Scan method that
// accepts the taskColumns layout (either *sql.Row or *sql.Rows). parent_task_id
// is NULL for top-level tasks, so it scans into sql.NullString and unwraps to
// the empty string (the model's representation of "no parent").
func scanTaskRow(r scanner) (*model.TaskRow, error) {
	var t model.TaskRow
	var parentTaskID sql.NullString
	if err := r.Scan(
		&t.ID, &t.Summary, &t.Description, &t.ChannelID, &parentTaskID,
		&t.Status, &t.OrderKey, &t.IsAllDay, &t.DueAt, &t.CompletedAt,
		&t.CancelledAt, &t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if parentTaskID.Valid {
		t.ParentTaskID = parentTaskID.String
	}
	return &t, nil
}

// scanTaskRows scans every remaining row from a *sql.Rows into a slice.
func scanTaskRows(rows *sql.Rows) ([]model.TaskRow, error) {
	var tasks []model.TaskRow
	for rows.Next() {
		t, err := scanTaskRow(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tasks, nil
}

// scanner is the common Scan surface of *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}
