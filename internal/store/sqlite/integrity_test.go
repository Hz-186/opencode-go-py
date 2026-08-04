package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/event"
)

func TestRealStoreIntegrityCheckAcceptsHealthyDatabase(t *testing.T) {
	t.Parallel()

	store := openRealTestStore(t, nil)
	service, err := event.NewService(store, storeFixedEventIDs("evt_integrity_healthy"))
	if err != nil {
		t.Fatalf("new event service: %v", err)
	}
	if _, err := service.Publish(context.Background(), realStoreDefinition(),
		realStoreData("fixture-integrity", "healthy"), event.PublishOptions{}); err != nil {
		t.Fatalf("publish healthy event: %v", err)
	}
	report, err := store.CheckIntegrity(context.Background())
	if err != nil {
		t.Fatalf("check healthy database: %v", err)
	}
	if !report.Healthy() || len(report.QuickCheck) != 0 || len(report.ForeignKeys) != 0 ||
		len(report.Sequences) != 0 || len(report.Events) != 0 {
		t.Fatalf("healthy integrity report = %+v", report)
	}
}

func TestRealStoreIntegrityCheckReportsSequenceDriftAndBlocksReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sequence-drift.sqlite")
	options := DefaultOpenOptions(path)
	store, err := Open(context.Background(), options)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	service, err := event.NewService(store, storeFixedEventIDs("evt_integrity_drift"))
	if err != nil {
		t.Fatalf("new event service: %v", err)
	}
	if _, err := service.Publish(context.Background(), realStoreDefinition(),
		realStoreData("fixture-drift", "event"), event.PublishOptions{}); err != nil {
		t.Fatalf("publish event: %v", err)
	}
	if err := store.Write(context.Background(), func(ctx context.Context, tx event.Transaction) error {
		_, err := tx.(SQLExecutor).ExecContext(ctx,
			"UPDATE event_sequence SET seq = ? WHERE aggregate_id = ?", 7, "fixture-drift")
		return err
	}); err != nil {
		t.Fatalf("seed sequence drift: %v", err)
	}
	report, err := store.CheckIntegrity(context.Background())
	assertSequenceIntegrityError(t, report, err, "fixture-drift", 7, 0, 1)
	if err := store.Close(); err != nil {
		t.Fatalf("close corrupt store: %v", err)
	}

	_, err = Open(context.Background(), options)
	var integrityErr *IntegrityError
	if !errors.Is(err, ErrIntegrity) || !errors.Is(err, ErrStorageCorrupt) ||
		!errors.As(err, &integrityErr) {
		t.Fatalf("reopen corrupt database error = %#v, want IntegrityError", err)
	}
}

func TestRealStoreIntegrityCheckReportsForeignKeyAndPayloadViolations(t *testing.T) {
	t.Parallel()

	store := openRealTestStore(t, nil)
	database, err := sql.Open(sqliteDriverName, store.dsn)
	if err != nil {
		t.Fatalf("open corruption connection: %v", err)
	}
	defer database.Close()
	connection, err := database.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire corruption connection: %v", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(context.Background(), "PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatalf("disable foreign keys on corruption connection: %v", err)
	}
	if _, err := connection.ExecContext(context.Background(), `
INSERT INTO event(id, aggregate_id, seq, type, data)
VALUES (?, ?, ?, ?, ?)`, "evt_integrity_orphan", "missing-sequence", 0,
		"fixture.changed.1", "{\"truncated\":"); err != nil {
		t.Fatalf("seed orphan event: %v", err)
	}

	report, err := store.CheckIntegrity(context.Background())
	var integrityErr *IntegrityError
	if !errors.Is(err, ErrIntegrity) || !errors.Is(err, ErrStorageCorrupt) ||
		!errors.As(err, &integrityErr) {
		t.Fatalf("integrity error = %#v, want IntegrityError", err)
	}
	if len(report.ForeignKeys) != 1 || report.ForeignKeys[0].Table != "event" ||
		report.ForeignKeys[0].Parent != "event_sequence" {
		t.Fatalf("foreign key violations = %+v", report.ForeignKeys)
	}
	if len(report.Events) != 1 || report.Events[0].ID != "evt_integrity_orphan" {
		t.Fatalf("event violations = %+v", report.Events)
	}
	if len(integrityErr.Report.ForeignKeys) != 1 || len(integrityErr.Report.Events) != 1 {
		t.Fatalf("typed integrity report = %+v", integrityErr.Report)
	}
}

func TestRealStoreIntegrityCheckHonorsCancellationAndClose(t *testing.T) {
	t.Parallel()

	store := openRealTestStore(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.CheckIntegrity(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled integrity check = %v, want context.Canceled", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if _, err := store.CheckIntegrity(context.Background()); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("integrity check after close = %v, want ErrStoreClosed", err)
	}
}

func assertSequenceIntegrityError(
	t *testing.T,
	report IntegrityReport,
	err error,
	aggregateID string,
	declared int64,
	maximum int64,
	count int64,
) {
	t.Helper()
	var integrityErr *IntegrityError
	if !errors.Is(err, ErrIntegrity) || !errors.Is(err, ErrStorageCorrupt) ||
		!errors.As(err, &integrityErr) {
		t.Fatalf("integrity error = %#v, want IntegrityError", err)
	}
	if report.Healthy() || len(report.Sequences) != 1 {
		t.Fatalf("sequence integrity report = %+v", report)
	}
	violation := report.Sequences[0]
	if violation.AggregateID != aggregateID || violation.Declared != declared ||
		violation.Maximum != maximum || violation.EventCount != count {
		t.Fatalf("sequence violation = %+v", violation)
	}
	if len(integrityErr.Report.Sequences) != 1 {
		t.Fatalf("typed integrity report = %+v", integrityErr.Report)
	}
}
