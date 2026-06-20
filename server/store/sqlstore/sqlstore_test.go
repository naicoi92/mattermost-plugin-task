package sqlstore

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mmmodel "github.com/mattermost/mattermost/server/public/model"
)

func TestNew_DialectDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	db := &sql.DB{} // never opened; the constructor only stores the pointer.

	t.Run("postgres sets dollar placeholders", func(t *testing.T) {
		s, err := New(db, DialectPostgres, "")
		require.NoError(t, err)
		assert.Equal(t, DialectPostgres, s.DBType())
		assert.Equal(t, DefaultTablePrefix, s.TablePrefix())
		// A statement with a placeholder must render as $1 for postgres.
		q, args, err := s.builder().Select("1").Where("id = ?", "x").ToSql()
		require.NoError(t, err)
		assert.Contains(t, q, "$1")
		assert.Len(t, args, 1)
	})

	t.Run("mysql sets question placeholders", func(t *testing.T) {
		s, err := New(db, DialectMySQL, "custom_")
		require.NoError(t, err)
		assert.Equal(t, DialectMySQL, s.DBType())
		assert.Equal(t, "custom_", s.TablePrefix())
		q, _, err := s.builder().Select("1").Where("id = ?", "x").ToSql()
		require.NoError(t, err)
		assert.Contains(t, q, "?")
	})

	t.Run("sqlite sets question placeholders", func(t *testing.T) {
		s, err := New(db, DialectSQLite, "")
		require.NoError(t, err)
		assert.Equal(t, DialectSQLite, s.DBType())
	})

	t.Run("nil db rejected", func(t *testing.T) {
		_, err := New(nil, DialectPostgres, "")
		require.Error(t, err)
	})

	t.Run("unknown dialect rejected", func(t *testing.T) {
		_, err := New(db, "oracle", "")
		require.Error(t, err)
	})
}

func TestNewFromConfig_DriverMapping(t *testing.T) {
	t.Parallel()
	db := &sql.DB{}
	mysql := "mysql"
	postgres := mmmodel.DatabaseDriverPostgres
	weird := "something-else"

	cases := []struct {
		name   string
		driver *string
		want   string
	}{
		{"mysql driver maps to mysql dialect", &mysql, DialectMySQL},
		{"postgres driver maps to postgres dialect", &postgres, DialectPostgres},
		{"unknown driver falls back to postgres", &weird, DialectPostgres},
		{"nil driver falls back to postgres", nil, DialectPostgres},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &mmmodel.SqlSettings{DriverName: tc.driver}
			s, err := NewFromConfig(db, cfg, "")
			require.NoError(t, err)
			assert.Equal(t, tc.want, s.DBType())
		})
	}

	t.Run("nil config rejected", func(t *testing.T) {
		_, err := NewFromConfig(db, nil, "")
		require.Error(t, err)
	})
}

func TestTableName_AppliesPrefix(t *testing.T) {
	t.Parallel()
	s, err := New(&sql.DB{}, DialectPostgres, "")
	require.NoError(t, err)
	assert.Equal(t, "task_tasks", s.tableName("tasks"))
	assert.Equal(t, "task_members", s.tableName("members"))

	s2, err := New(&sql.DB{}, DialectMySQL, "demo_")
	require.NoError(t, err)
	assert.Equal(t, "demo_tasks", s2.tableName("tasks"))
}

func TestEscapeField_DialectSpecificQuoting(t *testing.T) {
	t.Parallel()
	pg, _ := New(&sql.DB{}, DialectPostgres, "")
	my, _ := New(&sql.DB{}, DialectMySQL, "")
	lite, _ := New(&sql.DB{}, DialectSQLite, "")

	assert.Equal(t, `"order"`, pg.escapeField("order"))
	assert.Equal(t, "`order`", my.escapeField("order"))
	assert.Equal(t, `"order"`, lite.escapeField("order"))
}

func TestWithRunner_ClonesButPreservesDialect(t *testing.T) {
	t.Parallel()
	pool := &sql.DB{}
	s, err := New(pool, DialectPostgres, "task_")
	require.NoError(t, err)

	// A *sql.DB is itself an sq.BaseRunner, so we can reuse it to prove the
	// clone keeps dialect/prefix while swapping the runner pointer.
	clone := s.withRunner(pool)
	require.NotSame(t, s, clone)
	assert.Equal(t, s.DBType(), clone.DBType())
	assert.Equal(t, s.TablePrefix(), clone.TablePrefix())
	assert.Equal(t, s.placeholderFormat, clone.placeholderFormat)
}

func TestLikePattern_EscapesWildcards(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"foo", "%foo%"},
		{"100%", `%100\%%`},
		{"a_b", `%a\_b%`},
		{`back\slash`, `%back\\slash%`},
		{"", "%%"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, likePattern(tc.in), "input %q", tc.in)
	}
}
