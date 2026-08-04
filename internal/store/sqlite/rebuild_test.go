package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/codec"
	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/event"
)

func TestRealStoreRebuildsProjectionThroughVerifiedShadowSwap(t *testing.T) {
	t.Parallel()

	store := openRealTestStore(t, nil)
	seedRebuildEvents(t, store)
	if err := store.Write(context.Background(), func(ctx context.Context, tx event.Transaction) error {
		_, err := tx.(SQLExecutor).ExecContext(ctx, `
CREATE TABLE projection_live (
  event_id TEXT PRIMARY KEY,
  aggregate_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  data TEXT NOT NULL
)`)
		return err
	}); err != nil {
		t.Fatalf("create empty live projection: %v", err)
	}
	var captured RebuildExecutor
	summary, err := store.RebuildProjection(context.Background(), ProjectionRebuild{
		Name: "fixture projection",
		CreateShadow: func(ctx context.Context, executor RebuildExecutor) error {
			captured = executor
			_, err := executor.ExecContext(ctx, `
CREATE TABLE projection_shadow (
  event_id TEXT PRIMARY KEY,
  aggregate_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  data TEXT NOT NULL
)`)
			return err
		},
		Project: func(ctx context.Context, executor RebuildExecutor, record event.StoredEvent) error {
			_, err := executor.ExecContext(ctx, `
INSERT INTO projection_shadow(event_id, aggregate_id, seq, data)
VALUES (?, ?, ?, ?)`, record.ID, record.AggregateID, record.Sequence, string(record.Data))
			return err
		},
		Verify: func(ctx context.Context, executor RebuildExecutor, summary RebuildSummary) error {
			row, err := executor.QueryRowContext(ctx,
				"SELECT COUNT(*), COALESCE(MAX(seq), -1) FROM projection_shadow")
			if err != nil {
				return err
			}
			var count, maximum int64
			if err := row.Scan(&count, &maximum); err != nil {
				return err
			}
			if count != summary.EventCount || maximum != summary.MaximumSequence {
				return errors.New("shadow projection does not match source summary")
			}
			return nil
		},
		Swap: func(ctx context.Context, executor RebuildExecutor) error {
			if _, err := executor.ExecContext(ctx, "DROP TABLE projection_live"); err != nil {
				return err
			}
			_, err := executor.ExecContext(ctx,
				"ALTER TABLE projection_shadow RENAME TO projection_live")
			return err
		},
	})
	if err != nil {
		t.Fatalf("rebuild projection: %v", err)
	}
	if summary.EventCount != 2 || summary.AggregateCount != 1 ||
		summary.MaximumSequence != 1 || len(summary.SHA256) != 64 {
		t.Fatalf("rebuild summary = %+v", summary)
	}
	var liveCount int64
	if err := store.readerDB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM projection_live").Scan(&liveCount); err != nil {
		t.Fatalf("read rebuilt projection: %v", err)
	}
	if liveCount != 2 {
		t.Fatalf("rebuilt projection rows = %d, want 2", liveCount)
	}
	if _, err := captured.ExecContext(context.Background(), "SELECT 1"); !errors.Is(err, ErrTransactionClosed) {
		t.Fatalf("expired rebuild executor error = %v, want ErrTransactionClosed", err)
	}
}

func TestRealStoreRebuildFailureAndCancellationPreserveLiveProjection(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		verify func(context.Context, RebuildExecutor, RebuildSummary) error
		want   error
	}{
		{
			name: "verification failure",
			verify: func(context.Context, RebuildExecutor, RebuildSummary) error {
				return errors.New("shadow verification failed")
			},
			want: ErrRebuild,
		},
		{
			name: "cancellation",
			verify: func(ctx context.Context, _ RebuildExecutor, _ RebuildSummary) error {
				return ctx.Err()
			},
			want: context.Canceled,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := openRealTestStore(t, nil)
			seedRebuildEvents(t, store)
			if err := store.Write(context.Background(), func(ctx context.Context, tx event.Transaction) error {
				_, err := tx.(SQLExecutor).ExecContext(ctx,
					"CREATE TABLE projection_live (event_id TEXT PRIMARY KEY)")
				if err != nil {
					return err
				}
				_, err = tx.(SQLExecutor).ExecContext(ctx,
					"INSERT INTO projection_live(event_id) VALUES (?)", "sentinel")
				return err
			}); err != nil {
				t.Fatalf("seed live projection: %v", err)
			}
			ctx := context.Background()
			if test.name == "cancellation" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				originalVerify := test.verify
				test.verify = func(ctx context.Context, executor RebuildExecutor, summary RebuildSummary) error {
					cancel()
					return originalVerify(ctx, executor, summary)
				}
			}
			_, err := store.RebuildProjection(ctx, ProjectionRebuild{
				Name: "failing projection",
				CreateShadow: func(ctx context.Context, executor RebuildExecutor) error {
					_, err := executor.ExecContext(ctx,
						"CREATE TABLE projection_shadow (event_id TEXT PRIMARY KEY)")
					return err
				},
				Project: func(ctx context.Context, executor RebuildExecutor, record event.StoredEvent) error {
					_, err := executor.ExecContext(ctx,
						"INSERT INTO projection_shadow(event_id) VALUES (?)", record.ID)
					return err
				},
				Verify: test.verify,
				Swap: func(ctx context.Context, executor RebuildExecutor) error {
					_, err := executor.ExecContext(ctx, "DROP TABLE projection_live")
					return err
				},
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("rebuild failure = %v, want %v", err, test.want)
			}
			var liveCount, shadowExists int
			if err := store.readerDB.QueryRowContext(context.Background(),
				"SELECT COUNT(*) FROM projection_live").Scan(&liveCount); err != nil {
				t.Fatalf("read preserved live projection: %v", err)
			}
			if err := store.readerDB.QueryRowContext(context.Background(), `
SELECT EXISTS (SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = 'projection_shadow')
`).Scan(&shadowExists); err != nil {
				t.Fatalf("inspect rolled back shadow projection: %v", err)
			}
			if liveCount != 1 || shadowExists != 0 {
				t.Fatalf("failed rebuild live/shadow state = %d/%d", liveCount, shadowExists)
			}
		})
	}
}

func TestRealStoreReplayRebuildMatchesLiveProjectionByteForByte(t *testing.T) {
	t.Parallel()

	store := openRealTestStore(t, nil)
	if err := store.Write(context.Background(), func(ctx context.Context, tx event.Transaction) error {
		_, err := tx.(SQLExecutor).ExecContext(ctx, replaySnapshotProjectionDDL("projection_live"))
		return err
	}); err != nil {
		t.Fatalf("create live replay snapshot projection: %v", err)
	}
	service, err := event.NewService(store, storeFixedEventIDs(
		"evt_snapshot_a0", "evt_snapshot_b0", "evt_snapshot_a1", "evt_snapshot_b1"))
	if err != nil {
		t.Fatalf("new event service: %v", err)
	}
	definition := realStoreDefinition()
	if err := service.Project(definition,
		func(ctx context.Context, tx event.Transaction, envelope domain.EventEnvelope) error {
			data := string(mustCanonicalEventData(t, envelope.Data))
			_, err := tx.(SQLExecutor).ExecContext(ctx, replaySnapshotProjectionSQL("projection_live"),
				envelope.Durable.AggregateID, envelope.Durable.Sequence,
				data, envelope.Durable.Sequence, data)
			return err
		}); err != nil {
		t.Fatalf("register live replay snapshot projector: %v", err)
	}
	for _, fixture := range []struct {
		aggregate string
		text      string
	}{
		{aggregate: "fixture-snapshot-a", text: "alpha"},
		{aggregate: "fixture-snapshot-b", text: "中文"},
		{aggregate: "fixture-snapshot-a", text: "omega"},
		{aggregate: "fixture-snapshot-b", text: "emoji 🚀"},
	} {
		if _, err := service.Publish(context.Background(), definition,
			realStoreData(fixture.aggregate, fixture.text), event.PublishOptions{}); err != nil {
			t.Fatalf("publish live snapshot event %q/%q: %v", fixture.aggregate, fixture.text, err)
		}
	}
	want := readReplaySnapshot(t, store, "projection_live")

	if _, err := store.RebuildProjection(context.Background(), ProjectionRebuild{
		Name: "byte exact replay snapshot",
		CreateShadow: func(ctx context.Context, executor RebuildExecutor) error {
			_, err := executor.ExecContext(ctx, replaySnapshotProjectionDDL("projection_shadow"))
			return err
		},
		Project: func(ctx context.Context, executor RebuildExecutor, record event.StoredEvent) error {
			_, err := executor.ExecContext(ctx, replaySnapshotProjectionSQL("projection_shadow"),
				record.AggregateID, record.Sequence, string(record.Data), record.Sequence, string(record.Data))
			return err
		},
		Verify: func(ctx context.Context, executor RebuildExecutor, _ RebuildSummary) error {
			row, err := executor.QueryRowContext(ctx, replaySnapshotQuery("projection_shadow"))
			if err != nil {
				return err
			}
			var got string
			if err := row.Scan(&got); err != nil {
				return err
			}
			if got != want {
				return errors.New("replayed shadow snapshot differs from live projection bytes")
			}
			return nil
		},
		Swap: func(ctx context.Context, executor RebuildExecutor) error {
			if _, err := executor.ExecContext(ctx, "DROP TABLE projection_live"); err != nil {
				return err
			}
			_, err := executor.ExecContext(ctx,
				"ALTER TABLE projection_shadow RENAME TO projection_live")
			return err
		},
	}); err != nil {
		t.Fatalf("rebuild byte exact replay snapshot: %v", err)
	}
	if got := readReplaySnapshot(t, store, "projection_live"); got != want {
		t.Fatalf("rebuilt projection snapshot bytes = %q, want %q", got, want)
	}
}

func replaySnapshotProjectionDDL(table string) string {
	return `CREATE TABLE ` + table + ` (
  aggregate_id TEXT PRIMARY KEY,
  event_count INTEGER NOT NULL,
  maximum_seq INTEGER NOT NULL,
  transcript TEXT NOT NULL
)`
}

func replaySnapshotProjectionSQL(table string) string {
	return `INSERT INTO ` + table + `(aggregate_id, event_count, maximum_seq, transcript)
VALUES (?, 1, ?, json_array(json(?)))
ON CONFLICT(aggregate_id) DO UPDATE SET
  event_count = event_count + 1,
  maximum_seq = ?,
  transcript = json_insert(transcript, '$[#]', json(?))`
}

func replaySnapshotQuery(table string) string {
	return `SELECT COALESCE(json_group_array(json(value)), '[]')
FROM (
  SELECT json_object(
    'aggregate_id', aggregate_id,
    'event_count', event_count,
    'maximum_seq', maximum_seq,
    'transcript', json(transcript)
  ) AS value
  FROM ` + table + `
  ORDER BY aggregate_id
)`
}

func readReplaySnapshot(t *testing.T, store *Store, table string) string {
	t.Helper()
	var snapshot string
	if err := store.readerDB.QueryRowContext(context.Background(),
		replaySnapshotQuery(table)).Scan(&snapshot); err != nil {
		t.Fatalf("read %s replay snapshot: %v", table, err)
	}
	return snapshot
}

func mustCanonicalEventData(t *testing.T, data domain.JSONValue) []byte {
	t.Helper()
	encoded, err := codec.EncodeJSONValue(data)
	if err != nil {
		t.Fatalf("encode canonical event data: %v", err)
	}
	return encoded
}

func seedRebuildEvents(t *testing.T, store *Store) {
	t.Helper()
	service, err := event.NewService(store,
		storeFixedEventIDs("evt_rebuild_zero", "evt_rebuild_one"))
	if err != nil {
		t.Fatalf("new event service: %v", err)
	}
	for _, value := range []string{"zero", "one"} {
		if _, err := service.Publish(context.Background(), realStoreDefinition(),
			realStoreData("fixture-rebuild", value), event.PublishOptions{}); err != nil {
			t.Fatalf("publish rebuild event %q: %v", value, err)
		}
	}
}
