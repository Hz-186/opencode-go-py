package sqlite

import (
	"context"
	"errors"
	"testing"

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
