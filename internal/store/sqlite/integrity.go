package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrIntegrity = errors.New("sqlite integrity check failed")

type ForeignKeyViolation struct {
	Table        string
	RowID        int64
	Parent       string
	ForeignKeyID int64
}

type SequenceViolation struct {
	AggregateID string
	Declared    int64
	Minimum     int64
	Maximum     int64
	EventCount  int64
	Reason      string
}

type EventViolation struct {
	RowID  int64
	ID     string
	Reason string
}

// IntegrityReport contains deterministic, machine-readable evidence from all
// canonical SQLite and Event Store invariants.
type IntegrityReport struct {
	QuickCheck  []string
	ForeignKeys []ForeignKeyViolation
	Sequences   []SequenceViolation
	Events      []EventViolation
}

func (report IntegrityReport) Healthy() bool {
	return len(report.QuickCheck) == 0 && len(report.ForeignKeys) == 0 &&
		len(report.Sequences) == 0 && len(report.Events) == 0
}

// IntegrityError preserves the complete report while participating in both
// the integrity and typed storage-corruption error categories.
type IntegrityError struct {
	Report IntegrityReport
}

func (err *IntegrityError) Error() string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%v: quick=%d foreign_keys=%d sequences=%d events=%d",
		ErrIntegrity, len(err.Report.QuickCheck), len(err.Report.ForeignKeys),
		len(err.Report.Sequences), len(err.Report.Events))
}

func (err *IntegrityError) Unwrap() []error {
	if err == nil {
		return nil
	}
	return []error{ErrIntegrity, ErrStorage, ErrStorageCorrupt}
}

// CheckIntegrity runs against one stable read snapshot. The connection is
// never returned to the pool with an open transaction, including cancellation.
func (store *Store) CheckIntegrity(ctx context.Context) (IntegrityReport, error) {
	if err := ctx.Err(); err != nil {
		return IntegrityReport{}, err
	}
	if store == nil || store.closed.Load() {
		return IntegrityReport{}, ErrStoreClosed
	}
	connection, err := store.readerDB.Conn(ctx)
	if err != nil {
		return IntegrityReport{}, wrapStorage(ctx, "acquire integrity connection", err)
	}
	defer connection.Close()
	return checkSQLiteIntegrity(ctx, connection, store.rollbackTimeout)
}

func checkSQLiteIntegrity(
	ctx context.Context,
	connection *sql.Conn,
	rollbackTimeout time.Duration,
) (_ IntegrityReport, returnErr error) {
	if err := ctx.Err(); err != nil {
		return IntegrityReport{}, err
	}
	if connection == nil || rollbackTimeout <= 0 {
		return IntegrityReport{}, fmt.Errorf("%w: integrity checker dependencies are invalid",
			ErrInvalidStoreInput)
	}
	if _, err := connection.ExecContext(ctx, "BEGIN"); err != nil {
		return IntegrityReport{}, wrapStorage(ctx, "begin integrity snapshot", err)
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		if _, err := connection.ExecContext(rollbackCtx, "ROLLBACK"); err != nil {
			wrapped := wrapStorage(rollbackCtx, "close integrity snapshot", err)
			if returnErr == nil {
				returnErr = wrapped
			} else {
				returnErr = errors.Join(returnErr, wrapped)
			}
		}
	}()

	report, err := collectSQLiteIntegrity(ctx, connection)
	if err != nil {
		return IntegrityReport{}, err
	}
	if !report.Healthy() {
		return report, &IntegrityError{Report: report}
	}
	return report, nil
}

func collectSQLiteIntegrity(ctx context.Context, connection *sql.Conn) (IntegrityReport, error) {
	report := IntegrityReport{}
	if err := collectQuickCheck(ctx, connection, &report); err != nil {
		return IntegrityReport{}, err
	}
	if err := collectForeignKeyViolations(ctx, connection, &report); err != nil {
		return IntegrityReport{}, err
	}
	if err := collectEventViolations(ctx, connection, &report); err != nil {
		return IntegrityReport{}, err
	}
	if err := collectSequenceViolations(ctx, connection, &report); err != nil {
		return IntegrityReport{}, err
	}
	return report, nil
}

func collectQuickCheck(ctx context.Context, connection *sql.Conn, report *IntegrityReport) error {
	rows, err := connection.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return wrapStorage(ctx, "run SQLite quick_check", err)
	}
	defer rows.Close()
	for rows.Next() {
		var message string
		if err := rows.Scan(&message); err != nil {
			return wrapStorage(ctx, "scan SQLite quick_check", err)
		}
		message = strings.TrimSpace(message)
		if !strings.EqualFold(message, "ok") {
			report.QuickCheck = append(report.QuickCheck, message)
		}
	}
	if err := rows.Err(); err != nil {
		return wrapStorage(ctx, "iterate SQLite quick_check", err)
	}
	sort.Strings(report.QuickCheck)
	return nil
}

func collectForeignKeyViolations(ctx context.Context, connection *sql.Conn, report *IntegrityReport) error {
	rows, err := connection.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return wrapStorage(ctx, "run SQLite foreign_key_check", err)
	}
	defer rows.Close()
	for rows.Next() {
		var violation ForeignKeyViolation
		var rowID sql.NullInt64
		if err := rows.Scan(&violation.Table, &rowID, &violation.Parent, &violation.ForeignKeyID); err != nil {
			return wrapStorage(ctx, "scan SQLite foreign_key_check", err)
		}
		if rowID.Valid {
			violation.RowID = rowID.Int64
		}
		report.ForeignKeys = append(report.ForeignKeys, violation)
	}
	if err := rows.Err(); err != nil {
		return wrapStorage(ctx, "iterate SQLite foreign_key_check", err)
	}
	sort.Slice(report.ForeignKeys, func(left, right int) bool {
		a, b := report.ForeignKeys[left], report.ForeignKeys[right]
		if a.Table != b.Table {
			return a.Table < b.Table
		}
		if a.RowID != b.RowID {
			return a.RowID < b.RowID
		}
		if a.Parent != b.Parent {
			return a.Parent < b.Parent
		}
		return a.ForeignKeyID < b.ForeignKeyID
	})
	return nil
}

func collectEventViolations(ctx context.Context, connection *sql.Conn, report *IntegrityReport) error {
	rows, err := connection.QueryContext(ctx, `
SELECT rowid, COALESCE(CAST(id AS TEXT), '')
FROM event
WHERE typeof(id) <> 'text'
   OR typeof(aggregate_id) <> 'text'
   OR typeof(seq) <> 'integer'
   OR typeof(type) <> 'text'
   OR typeof(data) <> 'text'
   OR trim(id) = '' OR trim(id) <> id OR instr(id, char(0)) > 0 OR id NOT GLOB 'evt_*'
   OR trim(aggregate_id) = '' OR trim(aggregate_id) <> aggregate_id OR instr(aggregate_id, char(0)) > 0
   OR seq < 0
   OR trim(type) = '' OR trim(type) <> type OR instr(type, char(0)) > 0
   OR CASE
        WHEN typeof(data) <> 'text' THEN 1
        WHEN json_valid(data) = 0 THEN 1
        WHEN json_type(data) <> 'object' THEN 1
        WHEN json(data) || char(10) <> data THEN 1
        ELSE 0
      END = 1
ORDER BY rowid ASC`)
	if err != nil {
		return wrapStorage(ctx, "query invalid event rows", err)
	}
	defer rows.Close()
	for rows.Next() {
		var violation EventViolation
		if err := rows.Scan(&violation.RowID, &violation.ID); err != nil {
			return wrapStorage(ctx, "scan invalid event row", err)
		}
		violation.Reason = "event row violates canonical identity, type, sequence, or JSON data"
		report.Events = append(report.Events, violation)
	}
	if err := rows.Err(); err != nil {
		return wrapStorage(ctx, "iterate invalid event rows", err)
	}
	return nil
}

func collectSequenceViolations(ctx context.Context, connection *sql.Conn, report *IntegrityReport) error {
	malformed, err := connection.QueryContext(ctx, `
SELECT COALESCE(CAST(aggregate_id AS TEXT), ''), COALESCE(CAST(seq AS TEXT), ''),
       COALESCE(CAST(owner_id AS TEXT), '')
FROM event_sequence
WHERE typeof(aggregate_id) <> 'text'
   OR typeof(seq) <> 'integer'
   OR (owner_id IS NOT NULL AND typeof(owner_id) <> 'text')
   OR trim(aggregate_id) = '' OR trim(aggregate_id) <> aggregate_id OR instr(aggregate_id, char(0)) > 0
   OR seq < 0
   OR (owner_id IS NOT NULL AND
       (trim(owner_id) = '' OR trim(owner_id) <> owner_id OR instr(owner_id, char(0)) > 0))
ORDER BY aggregate_id ASC`)
	if err != nil {
		return wrapStorage(ctx, "query malformed event sequences", err)
	}
	for malformed.Next() {
		var aggregateID, declaredText, ownerID string
		if err := malformed.Scan(&aggregateID, &declaredText, &ownerID); err != nil {
			malformed.Close()
			return wrapStorage(ctx, "scan malformed event sequence", err)
		}
		declared, _ := strconv.ParseInt(declaredText, 10, 64)
		report.Sequences = append(report.Sequences, SequenceViolation{
			AggregateID: aggregateID, Declared: declared, Minimum: -1, Maximum: -1,
			Reason: "event sequence row violates canonical identity, owner, or integer sequence",
		})
	}
	if err := malformed.Err(); err != nil {
		malformed.Close()
		return wrapStorage(ctx, "iterate malformed event sequences", err)
	}
	if err := malformed.Close(); err != nil {
		return wrapStorage(ctx, "close malformed event sequences", err)
	}

	rows, err := connection.QueryContext(ctx, `
SELECT s.aggregate_id, s.seq,
       COALESCE(MIN(e.seq), -1), COALESCE(MAX(e.seq), -1), COUNT(e.rowid)
FROM event_sequence AS s
LEFT JOIN event AS e
  ON e.aggregate_id = s.aggregate_id AND typeof(e.seq) = 'integer'
WHERE typeof(s.aggregate_id) = 'text' AND typeof(s.seq) = 'integer' AND s.seq >= 0
GROUP BY s.aggregate_id, s.seq
HAVING MIN(e.seq) IS NULL OR MIN(e.seq) <> 0 OR MAX(e.seq) <> s.seq OR COUNT(e.rowid) <> s.seq + 1
ORDER BY s.aggregate_id ASC`)
	if err != nil {
		return wrapStorage(ctx, "query event sequence invariants", err)
	}
	defer rows.Close()
	for rows.Next() {
		var violation SequenceViolation
		if err := rows.Scan(&violation.AggregateID, &violation.Declared, &violation.Minimum,
			&violation.Maximum, &violation.EventCount); err != nil {
			return wrapStorage(ctx, "scan event sequence invariant", err)
		}
		violation.Reason = "declared sequence does not match a contiguous zero-based event range"
		report.Sequences = append(report.Sequences, violation)
	}
	if err := rows.Err(); err != nil {
		return wrapStorage(ctx, "iterate event sequence invariants", err)
	}
	sort.Slice(report.Sequences, func(left, right int) bool {
		return report.Sequences[left].AggregateID < report.Sequences[right].AggregateID
	})
	return nil
}
