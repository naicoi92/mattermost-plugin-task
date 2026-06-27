package sqlstore

import (
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// Register the pure-Go sqlite driver used by morph's sqlite driver. Pure-Go
	// (not cgo mattn/go-sqlite3) keeps the test suite cgo-free, which matters
	// because the plugin builds with CGO_ENABLED=0 and CI's `make test` runs
	// without forcing cgo on.
	_ "modernc.org/sqlite"
)

// testDBCounter produces a unique per-call database name so each test gets
// its own isolated in-memory sqlite database. modernc/sqlite keys shared
// in-memory DBs by the filename in the "file:<name>?mode=memory&cache=shared"
// DSN; a unique name keeps tests from seeing each other's data. It is an
// atomic.Int64 so t.Parallel tests don't race the counter.
var testDBCounter atomic.Int64

// newSQLiteTestStore opens an isolated in-memory sqlite database and wraps it
// in a SQLStore configured for the sqlite dialect. Each call allocates a
// unique shared-cache DB name so tests are isolated; foreign keys are enabled
// via the _pragma DSN parameter so FK ON DELETE CASCADE behaves the same way
// postgres does in production.
func newSQLiteTestStore(t *testing.T) *SQLStore {
	t.Helper()
	id := testDBCounter.Add(1)
	dsn := fmt.Sprintf("file:testdb%d?mode=memory&cache=shared&_pragma=foreign_keys(1)", id)
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// Force the connection pool to a single open connection: shared-cache
	// in-memory DBs are per-process and the morph driver grabs its own Conn
	// from the pool, so we keep the pool to one connection to guarantee every
	// query sees the same schema and data.
	db.SetMaxOpenConns(1)

	// cache=shared + a unique name makes every connection to this DSN share
	// one DB, so the morph driver's connection sees the schema the test set
	// up; foreign_keys pragma is applied per-connection by the driver via the
	// _pragma param so FK CASCADE works in tests exactly as in postgres.
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

	// The schema_migrations table must exist and record the bootstrap (v1).
	var version int64
	var name string
	err := s.db.QueryRow(
		"SELECT version, name FROM "+migrationTableName+" WHERE version = 1",
	).Scan(&version, &name)
	require.NoError(t, err)
	assert.EqualValues(t, 1, version)
	assert.Equal(t, "init", name)

	// Snapshot the applied-version count after the first run; the number grows
	// as migrations are added, so we compare run-1 vs run-2 rather than
	// hard-coding a count.
	var afterFirst int64
	require.NoError(t, s.db.QueryRow("SELECT COUNT(*) FROM "+migrationTableName).Scan(&afterFirst))
	require.GreaterOrEqual(t, afterFirst, int64(1))

	// Second application must be a no-op: morph sees every version already
	// applied and applies nothing. The applied-version count must not grow.
	runMigrationsSilent(t, s)
	var afterSecond int64
	require.NoError(t, s.db.QueryRow("SELECT COUNT(*) FROM "+migrationTableName).Scan(&afterSecond))
	assert.Equal(t, afterFirst, afterSecond, "second migration run must not add new versions")
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

// TestRunMigrations_OverdueTrackingColumn asserts migration 000011 added the
// nullable last_overdue_sent_at BIGINT column to the tasks table. This is the
// dedupe bookkeeping column for the daily overdue notification job: the
// scheduler stamps it with now-ms each time it sends an overdue DM, and skips a
// task whose stamp is already within the current UTC day (see change
// notification-overdue-and-context, design D2).
func TestRunMigrations_OverdueTrackingColumn(t *testing.T) {
	s := newSQLiteTestStore(t)
	runMigrationsSilent(t, s)

	cols, err := columnsOf(t, s.db, s.tableName(taskTableShort))
	require.NoError(t, err)
	assert.Contains(t, cols, "last_overdue_sent_at",
		"migration 000011 must add last_overdue_sent_at to the tasks table")
}

func TestRenderMigrations_TemplatePrefixAndDialectFlags(t *testing.T) {
	// Use a synthetic fstest.MapFS so the assertion really exercises
	// {{prefix}} substitution and {{if sqlite}}/{{if postgres}} branches,
	// independent of whatever the real bootstrap migration happens to contain.
	t.Run("prefix and dialect branch render", func(t *testing.T) {
		fsys := fstest.MapFS{
			"migrations/000001_tpl.up.sql": &fstest.MapFile{
				Data: []byte(`CREATE TABLE {{prefix}}items (id {{if sqlite}}INTEGER{{else}}BIGINT{{end}});`),
			},
			"migrations/000001_tpl.down.sql": &fstest.MapFile{
				Data: []byte(`{{dropIfExists (printf "%sitems" (prefix))}}`),
			},
		}

		t.Run("sqlite dialect + demo_ prefix", func(t *testing.T) {
			out, err := renderMigrations(fsys, migrationsDir, DialectSQLite, "demo_")
			require.NoError(t, err)
			up, aErr := out.asset("000001_tpl.up.sql")
			require.NoError(t, aErr)
			assert.Equal(t, "CREATE TABLE demo_items (id INTEGER);", string(up))
			down, dErr := out.asset("000001_tpl.down.sql")
			require.NoError(t, dErr)
			assert.Equal(t, "DROP TABLE IF EXISTS demo_items;", strings.TrimSpace(string(down)))
		})

		t.Run("postgres dialect uses BIGINT branch", func(t *testing.T) {
			out, err := renderMigrations(fsys, migrationsDir, DialectPostgres, "task_")
			require.NoError(t, err)
			up, aErr := out.asset("000001_tpl.up.sql")
			require.NoError(t, aErr)
			assert.Contains(t, string(up), "id BIGINT")
			assert.Contains(t, string(up), "CREATE TABLE task_items")
		})
	})

	t.Run("real migrations render without error", func(t *testing.T) {
		out, err := renderMigrations(migrationFS, migrationsDir, DialectSQLite, "task_")
		require.NoError(t, err)
		// At least the bootstrap pair; more migrations land in later PRs, so
		// assert presence rather than exact match to stay forward-compatible.
		assert.Contains(t, out.names, "000001_init.up.sql")
		assert.Contains(t, out.names, "000001_init.down.sql")
		// Every up must have a down (validateMigrationPairs already enforces
		// this, but the assertion documents the invariant for readers).
		assert.GreaterOrEqual(t, len(out.names), 2)
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
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Logf("close pragma rows: %v", cerr)
		}
	}()
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
