package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMigrationCatalogIsDeterministicAndV2Only(t *testing.T) {
	t.Parallel()

	catalog, err := MigrationCatalog()
	if err != nil {
		t.Fatalf("migration catalog: %v", err)
	}
	if len(catalog) != 1 || catalog[0].ID != "000001_event_store" {
		t.Fatalf("migration catalog = %+v", catalog)
	}
	if len(catalog[0].Checksum) != 64 || !strings.HasSuffix(catalog[0].SQL, "\n") {
		t.Fatalf("migration checksum/newline = %q/%v", catalog[0].Checksum, strings.HasSuffix(catalog[0].SQL, "\n"))
	}
	for _, required := range []string{
		"CREATE TABLE schema_migration", "CREATE TABLE event_sequence", "CREATE TABLE event",
		"event_aggregate_seq_idx", "event_aggregate_type_seq_idx",
	} {
		if !strings.Contains(catalog[0].SQL, required) {
			t.Fatalf("migration is missing %q", required)
		}
	}
	for _, legacy := range []string{"CREATE TABLE message", "CREATE TABLE part", "session_v1", "permission_v1"} {
		if strings.Contains(strings.ToLower(catalog[0].SQL), legacy) {
			t.Fatalf("migration contains legacy schema %q", legacy)
		}
	}
	second, err := MigrationCatalog()
	if err != nil {
		t.Fatalf("second migration catalog: %v", err)
	}
	if !reflect.DeepEqual(catalog, second) {
		t.Fatalf("migration catalog is not deterministic: first=%+v second=%+v", catalog, second)
	}
}

func TestPlanMigrationsRejectsChecksumDriftAndDowngrade(t *testing.T) {
	t.Parallel()

	catalog, err := MigrationCatalog()
	if err != nil {
		t.Fatalf("migration catalog: %v", err)
	}
	plan, err := PlanMigrations(catalog, nil)
	if err != nil {
		t.Fatalf("plan empty database: %v", err)
	}
	if !reflect.DeepEqual(plan, catalog) {
		t.Fatalf("empty database plan = %+v, want %+v", plan, catalog)
	}
	plan, err = PlanMigrations(catalog, []AppliedMigration{{ID: catalog[0].ID, Checksum: catalog[0].Checksum}})
	if err != nil || len(plan) != 0 {
		t.Fatalf("fully migrated plan = %+v, error=%v", plan, err)
	}
	_, err = PlanMigrations(catalog, []AppliedMigration{{ID: catalog[0].ID, Checksum: strings.Repeat("0", 64)}})
	if !errors.Is(err, ErrMigrationChecksum) {
		t.Fatalf("checksum drift error = %v, want ErrMigrationChecksum", err)
	}
	_, err = PlanMigrations(catalog, []AppliedMigration{{ID: "999999_future", Checksum: strings.Repeat("f", 64)}})
	if !errors.Is(err, ErrMigrationDowngrade) {
		t.Fatalf("future migration error = %v, want ErrMigrationDowngrade", err)
	}
}

func TestMigrationCatalogValidationRejectsAmbiguousInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		migrations []Migration
	}{
		{name: "empty ID", migrations: []Migration{{SQL: "SELECT 1;\n"}}},
		{name: "invalid ID", migrations: []Migration{{ID: "1_bad", SQL: "SELECT 1;\n"}}},
		{name: "empty SQL", migrations: []Migration{{ID: "000001_empty"}}},
		{name: "duplicate", migrations: []Migration{{ID: "000001_one", SQL: "SELECT 1;\n"}, {ID: "000001_one", SQL: "SELECT 2;\n"}}},
		{name: "out of order", migrations: []Migration{{ID: "000002_two", SQL: "SELECT 2;\n"}, {ID: "000001_one", SQL: "SELECT 1;\n"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateMigrationCatalog(test.migrations); !errors.Is(err, ErrInvalidMigration) {
				t.Fatalf("catalog error = %v, want ErrInvalidMigration", err)
			}
		})
	}
}

func TestApplyMigrationsResumesAnExactPrefixAfterAtomicFailure(t *testing.T) {
	t.Parallel()

	catalog := []Migration{
		{ID: "000001_one", SQL: "SELECT 1;\n"},
		{ID: "000002_two", SQL: "SELECT 2;\n"},
		{ID: "000003_three", SQL: "SELECT 3;\n"},
	}
	failure := errors.New("migration transaction failed")
	backend := &fakeMigrationBackend{failID: "000002_two", failure: failure}
	now := func() time.Time { return time.UnixMilli(1234) }
	applied, err := ApplyMigrations(context.Background(), backend, catalog, now)
	if applied != 1 || !errors.Is(err, ErrMigrationApply) || !errors.Is(err, failure) {
		t.Fatalf("first apply count/error = %d/%v", applied, err)
	}
	if got := backend.appliedIDs(); !reflect.DeepEqual(got, []string{"000001_one"}) {
		t.Fatalf("failed apply retained migrations = %v", got)
	}
	backend.failID = ""
	applied, err = ApplyMigrations(context.Background(), backend, catalog, now)
	if err != nil || applied != 2 {
		t.Fatalf("resume apply count/error = %d/%v", applied, err)
	}
	if got := backend.appliedIDs(); !reflect.DeepEqual(got, []string{"000001_one", "000002_two", "000003_three"}) {
		t.Fatalf("resumed migrations = %v", got)
	}
	for _, migration := range backend.applied {
		if migration.Checksum == "" || migration.TimeApplied != 1234 {
			t.Fatalf("applied migration evidence = %+v", migration)
		}
	}
}

func TestApplyMigrationsPreservesPlanningBackendAndCancellationErrors(t *testing.T) {
	t.Parallel()

	catalog := []Migration{{ID: "000001_one", SQL: "SELECT 1;\n"}, {ID: "000002_two", SQL: "SELECT 2;\n"}}
	now := func() time.Time { return time.UnixMilli(1) }

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	backend := &fakeMigrationBackend{}
	if applied, err := ApplyMigrations(canceled, backend, catalog, now); applied != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled apply count/error = %d/%v", applied, err)
	}
	if backend.reads != 0 || len(backend.applied) != 0 {
		t.Fatalf("pre-canceled apply touched backend: reads=%d applied=%d", backend.reads, len(backend.applied))
	}

	readFailure := errors.New("read migrations failed")
	backend = &fakeMigrationBackend{readFailure: readFailure}
	if applied, err := ApplyMigrations(context.Background(), backend, catalog, now); applied != 0 ||
		!errors.Is(err, ErrMigrationApply) || !errors.Is(err, readFailure) {
		t.Fatalf("backend read count/error = %d/%v", applied, err)
	}

	validated, err := validateMigrationCatalog(catalog)
	if err != nil {
		t.Fatalf("validate catalog fixture: %v", err)
	}
	backend = &fakeMigrationBackend{applied: []fakeAppliedMigration{{
		ID: validated[0].ID, Checksum: strings.Repeat("f", 64), TimeApplied: 1,
	}}}
	if applied, err := ApplyMigrations(context.Background(), backend, catalog, now); applied != 0 ||
		!errors.Is(err, ErrMigrationChecksum) {
		t.Fatalf("checksum planning count/error = %d/%v", applied, err)
	}

	ctx, cancelDuring := context.WithCancel(context.Background())
	backend = &fakeMigrationBackend{afterApply: func(migration Migration) {
		if migration.ID == "000001_one" {
			cancelDuring()
		}
	}}
	if applied, err := ApplyMigrations(ctx, backend, catalog, now); applied != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-apply cancellation count/error = %d/%v", applied, err)
	}
	if got := backend.appliedIDs(); !reflect.DeepEqual(got, []string{"000001_one"}) {
		t.Fatalf("mid-apply cancellation retained = %v", got)
	}
}

func TestSQLMigrationBackendReadsAndAppliesImmediateTransactions(t *testing.T) {
	t.Parallel()

	migration, err := validateMigrationCatalog([]Migration{{ID: "000001_one", SQL: "SELECT 1;\n"}})
	if err != nil {
		t.Fatalf("validate migration fixture: %v", err)
	}
	current := migration[0]

	t.Run("read applied prefix", func(t *testing.T) {
		db, script := openMigrationScriptDB(t, []migrationSQLStep{
			queryStep("SELECT EXISTS", []string{"exists"}, []driver.Value{int64(1)}),
			queryStep("SELECT id, checksum", []string{"id", "checksum"},
				[]driver.Value{"000001_one", current.Checksum},
				[]driver.Value{"000002_two", strings.Repeat("2", 64)}),
		})
		backend, err := NewSQLMigrationBackend(db, time.Second)
		if err != nil {
			t.Fatalf("new SQL migration backend: %v", err)
		}
		applied, err := backend.AppliedMigrations(context.Background())
		if err != nil {
			t.Fatalf("read applied migrations: %v", err)
		}
		want := []AppliedMigration{
			{ID: "000001_one", Checksum: current.Checksum},
			{ID: "000002_two", Checksum: strings.Repeat("2", 64)},
		}
		if !reflect.DeepEqual(applied, want) {
			t.Fatalf("applied migrations = %+v, want %+v", applied, want)
		}
		script.assertDone(t)
	})

	t.Run("apply new migration", func(t *testing.T) {
		db, script := openMigrationScriptDB(t, []migrationSQLStep{
			execStep("BEGIN IMMEDIATE"),
			queryStep("SELECT EXISTS", []string{"exists"}, []driver.Value{int64(0)}),
			execStep("SELECT 1;"),
			execStep("INSERT INTO schema_migration"),
			execStep("COMMIT"),
		})
		backend, err := NewSQLMigrationBackend(db, time.Second)
		if err != nil {
			t.Fatalf("new SQL migration backend: %v", err)
		}
		inserted, err := backend.ApplyMigration(context.Background(), current, 42)
		if err != nil || !inserted {
			t.Fatalf("apply migration inserted/error = %v/%v", inserted, err)
		}
		script.assertDone(t)
	})

	t.Run("concurrent exact migration is no-op", func(t *testing.T) {
		db, script := openMigrationScriptDB(t, []migrationSQLStep{
			execStep("BEGIN IMMEDIATE"),
			queryStep("SELECT EXISTS", []string{"exists"}, []driver.Value{int64(1)}),
			queryStep("SELECT checksum", []string{"checksum"}, []driver.Value{current.Checksum}),
			execStep("COMMIT"),
		})
		backend, err := NewSQLMigrationBackend(db, time.Second)
		if err != nil {
			t.Fatalf("new SQL migration backend: %v", err)
		}
		inserted, err := backend.ApplyMigration(context.Background(), current, 42)
		if err != nil || inserted {
			t.Fatalf("exact migration inserted/error = %v/%v", inserted, err)
		}
		script.assertDone(t)
	})

	t.Run("checksum drift rolls back", func(t *testing.T) {
		db, script := openMigrationScriptDB(t, []migrationSQLStep{
			execStep("BEGIN IMMEDIATE"),
			queryStep("SELECT EXISTS", []string{"exists"}, []driver.Value{int64(1)}),
			queryStep("SELECT checksum", []string{"checksum"}, []driver.Value{strings.Repeat("f", 64)}),
			execStep("ROLLBACK"),
		})
		backend, err := NewSQLMigrationBackend(db, time.Second)
		if err != nil {
			t.Fatalf("new SQL migration backend: %v", err)
		}
		inserted, err := backend.ApplyMigration(context.Background(), current, 42)
		if inserted || !errors.Is(err, ErrMigrationChecksum) {
			t.Fatalf("checksum drift inserted/error = %v/%v", inserted, err)
		}
		script.assertDone(t)
	})
}

func TestSQLMigrationBackendRollsBackCommitAndCancellationFailuresIndependently(t *testing.T) {
	t.Parallel()

	migrations, err := validateMigrationCatalog([]Migration{{ID: "000001_one", SQL: "SELECT 1;\n"}})
	if err != nil {
		t.Fatalf("validate migration fixture: %v", err)
	}
	current := migrations[0]

	t.Run("commit failure", func(t *testing.T) {
		commitFailure := errors.New("commit failed")
		rollbackFailure := errors.New("rollback failed")
		db, script := openMigrationScriptDB(t, []migrationSQLStep{
			execStep("BEGIN IMMEDIATE"),
			queryStep("SELECT EXISTS", []string{"exists"}, []driver.Value{int64(0)}),
			execStep("SELECT 1;"),
			execStep("INSERT INTO schema_migration"),
			{kind: "exec", contains: "COMMIT", err: commitFailure},
			{kind: "exec", contains: "ROLLBACK", err: rollbackFailure},
		})
		backend, err := NewSQLMigrationBackend(db, time.Second)
		if err != nil {
			t.Fatalf("new SQL migration backend: %v", err)
		}
		inserted, err := backend.ApplyMigration(context.Background(), current, 42)
		if inserted || !errors.Is(err, ErrMigrationApply) || !errors.Is(err, commitFailure) ||
			!errors.Is(err, rollbackFailure) {
			t.Fatalf("commit failure inserted/error = %v/%v", inserted, err)
		}
		script.assertDone(t)
	})

	t.Run("canceled migration uses independent rollback context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		db, script := openMigrationScriptDB(t, []migrationSQLStep{
			execStep("BEGIN IMMEDIATE"),
			queryStep("SELECT EXISTS", []string{"exists"}, []driver.Value{int64(0)}),
			{
				kind: "exec", contains: "SELECT 1;", err: context.Canceled,
				action: cancel,
			},
			{kind: "exec", contains: "ROLLBACK", requireActiveContext: true},
		})
		backend, err := NewSQLMigrationBackend(db, time.Second)
		if err != nil {
			t.Fatalf("new SQL migration backend: %v", err)
		}
		inserted, err := backend.ApplyMigration(ctx, current, 42)
		if inserted || !errors.Is(err, ErrMigrationApply) || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled migration inserted/error = %v/%v", inserted, err)
		}
		script.assertDone(t)
	})
}

func TestConfigureSQLiteConnectionAppliesAndVerifiesCanonicalPolicy(t *testing.T) {
	t.Parallel()

	db, script := openMigrationScriptDB(t, []migrationSQLStep{
		execStep("PRAGMA foreign_keys = ON"),
		queryStep("PRAGMA foreign_keys", []string{"foreign_keys"}, []driver.Value{int64(1)}),
		execStep("PRAGMA busy_timeout = 5000"),
		queryStep("PRAGMA busy_timeout", []string{"busy_timeout"}, []driver.Value{int64(5000)}),
		execStep("PRAGMA temp_store = MEMORY"),
		queryStep("PRAGMA temp_store", []string{"temp_store"}, []driver.Value{int64(2)}),
		queryStep("PRAGMA journal_mode = WAL", []string{"journal_mode"}, []driver.Value{"wal"}),
		execStep("PRAGMA synchronous = NORMAL"),
		queryStep("PRAGMA synchronous", []string{"synchronous"}, []driver.Value{int64(1)}),
	})
	connection, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire scripted connection: %v", err)
	}
	defer connection.Close()
	policy := DefaultConnectionPolicy()
	if policy.BusyTimeout != 5*time.Second || policy.Synchronous != SynchronousNormal {
		t.Fatalf("default connection policy = %+v", policy)
	}
	if err := ConfigureSQLiteConnection(context.Background(), connection, policy); err != nil {
		t.Fatalf("configure SQLite connection: %v", err)
	}
	script.assertDone(t)
}

func TestConfigureSQLiteConnectionSupportsFullSynchronousMode(t *testing.T) {
	t.Parallel()

	db, script := openMigrationScriptDB(t, []migrationSQLStep{
		execStep("PRAGMA foreign_keys = ON"),
		queryStep("PRAGMA foreign_keys", []string{"foreign_keys"}, []driver.Value{int64(1)}),
		execStep("PRAGMA busy_timeout = 1000"),
		queryStep("PRAGMA busy_timeout", []string{"busy_timeout"}, []driver.Value{int64(1000)}),
		execStep("PRAGMA temp_store = MEMORY"),
		queryStep("PRAGMA temp_store", []string{"temp_store"}, []driver.Value{int64(2)}),
		queryStep("PRAGMA journal_mode = WAL", []string{"journal_mode"}, []driver.Value{"WAL"}),
		execStep("PRAGMA synchronous = FULL"),
		queryStep("PRAGMA synchronous", []string{"synchronous"}, []driver.Value{int64(2)}),
	})
	connection, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire scripted connection: %v", err)
	}
	defer connection.Close()
	if err := ConfigureSQLiteConnection(context.Background(), connection, ConnectionPolicy{
		BusyTimeout: time.Second,
		Synchronous: SynchronousFull,
	}); err != nil {
		t.Fatalf("configure FULL SQLite connection: %v", err)
	}
	script.assertDone(t)
}

func TestConfigureSQLiteConnectionRejectsInvalidPolicyAndJournalMode(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		policy ConnectionPolicy
	}{
		{name: "zero busy timeout", policy: ConnectionPolicy{Synchronous: SynchronousNormal}},
		{name: "sub-millisecond busy timeout", policy: ConnectionPolicy{
			BusyTimeout: 500 * time.Microsecond, Synchronous: SynchronousNormal,
		}},
		{name: "excessive busy timeout", policy: ConnectionPolicy{
			BusyTimeout: (time.Duration(1<<31) + 1) * time.Millisecond, Synchronous: SynchronousNormal,
		}},
		{name: "invalid synchronous mode", policy: ConnectionPolicy{
			BusyTimeout: time.Second, Synchronous: SynchronousMode("OFF"),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, script := openMigrationScriptDB(t, nil)
			connection, err := db.Conn(context.Background())
			if err != nil {
				t.Fatalf("acquire scripted connection: %v", err)
			}
			defer connection.Close()
			if err := ConfigureSQLiteConnection(context.Background(), connection, test.policy); !errors.Is(err, ErrSQLiteConfiguration) {
				t.Fatalf("policy error = %v, want ErrSQLiteConfiguration", err)
			}
			script.assertDone(t)
		})
	}

	db, script := openMigrationScriptDB(t, []migrationSQLStep{
		execStep("PRAGMA foreign_keys = ON"),
		queryStep("PRAGMA foreign_keys", []string{"foreign_keys"}, []driver.Value{int64(1)}),
		execStep("PRAGMA busy_timeout = 5000"),
		queryStep("PRAGMA busy_timeout", []string{"busy_timeout"}, []driver.Value{int64(5000)}),
		execStep("PRAGMA temp_store = MEMORY"),
		queryStep("PRAGMA temp_store", []string{"temp_store"}, []driver.Value{int64(2)}),
		queryStep("PRAGMA journal_mode = WAL", []string{"journal_mode"}, []driver.Value{"delete"}),
	})
	connection, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire scripted connection: %v", err)
	}
	defer connection.Close()
	if err := ConfigureSQLiteConnection(context.Background(), connection, DefaultConnectionPolicy()); !errors.Is(err, ErrSQLiteConfiguration) || !strings.Contains(err.Error(), "delete") {
		t.Fatalf("journal mode error = %v", err)
	}
	script.assertDone(t)
}

func TestRealMigrationsRejectChecksumDriftAndDowngrade(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		seed func(*testing.T, *sql.DB)
		want error
	}{
		{
			name: "checksum drift",
			seed: func(t *testing.T, database *sql.DB) {
				t.Helper()
				if _, err := database.ExecContext(context.Background(),
					"UPDATE schema_migration SET checksum = ? WHERE id = ?",
					strings.Repeat("f", 64), "000001_event_store"); err != nil {
					t.Fatalf("tamper migration checksum: %v", err)
				}
			},
			want: ErrMigrationChecksum,
		},
		{
			name: "future migration",
			seed: func(t *testing.T, database *sql.DB) {
				t.Helper()
				if _, err := database.ExecContext(context.Background(), `
INSERT INTO schema_migration(id, checksum, time_applied)
VALUES (?, ?, ?)`, "999999_future", strings.Repeat("9", 64), 1); err != nil {
					t.Fatalf("insert future migration: %v", err)
				}
			},
			want: ErrMigrationDowngrade,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			options := DefaultOpenOptions(filepath.Join(t.TempDir(), "migration.sqlite"))
			store, err := Open(context.Background(), options)
			if err != nil {
				t.Fatalf("open migrated store: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("close migrated store: %v", err)
			}
			database, err := sql.Open(sqliteDriverName, sqliteDSN(options))
			if err != nil {
				t.Fatalf("open migration tamper database: %v", err)
			}
			test.seed(t, database)
			if err := database.Close(); err != nil {
				t.Fatalf("close migration tamper database: %v", err)
			}
			if reopened, err := Open(context.Background(), options); !errors.Is(err, test.want) {
				if reopened != nil {
					_ = reopened.Close()
				}
				t.Fatalf("reopen migration error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRealMigrationSQLAndCommitFailuresRollBackAtomically(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		migration Migration
		tables    []string
	}{
		{
			name: "SQL failure",
			migration: Migration{ID: "000002_sql_failure", SQL: `
CREATE TABLE migration_sql_probe (id INTEGER PRIMARY KEY);
INSERT INTO table_that_does_not_exist(id) VALUES (1);
`},
			tables: []string{"migration_sql_probe"},
		},
		{
			name: "deferred constraint commit failure",
			migration: Migration{ID: "000002_commit_failure", SQL: `
CREATE TABLE migration_parent (id INTEGER PRIMARY KEY);
CREATE TABLE migration_child (
  id INTEGER PRIMARY KEY,
  parent_id INTEGER NOT NULL,
  FOREIGN KEY (parent_id) REFERENCES migration_parent(id) DEFERRABLE INITIALLY DEFERRED
);
INSERT INTO migration_child(id, parent_id) VALUES (1, 404);
`},
			tables: []string{"migration_parent", "migration_child"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			options := DefaultOpenOptions(filepath.Join(t.TempDir(), "migration-fault.sqlite"))
			store, err := Open(context.Background(), options)
			if err != nil {
				t.Fatalf("open migrated store: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("close migrated store: %v", err)
			}
			database, err := sql.Open(sqliteDriverName, sqliteDSN(options))
			if err != nil {
				t.Fatalf("open migration fault database: %v", err)
			}
			defer database.Close()
			backend, err := NewSQLMigrationBackend(database, time.Second)
			if err != nil {
				t.Fatalf("new real migration backend: %v", err)
			}
			inserted, err := backend.ApplyMigration(context.Background(), test.migration, 2)
			if inserted || !errors.Is(err, ErrMigrationApply) {
				t.Fatalf("fault migration inserted/error = %v/%v", inserted, err)
			}
			for _, table := range test.tables {
				var exists int
				if err := database.QueryRowContext(context.Background(), `
SELECT EXISTS (SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = ?)`, table).Scan(&exists); err != nil {
					t.Fatalf("inspect rolled back table %q: %v", table, err)
				}
				if exists != 0 {
					t.Fatalf("migration failure retained table %q", table)
				}
			}
			var migrationRows int
			if err := database.QueryRowContext(context.Background(),
				"SELECT COUNT(*) FROM schema_migration WHERE id = ?", test.migration.ID,
			).Scan(&migrationRows); err != nil {
				t.Fatalf("inspect rolled back migration row: %v", err)
			}
			if migrationRows != 0 {
				t.Fatalf("migration failure retained %d checksum rows", migrationRows)
			}
		})
	}
}

type fakeMigrationBackend struct {
	applied     []fakeAppliedMigration
	reads       int
	readFailure error
	failID      string
	failure     error
	afterApply  func(Migration)
}

type fakeAppliedMigration struct {
	ID          string
	Checksum    string
	TimeApplied int64
}

func (backend *fakeMigrationBackend) AppliedMigrations(context.Context) ([]AppliedMigration, error) {
	backend.reads++
	if backend.readFailure != nil {
		return nil, backend.readFailure
	}
	result := make([]AppliedMigration, len(backend.applied))
	for index, migration := range backend.applied {
		result[index] = AppliedMigration{ID: migration.ID, Checksum: migration.Checksum}
	}
	return result, nil
}

func (backend *fakeMigrationBackend) ApplyMigration(
	_ context.Context,
	migration Migration,
	timeApplied int64,
) (bool, error) {
	if migration.ID == backend.failID {
		return false, backend.failure
	}
	backend.applied = append(backend.applied, fakeAppliedMigration{
		ID: migration.ID, Checksum: migration.Checksum, TimeApplied: timeApplied,
	})
	if backend.afterApply != nil {
		backend.afterApply(migration)
	}
	return true, nil
}

func (backend *fakeMigrationBackend) appliedIDs() []string {
	result := make([]string, len(backend.applied))
	for index, migration := range backend.applied {
		result[index] = migration.ID
	}
	return result
}

const migrationScriptDriverName = "opencode_sqlite_migration_script"

var (
	migrationScriptDriverOnce sync.Once
	migrationScriptCounter    atomic.Uint64
	migrationScripts          sync.Map
)

type migrationSQLStep struct {
	kind                 string
	contains             string
	columns              []string
	rows                 [][]driver.Value
	err                  error
	action               func()
	requireActiveContext bool
}

func execStep(contains string) migrationSQLStep {
	return migrationSQLStep{kind: "exec", contains: contains}
}

func queryStep(contains string, columns []string, rows ...[]driver.Value) migrationSQLStep {
	return migrationSQLStep{kind: "query", contains: contains, columns: columns, rows: rows}
}

type migrationSQLScript struct {
	mu      sync.Mutex
	steps   []migrationSQLStep
	failure error
}

func openMigrationScriptDB(t *testing.T, steps []migrationSQLStep) (*sql.DB, *migrationSQLScript) {
	t.Helper()
	migrationScriptDriverOnce.Do(func() {
		sql.Register(migrationScriptDriverName, migrationScriptDriver{})
	})
	name := fmt.Sprintf("script-%d", migrationScriptCounter.Add(1))
	script := &migrationSQLScript{steps: append([]migrationSQLStep(nil), steps...)}
	migrationScripts.Store(name, script)
	db, err := sql.Open(migrationScriptDriverName, name)
	if err != nil {
		t.Fatalf("open migration script database: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		migrationScripts.Delete(name)
	})
	return db, script
}

func (script *migrationSQLScript) next(kind, query string, ctx context.Context) (migrationSQLStep, error) {
	script.mu.Lock()
	defer script.mu.Unlock()
	if script.failure != nil {
		return migrationSQLStep{}, script.failure
	}
	if len(script.steps) == 0 {
		script.failure = fmt.Errorf("unexpected %s SQL: %s", kind, query)
		return migrationSQLStep{}, script.failure
	}
	step := script.steps[0]
	script.steps = script.steps[1:]
	if step.kind != kind || !strings.Contains(query, step.contains) {
		script.failure = fmt.Errorf("SQL step = %s %q, want %s containing %q",
			kind, query, step.kind, step.contains)
		return migrationSQLStep{}, script.failure
	}
	if step.action != nil {
		step.action()
	}
	if step.requireActiveContext && ctx.Err() != nil {
		script.failure = fmt.Errorf("%s used canceled context: %w", step.contains, ctx.Err())
		return migrationSQLStep{}, script.failure
	}
	return step, nil
}

func (script *migrationSQLScript) assertDone(t *testing.T) {
	t.Helper()
	script.mu.Lock()
	defer script.mu.Unlock()
	if script.failure != nil {
		t.Fatalf("migration SQL script failure: %v", script.failure)
	}
	if len(script.steps) != 0 {
		t.Fatalf("migration SQL script has %d unconsumed steps; next=%s %q",
			len(script.steps), script.steps[0].kind, script.steps[0].contains)
	}
}

type migrationScriptDriver struct{}

func (migrationScriptDriver) Open(name string) (driver.Conn, error) {
	value, ok := migrationScripts.Load(name)
	if !ok {
		return nil, fmt.Errorf("unknown migration SQL script %q", name)
	}
	return &migrationScriptConn{script: value.(*migrationSQLScript)}, nil
}

type migrationScriptConn struct {
	script *migrationSQLScript
}

func (connection *migrationScriptConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("script driver does not support prepared statements")
}

func (connection *migrationScriptConn) Close() error { return nil }

func (connection *migrationScriptConn) Begin() (driver.Tx, error) {
	return nil, errors.New("script driver requires explicit BEGIN IMMEDIATE")
}

func (connection *migrationScriptConn) ExecContext(
	ctx context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Result, error) {
	step, err := connection.script.next("exec", query, ctx)
	if err != nil {
		return nil, err
	}
	if step.err != nil {
		return nil, step.err
	}
	return driver.RowsAffected(1), nil
}

func (connection *migrationScriptConn) QueryContext(
	ctx context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Rows, error) {
	step, err := connection.script.next("query", query, ctx)
	if err != nil {
		return nil, err
	}
	if step.err != nil {
		return nil, step.err
	}
	return &migrationScriptRows{columns: step.columns, rows: step.rows}, nil
}

type migrationScriptRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (rows *migrationScriptRows) Columns() []string { return rows.columns }
func (rows *migrationScriptRows) Close() error      { return nil }

func (rows *migrationScriptRows) Next(values []driver.Value) error {
	if rows.index >= len(rows.rows) {
		return io.EOF
	}
	copy(values, rows.rows[rows.index])
	rows.index++
	return nil
}
