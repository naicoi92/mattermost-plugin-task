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
const taskTableShort = "tasks"

// taskColumns lists every column of task_tasks in the order scanRow reads
// them. Keeping it in one place means a missing column in a SELECT surfaces
// as a single compile-site edit rather than scattered magic numbers.
var taskColumns = []string{
	"id", "summary", "description", "channel_id", "parent_task_id",
	"status", "priority", "order_key", "is_all_day", "due_at", "completed_at",
	"cancelled_at", "created_at", "updated_at", "channel_post_id",
}

// when no row matches the given id. Service-layer code checks errors.Is to
// translate it into the appropriate not-found response.

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
			nullableString(task.ParentTaskID), task.Status, task.Priority,
			task.OrderKey, task.IsAllDay, task.DueAt, task.CompletedAt,
			task.CancelledAt, task.CreatedAt, task.UpdatedAt, task.ChannelPostID,
		)
	if _, err := qb.ExecContext(ctx); err != nil {
		return model.TaskRow{}, fmt.Errorf("create task %s: %w", task.ID, err)
	}
	return task, nil
}

// GetTask selects a single task by id. Returns store.ErrTaskNotFound when the row is
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
			return nil, store.ErrTaskNotFound
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
		"priority":       task.Priority,
		"order_key":      task.OrderKey,
		"is_all_day":     task.IsAllDay,
		"due_at":         task.DueAt,
		"completed_at":   task.CompletedAt,
		"cancelled_at":   task.CancelledAt,
		"updated_at":     task.UpdatedAt,
	}
	// Postgres and sqlite both support UPDATE ... RETURNING (sqlite since
	// 3.35, 2021; the plugin's min sqlite is well past that). MySQL does NOT
	// support RETURNING on UPDATE (incl. 8.0) — but the plugin's MVP runs
	// migrations/queries only against postgres (production) and sqlite (test),
	// so this is acceptable. If mysql becomes a supported production dialect,
	// split this into UPDATE then SELECT-by-id.
	qb := s.builder().
		Update(s.tableName(taskTableShort)).
		SetMap(updates).
		Where(sq.Eq{"id": task.ID}).
		Suffix("RETURNING " + joinColumns(taskColumns))
	row := qb.QueryRowContext(ctx)
	updated, err := scanTaskRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.TaskRow{}, store.ErrTaskNotFound
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
		return store.ErrTaskNotFound
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
		return store.ErrTaskNotFound
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
	// ordered ascending so pages flow in creation/kanban order. Qualify with
	// the t. alias because ScopeMine joins task_members which could otherwise
	// make order_key ambiguous.
	builder = builder.
		OrderByClause("t." + s.escapeField("order_key") + " ASC").
		Limit(toUint64(q.Limit + 1)) // +1 to detect HasMore.
	if q.AfterOrderKey != "" {
		builder = builder.Where(sq.Gt{"t.order_key": q.AfterOrderKey})
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
		// ListTasks returns TaskRow values; Task assembly (creator/
		// assignee/posts/reminder) is a separate assembleTask call to keep the
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
	builder = builder.GroupBy("t.status")

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
		Limit(toUint64(limit))

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
//
// The done count is built with a SUM(CASE WHEN status='done' THEN 1 ELSE 0 END)
// expression. model.StatusDone is interpolated via fmt.Sprintf rather than a
// bound parameter because squirrel does not support parameterised expressions
// inside Select(); this is safe because StatusDone is a compile-time constant
// defined in this module (not user input), so there is no injection surface.
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

// ListAllTasksForTest returns every task row ordered by order_key with no
// scope/filter. Test-only (see store.Store interface); production code uses the
// scope-driven ListTasks path.
func (s *SQLStore) ListAllTasksForTest(ctx context.Context) ([]model.TaskRow, error) {
	qb := s.builder().
		Select(taskColumns...).
		From(s.tableName(taskTableShort)).
		OrderByClause(s.escapeField("order_key") + " ASC")
	rows, err := qb.QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list all tasks (test): %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanTaskRows(rows)
}

// ListTasksByMember returns every task on which userID holds the creator or
// assignee role (JOIN task_members), ordered by order_key, capped at limit.
// Used by the deactivation hook to find DM-scoped tasks that may need
// migrating away from the deactivated user.
func (s *SQLStore) ListTasksByMember(ctx context.Context, userID string, limit int) ([]model.TaskRow, error) {
	if userID == "" {
		return nil, errors.New("list tasks by member: user id is required")
	}
	t := s.tableName(taskTableShort)
	m := s.tableName(membersTableShort)
	selCols := make([]string, len(taskColumns))
	for i, c := range taskColumns {
		selCols[i] = "t." + c
	}
	qb := s.builder().
		Select(selCols...).
		From(t + " t").
		Join(m + " m ON m.task_id = t.id").
		Where(sq.Eq{"m.user_id": userID}).
		OrderByClause(s.escapeField("t.order_key") + " ASC")
	if limit > 0 {
		qb = qb.Limit(uint64(limit))
	}
	rows, err := qb.QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tasks by member %s: %w", userID, err)
	}
	defer func() { _ = rows.Close() }()
	return scanTaskRows(rows)
}

// applyTaskFilters returns a SelectBuilder with the WHERE clauses implied by
// the ListQuery (scope, status, priority, due). Columns selects what to
// project; both ListTasks and CountTasksByStatus use it so the WHERE stays
// identical.
//
// The main table is always aliased `t` so that when ScopeDirect JOINs
// task_members (which also has a created_at column), SELECT/WHERE column
// references can be qualified unambiguously as t.<col>.
func (s *SQLStore) applyTaskFilters(q store.ListQuery, columns ...string) (sq.SelectBuilder, error) {
	// Qualify simple column names with the main-table alias `t`. When
	// ScopeDirect joins task_members (which also has a created_at column), bare
	// column names would be ambiguous; qualifying as t.<col> resolves it.
	// Expressions (COUNT(*), aggregates, "col AS x") are passed through
	// unchanged so we don't break their syntax.
	qcols := make([]string, len(columns))
	for i, c := range columns {
		qcols[i] = qualifyColumn(c)
	}
	b := s.builder().Select(qcols...).From(s.tableName(taskTableShort) + " AS t")

	switch q.Scope {
	case store.ScopeChannel:
		// "This channel" = tasks whose channel_id matches. One indexed
		// equality predicate (tasks_channel_idx); no join needed.
		if q.ChannelID == "" {
			return b, errors.New("list tasks: scope=channel requires ChannelID")
		}
		b = b.Where(sq.Eq{"t.channel_id": q.ChannelID})
	case store.ScopeDirect:
		// "DM" = tasks shared between exactly two users: every task on which
		// BOTH UserID AND PartnerID hold the assignee or creator role. The
		// mutual-membership requirement is what makes this a DM scope: a caller
		// can't enumerate a third user's tasks by guessing partner_id — only
		// tasks the caller is themselves a member of are returned.
		//
		// Implemented as two EXISTS subqueries (one per user) rather than a
		// single `user_id IN (me, partner)` union predicate, because the union
		// form would match tasks where only ONE of the two users is a member
		// (leaking the partner's tasks to an unrelated caller). Both EXISTS
		// subqueries use the members_user_idx (user_id, role) index.
		if q.UserID == "" || q.PartnerID == "" {
			return b, errors.New("list tasks: scope=direct requires UserID and PartnerID")
		}
		membersTable := s.tableName(membersTableShort)
		roles := "('assignee','creator')"
		b = b.Where(sq.And{
			sq.Expr(
				"EXISTS (SELECT 1 FROM "+membersTable+
					" AS mm WHERE mm.task_id = t.id AND mm.user_id = ? AND mm.role IN "+roles+")",
				q.UserID,
			),
			sq.Expr(
				"EXISTS (SELECT 1 FROM "+membersTable+
					" AS mp WHERE mp.task_id = t.id AND mp.user_id = ? AND mp.role IN "+roles+")",
				q.PartnerID,
			),
		})
	default:
		return b, fmt.Errorf("list tasks: unknown scope %q", q.Scope)
	}

	if q.Status != "" {
		b = b.Where(sq.Eq{"t.status": q.Status})
	}
	if q.Priority != "" {
		b = b.Where(sq.Eq{"t.priority": q.Priority})
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
		b = b.Where(sq.NotEq{"t.due_at": nil})
		if start > 0 {
			b = b.Where(sq.GtOrEq{"t.due_at": start})
		}
		if end > 0 {
			b = b.Where(sq.Lt{"t.due_at": end})
		}
		if q.Due == store.DueOverdue {
			// Overdue excludes done/cancelled tasks. NotEq with a slice
			// renders as "status NOT IN (?, ?)".
			b = b.Where(sq.NotEq{"t.status": []string{model.StatusDone, model.StatusCancelled}})
		}
	}
	return b, nil
}

// countTasks returns the filtered total. It rebuilds the same WHERE (minus
// order_key cursor / limit) so the total matches the page set. Both scopes use
// plain COUNT(*): ScopeChannel is a single-table filter, and ScopeDirect uses
// EXISTS subqueries (no JOIN row multiplication), so no DISTINCT is needed.
func (s *SQLStore) countTasks(ctx context.Context, q store.ListQuery) (int, error) {
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

// qualifyColumn prefixes a bare column name with the main-table alias `t.` so
// JOINs against task_members (which shares some column names like created_at)
// stay unambiguous. SQL expressions — anything containing a space, paren,
// comma, or already-qualified name — are returned unchanged so aggregates and
// aliases ("COUNT(*) AS cnt") keep their syntax.
func qualifyColumn(col string) string {
	if col == "" || strings.ContainsAny(col, " (),.*") {
		return col
	}
	return "t." + col
}

// toUint64 converts a non-negative int to uint64 without tripping gosec's G115
// (integer-overflow) check, which a bare uint64(n) cast does because a
// negative int would wrap. All callers in this package pass already-validated
// non-negative values (page limits); the clamp keeps the conversion sound
// even if a future caller forgets to validate.
func toUint64(n int) uint64 {
	if n < 0 {
		return 0
	}
	return uint64(n) //nolint:gosec // G115: n clamped to >= 0 above
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
	var channelPostID sql.NullString
	if err := r.Scan(
		&t.ID, &t.Summary, &t.Description, &t.ChannelID, &parentTaskID,
		&t.Status, &t.Priority, &t.OrderKey, &t.IsAllDay, &t.DueAt,
		&t.CompletedAt, &t.CancelledAt, &t.CreatedAt, &t.UpdatedAt, &channelPostID,
	); err != nil {
		return nil, err
	}
	if parentTaskID.Valid {
		t.ParentTaskID = parentTaskID.String
	}
	if channelPostID.Valid {
		t.ChannelPostID = &channelPostID.String
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
