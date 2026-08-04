package sqlite

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/Hz-186/opencode-go-py/internal/codec"
	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/event"
)

const (
	realStorePropertySeeds  = 10_000
	tenMillionEventCount    = 10_000_000
	tenMillionBenchmarkFlag = "OPENCODE_SQLITE_10M_BENCHMARK"
)

func TestRealStoreTenThousandSeedSequenceProperty(t *testing.T) {
	store := openRealTestStore(t, nil)
	model := make(map[string][]domain.EventID)
	err := store.Write(context.Background(), func(ctx context.Context, tx event.Transaction) error {
		for seed := range realStorePropertySeeds {
			aggregateID := fmt.Sprintf("property-%03d", propertyAggregate(seed))
			expected := model[aggregateID]
			state, found, err := tx.Sequence(ctx, aggregateID)
			if err != nil {
				return fmt.Errorf("seed %d read sequence: %w", seed, err)
			}
			if found != (len(expected) != 0) {
				return fmt.Errorf("seed %d aggregate %q found=%v events=%d",
					seed, aggregateID, found, len(expected))
			}
			if found && state.Latest != int64(len(expected)-1) {
				return fmt.Errorf("seed %d aggregate %q latest=%d want=%d",
					seed, aggregateID, state.Latest, len(expected)-1)
			}
			sequence := int64(len(expected))
			id := domain.EventID(fmt.Sprintf("evt_property_%05d", seed))
			data, err := codec.EncodeJSONValue(realStoreData(aggregateID, fmt.Sprintf("seed-%d", seed)))
			if err != nil {
				return fmt.Errorf("seed %d encode data: %w", seed, err)
			}
			if err := tx.PutSequence(ctx, aggregateID, event.SequenceState{Latest: sequence}); err != nil {
				return fmt.Errorf("seed %d put sequence: %w", seed, err)
			}
			if err := tx.InsertEvent(ctx, event.StoredEvent{
				ID: id, AggregateID: aggregateID, Sequence: sequence,
				Type: "fixture.changed.1", Data: data,
			}); err != nil {
				return fmt.Errorf("seed %d insert event: %w", seed, err)
			}
			model[aggregateID] = append(expected, id)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("10k property transaction: %v", err)
	}
	for aggregateID, expected := range model {
		history, err := store.History(context.Background(), aggregateID, -1, len(expected))
		if err != nil {
			t.Fatalf("read property aggregate %q: %v", aggregateID, err)
		}
		if len(history) != len(expected) {
			t.Fatalf("property aggregate %q history=%d want=%d",
				aggregateID, len(history), len(expected))
		}
		for index, record := range history {
			if record.ID != expected[index] || record.Sequence != int64(index) {
				t.Fatalf("property aggregate %q event %d = %q/%d want %q/%d",
					aggregateID, index, record.ID, record.Sequence, expected[index], index)
			}
		}
	}
	if report, err := store.CheckIntegrity(context.Background()); err != nil || !report.Healthy() {
		t.Fatalf("10k property integrity report/error = %+v/%v", report, err)
	}
}

func BenchmarkRealStoreHistoryTenMillionEvents(b *testing.B) {
	if os.Getenv(tenMillionBenchmarkFlag) != "1" {
		b.Skip("set OPENCODE_SQLITE_10M_BENCHMARK=1 to build the 10M Event capacity fixture")
	}
	store := openRealTestStore(b, nil)
	b.StopTimer()
	if err := store.Write(context.Background(), func(ctx context.Context, tx event.Transaction) error {
		executor := tx.(SQLExecutor)
		if _, err := executor.ExecContext(ctx, `
INSERT INTO event_sequence(aggregate_id, seq, owner_id)
VALUES ('benchmark-history', ?, NULL)`, tenMillionEventCount-1); err != nil {
			return err
		}
		_, err := executor.ExecContext(ctx, `
WITH RECURSIVE generated(seq) AS (
  VALUES(0)
  UNION ALL
  SELECT seq + 1 FROM generated WHERE seq + 1 < ?
)
INSERT INTO event(id, aggregate_id, seq, type, data)
SELECT printf('evt_benchmark_%08d', seq),
       'benchmark-history',
       seq,
       'fixture.changed.1',
       '{"fixtureID":"benchmark-history","text":"capacity"}' || char(10)
FROM generated`, tenMillionEventCount)
		return err
	}); err != nil {
		b.Fatalf("build 10M Event fixture: %v", err)
	}
	var count, minimum, maximum int64
	if err := store.readerDB.QueryRowContext(context.Background(), `
SELECT COUNT(*), COALESCE(MIN(seq), -1), COALESCE(MAX(seq), -1)
FROM event
WHERE aggregate_id = 'benchmark-history'`).Scan(&count, &minimum, &maximum); err != nil {
		b.Fatalf("verify 10M Event sequence: %v", err)
	}
	if count != tenMillionEventCount || minimum != 0 || maximum != tenMillionEventCount-1 {
		b.Fatalf("10M Event sequence count/min/max = %d/%d/%d, want %d/0/%d",
			count, minimum, maximum, tenMillionEventCount, tenMillionEventCount-1)
	}
	durations := make([]time.Duration, 0, b.N)
	b.ReportMetric(tenMillionEventCount, "events")
	b.StartTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		started := time.Now()
		history, err := store.History(context.Background(), "benchmark-history",
			tenMillionEventCount-101, 100)
		elapsed := time.Since(started)
		if err != nil || len(history) != 100 {
			b.Fatalf("read 10M history page length/error = %d/%v", len(history), err)
		}
		durations = append(durations, elapsed)
	}
	b.StopTimer()
	sort.Slice(durations, func(left, right int) bool { return durations[left] < durations[right] })
	p95 := durations[(len(durations)-1)*95/100]
	b.ReportMetric(float64(p95.Microseconds())/1000, "p95-ms")
	if p95 > 20*time.Millisecond {
		b.Fatalf("10M Event history p95 = %s, want <= 20ms", p95)
	}
}

func propertyAggregate(seed int) int {
	value := uint64(seed + 1)
	value ^= value >> 12
	value ^= value << 25
	value ^= value >> 27
	return int((value * 2685821657736338717) % 257)
}
