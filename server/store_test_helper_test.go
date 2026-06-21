package main

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/store"
	"github.com/naicoi92/mattermost-plugin-task/server/store/sqlstore"

	_ "modernc.org/sqlite"
)

// srvTestDBCounter allocates a unique in-memory sqlite DB name per test so the
// server-package HTTP/integration tests are isolated.
var srvTestDBCounter atomic.Int64

// newTestTaskStore opens an isolated in-memory sqlite SQLStore with migrations
// applied. Used by the server-package tests (api/dialog/websocket/integration)
// to back the task.Service with a real store so WithTx atomicity and FK
// cascades are exercised truthfully.
func newTestTaskStore(t *testing.T) store.Store {
	t.Helper()
	id := srvTestDBCounter.Add(1)
	dsn := fmt.Sprintf("file:srvpkgdb%d?mode=memory&cache=shared&_pragma=foreign_keys(1)", id)
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	s, err := sqlstore.New(db, sqlstore.DialectSQLite, "")
	require.NoError(t, err)
	require.NoError(t, s.RunMigrations(nil))
	return s
}

// ctxBG is a background context shorthand for test fixture calls.
func ctxBG() context.Context { return context.Background() }

// allTasks returns every task in the store as assembled Task entities with
// creator/assignee populated from task_members. A test helper that replaces
// the old fakeStore.tasks map iteration: the SQL store has no .tasks field, so
// tests assert via this snapshot. Uses the unfiltered ListAllTasksForTest path
// (the production list path is scope-driven and has no "list everything" case).
func allTasks(t *testing.T, s store.Store) []*taskmodel.Task {
	t.Helper()
	rows, err := s.ListAllTasksForTest(ctxBG())
	require.NoError(t, err)
	out := make([]*taskmodel.Task, 0, len(rows))
	for i := range rows {
		row := rows[i]
		t2 := &taskmodel.Task{TaskRow: row}
		// Populate creator/assignee so tests asserting on them (the old
		// fakeStore exposed them directly) keep working.
		if c, e := s.GetMemberByRole(ctxBG(), row.ID, taskmodel.MemberRoleCreator); e == nil {
			t2.CreatorID = c
		}
		if a, e := s.GetMemberByRole(ctxBG(), row.ID, taskmodel.MemberRoleAssignee); e == nil {
			t2.AssigneeID = a
		}
		out = append(out, t2)
	}
	return out
}
