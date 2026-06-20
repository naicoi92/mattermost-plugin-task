// Package sqlstore implements the Task plugin's persistence layer on top of the
// Mattermost server's relational database.
//
// The store reuses the cluster's primary database connection obtained through
// pluginapi.Store.GetMasterDB (available since the plugin's min_server_version
// 10.7.0), so no separate database is provisioned for the plugin. All plugin
// tables use a shared `task_` prefix to namespace them away from server tables.
//
// The package is dialect-aware: production runs on postgres (or mysql), while
// unit/integration tests run on an in-memory sqlite database. Query building is
// done with squirrel so the same builder code renders the correct placeholder
// and identifier-quoting style for each dialect.
//
// This file provides the SQLStore scaffold (constructor, dialect helpers and
// the withRunner clone used to run statements against either the connection
// pool or an in-flight transaction). Entity repositories and the Store
// interface are added by subsequent issues (M2-x, M3-1).
package sqlstore

import (
	"database/sql"
	"fmt"
	"strings"

	sq "github.com/Masterminds/squirrel"

	"github.com/mattermost/mattermost/server/public/model"
)

// Dialect identifiers derived from the server's SqlSettings.DriverName. They
// drive placeholder rendering and identifier quoting across every repository.
const (
	DialectPostgres = "postgres"
	DialectMySQL    = "mysql"
	DialectSQLite   = "sqlite3"
)

// DefaultTablePrefix namespaces every plugin table away from the Mattermost
// server tables. Boards uses a similar scheme (`w_`); this plugin uses `task_`
// so a glance at the schema makes ownership obvious.
const DefaultTablePrefix = "task_"

// SQLStore holds the shared database connection and dialect metadata. Every
// repository method is defined on *SQLStore and shares this state, but writes
// that need to run inside a transaction operate on a clone produced by
// withRunner (see WithTx in M3-1) whose `runner` field points at the tx.
type SQLStore struct {
	// db is the server's primary *sql.DB pool obtained via GetMasterDB. It is
	// shared for the lifetime of the plugin and never closed by the plugin.
	db *sql.DB

	// dbType is the active dialect identifier (one of the Dialect* constants).
	dbType string

	// tablePrefix is prepended to every short table name (e.g. "tasks" ->
	// "task_tasks").
	tablePrefix string

	// placeholderFormat renders "?" placeholders in the dialect-appropriate
	// style (postgres: $1/$2..., mysql/sqlite: ?).
	placeholderFormat sq.PlaceholderFormat

	// runner is the squirrel BaseRunner used to execute statements. For the
	// pool-level store this is `db`; for a transaction-bound clone (produced by
	// withRunner) it is the *sql.Tx so all statements in a unit of work share
	// the same transaction.
	runner sq.BaseRunner
}

// New constructs a SQLStore bound to the given pool. The dbType must be one of
// the Dialect* constants; an unknown value yields an error so a
// misconfiguration fails fast at activation rather than producing subtly wrong
// SQL later. tablePrefix may be empty to fall back to DefaultTablePrefix.
func New(db *sql.DB, dbType, tablePrefix string) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("sqlstore: db must not be nil")
	}
	if tablePrefix == "" {
		tablePrefix = DefaultTablePrefix
	}
	ph, err := placeholderFor(dbType)
	if err != nil {
		return nil, err
	}
	return &SQLStore{
		db:                db,
		dbType:            dbType,
		tablePrefix:       tablePrefix,
		placeholderFormat: ph,
		runner:            db,
	}, nil
}

// NewFromConfig derives the dialect from the server SqlSettings. The Mattermost
// server exposes its active driver via SqlSettings.DriverName; we map it to
// our Dialect* constants so callers don't have to. It is a convenience used by
// the plugin OnActivate wiring (M4-1).
func NewFromConfig(db *sql.DB, cfg *model.SqlSettings, tablePrefix string) (*SQLStore, error) {
	if cfg == nil {
		return nil, fmt.Errorf("sqlstore: SqlSettings must not be nil")
	}
	driver := ""
	if cfg.DriverName != nil {
		driver = *cfg.DriverName
	}
	return New(db, dialectForDriver(driver), tablePrefix)
}

// driverMySQL / driverPostgres mirror the values Mattermost stores in
// SqlSettings.DriverName. The public model package only exposes
// DatabaseDriverPostgres; mysql is reproduced here as a literal so this file
// does not depend on an unexported/internal constant.
const (
	driverPostgres = "postgres"
	driverMySQL    = "mysql"
)

// dialectForDriver normalises a SqlSettings.DriverName value into one of our
// Dialect* constants. Unknown drivers fall through to postgres (the server
// default), which keeps the constructor forgiving while still producing valid
// SQL for the two production drivers Mattermost actually ships.
func dialectForDriver(driver string) string {
	switch driver {
	case driverMySQL:
		return DialectMySQL
	case driverPostgres: // == model.DatabaseDriverPostgres ("postgres")
		return DialectPostgres
	default:
		// Includes the empty case (config default applies later in the
		// server). Postgres is the Mattermost default driver.
		return DialectPostgres
	}
}

// placeholderFor maps a dialect to the squirrel placeholder renderer. Postgres
// requires positional $1/$2/... placeholders; mysql and sqlite accept the bare
// "?" placeholder that squirrel emits by default.
func placeholderFor(dbType string) (sq.PlaceholderFormat, error) {
	switch dbType {
	case DialectPostgres:
		return sq.Dollar, nil
	case DialectMySQL, DialectSQLite:
		return sq.Question, nil
	default:
		return nil, fmt.Errorf("sqlstore: unsupported dialect %q (want %q, %q or %q)", dbType, DialectPostgres, DialectMySQL, DialectSQLite)
	}
}

// DBType returns the active dialect identifier. Repositories branch on this
// only when a feature genuinely diverges (e.g. ILIKE vs LIKE); the common path
// is dialect-agnostic via squirrel.
func (s *SQLStore) DBType() string { return s.dbType }

// TablePrefix returns the configured table prefix, exposed so migrations can
// substitute it into raw SQL templates.
func (s *SQLStore) TablePrefix() string { return s.tablePrefix }

// tableName returns the fully-qualified short table name: the configured prefix
// plus the short name. For example tableName("tasks") -> "task_tasks". The
// result is intentionally unquoted; callers that need quoted identifiers should
// route through escapeField.
func (s *SQLStore) tableName(short string) string {
	return s.tablePrefix + short
}

// escapeField quotes a SQL identifier using the dialect-appropriate quote
// character: backticks for mysql, double quotes for postgres/sqlite. Quoting
// every identifier lets us reserve words like "order" or "key" as column names
// without per-dialect special casing.
func (s *SQLStore) escapeField(name string) string {
	if s.dbType == DialectMySQL {
		return "`" + name + "`"
	}
	return `"` + name + `"`
}

// builder returns a squirrel statement builder preconfigured with this store's
// placeholder format and runner. Repository methods use it to build queries so
// placeholder substitution and the active runner (pool vs tx) are handled in
// one place.
func (s *SQLStore) builder() sq.StatementBuilderType {
	return sq.StatementBuilder.RunWith(s.runner).PlaceholderFormat(s.placeholderFormat)
}

// withRunner returns a shallow clone of the store bound to a different runner
// (typically a *sql.Tx). All other fields (db, dbType, prefix, placeholder
// format) are shared with the parent, so a transaction-bound clone renders
// SQL identically to the pool store. This is the mechanism WithTx (M3-1) uses
// to hand repositories a tx-scoped Store.
func (s *SQLStore) withRunner(runner sq.BaseRunner) *SQLStore {
	clone := *s
	clone.runner = runner
	return &clone
}

// likePattern escapes SQL LIKE/ILIKE metacharacters in a user-supplied keyword
// and wraps it in %...% for a substring search. Underscore and percent are the
// two wildcards common to all supported dialects; escaping them keeps the
// search behaving as a plain "contains" regardless of the input.
func likePattern(keyword string) string {
	keyword = strings.ReplaceAll(keyword, `\`, `\\`)
	keyword = strings.ReplaceAll(keyword, "%", `\%`)
	keyword = strings.ReplaceAll(keyword, "_", `\_`)
	return "%" + keyword + "%"
}
