package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidMigration    = errors.New("invalid migration")
	ErrMigrationChecksum   = errors.New("migration checksum mismatch")
	ErrMigrationDowngrade  = errors.New("database migration is newer than this binary")
	ErrMigrationApply      = errors.New("migration apply failed")
	ErrSQLiteConfiguration = errors.New("sqlite connection configuration failed")
)

var migrationID = regexp.MustCompile(`^[0-9]{6}_[a-z0-9_]+$`)

const (
	DefaultBusyTimeout                         = 5 * time.Second
	maxBusyTimeoutMilliseconds                 = int64(1<<31 - 1)
	SynchronousNormal          SynchronousMode = "NORMAL"
	SynchronousFull            SynchronousMode = "FULL"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Migration struct {
	ID       string
	SQL      string
	Checksum string
}

type AppliedMigration struct {
	ID       string
	Checksum string
}

type SynchronousMode string

type ConnectionPolicy struct {
	BusyTimeout time.Duration
	Synchronous SynchronousMode
}

func DefaultConnectionPolicy() ConnectionPolicy {
	return ConnectionPolicy{BusyTimeout: DefaultBusyTimeout, Synchronous: SynchronousNormal}
}

// ConfigureSQLiteConnection applies and reads back every canonical per-
// connection policy. Callers must close and discard the connection on error.
func ConfigureSQLiteConnection(ctx context.Context, connection *sql.Conn, policy ConnectionPolicy) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if connection == nil {
		return fmt.Errorf("%w: connection is nil", ErrSQLiteConfiguration)
	}
	if policy.BusyTimeout <= 0 || policy.BusyTimeout%time.Millisecond != 0 {
		return fmt.Errorf("%w: busy timeout must be a positive whole number of milliseconds",
			ErrSQLiteConfiguration)
	}
	busyMilliseconds := policy.BusyTimeout.Milliseconds()
	if busyMilliseconds > maxBusyTimeoutMilliseconds {
		return fmt.Errorf("%w: busy timeout %dms exceeds SQLite maximum %dms",
			ErrSQLiteConfiguration, busyMilliseconds, maxBusyTimeoutMilliseconds)
	}
	synchronousValue := 0
	switch policy.Synchronous {
	case SynchronousNormal:
		synchronousValue = 1
	case SynchronousFull:
		synchronousValue = 2
	default:
		return fmt.Errorf("%w: unsupported synchronous mode %q",
			ErrSQLiteConfiguration, policy.Synchronous)
	}

	if _, err := connection.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("%w: enable foreign keys: %w", ErrSQLiteConfiguration, err)
	}
	var foreignKeys int
	if err := connection.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("%w: read foreign_keys: %w", ErrSQLiteConfiguration, err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("%w: foreign_keys read back as %d, want 1", ErrSQLiteConfiguration, foreignKeys)
	}

	busySQL := "PRAGMA busy_timeout = " + strconv.FormatInt(busyMilliseconds, 10)
	if _, err := connection.ExecContext(ctx, busySQL); err != nil {
		return fmt.Errorf("%w: set busy_timeout: %w", ErrSQLiteConfiguration, err)
	}
	var actualBusy int64
	if err := connection.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&actualBusy); err != nil {
		return fmt.Errorf("%w: read busy_timeout: %w", ErrSQLiteConfiguration, err)
	}
	if actualBusy != busyMilliseconds {
		return fmt.Errorf("%w: busy_timeout read back as %dms, want %dms",
			ErrSQLiteConfiguration, actualBusy, busyMilliseconds)
	}

	if _, err := connection.ExecContext(ctx, "PRAGMA temp_store = MEMORY"); err != nil {
		return fmt.Errorf("%w: set temp_store: %w", ErrSQLiteConfiguration, err)
	}
	var tempStore int
	if err := connection.QueryRowContext(ctx, "PRAGMA temp_store").Scan(&tempStore); err != nil {
		return fmt.Errorf("%w: read temp_store: %w", ErrSQLiteConfiguration, err)
	}
	if tempStore != 2 {
		return fmt.Errorf("%w: temp_store read back as %d, want 2 (MEMORY)",
			ErrSQLiteConfiguration, tempStore)
	}

	var journalMode string
	if err := connection.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return fmt.Errorf("%w: enable WAL journal mode: %w", ErrSQLiteConfiguration, err)
	}
	if !strings.EqualFold(strings.TrimSpace(journalMode), "wal") {
		return fmt.Errorf("%w: journal_mode read back as %q, want WAL",
			ErrSQLiteConfiguration, journalMode)
	}

	synchronousSQL := "PRAGMA synchronous = " + string(policy.Synchronous)
	if _, err := connection.ExecContext(ctx, synchronousSQL); err != nil {
		return fmt.Errorf("%w: set synchronous mode: %w", ErrSQLiteConfiguration, err)
	}
	var actualSynchronous int
	if err := connection.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&actualSynchronous); err != nil {
		return fmt.Errorf("%w: read synchronous mode: %w", ErrSQLiteConfiguration, err)
	}
	if actualSynchronous != synchronousValue {
		return fmt.Errorf("%w: synchronous read back as %d, want %d (%s)",
			ErrSQLiteConfiguration, actualSynchronous, synchronousValue, policy.Synchronous)
	}
	return nil
}

// MigrationBackend owns the storage-specific atomic transaction. A successful
// ApplyMigration call must commit both the migration SQL and its checksum row;
// an error must expose neither.
type MigrationBackend interface {
	AppliedMigrations(context.Context) ([]AppliedMigration, error)
	ApplyMigration(context.Context, Migration, int64) (bool, error)
}

// SQLMigrationBackend applies migrations on a dedicated database/sql
// connection using explicit SQLite BEGIN IMMEDIATE transactions.
type SQLMigrationBackend struct {
	db              *sql.DB
	rollbackTimeout time.Duration
	applyMu         sync.Mutex
}

// NewSQLMigrationBackend validates the operational dependencies needed to
// guarantee an independent bounded rollback after caller cancellation.
func NewSQLMigrationBackend(db *sql.DB, rollbackTimeout time.Duration) (*SQLMigrationBackend, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: SQL database is nil", ErrMigrationApply)
	}
	if rollbackTimeout <= 0 {
		return nil, fmt.Errorf("%w: rollback timeout must be positive", ErrMigrationApply)
	}
	return &SQLMigrationBackend{db: db, rollbackTimeout: rollbackTimeout}, nil
}

// AppliedMigrations reads the durable migration prefix in canonical ID order.
// A brand-new SQLite database has no schema_migration table and yields an empty
// prefix without relying on driver-specific error strings.
func (backend *SQLMigrationBackend) AppliedMigrations(ctx context.Context) ([]AppliedMigration, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	exists, err := schemaMigrationTableExists(ctx, backend.db)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect schema_migration table: %w", ErrMigrationApply, err)
	}
	if !exists {
		return nil, nil
	}
	rows, err := backend.db.QueryContext(ctx, `
SELECT id, checksum
FROM schema_migration
ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("%w: query schema_migration: %w", ErrMigrationApply, err)
	}
	defer rows.Close()
	result := make([]AppliedMigration, 0)
	for rows.Next() {
		var migration AppliedMigration
		if err := rows.Scan(&migration.ID, &migration.Checksum); err != nil {
			return nil, fmt.Errorf("%w: scan schema_migration: %w", ErrMigrationApply, err)
		}
		result = append(result, migration)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate schema_migration: %w", ErrMigrationApply, err)
	}
	return result, nil
}

// ApplyMigration commits one migration and its checksum row in the same
// immediate transaction. It returns false when another serialized migrator
// already committed the exact migration while this caller was waiting.
func (backend *SQLMigrationBackend) ApplyMigration(
	ctx context.Context,
	migration Migration,
	timeApplied int64,
) (inserted bool, returnErr error) {
	validated, err := validateMigrationCatalog([]Migration{migration})
	if err != nil {
		return false, err
	}
	migration = validated[0]
	if timeApplied < 0 {
		return false, fmt.Errorf("%w: migration %q time is before Unix epoch", ErrMigrationApply, migration.ID)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	backend.applyMu.Lock()
	defer backend.applyMu.Unlock()
	connection, err := backend.db.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("%w: acquire migration connection: %w", ErrMigrationApply, err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return false, fmt.Errorf("%w: begin migration %q: %w", ErrMigrationApply, migration.ID, err)
	}
	active := true
	defer func() {
		if !active {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backend.rollbackTimeout)
		defer cancel()
		if _, rollbackErr := connection.ExecContext(rollbackCtx, "ROLLBACK"); rollbackErr != nil {
			wrapped := fmt.Errorf("%w: rollback migration %q: %w", ErrMigrationApply, migration.ID, rollbackErr)
			if returnErr == nil {
				returnErr = wrapped
			} else {
				returnErr = errors.Join(returnErr, wrapped)
			}
		}
	}()

	exists, err := schemaMigrationTableExists(ctx, connection)
	if err != nil {
		return false, fmt.Errorf("%w: inspect migration %q: %w", ErrMigrationApply, migration.ID, err)
	}
	if exists {
		var checksum string
		err := connection.QueryRowContext(ctx,
			"SELECT checksum FROM schema_migration WHERE id = ?", migration.ID,
		).Scan(&checksum)
		switch {
		case err == nil:
			if checksum != migration.Checksum {
				return false, fmt.Errorf("%w: migration %q", ErrMigrationChecksum, migration.ID)
			}
			if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
				return false, fmt.Errorf("%w: commit exact migration %q: %w", ErrMigrationApply, migration.ID, err)
			}
			active = false
			return false, nil
		case errors.Is(err, sql.ErrNoRows):
			// The migration is still pending in this immediate transaction.
		default:
			return false, fmt.Errorf("%w: inspect migration row %q: %w", ErrMigrationApply, migration.ID, err)
		}
	}
	if _, err := connection.ExecContext(ctx, migration.SQL); err != nil {
		return false, fmt.Errorf("%w: execute migration %q: %w", ErrMigrationApply, migration.ID, err)
	}
	if _, err := connection.ExecContext(ctx,
		"INSERT INTO schema_migration(id, checksum, time_applied) VALUES (?, ?, ?)",
		migration.ID, migration.Checksum, timeApplied,
	); err != nil {
		return false, fmt.Errorf("%w: record migration %q: %w", ErrMigrationApply, migration.ID, err)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return false, fmt.Errorf("%w: commit migration %q: %w", ErrMigrationApply, migration.ID, err)
	}
	active = false
	return true, nil
}

type migrationRowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func schemaMigrationTableExists(ctx context.Context, queryer migrationRowQuerier) (bool, error) {
	var exists int
	err := queryer.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM sqlite_schema
  WHERE type = 'table' AND name = 'schema_migration'
)`).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists != 0, nil
}

// MigrationCatalog returns an isolated, checksum-complete catalog embedded in
// the binary. Published migration bytes are immutable once released.
func MigrationCatalog() ([]Migration, error) {
	files, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return nil, fmt.Errorf("list embedded migrations: %w", err)
	}
	sort.Strings(files)
	migrations := make([]Migration, 0, len(files))
	for _, file := range files {
		content, err := migrationFiles.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read embedded migration %q: %w", file, err)
		}
		name := path.Base(file)
		migrations = append(migrations, Migration{
			ID:  strings.TrimSuffix(name, path.Ext(name)),
			SQL: string(content),
		})
	}
	return validateMigrationCatalog(migrations)
}

// PlanMigrations verifies that applied migrations are an exact prefix of the
// binary catalog. It refuses checksum drift, holes, reordering, and downgrade.
func PlanMigrations(catalog []Migration, applied []AppliedMigration) ([]Migration, error) {
	validated, err := validateMigrationCatalog(catalog)
	if err != nil {
		return nil, err
	}
	if len(applied) > len(validated) {
		return nil, fmt.Errorf("%w: database has %d migrations, binary has %d",
			ErrMigrationDowngrade, len(applied), len(validated))
	}
	for index, current := range applied {
		expected := validated[index]
		if current.ID != expected.ID {
			return nil, fmt.Errorf("%w: applied migration %d is %q, expected %q",
				ErrMigrationDowngrade, index, current.ID, expected.ID)
		}
		if current.Checksum != expected.Checksum {
			return nil, fmt.Errorf("%w: migration %q", ErrMigrationChecksum, current.ID)
		}
	}
	return append([]Migration(nil), validated[len(applied):]...), nil
}

// ApplyMigrations verifies the durable prefix and applies each remaining
// migration atomically. The returned count is the newly committed prefix, so a
// caller can report progress and safely retry after an operational failure.
func ApplyMigrations(
	ctx context.Context,
	backend MigrationBackend,
	catalog []Migration,
	now func() time.Time,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if backend == nil {
		return 0, fmt.Errorf("%w: backend is nil", ErrMigrationApply)
	}
	if now == nil {
		return 0, fmt.Errorf("%w: migration clock is nil", ErrInvalidMigration)
	}
	applied, err := backend.AppliedMigrations(ctx)
	if err != nil {
		return 0, fmt.Errorf("%w: read applied migrations: %w", ErrMigrationApply, err)
	}
	plan, err := PlanMigrations(catalog, applied)
	if err != nil {
		return 0, err
	}
	committed := 0
	for _, migration := range plan {
		if err := ctx.Err(); err != nil {
			return committed, err
		}
		timeApplied := now().UnixMilli()
		if timeApplied < 0 {
			return committed, fmt.Errorf("%w: migration %q clock is before Unix epoch",
				ErrMigrationApply, migration.ID)
		}
		inserted, err := backend.ApplyMigration(ctx, migration, timeApplied)
		if err != nil {
			return committed, fmt.Errorf("%w: apply migration %q: %w", ErrMigrationApply, migration.ID, err)
		}
		if inserted {
			committed++
		}
	}
	return committed, nil
}

func validateMigrationCatalog(migrations []Migration) ([]Migration, error) {
	if len(migrations) == 0 {
		return nil, fmt.Errorf("%w: catalog is empty", ErrInvalidMigration)
	}
	result := make([]Migration, len(migrations))
	previous := ""
	for index, migration := range migrations {
		if !migrationID.MatchString(migration.ID) {
			return nil, fmt.Errorf("%w: migration %d has invalid ID %q", ErrInvalidMigration, index, migration.ID)
		}
		if previous != "" && migration.ID <= previous {
			return nil, fmt.Errorf("%w: migration %q does not follow %q", ErrInvalidMigration, migration.ID, previous)
		}
		if !utf8.ValidString(migration.SQL) || strings.TrimSpace(migration.SQL) == "" {
			return nil, fmt.Errorf("%w: migration %q SQL must be non-empty UTF-8", ErrInvalidMigration, migration.ID)
		}
		if strings.Contains(migration.SQL, "\r") || !strings.HasSuffix(migration.SQL, "\n") {
			return nil, fmt.Errorf("%w: migration %q SQL must use LF and end with a newline", ErrInvalidMigration, migration.ID)
		}
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(migration.SQL)))
		if migration.Checksum != "" && migration.Checksum != digest {
			return nil, fmt.Errorf("%w: catalog migration %q", ErrMigrationChecksum, migration.ID)
		}
		migration.Checksum = digest
		result[index] = migration
		previous = migration.ID
	}
	return result, nil
}
