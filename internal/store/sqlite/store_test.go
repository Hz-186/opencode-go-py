package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/event"
)

func TestRealStoreMigratesReopensAndPersistsHistory(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "真实 event store.sqlite")
	options := DefaultOpenOptions(path)
	options.MaxReaders = 2
	store, err := Open(context.Background(), options)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	service, err := event.NewService(store, storeFixedEventIDs("evt_real_zero", "evt_real_one"))
	if err != nil {
		t.Fatalf("new event service: %v", err)
	}
	definition := realStoreDefinition()
	for _, text := range []string{"zero", "one"} {
		if _, err := service.Publish(context.Background(), definition,
			realStoreData("fixture-real", text), event.PublishOptions{}); err != nil {
			t.Fatalf("publish %q: %v", text, err)
		}
	}
	history, err := store.History(context.Background(), "fixture-real", 0, 10)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if len(history) != 1 || history[0].ID != "evt_real_one" || history[0].Sequence != 1 {
		t.Fatalf("history after sequence 0 = %+v", history)
	}
	var migrationCount int
	if err := store.readerDB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM schema_migration").Scan(&migrationCount); err != nil {
		t.Fatalf("read migration count: %v", err)
	}
	if migrationCount != 1 {
		t.Fatalf("migration count = %d, want 1", migrationCount)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	reopened, err := Open(context.Background(), options)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	history, err = reopened.History(context.Background(), "fixture-real", -1, 10)
	if err != nil {
		t.Fatalf("read reopened history: %v", err)
	}
	if got := []domain.EventID{history[0].ID, history[1].ID}; !reflect.DeepEqual(got, []domain.EventID{"evt_real_zero", "evt_real_one"}) {
		t.Fatalf("reopened history IDs = %v", got)
	}
	for pragma, want := range map[string]int{
		"foreign_keys": 1,
		"busy_timeout": int(options.Policy.BusyTimeout.Milliseconds()),
		"temp_store":   2,
		"synchronous":  1,
	} {
		var got int
		if err := reopened.writer.QueryRowContext(context.Background(), "PRAGMA "+pragma).Scan(&got); err != nil {
			t.Fatalf("read PRAGMA %s: %v", pragma, err)
		}
		if got != want {
			t.Fatalf("PRAGMA %s = %d, want %d", pragma, got, want)
		}
	}
	var journal string
	if err := reopened.writer.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	if journal != "wal" {
		t.Fatalf("journal mode = %q, want wal", journal)
	}
}

func TestRealStoreRollsBackProjectionSequenceAndEventTogether(t *testing.T) {
	t.Parallel()

	store := openRealTestStore(t, nil)
	if err := store.Write(context.Background(), func(ctx context.Context, tx event.Transaction) error {
		executor, ok := tx.(SQLExecutor)
		if !ok {
			return errors.New("transaction does not expose SQLExecutor")
		}
		_, err := executor.ExecContext(ctx,
			"CREATE TABLE projection_probe (event_id TEXT PRIMARY KEY, value TEXT NOT NULL)")
		return err
	}); err != nil {
		t.Fatalf("create projection probe: %v", err)
	}
	service, err := event.NewService(store, storeFixedEventIDs("evt_rollback", "evt_committed"))
	if err != nil {
		t.Fatalf("new event service: %v", err)
	}
	definition := realStoreDefinition()
	if err := service.Project(definition, func(ctx context.Context, tx event.Transaction, envelope domain.EventEnvelope) error {
		executor, ok := tx.(SQLExecutor)
		if !ok {
			return errors.New("projector transaction does not expose SQLExecutor")
		}
		_, err := executor.ExecContext(ctx,
			"INSERT INTO projection_probe(event_id, value) VALUES (?, ?)",
			envelope.ID, envelope.Data.Object["text"].String)
		return err
	}); err != nil {
		t.Fatalf("register projector: %v", err)
	}
	commitFailure := errors.New("local commit failed")
	_, err = service.Publish(context.Background(), definition,
		realStoreData("fixture-rollback", "rolled back"), event.PublishOptions{
			Commit: func(context.Context, event.Transaction, int64) error { return commitFailure },
		})
	if !errors.Is(err, commitFailure) {
		t.Fatalf("failed publish error = %v, want commit failure", err)
	}
	assertRealStoreCounts(t, store, 0, 0, 0)

	committed, err := service.Publish(context.Background(), definition,
		realStoreData("fixture-rollback", "committed"), event.PublishOptions{})
	if err != nil {
		t.Fatalf("committed publish: %v", err)
	}
	if committed.Durable == nil || committed.Durable.Sequence != 0 {
		t.Fatalf("committed event durable = %+v, want sequence 0", committed.Durable)
	}
	assertRealStoreCounts(t, store, 1, 1, 1)
}

func TestRealStoreConcurrentPublishAllocatesContiguousSequences(t *testing.T) {
	t.Parallel()

	store := openRealTestStore(t, nil)
	const count = 64
	ids := make([]domain.EventID, count)
	for index := range count {
		ids[index] = domain.EventID(fmt.Sprintf("evt_sql_concurrent_%03d", index))
	}
	service, err := event.NewService(store, storeFixedEventIDs(ids...))
	if err != nil {
		t.Fatalf("new event service: %v", err)
	}
	start := make(chan struct{})
	errs := make(chan error, count)
	var group sync.WaitGroup
	group.Add(count)
	for index := range count {
		go func() {
			defer group.Done()
			<-start
			_, err := service.Publish(context.Background(), realStoreDefinition(),
				realStoreData("fixture-concurrent", fmt.Sprintf("value-%d", index)), event.PublishOptions{})
			errs <- err
		}()
	}
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent publish: %v", err)
		}
	}
	history, err := store.History(context.Background(), "fixture-concurrent", -1, count)
	if err != nil {
		t.Fatalf("read concurrent history: %v", err)
	}
	if len(history) != count {
		t.Fatalf("history length = %d, want %d", len(history), count)
	}
	for index, record := range history {
		if record.Sequence != int64(index) {
			t.Fatalf("history %d sequence = %d, want %d", index, record.Sequence, index)
		}
	}
}

func TestRealStoreBusyIsTypedAndDoesNotLeakState(t *testing.T) {
	t.Parallel()

	store := openRealTestStore(t, func(options *OpenOptions) {
		options.Policy.BusyTimeout = 20 * time.Millisecond
	})
	lockerDB, err := sql.Open(sqliteDriverName, store.dsn)
	if err != nil {
		t.Fatalf("open lock database: %v", err)
	}
	defer lockerDB.Close()
	locker, err := lockerDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire lock connection: %v", err)
	}
	defer locker.Close()
	if _, err := locker.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin external immediate transaction: %v", err)
	}
	locked := true
	defer func() {
		if locked {
			_, _ = locker.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	service, err := event.NewService(store, storeFixedEventIDs("evt_busy", "evt_after_busy"))
	if err != nil {
		t.Fatalf("new event service: %v", err)
	}
	_, err = service.Publish(context.Background(), realStoreDefinition(),
		realStoreData("fixture-busy", "blocked"), event.PublishOptions{})
	if !errors.Is(err, ErrStorageBusy) || !errors.Is(err, ErrStorage) {
		t.Fatalf("busy publish error = %v, want typed storage busy", err)
	}
	history, historyErr := store.History(context.Background(), "fixture-busy", -1, 10)
	if historyErr != nil || len(history) != 0 {
		t.Fatalf("busy publish leaked history/error = %+v/%v", history, historyErr)
	}
	if _, err := locker.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatalf("release external transaction: %v", err)
	}
	locked = false
	committed, err := service.Publish(context.Background(), realStoreDefinition(),
		realStoreData("fixture-busy", "committed"), event.PublishOptions{})
	if err != nil || committed.Durable == nil || committed.Durable.Sequence != 0 {
		t.Fatalf("publish after busy event/error = %+v/%v", committed, err)
	}
}

func TestRealStoreWriterQueueHonorsCancellationAndClose(t *testing.T) {
	t.Parallel()

	store := openRealTestStore(t, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- store.Write(context.Background(), func(context.Context, event.Transaction) error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first writer did not enter transaction")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := store.Write(ctx, func(context.Context, event.Transaction) error {
		t.Fatal("canceled queued writer entered transaction")
		return nil
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued writer error = %v, want context.DeadlineExceeded", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first writer: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if err := store.Write(context.Background(), func(context.Context, event.Transaction) error { return nil }); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("write after close error = %v, want ErrStoreClosed", err)
	}
	if _, err := store.History(context.Background(), "fixture", -1, 1); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("history after close error = %v, want ErrStoreClosed", err)
	}
}

func TestRealStoreRejectsInvalidDirectTransactionInput(t *testing.T) {
	t.Parallel()

	store := openRealTestStore(t, nil)
	err := store.Write(context.Background(), func(ctx context.Context, tx event.Transaction) error {
		checks := []struct {
			name string
			run  func() error
		}{
			{name: "empty sequence aggregate", run: func() error {
				_, _, err := tx.Sequence(ctx, "")
				return err
			}},
			{name: "invalid event ID", run: func() error {
				_, _, err := tx.EventByID(ctx, "not-an-event-id")
				return err
			}},
			{name: "negative event sequence", run: func() error {
				_, _, err := tx.EventAt(ctx, "fixture", -1)
				return err
			}},
			{name: "negative latest sequence", run: func() error {
				return tx.PutSequence(ctx, "fixture", event.SequenceState{Latest: -1})
			}},
			{name: "invalid stored event", run: func() error {
				return tx.InsertEvent(ctx, event.StoredEvent{
					ID: "evt_invalid", AggregateID: "fixture", Sequence: 0,
					Type: "fixture.changed.1", Data: []byte("{\"duplicate\":1,\"duplicate\":2}\n"),
				})
			}},
			{name: "NUL aggregate delete", run: func() error {
				return tx.DeleteAggregate(ctx, "fixture\x00escape")
			}},
			{name: "empty projector SQL", run: func() error {
				executor := tx.(SQLExecutor)
				_, err := executor.ExecContext(ctx, " \n\t")
				return err
			}},
		}
		for _, check := range checks {
			if err := check.run(); !errors.Is(err, ErrInvalidStoreInput) {
				t.Errorf("%s error = %v, want ErrInvalidStoreInput", check.name, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("write containing rejected direct calls: %v", err)
	}
}

func TestRealStoreTransactionExpiresAfterWriteCallback(t *testing.T) {
	t.Parallel()

	store := openRealTestStore(t, nil)
	var captured event.Transaction
	if err := store.Write(context.Background(), func(_ context.Context, tx event.Transaction) error {
		captured = tx
		return nil
	}); err != nil {
		t.Fatalf("capture transaction: %v", err)
	}
	ctx := context.Background()
	checks := []struct {
		name string
		run  func() error
	}{
		{name: "sequence", run: func() error { _, _, err := captured.Sequence(ctx, "fixture"); return err }},
		{name: "event by ID", run: func() error { _, _, err := captured.EventByID(ctx, "evt_closed"); return err }},
		{name: "event at", run: func() error { _, _, err := captured.EventAt(ctx, "fixture", 0); return err }},
		{name: "put sequence", run: func() error {
			return captured.PutSequence(ctx, "fixture", event.SequenceState{Latest: 0})
		}},
		{name: "insert event", run: func() error {
			return captured.InsertEvent(ctx, event.StoredEvent{
				ID: "evt_closed", AggregateID: "fixture", Sequence: 0,
				Type: "fixture.changed.1", Data: []byte("{}\n"),
			})
		}},
		{name: "delete aggregate", run: func() error { return captured.DeleteAggregate(ctx, "fixture") }},
		{name: "projector SQL", run: func() error {
			_, err := captured.(SQLExecutor).ExecContext(ctx, "SELECT 1")
			return err
		}},
	}
	for _, check := range checks {
		if err := check.run(); !errors.Is(err, ErrTransactionClosed) {
			t.Errorf("expired %s error = %v, want ErrTransactionClosed", check.name, err)
		}
	}
}

func TestRealStoreHistoryScanFailureIsTyped(t *testing.T) {
	t.Parallel()

	store := openRealTestStore(t, nil)
	if err := store.Write(context.Background(), func(ctx context.Context, tx event.Transaction) error {
		executor := tx.(SQLExecutor)
		if _, err := executor.ExecContext(ctx,
			"INSERT INTO event_sequence(aggregate_id, seq) VALUES (?, ?)", "fixture-corrupt", 0); err != nil {
			return err
		}
		_, err := executor.ExecContext(ctx, `
INSERT INTO event(id, aggregate_id, seq, type, data)
VALUES (?, ?, ?, ?, ?)`, "evt_corrupt", "fixture-corrupt", "not-an-integer", "fixture.changed.1", "{}\n")
		return err
	}); err != nil {
		t.Fatalf("seed malformed row: %v", err)
	}
	_, err := store.History(context.Background(), "fixture-corrupt", -1, 10)
	if !errors.Is(err, ErrStorage) {
		t.Fatalf("history scan error = %v, want typed ErrStorage", err)
	}
	var storageErr *StorageError
	if !errors.As(err, &storageErr) || storageErr.Operation != "scan event history" {
		t.Fatalf("history scan typed error = %#v, want scan event history StorageError", err)
	}
}

func TestRealStoreRollbackFailureBreaksWriter(t *testing.T) {
	t.Parallel()

	store := openRealTestStore(t, nil)
	callbackFailure := errors.New("callback failed")
	err := store.Write(context.Background(), func(_ context.Context, tx event.Transaction) error {
		if err := tx.(*sqlEventTransaction).connection.Close(); err != nil {
			return fmt.Errorf("close transaction connection: %w", err)
		}
		return callbackFailure
	})
	if !errors.Is(err, callbackFailure) || !errors.Is(err, ErrStorage) {
		t.Fatalf("rollback failure error = %v, want callback failure and ErrStorage", err)
	}
	if !store.broken.Load() {
		t.Fatal("store did not mark writer broken after rollback failure")
	}
	if err := store.Write(context.Background(), func(context.Context, event.Transaction) error {
		return nil
	}); !errors.Is(err, ErrStoreBroken) {
		t.Fatalf("write after rollback failure = %v, want ErrStoreBroken", err)
	}
}

func openRealTestStore(t testing.TB, configure func(*OpenOptions)) *Store {
	t.Helper()
	options := DefaultOpenOptions(filepath.Join(t.TempDir(), "event.sqlite"))
	if configure != nil {
		configure(&options)
	}
	store, err := Open(context.Background(), options)
	if err != nil {
		t.Fatalf("open real store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close real store: %v", err)
		}
	})
	return store
}

func realStoreDefinition() event.Definition {
	return event.Definition{
		Type:    "fixture.changed",
		Durable: &event.DurableDefinition{Version: 1, AggregateField: "fixtureID"},
	}
}

func realStoreData(aggregateID, text string) domain.JSONValue {
	return domain.JSONObject(map[string]domain.JSONValue{
		"fixtureID": domain.JSONString(aggregateID),
		"text":      domain.JSONString(text),
	})
}

func storeFixedEventIDs(ids ...domain.EventID) event.EventIDGenerator {
	var mutex sync.Mutex
	index := 0
	return func() (domain.EventID, error) {
		mutex.Lock()
		defer mutex.Unlock()
		if index >= len(ids) {
			return "", errors.New("event ID fixture exhausted")
		}
		id := ids[index]
		index++
		return id, nil
	}
}

func assertRealStoreCounts(t *testing.T, store *Store, sequences, events, projections int) {
	t.Helper()
	for table, want := range map[string]int{
		"event_sequence":   sequences,
		"event":            events,
		"projection_probe": projections,
	} {
		var got int
		if err := store.readerDB.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM "+table).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("%s count = %d, want %d", table, got, want)
		}
	}
}
