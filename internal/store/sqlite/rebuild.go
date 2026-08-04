package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"strings"
	"sync/atomic"

	"github.com/Hz-186/opencode-go-py/internal/event"
)

var ErrRebuild = errors.New("sqlite projection rebuild failed")

type SQLRow interface {
	Scan(...any) error
}

// RebuildExecutor is valid only inside ProjectionRebuild callbacks.
type RebuildExecutor interface {
	SQLExecutor
	QueryRowContext(context.Context, string, ...any) (SQLRow, error)
}

type RebuildSummary struct {
	EventCount      int64
	AggregateCount  int64
	MaximumSequence int64
	SHA256          string
}

type ProjectionRebuild struct {
	Name         string
	CreateShadow func(context.Context, RebuildExecutor) error
	Project      func(context.Context, RebuildExecutor, event.StoredEvent) error
	Verify       func(context.Context, RebuildExecutor, RebuildSummary) error
	Swap         func(context.Context, RebuildExecutor) error
}

// RebuildProjection streams the authoritative Event log through a shadow
// projector and swaps it only after caller-supplied verification. The sole
// writer fence and one immediate transaction make create/project/verify/swap
// atomic; cancellation leaves the live projection unchanged.
func (store *Store) RebuildProjection(
	ctx context.Context,
	rebuild ProjectionRebuild,
) (_ RebuildSummary, returnErr error) {
	if err := ctx.Err(); err != nil {
		return RebuildSummary{}, err
	}
	if store == nil || store.closed.Load() {
		return RebuildSummary{}, ErrStoreClosed
	}
	if store.broken.Load() {
		return RebuildSummary{}, ErrStoreBroken
	}
	if err := validateProjectionRebuild(rebuild); err != nil {
		return RebuildSummary{}, err
	}
	select {
	case <-ctx.Done():
		return RebuildSummary{}, ctx.Err()
	case <-store.closeCh:
		return RebuildSummary{}, ErrStoreClosed
	case <-store.writeToken:
	}
	defer func() { store.writeToken <- struct{}{} }()
	if store.closed.Load() {
		return RebuildSummary{}, ErrStoreClosed
	}
	if store.broken.Load() {
		return RebuildSummary{}, ErrStoreBroken
	}
	if _, err := store.writer.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return RebuildSummary{}, wrapStorage(ctx, "begin projection rebuild", err)
	}
	active := true
	executor := &sqlRebuildExecutor{connection: store.writer}
	executor.active.Store(true)
	defer func() {
		executor.active.Store(false)
		if !active {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), store.rollbackTimeout)
		defer cancel()
		if err := rollbackEventTransaction(rollbackCtx, store.writer); err != nil {
			store.broken.Store(true)
			wrapped := wrapStorage(rollbackCtx, "rollback projection rebuild", err)
			if returnErr == nil {
				returnErr = wrapped
			} else {
				returnErr = errors.Join(returnErr, wrapped)
			}
		}
	}()

	report, err := collectSQLiteIntegrity(ctx, store.writer)
	if err != nil {
		return RebuildSummary{}, err
	}
	if !report.Healthy() {
		return RebuildSummary{}, &IntegrityError{Report: report}
	}
	if err := rebuild.CreateShadow(ctx, executor); err != nil {
		return RebuildSummary{}, fmt.Errorf("%w: create shadow for %q: %w", ErrRebuild, rebuild.Name, err)
	}
	rows, err := store.readerDB.QueryContext(ctx, `
SELECT id, aggregate_id, seq, type, data
FROM event
ORDER BY aggregate_id ASC, seq ASC`)
	if err != nil {
		return RebuildSummary{}, wrapStorage(ctx, "query projection rebuild source", err)
	}
	hasher := sha256.New()
	summary := RebuildSummary{MaximumSequence: -1}
	lastAggregate := ""
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			rows.Close()
			return RebuildSummary{}, err
		}
		record, err := scanStoredEvent(rows)
		if err != nil {
			rows.Close()
			return RebuildSummary{}, wrapStorage(ctx, "scan projection rebuild source", err)
		}
		if err := validateStoredEvent(record); err != nil {
			rows.Close()
			return RebuildSummary{}, storageInvariant("validate projection rebuild source", err.Error())
		}
		if record.AggregateID != lastAggregate {
			summary.AggregateCount++
			lastAggregate = record.AggregateID
		}
		summary.EventCount++
		if record.Sequence > summary.MaximumSequence {
			summary.MaximumSequence = record.Sequence
		}
		hashStoredEvent(hasher, record)
		if err := rebuild.Project(ctx, executor, record); err != nil {
			rows.Close()
			return RebuildSummary{}, fmt.Errorf("%w: project %q event %q: %w",
				ErrRebuild, rebuild.Name, record.ID, err)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return RebuildSummary{}, wrapStorage(ctx, "iterate projection rebuild source", err)
	}
	if err := rows.Close(); err != nil {
		return RebuildSummary{}, wrapStorage(ctx, "close projection rebuild source", err)
	}
	summary.SHA256 = fmt.Sprintf("%x", hasher.Sum(nil))
	if err := ctx.Err(); err != nil {
		return RebuildSummary{}, err
	}
	if err := rebuild.Verify(ctx, executor, summary); err != nil {
		return RebuildSummary{}, fmt.Errorf("%w: verify shadow for %q: %w", ErrRebuild, rebuild.Name, err)
	}
	if err := ctx.Err(); err != nil {
		return RebuildSummary{}, err
	}
	if err := rebuild.Swap(ctx, executor); err != nil {
		return RebuildSummary{}, fmt.Errorf("%w: swap shadow for %q: %w", ErrRebuild, rebuild.Name, err)
	}
	if err := ctx.Err(); err != nil {
		return RebuildSummary{}, err
	}
	if _, err := store.writer.ExecContext(ctx, "COMMIT"); err != nil {
		return RebuildSummary{}, wrapStorage(ctx, "commit projection rebuild", err)
	}
	active = false
	return summary, nil
}

func validateProjectionRebuild(rebuild ProjectionRebuild) error {
	if err := validateStoreIdentity("projection rebuild name", rebuild.Name); err != nil {
		return err
	}
	if rebuild.CreateShadow == nil || rebuild.Project == nil || rebuild.Verify == nil || rebuild.Swap == nil {
		return fmt.Errorf("%w: projection rebuild callbacks must all be non-nil", ErrInvalidStoreInput)
	}
	return nil
}

type sqlRebuildExecutor struct {
	connection *sql.Conn
	active     atomic.Bool
}

func (executor *sqlRebuildExecutor) ExecContext(
	ctx context.Context,
	statement string,
	arguments ...any,
) (sql.Result, error) {
	if err := executor.checkActive(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(statement) == "" || strings.IndexByte(statement, 0) >= 0 {
		return nil, fmt.Errorf("%w: rebuild SQL must be non-empty without NUL", ErrInvalidStoreInput)
	}
	result, err := executor.connection.ExecContext(ctx, statement, arguments...)
	if err != nil {
		return nil, wrapStorage(ctx, "execute projection rebuild statement", err)
	}
	return result, nil
}

func (executor *sqlRebuildExecutor) QueryRowContext(
	ctx context.Context,
	statement string,
	arguments ...any,
) (SQLRow, error) {
	if err := executor.checkActive(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(statement) == "" || strings.IndexByte(statement, 0) >= 0 {
		return nil, fmt.Errorf("%w: rebuild SQL must be non-empty without NUL", ErrInvalidStoreInput)
	}
	return &rebuildRow{ctx: ctx, row: executor.connection.QueryRowContext(ctx, statement, arguments...)}, nil
}

func (executor *sqlRebuildExecutor) checkActive() error {
	if executor == nil || !executor.active.Load() {
		return ErrTransactionClosed
	}
	return nil
}

type rebuildRow struct {
	ctx context.Context
	row *sql.Row
}

func (row *rebuildRow) Scan(destinations ...any) error {
	if err := row.row.Scan(destinations...); err != nil {
		return wrapStorage(row.ctx, "scan projection rebuild query", err)
	}
	return nil
}

func hashStoredEvent(hasher hash.Hash, record event.StoredEvent) {
	writeRebuildHashField(hasher, []byte(record.ID))
	writeRebuildHashField(hasher, []byte(record.AggregateID))
	var sequence [8]byte
	binary.BigEndian.PutUint64(sequence[:], uint64(record.Sequence))
	hasher.Write(sequence[:])
	writeRebuildHashField(hasher, []byte(record.Type))
	writeRebuildHashField(hasher, record.Data)
}

func writeRebuildHashField(hasher hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	hasher.Write(length[:])
	hasher.Write(value)
}
