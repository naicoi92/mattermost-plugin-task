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

// membersTableShort is the short (unprefixed) name of the task_members table.
const membersTableShort = "members"

// membersColumns lists every column of task_members in scan order.
var membersColumns = []string{"task_id", "user_id", "role", "created_at"}

// store.ErrMemberNotFound is returned by GetMemberByRole / RemoveMember when no
// matching membership edge exists.

// AddMember records a (task, user, role) edge. The composite primary key makes
// it idempotent: re-adding an existing edge is a no-op rather than an error,
// so callers don't need to check existence first. Unknown roles are rejected
// to keep the role namespace controlled.
func (s *SQLStore) AddMember(ctx context.Context, taskID, userID, role string) error {
	if taskID == "" {
		return errors.New("add member: task id is required")
	}
	if userID == "" {
		return errors.New("add member: user id is required")
	}
	if !model.IsValidMemberRole(role) {
		return fmt.Errorf("add member: invalid role %q", role)
	}
	// ON CONFLICT DO NOTHING renders the insert idempotent across dialects:
	//   postgres : ON CONFLICT (task_id, user_id, role) DO NOTHING
	//   sqlite   : ON CONFLICT (task_id, user_id, role) DO NOTHING
	//   mysql    : INSERT IGNORE (rendered by Suffix is dialect-specific;
	//              the MVP runs postgres+sqlite only, see tasks.go MySQL note)
	qb := s.builder().
		Insert(s.tableName(membersTableShort)).
		Columns(membersColumns...).
		Values(taskID, userID, role, time.Now().UnixMilli()).
		Suffix("ON CONFLICT DO NOTHING")
	if _, err := qb.ExecContext(ctx); err != nil {
		return fmt.Errorf("add member %s/%s/%s: %w", taskID, userID, role, err)
	}
	return nil
}

// RemoveMember deletes a single (task, user, role) edge. It is a no-op
// (returns nil) if the edge does not exist, matching the idempotent contract
// of AddMember — callers that need to assert existence should use
// GetMemberByRole first.
func (s *SQLStore) RemoveMember(ctx context.Context, taskID, userID, role string) error {
	if taskID == "" || userID == "" || role == "" {
		return errors.New("remove member: task id, user id and role are required")
	}
	_, err := s.builder().
		Delete(s.tableName(membersTableShort)).
		Where(sq.Eq{
			"task_id": taskID,
			"user_id": userID,
			"role":    role,
		}).
		ExecContext(ctx)
	if err != nil {
		return fmt.Errorf("remove member %s/%s/%s: %w", taskID, userID, role, err)
	}
	return nil
}

// ListMembers returns every membership edge of a task, ordered by created_at
// then role so the result is stable. Used to populate the Task relations
// and by permission checks.
func (s *SQLStore) ListMembers(ctx context.Context, taskID string) ([]model.TaskMember, error) {
	if taskID == "" {
		return nil, errors.New("list members: task id is required")
	}
	rows, err := s.builder().
		Select(membersColumns...).
		From(s.tableName(membersTableShort)).
		Where(sq.Eq{"task_id": taskID}).
		OrderByClause(s.escapeField("created_at") + " ASC").
		QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list members %s: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()

	var members []model.TaskMember
	for rows.Next() {
		var m model.TaskMember
		if err := rows.Scan(&m.TaskID, &m.UserID, &m.Role, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("list members %s: %w", taskID, err)
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list members %s: %w", taskID, err)
	}
	return members, nil
}

// GetMemberByRole returns the user_id of the single member holding `role` on
// `taskID` (e.g. the assignee or creator). Returns store.ErrMemberNotFound when no
// such edge exists.
//
// The schema (PK task_id+user_id+role) deliberately allows several users in
// the same role on a task — that's the future-proofing decision (see
// SQL_MIGRATION_PLAN.md §3.2): the schema is ready for multi-assignee /
// follower without a later migration. This method returns the first such row;
// the MVP enforces one creator + one assignee per task at the application
// layer. If multi-assignee is enabled later, a List variant should be added.
func (s *SQLStore) GetMemberByRole(ctx context.Context, taskID, role string) (string, error) {
	if taskID == "" {
		return "", errors.New("get member by role: task id is required")
	}
	if role == "" {
		return "", errors.New("get member by role: role is required")
	}
	// Validate role so a typo like "assginee" fails loudly here rather than
	// being masked as store.ErrMemberNotFound.
	if !model.IsValidMemberRole(role) {
		return "", fmt.Errorf("get member by role: invalid role %q", role)
	}
	var userID string
	err := s.builder().
		Select("user_id").
		From(s.tableName(membersTableShort)).
		Where(sq.Eq{"task_id": taskID, "role": role}).
		Limit(1).
		QueryRowContext(ctx).
		Scan(&userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", store.ErrMemberNotFound
		}
		return "", fmt.Errorf("get member by role %s/%s: %w", taskID, role, err)
	}
	return userID, nil
}

// SwapAssignee atomically changes the assignee of a task from oldID to newID.
// It is implemented as a single UPDATE on the role='assignee' edge, which is
// inherently atomic; a DELETE+INSERT would also work but requires a
// transaction (WithTx, M3-1) to stay atomic across two statements. If the
// task has no current assignee edge (oldID mismatch is tolerated), the method
// falls back to inserting the new assignee directly.
func (s *SQLStore) SwapAssignee(ctx context.Context, taskID, oldID, newID string) error {
	if taskID == "" || newID == "" {
		return errors.New("swap assignee: task id and new user id are required")
	}
	if oldID == newID {
		// Nothing to do; idempotent.
		return nil
	}
	// Update the assignee row's user_id in place. RowsAffected==0 means
	// either no assignee edge exists or it already pointed at newID; either
	// way we ensure the new assignee edge exists.
	res, err := s.builder().
		Update(s.tableName(membersTableShort)).
		Set("user_id", newID).
		Where(sq.Eq{"task_id": taskID, "role": model.MemberRoleAssignee}).
		ExecContext(ctx)
	if err != nil {
		return fmt.Errorf("swap assignee %s: %w", taskID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("swap assignee %s: rows affected: %w", taskID, err)
	}
	if rows == 0 {
		// No existing assignee edge — create one.
		if err := s.AddMember(ctx, taskID, newID, model.MemberRoleAssignee); err != nil {
			return fmt.Errorf("swap assignee %s: insert new: %w", taskID, err)
		}
	}
	return nil
}
