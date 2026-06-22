package main

import (
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

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
