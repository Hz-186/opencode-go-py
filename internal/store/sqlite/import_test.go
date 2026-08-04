package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/event"
)

func TestRealStoreCanonicalReplayImportIsValidatedIdempotentAndProjected(t *testing.T) {
	t.Parallel()

	source := openRealTestStore(t, nil)
	sourceService, err := event.NewService(source,
		storeFixedEventIDs("evt_import_zero", "evt_import_one", "evt_import_two"))
	if err != nil {
		t.Fatalf("new source event service: %v", err)
	}
	entries := make([]event.ReplayEntry, 0, 3)
	for _, value := range []string{"zero", "one", "two"} {
		envelope, err := sourceService.Publish(context.Background(), realStoreDefinition(),
			realStoreData("fixture-import", value), event.PublishOptions{})
		if err != nil {
			t.Fatalf("publish source event %q: %v", value, err)
		}
		entries = append(entries, event.ReplayEntry{Definition: realStoreDefinition(), Event: envelope})
	}

	destination := openRealTestStore(t, nil)
	if err := destination.Write(context.Background(), func(ctx context.Context, tx event.Transaction) error {
		_, err := tx.(SQLExecutor).ExecContext(ctx, `
CREATE TABLE import_projection (
  event_id TEXT PRIMARY KEY,
  seq INTEGER NOT NULL
)`)
		return err
	}); err != nil {
		t.Fatalf("create import projection: %v", err)
	}
	destinationService, err := event.NewService(destination, storeFixedEventIDs())
	if err != nil {
		t.Fatalf("new destination event service: %v", err)
	}
	if err := destinationService.Project(realStoreDefinition(),
		func(ctx context.Context, tx event.Transaction, envelope domain.EventEnvelope) error {
			_, err := tx.(SQLExecutor).ExecContext(ctx,
				"INSERT INTO import_projection(event_id, seq) VALUES (?, ?)",
				envelope.ID, envelope.Durable.Sequence)
			return err
		}); err != nil {
		t.Fatalf("register import projector: %v", err)
	}
	aggregate, err := destinationService.ReplayAll(context.Background(), entries,
		event.ReplayOptions{OwnerID: "canonical-import-v1", StrictOwner: true})
	if err != nil || aggregate != "fixture-import" {
		t.Fatalf("canonical replay import aggregate/error = %q/%v", aggregate, err)
	}
	history, err := destination.History(context.Background(), "fixture-import", -1, 10)
	if err != nil {
		t.Fatalf("read imported history: %v", err)
	}
	if got := backupEventIDs(history); !reflect.DeepEqual(got,
		[]domain.EventID{"evt_import_zero", "evt_import_one", "evt_import_two"}) {
		t.Fatalf("imported event IDs = %v", got)
	}
	assertImportProjectionCount(t, destination, 3)

	if _, err := destinationService.ReplayAll(context.Background(), entries,
		event.ReplayOptions{OwnerID: "canonical-import-v1", StrictOwner: true}); err != nil {
		t.Fatalf("retry exact canonical replay import: %v", err)
	}
	assertImportProjectionCount(t, destination, 3)

	invalid := append([]event.ReplayEntry(nil), entries...)
	invalid[2].Event.Durable = cloneEventDurable(invalid[2].Event.Durable)
	invalid[2].Event.Durable.Sequence = 9
	if _, err := destinationService.ReplayAll(context.Background(), invalid,
		event.ReplayOptions{OwnerID: "canonical-import-v1", StrictOwner: true}); !errors.Is(err, event.ErrSequenceConflict) {
		t.Fatalf("invalid canonical import error = %v, want ErrSequenceConflict", err)
	}
	history, err = destination.History(context.Background(), "fixture-import", -1, 10)
	if err != nil || len(history) != 3 {
		t.Fatalf("invalid import changed history/error = %d/%v", len(history), err)
	}
	assertImportProjectionCount(t, destination, 3)
}

func cloneEventDurable(value *domain.EventDurable) *domain.EventDurable {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func assertImportProjectionCount(t *testing.T, store *Store, want int) {
	t.Helper()
	var got int
	if err := store.readerDB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM import_projection").Scan(&got); err != nil {
		t.Fatalf("count import projection: %v", err)
	}
	if got != want {
		t.Fatalf("import projection count = %d, want %d", got, want)
	}
}
