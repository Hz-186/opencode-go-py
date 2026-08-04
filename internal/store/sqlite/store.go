// Package sqlite implements the canonical Go-owned durable Event Store.
package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hz-186/opencode-go-py/internal/codec"
	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/event"
	modernsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	sqliteDriverName       = "sqlite"
	DefaultMaxReaders      = 4
	MaximumReaders         = 64
	DefaultRollbackTimeout = 5 * time.Second
	MaximumHistoryPage     = 10_000
)

var (
	ErrInvalidStoreInput = errors.New("invalid sqlite store input")
	ErrStoreClosed       = errors.New("sqlite store is closed")
	ErrStoreBroken       = errors.New("sqlite writer is unusable")
	ErrTransactionClosed = errors.New("sqlite transaction is closed")

	ErrStorage           = errors.New("storage failure")
	ErrStorageBusy       = errors.New("storage busy")
	ErrStorageFull       = errors.New("storage full")
	ErrStorageIO         = errors.New("storage I/O failure")
	ErrStorageCorrupt    = errors.New("storage corruption")
	ErrStorageConstraint = errors.New("storage constraint violation")
	ErrStorageReadOnly   = errors.New("storage is read-only")
)

type OpenOptions struct {
	Path            string
	Policy          ConnectionPolicy
	MaxReaders      int
	RollbackTimeout time.Duration
	Clock           func() time.Time
}

func DefaultOpenOptions(path string) OpenOptions {
	return OpenOptions{
		Path:            path,
		Policy:          DefaultConnectionPolicy(),
		MaxReaders:      DefaultMaxReaders,
		RollbackTimeout: DefaultRollbackTimeout,
		Clock:           time.Now,
	}
}

// StorageError preserves both a stable category and the driver's typed cause.
type StorageError struct {
	Operation  string
	Kind       error
	SQLiteCode int
	Cause      error
}

func (err *StorageError) Error() string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s: %v: %v", err.Operation, err.Kind, err.Cause)
}

func (err *StorageError) Unwrap() []error {
	if err == nil {
		return nil
	}
	return []error{ErrStorage, err.Kind, err.Cause}
}

func (err *StorageError) Code() int {
	if err == nil {
		return 0
	}
	return err.SQLiteCode
}

// SQLExecutor is the projection extension implemented by a Store transaction.
// It is valid only for the duration of the Event Store Write callback.
type SQLExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type Store struct {
	path            string
	dsn             string
	writerDB        *sql.DB
	readerDB        *sql.DB
	writer          *sql.Conn
	rollbackTimeout time.Duration

	writeToken chan struct{}
	closeCh    chan struct{}
	closed     atomic.Bool
	broken     atomic.Bool
	closeOnce  sync.Once
	closeErr   error
}

var _ event.Store = (*Store)(nil)

func Open(ctx context.Context, options OpenOptions) (_ *Store, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	validated, err := validateOpenOptions(options)
	if err != nil {
		return nil, err
	}
	dsn := sqliteDSN(validated)
	writerDB, err := sql.Open(sqliteDriverName, dsn)
	if err != nil {
		return nil, wrapStorage(ctx, "open writer database", err)
	}
	writerDB.SetMaxOpenConns(1)
	writerDB.SetMaxIdleConns(1)
	defer func() {
		if returnErr != nil {
			_ = writerDB.Close()
		}
	}()
	if err := writerDB.PingContext(ctx); err != nil {
		return nil, wrapStorage(ctx, "ping writer database", err)
	}
	if err := verifyPooledConnection(ctx, writerDB, validated.Policy); err != nil {
		return nil, err
	}
	catalog, err := MigrationCatalog()
	if err != nil {
		return nil, err
	}
	migrations, err := NewSQLMigrationBackend(writerDB, validated.RollbackTimeout)
	if err != nil {
		return nil, err
	}
	if _, err := ApplyMigrations(ctx, migrations, catalog, validated.Clock); err != nil {
		return nil, err
	}
	writer, err := writerDB.Conn(ctx)
	if err != nil {
		return nil, wrapStorage(ctx, "acquire writer connection", err)
	}
	defer func() {
		if returnErr != nil {
			_ = writer.Close()
		}
	}()
	if err := ConfigureSQLiteConnection(ctx, writer, validated.Policy); err != nil {
		return nil, err
	}
	if _, err := checkSQLiteIntegrity(ctx, writer, validated.RollbackTimeout); err != nil {
		return nil, err
	}

	readerDB, err := sql.Open(sqliteDriverName, dsn)
	if err != nil {
		return nil, wrapStorage(ctx, "open reader database", err)
	}
	readerDB.SetMaxOpenConns(validated.MaxReaders)
	readerDB.SetMaxIdleConns(validated.MaxReaders)
	defer func() {
		if returnErr != nil {
			_ = readerDB.Close()
		}
	}()
	if err := readerDB.PingContext(ctx); err != nil {
		return nil, wrapStorage(ctx, "ping reader database", err)
	}
	if err := verifyPooledConnection(ctx, readerDB, validated.Policy); err != nil {
		return nil, err
	}

	store := &Store{
		path: validated.Path, dsn: dsn, writerDB: writerDB, readerDB: readerDB, writer: writer,
		rollbackTimeout: validated.RollbackTimeout,
		writeToken:      make(chan struct{}, 1),
		closeCh:         make(chan struct{}),
	}
	store.writeToken <- struct{}{}
	return store, nil
}

func validateOpenOptions(options OpenOptions) (OpenOptions, error) {
	if strings.TrimSpace(options.Path) == "" || options.Path != strings.TrimSpace(options.Path) {
		return OpenOptions{}, fmt.Errorf("%w: database path must be non-empty without surrounding whitespace",
			ErrInvalidStoreInput)
	}
	if strings.IndexByte(options.Path, 0) >= 0 || !filepath.IsAbs(options.Path) {
		return OpenOptions{}, fmt.Errorf("%w: database path must be an absolute path without NUL",
			ErrInvalidStoreInput)
	}
	options.Path = filepath.Clean(options.Path)
	if options.MaxReaders <= 0 || options.MaxReaders > MaximumReaders {
		return OpenOptions{}, fmt.Errorf("%w: max readers must be in [1,%d]", ErrInvalidStoreInput, MaximumReaders)
	}
	if options.RollbackTimeout <= 0 {
		return OpenOptions{}, fmt.Errorf("%w: rollback timeout must be positive", ErrInvalidStoreInput)
	}
	if options.Clock == nil {
		return OpenOptions{}, fmt.Errorf("%w: migration clock is nil", ErrInvalidStoreInput)
	}
	if err := validateConnectionPolicy(options.Policy); err != nil {
		return OpenOptions{}, err
	}
	return options, nil
}

func validateConnectionPolicy(policy ConnectionPolicy) error {
	if policy.BusyTimeout <= 0 || policy.BusyTimeout%time.Millisecond != 0 ||
		policy.BusyTimeout.Milliseconds() > maxBusyTimeoutMilliseconds {
		return fmt.Errorf("%w: invalid busy timeout", ErrSQLiteConfiguration)
	}
	if policy.Synchronous != SynchronousNormal && policy.Synchronous != SynchronousFull {
		return fmt.Errorf("%w: unsupported synchronous mode %q",
			ErrSQLiteConfiguration, policy.Synchronous)
	}
	return nil
}

func sqliteDSN(options OpenOptions) string {
	uri := &url.URL{Scheme: "file", Path: options.Path}
	query := uri.Query()
	query.Set("mode", "rwc")
	query.Set("_busy_timeout", strconv.FormatInt(options.Policy.BusyTimeout.Milliseconds(), 10))
	query.Set("_foreign_keys", "on")
	query.Set("_journal_mode", "wal")
	query.Set("_synchronous", strings.ToLower(string(options.Policy.Synchronous)))
	query.Set("_pragma", "temp_store(MEMORY)")
	query.Set("_dqs", "0")
	query.Set("_error_rc", "1")
	uri.RawQuery = query.Encode()
	return uri.String()
}

func verifyPooledConnection(ctx context.Context, database *sql.DB, policy ConnectionPolicy) error {
	connection, err := database.Conn(ctx)
	if err != nil {
		return wrapStorage(ctx, "acquire policy verification connection", err)
	}
	defer connection.Close()
	return ConfigureSQLiteConnection(ctx, connection, policy)
}

func (store *Store) Write(
	ctx context.Context,
	run func(context.Context, event.Transaction) error,
) (returnErr error) {
	if run == nil {
		return fmt.Errorf("%w: write callback is nil", ErrInvalidStoreInput)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil || store.closed.Load() {
		return ErrStoreClosed
	}
	if store.broken.Load() {
		return ErrStoreBroken
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-store.closeCh:
		return ErrStoreClosed
	case <-store.writeToken:
	}
	defer func() { store.writeToken <- struct{}{} }()
	if store.closed.Load() {
		return ErrStoreClosed
	}
	if store.broken.Load() {
		return ErrStoreBroken
	}
	if _, err := store.writer.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return wrapStorage(ctx, "begin event transaction", err)
	}
	active := true
	transaction := &sqlEventTransaction{connection: store.writer}
	transaction.active.Store(true)
	defer func() {
		transaction.active.Store(false)
		if !active {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), store.rollbackTimeout)
		defer cancel()
		if rollbackErr := rollbackEventTransaction(rollbackCtx, store.writer); rollbackErr != nil {
			store.broken.Store(true)
			wrapped := wrapStorage(rollbackCtx, "rollback event transaction", rollbackErr)
			if returnErr == nil {
				returnErr = wrapped
			} else {
				returnErr = errors.Join(returnErr, wrapped)
			}
		}
	}()
	if err := run(ctx, transaction); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := store.writer.ExecContext(ctx, "COMMIT"); err != nil {
		return wrapStorage(ctx, "commit event transaction", err)
	}
	active = false
	return nil
}

// SQLite may automatically roll back a transaction after SQLITE_FULL,
// SQLITE_IOERR, SQLITE_NOMEM, or SQLITE_BUSY. In that state a subsequent
// ROLLBACK itself fails even though the connection is safe. Prove autocommit
// operationally with a fresh immediate transaction instead of parsing driver
// error strings; any failed proof permanently fences the writer.
func rollbackEventTransaction(ctx context.Context, connection *sql.Conn) error {
	if _, err := connection.ExecContext(ctx, "ROLLBACK"); err == nil {
		return nil
	} else {
		rollbackErr := err
		if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			return errors.Join(rollbackErr, fmt.Errorf("verify automatic rollback: %w", err))
		}
		if _, err := connection.ExecContext(ctx, "ROLLBACK"); err != nil {
			return errors.Join(rollbackErr, fmt.Errorf("close automatic rollback probe: %w", err))
		}
		return nil
	}
}

func (store *Store) History(
	ctx context.Context,
	aggregateID string,
	after int64,
	limit int,
) ([]event.StoredEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if store == nil || store.closed.Load() {
		return nil, ErrStoreClosed
	}
	if strings.TrimSpace(aggregateID) == "" || aggregateID != strings.TrimSpace(aggregateID) {
		return nil, fmt.Errorf("%w: aggregate ID must be non-empty without surrounding whitespace",
			ErrInvalidStoreInput)
	}
	if after < -1 || limit <= 0 || limit > MaximumHistoryPage {
		return nil, fmt.Errorf("%w: history cursor/limit is invalid", ErrInvalidStoreInput)
	}
	rows, err := store.readerDB.QueryContext(ctx, `
SELECT id, aggregate_id, seq, type, data
FROM event
WHERE aggregate_id = ? AND seq > ?
ORDER BY seq ASC
LIMIT ?`, aggregateID, after, limit)
	if err != nil {
		return nil, wrapStorage(ctx, "query event history", err)
	}
	defer rows.Close()
	result := make([]event.StoredEvent, 0, limit)
	for rows.Next() {
		record, err := scanStoredEvent(rows)
		if err != nil {
			return nil, wrapStorage(ctx, "scan event history", err)
		}
		if record.AggregateID != aggregateID || record.Sequence < 0 {
			return nil, storageInvariant("scan event history", "invalid aggregate identity")
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapStorage(ctx, "iterate event history", err)
	}
	return result, nil
}

func (store *Store) Close() error {
	if store == nil {
		return nil
	}
	store.closeOnce.Do(func() {
		store.closed.Store(true)
		close(store.closeCh)
		<-store.writeToken
		var closeErrors []error
		if store.writer != nil {
			if err := store.writer.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
				closeErrors = append(closeErrors, fmt.Errorf("close writer connection: %w", err))
			}
		}
		if store.writerDB != nil {
			if err := store.writerDB.Close(); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("close writer database: %w", err))
			}
		}
		if store.readerDB != nil {
			if err := store.readerDB.Close(); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("close reader database: %w", err))
			}
		}
		store.closeErr = errors.Join(closeErrors...)
	})
	return store.closeErr
}

type sqlEventTransaction struct {
	connection *sql.Conn
	active     atomic.Bool
}

var _ event.Transaction = (*sqlEventTransaction)(nil)
var _ SQLExecutor = (*sqlEventTransaction)(nil)

func (transaction *sqlEventTransaction) Sequence(
	ctx context.Context,
	aggregateID string,
) (event.SequenceState, bool, error) {
	if err := transaction.checkActive(); err != nil {
		return event.SequenceState{}, false, err
	}
	if err := validateStoreIdentity("aggregate ID", aggregateID); err != nil {
		return event.SequenceState{}, false, err
	}
	var latest int64
	var owner sql.NullString
	err := transaction.connection.QueryRowContext(ctx,
		"SELECT seq, owner_id FROM event_sequence WHERE aggregate_id = ?", aggregateID,
	).Scan(&latest, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return event.SequenceState{}, false, nil
	}
	if err != nil {
		return event.SequenceState{}, false, wrapStorage(ctx, "read event sequence", err)
	}
	if latest < 0 {
		return event.SequenceState{}, false, storageInvariant("read event sequence", "negative latest sequence")
	}
	return event.SequenceState{Latest: latest, OwnerID: owner.String}, true, nil
}

func (transaction *sqlEventTransaction) EventByID(
	ctx context.Context,
	id domain.EventID,
) (event.StoredEvent, bool, error) {
	if err := transaction.checkActive(); err != nil {
		return event.StoredEvent{}, false, err
	}
	if err := validateStoredEventID(id); err != nil {
		return event.StoredEvent{}, false, err
	}
	record, err := scanStoredEvent(transaction.connection.QueryRowContext(ctx, `
SELECT id, aggregate_id, seq, type, data
FROM event
WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return event.StoredEvent{}, false, nil
	}
	if err != nil {
		return event.StoredEvent{}, false, wrapStorage(ctx, "read event by ID", err)
	}
	return record, true, nil
}

func (transaction *sqlEventTransaction) EventAt(
	ctx context.Context,
	aggregateID string,
	sequence int64,
) (event.StoredEvent, bool, error) {
	if err := transaction.checkActive(); err != nil {
		return event.StoredEvent{}, false, err
	}
	if err := validateStoreIdentity("aggregate ID", aggregateID); err != nil {
		return event.StoredEvent{}, false, err
	}
	if sequence < 0 {
		return event.StoredEvent{}, false, fmt.Errorf("%w: event sequence must be non-negative",
			ErrInvalidStoreInput)
	}
	record, err := scanStoredEvent(transaction.connection.QueryRowContext(ctx, `
SELECT id, aggregate_id, seq, type, data
FROM event
WHERE aggregate_id = ? AND seq = ?`, aggregateID, sequence))
	if errors.Is(err, sql.ErrNoRows) {
		return event.StoredEvent{}, false, nil
	}
	if err != nil {
		return event.StoredEvent{}, false, wrapStorage(ctx, "read event at sequence", err)
	}
	return record, true, nil
}

func (transaction *sqlEventTransaction) PutSequence(
	ctx context.Context,
	aggregateID string,
	state event.SequenceState,
) error {
	if err := transaction.checkActive(); err != nil {
		return err
	}
	if err := validateStoreIdentity("aggregate ID", aggregateID); err != nil {
		return err
	}
	if state.Latest < 0 {
		return fmt.Errorf("%w: latest sequence must be non-negative", ErrInvalidStoreInput)
	}
	if state.OwnerID != "" {
		if err := validateStoreIdentity("owner ID", state.OwnerID); err != nil {
			return err
		}
	}
	var owner any
	if state.OwnerID != "" {
		owner = state.OwnerID
	}
	_, err := transaction.connection.ExecContext(ctx, `
INSERT INTO event_sequence(aggregate_id, seq, owner_id)
VALUES (?, ?, ?)
ON CONFLICT(aggregate_id) DO UPDATE SET
  seq = excluded.seq,
  owner_id = excluded.owner_id`, aggregateID, state.Latest, owner)
	if err != nil {
		return wrapStorage(ctx, "store event sequence", err)
	}
	return nil
}

func (transaction *sqlEventTransaction) InsertEvent(ctx context.Context, record event.StoredEvent) error {
	if err := transaction.checkActive(); err != nil {
		return err
	}
	if err := validateStoredEvent(record); err != nil {
		return err
	}
	_, err := transaction.connection.ExecContext(ctx, `
INSERT INTO event(id, aggregate_id, seq, type, data)
VALUES (?, ?, ?, ?, ?)`, record.ID, record.AggregateID, record.Sequence, record.Type, string(record.Data))
	if err != nil {
		return wrapStorage(ctx, "insert event", err)
	}
	return nil
}

func (transaction *sqlEventTransaction) DeleteAggregate(ctx context.Context, aggregateID string) error {
	if err := transaction.checkActive(); err != nil {
		return err
	}
	if err := validateStoreIdentity("aggregate ID", aggregateID); err != nil {
		return err
	}
	if _, err := transaction.connection.ExecContext(ctx,
		"DELETE FROM event_sequence WHERE aggregate_id = ?", aggregateID); err != nil {
		return wrapStorage(ctx, "delete event aggregate", err)
	}
	return nil
}

func (transaction *sqlEventTransaction) ExecContext(
	ctx context.Context,
	statement string,
	arguments ...any,
) (sql.Result, error) {
	if err := transaction.checkActive(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(statement) == "" || strings.IndexByte(statement, 0) >= 0 {
		return nil, fmt.Errorf("%w: projector SQL must be non-empty without NUL",
			ErrInvalidStoreInput)
	}
	result, err := transaction.connection.ExecContext(ctx, statement, arguments...)
	if err != nil {
		return nil, wrapStorage(ctx, "execute projector statement", err)
	}
	return result, nil
}

func (transaction *sqlEventTransaction) checkActive() error {
	if transaction == nil || !transaction.active.Load() {
		return ErrTransactionClosed
	}
	return nil
}

func validateStoreIdentity(label, value string) error {
	if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) || strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%w: %s must be non-empty without surrounding whitespace or NUL",
			ErrInvalidStoreInput, label)
	}
	return nil
}

func validateStoredEventID(id domain.EventID) error {
	if err := validateStoreIdentity("event ID", string(id)); err != nil {
		return err
	}
	if _, err := domain.ParseEventID(string(id)); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidStoreInput, err)
	}
	return nil
}

func validateStoredEvent(record event.StoredEvent) error {
	if err := validateStoredEventID(record.ID); err != nil {
		return err
	}
	if err := validateStoreIdentity("aggregate ID", record.AggregateID); err != nil {
		return err
	}
	if record.Sequence < 0 {
		return fmt.Errorf("%w: event sequence must be non-negative", ErrInvalidStoreInput)
	}
	if err := validateStoreIdentity("event type", record.Type); err != nil {
		return err
	}
	decoded, err := codec.DecodeJSONValue(record.Data)
	if err != nil {
		return fmt.Errorf("%w: invalid event data: %v", ErrInvalidStoreInput, err)
	}
	if decoded.Kind != domain.JSONKindObject {
		return fmt.Errorf("%w: event data must be a JSON object", ErrInvalidStoreInput)
	}
	canonical, err := codec.EncodeJSONValue(decoded)
	if err != nil {
		return fmt.Errorf("%w: encode canonical event data: %v", ErrInvalidStoreInput, err)
	}
	if !bytes.Equal(record.Data, canonical) {
		return fmt.Errorf("%w: event data must use canonical JSON bytes", ErrInvalidStoreInput)
	}
	return nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanStoredEvent(row rowScanner) (event.StoredEvent, error) {
	var id string
	var record event.StoredEvent
	if err := row.Scan(&id, &record.AggregateID, &record.Sequence, &record.Type, &record.Data); err != nil {
		return event.StoredEvent{}, err
	}
	record.ID = domain.EventID(id)
	return record, nil
}

func wrapStorage(ctx context.Context, operation string, cause error) error {
	if cause == nil {
		return nil
	}
	if ctx != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return fmt.Errorf("%s: %w", operation, contextErr)
		}
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, cause)
	}
	kind := ErrStorage
	code := 0
	var sqliteErr *modernsqlite.Error
	if errors.As(cause, &sqliteErr) {
		code = sqliteErr.Code()
		switch code & 0xff {
		case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
			kind = ErrStorageBusy
		case sqlite3.SQLITE_FULL:
			kind = ErrStorageFull
		case sqlite3.SQLITE_IOERR, sqlite3.SQLITE_CANTOPEN:
			kind = ErrStorageIO
		case sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB:
			kind = ErrStorageCorrupt
		case sqlite3.SQLITE_CONSTRAINT:
			kind = ErrStorageConstraint
		case sqlite3.SQLITE_READONLY, sqlite3.SQLITE_PERM:
			kind = ErrStorageReadOnly
		}
	}
	return &StorageError{Operation: operation, Kind: kind, SQLiteCode: code, Cause: cause}
}

func storageInvariant(operation, message string) error {
	return &StorageError{
		Operation: operation,
		Kind:      ErrStorageCorrupt,
		Cause:     errors.New(message),
	}
}
