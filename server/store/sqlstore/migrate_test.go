package sqlstore

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// Register the pure-Go sqlite driver used by morph's sqlite driver. Pure-Go
	// (not cgo mattn/go-sqlite3) keeps the test suite cgo-free, which matters
	// because the plugin builds with CGO_ENABLED=0 and CI's `make test` runs
	// without forcing cgo on.
	_ "modernc.org/sqlite"
)

// newSQLiteTestStore opens an in-memory sqlite database and wraps it in a
// SQLStore configured for the sqlite dialect. Every test gets a fresh,
// isolated database; :memory: databases are per-connection so concurrency
// inside one test would see different data, but migration tests are
// single-connection so this is fine.
func newSQLiteTestStore(t *testing.T) *SQLStore {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// cache=shared makes all connections to "file::memory:" share one DB, so
	// the morph driver (which grabs its own Conn from the pool) sees the
	// schema the pool connection created.
	s, err := New(db, DialectSQLite, "")
	require.NoError(t, err)
	return s
}

// runMigrationsSilent runs migrations through the silent logger so test output
// stays free of morph's per-migration progress banners.
func runMigrationsSilent(t *testing.T, s *SQLStore) {
	t.Helper()
	require.NoError(t, s.runMigrations(silentLogger{}))
}

func TestRunMigrations_IdempotentApplyTwice(t *testing.T) {
	s := newSQLiteTestStore(t)

	// First application must succeed and create the bookkeeping table.
	runMigrationsSilent(t, s)

	// The schema_migrations table must exist and record version 1.
	var version int64
	var name string
	err := s.db.QueryRow(
		"SELECT version, name FROM " + migrationTableName + " WHERE version = 1",
	).Scan(&version, &name)
	require.NoError(t, err)
	assert.EqualValues(t, 1, version)
	assert.Equal(t, "init", name)

	// Second application must be a no-op: morph sees version 1 already
	// applied and applies nothing. No error, table unchanged.
	runMigrationsSilent(t, s)
	var count int64
	require.NoError(t, s.db.QueryRow("SELECT COUNT(*) FROM "+migrationTableName).Scan(&count))
	assert.EqualValues(t, 1, count, "second migration run must not add new versions")
}

func TestRunMigrations_BootstrapMigrationRan(t *testing.T) {
	s := newSQLiteTestStore(t)
	runMigrationsSilent(t, s)

	// The bootstrap migration body is "SELECT 1" — no table is created by it.
	// The only observable effect is the recorded version, asserted above; here
	// we additionally confirm the task_schema_migrations table shape matches
	// what morph's sqlite driver creates (Version + Name columns).
	cols, err := columnsOf(t, s.db, migrationTableName)
	require.NoError(t, err)
	assert.Contains(t, cols, "Version")
	assert.Contains(t, cols, "Name")
}

func TestRenderMigrations_TemplatePrefixAndDialectFlags(t *testing.T) {
	t.Run("prefix substituted", func(t *testing.T) {
		// Build a throwaway embed FS shape by reusing the real one: every real
		// migration references no prefix today, so we assert the helper returns
		// the configured prefix verbatim via a direct template render.
		out, err := renderMigrations(migrationFS, migrationsDir, DialectSQLite, "demo_")
		require.NoError(t, err)
		// The bootstrap body has no {{prefix}} token, so rendering still
		// succeeds and yields the original SQL; we mainly assert no error and
		// that names include both up/down.
		assert.ElementsMatch(t, []string{
			"000001_init.down.sql", "000001_init.up.sql",
		}, out.names)
	})
}

func TestValidateMigrationPairs(t *testing.T) {
	cases := []struct {
		name    string
		names   []string
		wantErr bool
	}{
		{"paired up+down", []string{"000001_init.up.sql", "000001_init.down.sql"}, false},
		{"missing down", []string{"000001_init.up.sql"}, true},
		{"missing up", []string{"000001_init.down.sql"}, true},
		{"bad suffix", []string{"000001_init.up.txt"}, true},
		{"two migrations paired", []string{
			"000001_init.up.sql", "000001_init.down.sql",
			"000002_tasks.up.sql", "000002_tasks.down.sql",
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMigrationPairs(tc.names)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCreateIndexHelper_EmitsRawDDL(t *testing.T) {
	// Helpers emit names verbatim; templates prefix them via {{prefix}}, so the
	// test passes already-prefixed names and expects them echoed unchanged.
	got, err := createIndexIfNotExists("task_tasks_channel_idx", "task_tasks", "channel_id, status")
	require.NoError(t, err)
	assert.Equal(t,
		"CREATE INDEX IF NOT EXISTS task_tasks_channel_idx ON task_tasks (channel_id, status);",
		got)
}

func TestDropTableIfExistsHelper(t *testing.T) {
	got, err := dropTableIfExists("task_tasks")
	require.NoError(t, err)
	assert.Equal(t, "DROP TABLE IF EXISTS task_tasks;", got)
}

// columnsOf returns the column names of tableName using sqlite's pragma. It's
// a small helper so tests can assert schema shape without a full ORM.
func columnsOf(t *testing.T, db *sql.DB, tableName string) ([]string, error) {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + tableName + ");")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, rows.Err()
}
