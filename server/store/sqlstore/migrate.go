package sqlstore

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/mattermost/morph"
	"github.com/mattermost/morph/drivers"
	mysqldriver "github.com/mattermost/morph/drivers/mysql"
	postgresdriver "github.com/mattermost/morph/drivers/postgres"
	sqlitedriver "github.com/mattermost/morph/drivers/sqlite"
	"github.com/mattermost/morph/sources/embedded"
	"github.com/pkg/errors"

	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi/cluster"
)

// Embed all migration SQL files at compile time. The path is kept relative to
// this file so the directive resolves against server/store/sqlstore/migrations.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationsDir is the embed subdirectory holding the .sql files.
const migrationsDir = "migrations"

// migrationMutexKey is the cluster-scoped mutex name. Only one plugin node
// across the cluster holds this lock while running migrations, so two pods
// activating concurrently can't race the schema_migrations bookkeeping table.
const migrationMutexKey = "TaskMigrationMutex"

// migrationTableName is the morph bookkeeping table that records which
// migration versions have been applied. It is itself a task_-prefixed table
// so it namespaces cleanly alongside the plugin's own tables.
const migrationTableName = "task_schema_migrations"

// RunMigrations applies every pending embedded migration to the store's
// database, exactly once, on a single cluster node.
//
// Cluster safety is provided in two layers (mirroring mattermost-plugin-boards):
//  1. pluginapi cluster.Mutex(migrationMutexKey) — only one plugin node in the
//     cluster enters the migration section; others block until it finishes.
//  2. morph's own schema_migrations bookkeeping — within that node, morph
//     records applied versions, so a second call is a no-op (idempotent).
//
// The sql parameter is unused today but kept for future data migrations
// (Go-level migrations that read KV and write SQL); Boards uses the same
// signature. Callers (OnActivate) already hold the *SQLStore.
func (s *SQLStore) RunMigrations(_ plugin.API) error {
	return s.runMigrations(nil)
}

// runMigrations is the shared implementation; the logger may be nil to use
// morph's default progress logger (production) or a silentLogger (tests) to
// keep test output free of per-migration progress banners.
func (s *SQLStore) runMigrations(logger morph.Logger) error {
	// Render the embedded .sql files through the dialect template before
	// handing them to morph: morph treats each migration body as opaque SQL,
	// so dialect-specific DDL (prefix substitution, IF NOT EXISTS guards,
	// type names) must be resolved up front.
	rendered, err := renderMigrations(migrationFS, migrationsDir, s.dbType, s.tablePrefix)
	if err != nil {
		return errors.Wrap(err, "render migrations")
	}

	source, err := embedded.WithInstance(embedded.Resource(rendered.names, rendered.asset))
	if err != nil {
		return errors.Wrap(err, "build migration source")
	}

	driver, err := newDriver(s.db, s.dbType)
	if err != nil {
		return errors.Wrap(err, "build migration driver")
	}
	defer driver.Close() //nolint:errcheck // close-only on shutdown; nothing to act on

	opts := []morph.EngineOption{morph.SetMigrationTableName(migrationTableName)}
	if logger != nil {
		opts = append(opts, morph.WithLogger(logger))
	}
	engine, err := morph.New(context.Background(), driver, source, opts...)
	if err != nil {
		return errors.Wrap(err, "init migration engine")
	}
	defer engine.Close() //nolint:errcheck // releases driver bookkeeping conn; best-effort

	if err := engine.ApplyAll(); err != nil {
		return errors.Wrap(err, "apply migrations")
	}
	return nil
}

// silentLogger is a morph.Logger that discards all output. It is used by tests
// so the per-migration "== init: migrating ==" banners don't clutter the test
// runner output.
type silentLogger struct{}

func (silentLogger) Printf(string, ...any) {}
func (silentLogger) Println(...any)        {}

// RunMigrationsClusterSafe wraps RunMigrations with the cluster mutex so only
// one node runs migrations at a time. It is the entry point OnActivate uses
// (M4-1); tests call RunMigrations directly on a single in-memory DB.
func (s *SQLStore) RunMigrationsClusterSafe(api plugin.API) error {
	mutex, err := cluster.NewMutex(api, migrationMutexKey)
	if err != nil {
		return errors.Wrap(err, "create migration cluster mutex")
	}
	// LockWithContext blocks until the mutex is acquired or ctx is cancelled.
	// A bounded timeout would be more defensive, but activation is already a
	// synchronous startup step; matching Boards' behaviour of waiting.
	if err := mutex.LockWithContext(context.Background()); err != nil {
		return errors.Wrap(err, "acquire migration cluster mutex")
	}
	defer mutex.Unlock() //nolint:errcheck // best-effort release on shutdown path

	return s.RunMigrations(api)
}

// newDriver builds the morph driver bound to the store's existing *sql.DB
// pool. Reusing the pool (rather than morph.Open) means migrations run inside
// the same connection settings the plugin will use at runtime — important for
// postgres statement timeouts and search_path.
func newDriver(db *sql.DB, dbType string) (drivers.Driver, error) {
	switch dbType {
	case DialectPostgres:
		return postgresdriver.WithInstance(db)
	case DialectMySQL:
		return mysqldriver.WithInstance(db)
	case DialectSQLite:
		return sqlitedriver.WithInstance(db)
	default:
		return nil, fmt.Errorf("unsupported dialect %q for migrations", dbType)
	}
}

// renderedMigrations bundles the filename list and the asset lookup function
// that morph's embedded source expects. Each filename is the raw embedded
// basename (e.g. "000001_init.up.sql"); the asset func returns the
// dialect-rendered body for that name.
type renderedMigrations struct {
	names []string
	asset embedded.AssetFunc
}

// renderMigrations walks the migrations directory of fsys, runs every .sql
// file through the dialect template, and returns the rendered set keyed by
// basename. The template dialect flags (`{{postgres}}`, `{{sqlite}}`,
// `{{mysql}}`) let a single migration file emit slightly different DDL per
// dialect (e.g. serial vs AUTOINCREMENT), while `{{prefix}}` substitutes the
// configured table prefix so migrations stay namespace-correct.
//
// fsys is typed as fs.FS (not embed.FS) so tests can substitute a
// testing/fstest.MapFS to exercise template substitution in isolation.
func renderMigrations(fsys fs.FS, dir, dbType, prefix string) (*renderedMigrations, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, errors.Wrapf(err, "read migrations dir %q", dir)
	}

	funcs := template.FuncMap{
		"prefix":       func() string { return prefix },
		"postgres":     func() bool { return dbType == DialectPostgres },
		"mysql":        func() bool { return dbType == DialectMySQL },
		"sqlite":       func() bool { return dbType == DialectSQLite },
		"createIndex":  createIndexIfNotExists,
		"dropIfExists": dropTableIfExists,
		"notDialect":   func(name string) bool { return name != dbType },
	}

	var names []string
	rendered := make(map[string][]byte)

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, rErr := fs.ReadFile(fsys, filepath.Join(dir, e.Name()))
		if rErr != nil {
			return nil, errors.Wrapf(rErr, "read embedded migration %q", e.Name())
		}

		// Parse each migration as its own template so a syntax error in one
		// file points at that file, not at a merged glob.
		parsed, pErr := template.New(e.Name()).Funcs(funcs).Parse(string(raw))
		if pErr != nil {
			return nil, errors.Wrapf(pErr, "parse migration template %q", e.Name())
		}

		var buf strings.Builder
		if err := parsed.Execute(&buf, nil); err != nil {
			return nil, errors.Wrapf(err, "render migration template %q", e.Name())
		}

		rendered[e.Name()] = []byte(buf.String())
		names = append(names, e.Name())
	}

	sort.Strings(names)
	if len(names) == 0 {
		return nil, errors.New("no migrations found")
	}

	if err := validateMigrationPairs(names); err != nil {
		return nil, err
	}

	asset := func(name string) ([]byte, error) {
		body, ok := rendered[name]
		if !ok {
			return nil, fmt.Errorf("migration %q not rendered", name)
		}
		return body, nil
	}
	return &renderedMigrations{names: names, asset: asset}, nil
}

// validateMigrationPairs ensures every migration has both an up and a down
// script, in morph's `{version}_{id}.{up|down}.sql` form. A missing pair
// would crash ApplyDown later, so we fail fast at render time.
func validateMigrationPairs(names []string) error {
	seenUp := make(map[string]bool)
	seenDown := make(map[string]bool)
	for _, n := range names {
		base := strings.TrimSuffix(n, ".sql")
		if strings.HasSuffix(base, ".up") {
			seenUp[strings.TrimSuffix(base, ".up")] = true
		} else if strings.HasSuffix(base, ".down") {
			seenDown[strings.TrimSuffix(base, ".down")] = true
		} else {
			return fmt.Errorf("migration %q missing .up/.down direction suffix", n)
		}
	}
	for id := range seenUp {
		if !seenDown[id] {
			return fmt.Errorf("migration %q has up script but no down script", id)
		}
	}
	for id := range seenDown {
		if !seenUp[id] {
			return fmt.Errorf("migration %q has down script but no up script", id)
		}
	}
	return nil
}

// createIndexIfNotExists is a migration-template helper that emits a
// CREATE INDEX IF NOT EXISTS statement. The index and table names are emitted
// verbatim — callers prefix them via the {{prefix}} token in the template so
// the helper stays a pure DDL assembler.
//
// Dialect support: postgres and sqlite both accept the bare IF NOT EXISTS
// guard, which makes the statement idempotent across re-runs and safe under
// the migration engine. **MySQL does NOT support CREATE INDEX IF NOT EXISTS**
// (including 8.0); under mysql this helper would error. The plugin's MVP only
// runs migrations against postgres (production) and sqlite (test), so this is
// acceptable. If/when mysql becomes a supported production dialect, guard the
// call site with {{if mysql}} and use an information_schema pre-check, or add
// a mysql-specific helper.
//
// Example template usage:
//
//	{{createIndex (printf "%stasks_channel_idx" (prefix)) (printf "%stasks" (prefix)) "channel_id, status"}}
func createIndexIfNotExists(indexName, tableName, columns string) (string, error) {
	return fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (%s);",
		indexName, tableName, columns), nil
}

// dropTableIfExists emits a DROP TABLE IF EXISTS statement, the cross-dialect
// idempotent drop used by down migrations. Like createIndexIfNotExists the
// table name is emitted verbatim; prefix it via {{prefix}} in the template.
func dropTableIfExists(tableName string) (string, error) {
	return fmt.Sprintf("DROP TABLE IF EXISTS %s;", tableName), nil
}
