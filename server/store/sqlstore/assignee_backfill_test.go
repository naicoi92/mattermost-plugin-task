package sqlstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/store"
)

// renderAssigneeBackfillSQL renders the 000009_assignee_backfill.up migration
// through the same dialect template the live migration engine uses, so the
// test executes the LITERAL migration SQL (no drift between test and migration).
// It returns the rendered body for the sqlite dialect with no table prefix
// (the test store's configuration).
func renderAssigneeBackfillSQL(t *testing.T, s *SQLStore) string {
	t.Helper()
	// Render with the store's real prefix (DefaultTablePrefix = "task_") so
	// the executed SQL targets the same tables the migrations created.
	rm, err := renderMigrations(migrationFS, migrationsDir, s.dbType, s.tablePrefix)
	require.NoError(t, err)
	body, err := rm.asset("000009_assignee_backfill.up.sql")
	require.NoError(t, err)
	require.NotEmpty(t, body, "000009_assignee_backfill.up.sql rendered")
	return string(body)
}

// TestAssigneeBackfill_MaterializesMemberRowFromAssignedEvent is the CASE-B
// regression test for the assignee-403 gap (Problem 2, AC4). The deterministic
// listComments repro (TestListComments_AssigneeAllowedViaAssignAction) proved
// the live Create/Assign paths write the role='assignee' member row and that
// assembleTask loads it, so newly-assigned tasks are fine. The residual bug is
// HISTORICAL data: tasks that carry an audit 'assigned' event but no
// role='assignee' task_members row (e.g. assigned by an older plugin build
// before SetAssignee persisted the edge). This test seeds that exact bad-data
// shape directly and asserts the 000009 backfill materializes the member row
// from task_events.assigned.to_value so assembleTask can populate
// task.AssigneeID and the assignee stops seeing 403.
//
// It executes the literal rendered 000009 migration SQL (via
// renderAssigneeBackfillSQL), not a re-derived statement, so a change to the
// migration is covered here.
func TestAssigneeBackfill_MaterializesMemberRowFromAssignedEvent(t *testing.T) {
	s := newSQLiteTestStore(t)
	runMigrationsSilent(t, s) // 000009 ran once on the empty DB (no-op).
	ctx := context.Background()

	// T1: assigned event, NO member row — the bug shape. Expect backfill to
	// create the role='assignee' edge carrying to_value's user id.
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	require.NoError(t, s.AppendTaskEvent(ctx, model.TaskEvent{
		ID: "E1", TaskID: "T1", ActorID: "u-actor",
		EventType: model.EventAssigned, ToValue: new("u-a1"), CreatedAt: 1_000,
	}))

	// T2: assigned then later UNASSIGNED. Currently has no assignee, so the
	// backfill must NOT materialize a row (the latest assign-related event is
	// 'unassigned'). This guards against backfilling a stale assignee.
	mustCreate(t, s, ctx, fixture("T2", "k2"))
	require.NoError(t, s.AppendTaskEvent(ctx, model.TaskEvent{
		ID: "E2a", TaskID: "T2", ActorID: "u-actor",
		EventType: model.EventAssigned, ToValue: new("u-a2"), CreatedAt: 1_000,
	}))
	require.NoError(t, s.AppendTaskEvent(ctx, model.TaskEvent{
		ID: "E2b", TaskID: "T2", ActorID: "u-actor",
		EventType: model.EventUnassigned, FromValue: new("u-a2"), ToValue: new(""), CreatedAt: 2_000,
	}))

	// T3: assigned event AND a real role='assignee' member row already present.
	// The backfill must NOT overwrite the live edge (idempotent / NOT EXISTS).
	mustCreate(t, s, ctx, fixture("T3", "k3"))
	require.NoError(t, s.AddMember(ctx, "T3", "u-existing", model.MemberRoleAssignee))
	require.NoError(t, s.AppendTaskEvent(ctx, model.TaskEvent{
		ID: "E3", TaskID: "T3", ActorID: "u-actor",
		EventType: model.EventAssigned, ToValue: new("u-a3"), CreatedAt: 1_000,
	}))

	// Re-execute the literal 000009 migration SQL against the seeded bad data
	// (idempotent: it already ran once on the empty DB).
	_, err := s.db.Exec(renderAssigneeBackfillSQL(t, s))
	require.NoError(t, err, "000009 backfill SQL must exec cleanly")

	// T1: member row materialized from to_value.
	got, err := s.GetMemberByRole(ctx, "T1", model.MemberRoleAssignee)
	require.NoError(t, err)
	assert.Equal(t, "u-a1", got, "T1 assignee edge backfilled from task_events.assigned.to_value")

	// T2: no assignee edge (latest event was unassigned).
	_, err = s.GetMemberByRole(ctx, "T2", model.MemberRoleAssignee)
	assert.ErrorIs(t, err, store.ErrMemberNotFound, "T2 (currently unassigned) must not get a stale assignee edge")

	// T3: live edge preserved, not overwritten.
	got, err = s.GetMemberByRole(ctx, "T3", model.MemberRoleAssignee)
	require.NoError(t, err)
	assert.Equal(t, "u-existing", got, "T3 live assignee edge preserved (backfill idempotent)")

	// Idempotency: running the backfill a second time changes nothing.
	_, err = s.db.Exec(renderAssigneeBackfillSQL(t, s))
	require.NoError(t, err)
	got, err = s.GetMemberByRole(ctx, "T1", model.MemberRoleAssignee)
	require.NoError(t, err)
	assert.Equal(t, "u-a1", got, "second backfill run is a no-op (idempotent)")
}
