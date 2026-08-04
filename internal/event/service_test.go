package event

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

func TestPublishCommitsProjectorSequenceAndEventBeforeNotification(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	service, err := NewService(store, fixedEventIDs("evt_first", "evt_second"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	definition := Definition{
		Type:    "fixture.changed",
		Durable: &DurableDefinition{Version: 2, AggregateField: "fixtureID"},
	}
	var order []string
	if err := service.Project(definition, func(_ context.Context, _ Transaction, event domain.EventEnvelope) error {
		order = append(order, "projector")
		if event.Durable == nil || event.Durable.Sequence != int64(len(order)/3) {
			t.Fatalf("projector event durable = %+v", event.Durable)
		}
		return nil
	}); err != nil {
		t.Fatalf("register projector: %v", err)
	}
	unlisten, err := service.Listen(func(event domain.EventEnvelope) {
		order = append(order, "listener")
		if got := store.eventCount(); got != int(event.Durable.Sequence)+1 {
			t.Fatalf("listener observed %d committed events at sequence %d", got, event.Durable.Sequence)
		}
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer unlisten()

	for sequence, text := range []string{"one", "two"} {
		event, err := service.Publish(context.Background(), definition, durableFixtureData("fixture-1", text), PublishOptions{
			Commit: func(_ context.Context, _ Transaction, got int64) error {
				order = append(order, "commit")
				if got != int64(sequence) {
					t.Fatalf("commit sequence = %d, want %d", got, sequence)
				}
				if committed := store.eventCount(); committed != sequence {
					t.Fatalf("store exposed %d events before transaction commit, want %d", committed, sequence)
				}
				return nil
			},
		})
		if err != nil {
			t.Fatalf("publish sequence %d: %v", sequence, err)
		}
		if event.Durable == nil || event.Durable.AggregateID != "fixture-1" ||
			event.Durable.Sequence != int64(sequence) || event.Durable.Version != 2 {
			t.Fatalf("published durable event = %+v, want aggregate fixture-1 sequence %d version 2", event, sequence)
		}
	}

	wantOrder := []string{"projector", "commit", "listener", "projector", "commit", "listener"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("execution order = %v, want %v", order, wantOrder)
	}
	records := store.eventsFor("fixture-1")
	if len(records) != 2 || records[0].Sequence != 0 || records[1].Sequence != 1 ||
		records[0].Type != "fixture.changed.2" || records[1].Type != "fixture.changed.2" {
		t.Fatalf("stored records = %+v", records)
	}
}

func TestPublishRollsBackOnProjectorOrCommitFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		projector error
		commit    error
	}{
		{name: "projector", projector: errors.New("projector failed")},
		{name: "commit", commit: errors.New("commit failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStore()
			service, err := NewService(store, fixedEventIDs("evt_failure"))
			if err != nil {
				t.Fatalf("new service: %v", err)
			}
			definition := Definition{
				Type:    "fixture.changed",
				Durable: &DurableDefinition{Version: 1, AggregateField: "fixtureID"},
			}
			if err := service.Project(definition, func(context.Context, Transaction, domain.EventEnvelope) error {
				return test.projector
			}); err != nil {
				t.Fatalf("register projector: %v", err)
			}
			notified := false
			_, err = service.Listen(func(domain.EventEnvelope) { notified = true })
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			_, err = service.Publish(context.Background(), definition, durableFixtureData("fixture-1", "failed"), PublishOptions{
				Commit: func(context.Context, Transaction, int64) error { return test.commit },
			})
			want := test.projector
			if want == nil {
				want = test.commit
			}
			if !errors.Is(err, want) {
				t.Fatalf("publish error = %v, want %v", err, want)
			}
			if store.eventCount() != 0 || store.sequenceCount() != 0 || notified {
				t.Fatalf("failed publish leaked state: events=%d sequences=%d notified=%v",
					store.eventCount(), store.sequenceCount(), notified)
			}
		})
	}
}

func TestConcurrentPublishAllocatesContiguousAggregateSequences(t *testing.T) {
	store := newMemoryStore()
	const count = 100
	ids := make([]domain.EventID, count)
	for index := range count {
		ids[index] = domain.EventID(fmt.Sprintf("evt_concurrent_%03d", index))
	}
	service, err := NewService(store, fixedEventIDs(ids...))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	definition := Definition{
		Type:    "fixture.changed",
		Durable: &DurableDefinition{Version: 1, AggregateField: "fixtureID"},
	}
	start := make(chan struct{})
	errs := make(chan error, count)
	var wg sync.WaitGroup
	wg.Add(count)
	for index := range count {
		go func() {
			defer wg.Done()
			<-start
			_, err := service.Publish(context.Background(), definition,
				durableFixtureData("fixture-concurrent", fmt.Sprintf("event-%d", index)), PublishOptions{})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent publish: %v", err)
		}
	}
	records := store.eventsFor("fixture-concurrent")
	if len(records) != count {
		t.Fatalf("stored event count = %d, want %d", len(records), count)
	}
	for index, record := range records {
		if record.Sequence != int64(index) {
			t.Fatalf("record %d sequence = %d, want %d", index, record.Sequence, index)
		}
	}
}

func TestPublishRejectsConflictsAndInvalidContractsWithoutMutation(t *testing.T) {
	t.Parallel()

	definition := Definition{
		Type:    "fixture.changed",
		Durable: &DurableDefinition{Version: 1, AggregateField: "fixtureID"},
	}
	store := newMemoryStore()
	service, err := NewService(store, fixedEventIDs("evt_generated"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if _, err := service.Publish(context.Background(), definition, durableFixtureData("fixture-1", "first"),
		(PublishOptions{ID: "evt_duplicate"})); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	_, err = service.Publish(context.Background(), definition, durableFixtureData("fixture-1", "duplicate"),
		PublishOptions{ID: "evt_duplicate"})
	if !errors.Is(err, ErrEventConflict) {
		t.Fatalf("duplicate event error = %v, want ErrEventConflict", err)
	}
	if records := store.eventsFor("fixture-1"); len(records) != 1 || records[0].Sequence != 0 {
		t.Fatalf("duplicate event mutated history: %+v", records)
	}

	tests := []struct {
		name       string
		definition Definition
		data       domain.JSONValue
		options    PublishOptions
		want       error
	}{
		{name: "empty type", definition: Definition{}, data: durableFixtureData("fixture-2", "bad"), want: ErrInvalidDefinition},
		{name: "zero version", definition: Definition{Type: "bad", Durable: &DurableDefinition{AggregateField: "fixtureID"}}, data: durableFixtureData("fixture-2", "bad"), want: ErrInvalidDefinition},
		{name: "missing aggregate", definition: definition, data: domain.JSONObject(map[string]domain.JSONValue{"text": domain.JSONString("bad")}), want: ErrInvalidEvent},
		{name: "non-object", definition: definition, data: domain.JSONString("bad"), want: ErrInvalidEvent},
		{name: "live commit", definition: Definition{Type: "fixture.live"}, data: domain.JSONObject(map[string]domain.JSONValue{}), options: PublishOptions{Commit: func(context.Context, Transaction, int64) error { return nil }}, want: ErrInvalidEvent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.Publish(context.Background(), test.definition, test.data, test.options)
			if !errors.Is(err, test.want) {
				t.Fatalf("publish error = %v, want %v", err, test.want)
			}
		})
	}
	if store.eventCount() != 1 || store.sequenceCount() != 1 {
		t.Fatalf("invalid publishes mutated store: events=%d sequences=%d", store.eventCount(), store.sequenceCount())
	}
}

func TestReplayIsExactIdempotentAndFencesOwners(t *testing.T) {
	t.Parallel()

	definition := Definition{
		Type:    "fixture.changed",
		Durable: &DurableDefinition{Version: 1, AggregateField: "fixtureID"},
	}
	store := newMemoryStore()
	service, err := NewService(store, fixedEventIDs("evt_unused"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	projected := 0
	if err := service.Project(definition, func(context.Context, Transaction, domain.EventEnvelope) error {
		projected++
		return nil
	}); err != nil {
		t.Fatalf("register projector: %v", err)
	}
	notified := 0
	_, err = service.Listen(func(domain.EventEnvelope) { notified++ })
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	replayed := domain.EventEnvelope{
		ID: "evt_replayed", Type: definition.Type,
		Data:    durableFixtureData("fixture-replay", "first"),
		Durable: &domain.EventDurable{AggregateID: "fixture-replay", Sequence: 0, Version: 1},
	}
	inserted, err := service.Replay(context.Background(), definition, replayed, ReplayOptions{
		OwnerID: "owner-a", StrictOwner: true, Publish: true,
	})
	if err != nil || !inserted {
		t.Fatalf("initial replay inserted=%v error=%v", inserted, err)
	}
	inserted, err = service.Replay(context.Background(), definition, replayed, ReplayOptions{
		OwnerID: "owner-a", StrictOwner: true, Publish: true,
	})
	if err != nil || inserted {
		t.Fatalf("exact replay inserted=%v error=%v", inserted, err)
	}
	if projected != 1 || notified != 1 || store.eventCount() != 1 {
		t.Fatalf("exact replay duplicated effects: projected=%d notified=%d events=%d", projected, notified, store.eventCount())
	}

	divergent := replayed
	divergent.Data = durableFixtureData("fixture-replay", "divergent")
	if _, err := service.Replay(context.Background(), definition, divergent, ReplayOptions{OwnerID: "owner-a"}); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("divergent replay error = %v, want ErrReplayConflict", err)
	}
	hole := replayed
	hole.ID = "evt_hole"
	hole.Durable = &domain.EventDurable{AggregateID: "fixture-replay", Sequence: 2, Version: 1}
	if _, err := service.Replay(context.Background(), definition, hole, ReplayOptions{OwnerID: "owner-a"}); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("sequence hole error = %v, want ErrSequenceConflict", err)
	}
	next := replayed
	next.ID = "evt_next"
	next.Data = durableFixtureData("fixture-replay", "next")
	next.Durable = &domain.EventDurable{AggregateID: "fixture-replay", Sequence: 1, Version: 1}
	if _, err := service.Replay(context.Background(), definition, next, ReplayOptions{
		OwnerID: "owner-b", StrictOwner: true,
	}); !errors.Is(err, ErrOwnerConflict) {
		t.Fatalf("strict owner error = %v, want ErrOwnerConflict", err)
	}
	inserted, err = service.Replay(context.Background(), definition, next, ReplayOptions{OwnerID: "owner-b"})
	if err != nil || inserted {
		t.Fatalf("non-strict foreign owner replay inserted=%v error=%v", inserted, err)
	}
	if projected != 1 || store.eventCount() != 1 {
		t.Fatalf("rejected replay mutated state: projected=%d events=%d", projected, store.eventCount())
	}
}

func TestReplayRejectsEnvelopeDefinitionAndEventIDConflicts(t *testing.T) {
	t.Parallel()

	definition := Definition{
		Type:    "fixture.changed",
		Durable: &DurableDefinition{Version: 1, AggregateField: "fixtureID"},
	}
	store := newMemoryStore()
	service, err := NewService(store, fixedEventIDs("evt_seed"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if _, err := service.Publish(context.Background(), definition, durableFixtureData("fixture-one", "seed"), PublishOptions{}); err != nil {
		t.Fatalf("seed publish: %v", err)
	}
	tests := []struct {
		name  string
		event domain.EventEnvelope
		want  error
	}{
		{name: "live envelope", event: domain.EventEnvelope{ID: "evt_live", Type: definition.Type, Data: durableFixtureData("fixture-two", "bad")}, want: ErrInvalidEvent},
		{name: "type mismatch", event: domain.EventEnvelope{ID: "evt_type", Type: "other", Data: durableFixtureData("fixture-two", "bad"), Durable: &domain.EventDurable{AggregateID: "fixture-two", Sequence: 0, Version: 1}}, want: ErrInvalidEvent},
		{name: "version mismatch", event: domain.EventEnvelope{ID: "evt_version", Type: definition.Type, Data: durableFixtureData("fixture-two", "bad"), Durable: &domain.EventDurable{AggregateID: "fixture-two", Sequence: 0, Version: 2}}, want: ErrInvalidEvent},
		{name: "aggregate mismatch", event: domain.EventEnvelope{ID: "evt_aggregate", Type: definition.Type, Data: durableFixtureData("fixture-two", "bad"), Durable: &domain.EventDurable{AggregateID: "other", Sequence: 0, Version: 1}}, want: ErrInvalidEvent},
		{name: "event ID reuse", event: domain.EventEnvelope{ID: "evt_seed", Type: definition.Type, Data: durableFixtureData("fixture-two", "bad"), Durable: &domain.EventDurable{AggregateID: "fixture-two", Sequence: 0, Version: 1}}, want: ErrEventConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.Replay(context.Background(), definition, test.event, ReplayOptions{}); !errors.Is(err, test.want) {
				t.Fatalf("replay error = %v, want %v", err, test.want)
			}
		})
	}
	if store.eventCount() != 1 || store.sequenceCount() != 1 {
		t.Fatalf("invalid replay mutated store: events=%d sequences=%d", store.eventCount(), store.sequenceCount())
	}
}

func TestBoundedSubscribersOverflowInIsolationWithoutBlockingPublish(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	service, err := NewService(store, fixedEventIDs("evt_one", "evt_two", "evt_three", "evt_four"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	overflowed, err := service.All(2)
	if err != nil {
		t.Fatalf("subscribe overflow fixture: %v", err)
	}
	defer overflowed.Close()
	healthy, err := service.All(4)
	if err != nil {
		t.Fatalf("subscribe healthy fixture: %v", err)
	}
	defer healthy.Close()
	definition := Definition{Type: "fixture.live"}
	for index := range 4 {
		if _, err := service.Publish(context.Background(), definition, domain.JSONObject(map[string]domain.JSONValue{
			"index": domain.JSONNumber(fmt.Sprintf("%d", index)),
		}), PublishOptions{}); err != nil {
			t.Fatalf("publish %d: %v", index, err)
		}
	}

	var overflowedIDs []domain.EventID
	for event := range overflowed.Events() {
		overflowedIDs = append(overflowedIDs, event.ID)
	}
	if !reflect.DeepEqual(overflowedIDs, []domain.EventID{"evt_one", "evt_two"}) ||
		!errors.Is(overflowed.Err(), ErrSubscriberOverflow) {
		t.Fatalf("overflowed subscriber events/error = %v/%v", overflowedIDs, overflowed.Err())
	}
	var healthyIDs []domain.EventID
	for range 4 {
		select {
		case event := <-healthy.Events():
			healthyIDs = append(healthyIDs, event.ID)
		case <-time.After(time.Second):
			t.Fatal("healthy subscriber did not receive every event")
		}
	}
	if !reflect.DeepEqual(healthyIDs, []domain.EventID{"evt_one", "evt_two", "evt_three", "evt_four"}) || healthy.Err() != nil {
		t.Fatalf("healthy subscriber events/error = %v/%v", healthyIDs, healthy.Err())
	}
	if _, err := service.All(0); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("zero capacity error = %v, want ErrInvalidEvent", err)
	}
}

func TestTypedSubscriptionFiltersUnrelatedEventsWithoutConsumingCapacity(t *testing.T) {
	t.Parallel()

	service, err := NewService(newMemoryStore(), fixedEventIDs("evt_other_one", "evt_other_two", "evt_target"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	target := Definition{Type: "fixture.target"}
	subscription, err := service.Subscribe(target, 1)
	if err != nil {
		t.Fatalf("subscribe typed: %v", err)
	}
	defer subscription.Close()
	for _, text := range []string{"one", "two"} {
		if _, err := service.Publish(context.Background(), Definition{Type: "fixture.other"},
			domain.JSONObject(map[string]domain.JSONValue{"text": domain.JSONString(text)}), PublishOptions{}); err != nil {
			t.Fatalf("publish unrelated %q: %v", text, err)
		}
	}
	published, err := service.Publish(context.Background(), target,
		domain.JSONObject(map[string]domain.JSONValue{"text": domain.JSONString("target")}), PublishOptions{})
	if err != nil {
		t.Fatalf("publish target: %v", err)
	}
	select {
	case received := <-subscription.Events():
		if !reflect.DeepEqual(received, published) {
			t.Fatalf("typed subscription event = %+v, want %+v", received, published)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("typed subscription did not receive matching event")
	}
	if subscription.Err() != nil {
		t.Fatalf("typed subscription error = %v", subscription.Err())
	}
}

func TestDurableSubscriptionRetainsCommitAcrossHistoryLiveHandoff(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	service, err := NewService(store, fixedEventIDs("evt_zero", "evt_handoff", "evt_live"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	definition := Definition{
		Type:    "fixture.changed",
		Durable: &DurableDefinition{Version: 1, AggregateField: "fixtureID"},
	}
	if _, err := service.Publish(context.Background(), definition,
		durableFixtureData("fixture-stream", "zero"), PublishOptions{}); err != nil {
		t.Fatalf("seed history: %v", err)
	}
	readStarted := make(chan struct{})
	continueRead := make(chan struct{})
	var pause sync.Once
	store.historyHook = func() {
		pause.Do(func() {
			close(readStarted)
			<-continueRead
		})
	}
	subscription, err := service.Durable(context.Background(), DurableInput{
		AggregateID: "fixture-stream", After: 0, Capacity: 1, BatchSize: 1,
	})
	if err != nil {
		t.Fatalf("durable subscribe: %v", err)
	}
	defer subscription.Close()
	select {
	case <-readStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("initial history read did not start")
	}
	if _, err := service.Publish(context.Background(), definition,
		durableFixtureData("fixture-stream", "handoff"), PublishOptions{}); err != nil {
		t.Fatalf("publish during handoff: %v", err)
	}
	close(continueRead)
	select {
	case event := <-subscription.Events():
		if event.ID != "evt_handoff" || event.Durable == nil || event.Durable.Sequence != 1 {
			t.Fatalf("handoff event = %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("event committed during handoff was lost")
	}
	if _, err := service.Publish(context.Background(), definition,
		durableFixtureData("fixture-stream", "live"), PublishOptions{}); err != nil {
		t.Fatalf("publish live tail: %v", err)
	}
	select {
	case event := <-subscription.Events():
		if event.ID != "evt_live" || event.Durable == nil || event.Durable.Sequence != 2 {
			t.Fatalf("live event = %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("live durable event was not drained")
	}
	if subscription.Err() != nil {
		t.Fatalf("durable subscription error = %v", subscription.Err())
	}
}

func TestDefinitionRegistryRejectsConflictingExactDefinitionBeforeGeneratingID(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	service, err := NewService(store, fixedEventIDs("evt_valid"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	definition := Definition{
		Type:    "fixture.changed",
		Durable: &DurableDefinition{Version: 1, AggregateField: "fixtureID"},
	}
	if err := service.Project(definition, func(context.Context, Transaction, domain.EventEnvelope) error { return nil }); err != nil {
		t.Fatalf("register definition: %v", err)
	}
	conflicting := Definition{
		Type:    "fixture.changed",
		Durable: &DurableDefinition{Version: 1, AggregateField: "otherID"},
	}
	_, err = service.Publish(context.Background(), conflicting, domain.JSONObject(map[string]domain.JSONValue{
		"otherID": domain.JSONString("fixture-1"),
	}), PublishOptions{})
	if !errors.Is(err, ErrInvalidDefinition) {
		t.Fatalf("conflicting definition error = %v, want ErrInvalidDefinition", err)
	}
	if store.eventCount() != 0 {
		t.Fatalf("conflicting definition stored %d events", store.eventCount())
	}
	if store.writeCount() != 0 {
		t.Fatalf("conflicting definition entered %d store transactions", store.writeCount())
	}
	event, err := service.Publish(context.Background(), definition, durableFixtureData("fixture-1", "valid"), PublishOptions{})
	if err != nil {
		t.Fatalf("publish valid definition: %v", err)
	}
	if event.ID != "evt_valid" {
		t.Fatalf("valid event ID = %q, want unconsumed evt_valid", event.ID)
	}
	whitespace := Definition{
		Type:    "fixture.whitespace",
		Durable: &DurableDefinition{Version: 1, AggregateField: " fixtureID "},
	}
	if _, err := service.Publish(context.Background(), whitespace, durableFixtureData("fixture-2", "bad"), PublishOptions{}); !errors.Is(err, ErrInvalidDefinition) {
		t.Fatalf("whitespace aggregate field error = %v, want ErrInvalidDefinition", err)
	}
}

func TestDurableObserverDefectIsIsolatedAndReportedAfterCommit(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	service, err := NewService(store, fixedEventIDs("evt_observed"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	var reported error
	if err := service.SetObserverFailureHandler(func(err error) { reported = err }); err != nil {
		t.Fatalf("set observer failure handler: %v", err)
	}
	order := make([]string, 0, 2)
	_, err = service.Listen(func(event domain.EventEnvelope) {
		order = append(order, "defect")
		event.Data.Object["text"] = domain.JSONString("mutated")
		panic("listener failed")
	})
	if err != nil {
		t.Fatalf("listen defect: %v", err)
	}
	_, err = service.Listen(func(event domain.EventEnvelope) {
		order = append(order, event.Data.Object["text"].String)
	})
	if err != nil {
		t.Fatalf("listen healthy: %v", err)
	}
	definition := Definition{
		Type:    "fixture.changed",
		Durable: &DurableDefinition{Version: 1, AggregateField: "fixtureID"},
	}
	event, err := service.Publish(context.Background(), definition, durableFixtureData("fixture-1", "original"), PublishOptions{})
	if err != nil {
		t.Fatalf("publish with observer defect: %v", err)
	}
	if store.eventCount() != 1 || event.Data.Object["text"].String != "original" {
		t.Fatalf("observer defect changed committed/returned event: count=%d event=%+v", store.eventCount(), event)
	}
	if !reflect.DeepEqual(order, []string{"defect", "original"}) {
		t.Fatalf("observer order/data = %v", order)
	}
	if !errors.Is(reported, ErrObserverFailure) || !strings.Contains(reported.Error(), "listener failed") {
		t.Fatalf("reported observer failure = %v", reported)
	}
	if err := service.SetObserverFailureHandler(nil); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("nil observer handler error = %v, want ErrInvalidEvent", err)
	}
}

func TestDefinitionRegistryFreezesRegisteredDefinition(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	service, err := NewService(store, fixedEventIDs("evt_frozen"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	durable := &DurableDefinition{Version: 1, AggregateField: "fixtureID"}
	if err := service.Project(Definition{Type: "fixture.changed", Durable: durable},
		func(context.Context, Transaction, domain.EventEnvelope) error { return nil }); err != nil {
		t.Fatalf("register definition: %v", err)
	}
	durable.AggregateField = "mutatedByCaller"
	event, err := service.Publish(context.Background(), Definition{
		Type: "fixture.changed", Durable: &DurableDefinition{Version: 1, AggregateField: "fixtureID"},
	}, durableFixtureData("fixture-1", "frozen"), PublishOptions{})
	if err != nil {
		t.Fatalf("publish with independently frozen definition: %v", err)
	}
	if event.ID != "evt_frozen" || store.eventCount() != 1 {
		t.Fatalf("frozen definition publish = %+v, events=%d", event, store.eventCount())
	}
}

func TestObserverFailureHandlerPanicDoesNotBlockSubscribers(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	service, err := NewService(store, fixedEventIDs("evt_after_observer_failure"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if err := service.SetObserverFailureHandler(func(error) { panic("failure handler failed") }); err != nil {
		t.Fatalf("set observer failure handler: %v", err)
	}
	if _, err := service.Listen(func(domain.EventEnvelope) { panic("listener failed") }); err != nil {
		t.Fatalf("listen defect: %v", err)
	}
	live, err := service.All(1)
	if err != nil {
		t.Fatalf("subscribe live: %v", err)
	}
	defer live.Close()
	durable, err := service.Durable(context.Background(), DurableInput{
		AggregateID: "fixture-1", After: -1, Capacity: 1, BatchSize: 1,
	})
	if err != nil {
		t.Fatalf("subscribe durable: %v", err)
	}
	defer durable.Close()
	definition := Definition{
		Type: "fixture.changed", Durable: &DurableDefinition{Version: 1, AggregateField: "fixtureID"},
	}
	published, err := service.Publish(context.Background(), definition,
		durableFixtureData("fixture-1", "committed"), PublishOptions{})
	if err != nil {
		t.Fatalf("publish after observer failures: %v", err)
	}
	for name, events := range map[string]<-chan domain.EventEnvelope{
		"live": live.Events(), "durable": durable.Events(),
	} {
		select {
		case received := <-events:
			if received.ID != published.ID {
				t.Fatalf("%s subscriber event = %+v, want ID %q", name, received, published.ID)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s subscriber was blocked by observer failures", name)
		}
	}
}

func TestDurableSubscriptionRejectsHistoryDefectsAndCleansWake(t *testing.T) {
	t.Parallel()

	historyFailure := errors.New("history failed")
	valid := StoredEvent{
		ID: "evt_history", AggregateID: "fixture-history", Sequence: 0,
		Type: "fixture.changed.1", Data: []byte(`{"fixtureID":"fixture-history","text":"valid"}`),
	}
	tests := []struct {
		name       string
		record     StoredEvent
		historyErr error
		want       error
	}{
		{name: "sequence gap", record: func() StoredEvent { record := valid; record.Sequence = 1; return record }(), want: ErrSequenceConflict},
		{name: "invalid event ID", record: func() StoredEvent { record := valid; record.ID = ""; return record }(), want: ErrInvalidEvent},
		{name: "unversioned type", record: func() StoredEvent { record := valid; record.Type = "fixture.changed"; return record }(), want: ErrInvalidEvent},
		{name: "malformed JSON", record: func() StoredEvent { record := valid; record.Data = []byte(`{`); return record }(), want: ErrInvalidEvent},
		{name: "non-object JSON", record: func() StoredEvent { record := valid; record.Data = []byte(`"invalid"`); return record }(), want: ErrInvalidEvent},
		{name: "store failure", historyErr: historyFailure, want: historyFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStore()
			if test.record.ID != "" || test.record.Type != "" || test.record.Data != nil {
				store.events = []StoredEvent{test.record}
			}
			store.historyErr = test.historyErr
			service, err := NewService(store, fixedEventIDs("evt_unused"))
			if err != nil {
				t.Fatalf("new service: %v", err)
			}
			subscription, err := service.Durable(context.Background(), DurableInput{
				AggregateID: "fixture-history", After: -1, Capacity: 1, BatchSize: 1,
			})
			if err != nil {
				t.Fatalf("subscribe durable: %v", err)
			}
			waitDurableClosed(t, subscription)
			if !errors.Is(subscription.Err(), test.want) {
				t.Fatalf("durable error = %v, want %v", subscription.Err(), test.want)
			}
			assertNoDurableWakes(t, service)
		})
	}
}

func TestDurableSubscriptionCancellationCleansWake(t *testing.T) {
	t.Parallel()

	service, err := NewService(newMemoryStore(), fixedEventIDs("evt_unused"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	subscription, err := service.Durable(ctx, DurableInput{
		AggregateID: "fixture-cancel", After: -1, Capacity: 1, BatchSize: 1,
	})
	if err != nil {
		t.Fatalf("subscribe durable: %v", err)
	}
	cancel()
	waitDurableClosed(t, subscription)
	if !errors.Is(subscription.Err(), context.Canceled) {
		t.Fatalf("durable cancellation error = %v, want context.Canceled", subscription.Err())
	}
	assertNoDurableWakes(t, service)
}

func TestPublishTransactionFaultsNeverExposeHalfState(t *testing.T) {
	t.Parallel()

	failure := errors.New("injected transaction failure")
	for _, point := range []string{"EventByID", "Sequence", "PutSequence", "InsertEvent", "Commit"} {
		t.Run(point, func(t *testing.T) {
			store := newMemoryStore()
			if point == "Commit" {
				store.commitErr = failure
			} else {
				store.faultAt = point
				store.faultErr = failure
			}
			service, err := NewService(store, fixedEventIDs("evt_fault"))
			if err != nil {
				t.Fatalf("new service: %v", err)
			}
			notified := false
			if _, err := service.Listen(func(domain.EventEnvelope) { notified = true }); err != nil {
				t.Fatalf("listen: %v", err)
			}
			definition := Definition{
				Type: "fixture.changed", Durable: &DurableDefinition{Version: 1, AggregateField: "fixtureID"},
			}
			_, err = service.Publish(context.Background(), definition,
				durableFixtureData("fixture-fault", "fault"), PublishOptions{})
			if !errors.Is(err, failure) {
				t.Fatalf("publish error = %v, want injected failure", err)
			}
			if store.eventCount() != 0 || store.sequenceCount() != 0 || notified {
				t.Fatalf("fault %s leaked state: events=%d sequences=%d notified=%v",
					point, store.eventCount(), store.sequenceCount(), notified)
			}
		})
	}
}

func TestReplayTransactionFaultsNeverExposeHalfState(t *testing.T) {
	t.Parallel()

	failure := errors.New("injected replay transaction failure")
	for _, point := range []string{"Sequence", "EventByID", "PutSequence", "InsertEvent", "Commit"} {
		t.Run(point, func(t *testing.T) {
			store := newMemoryStore()
			if point == "Commit" {
				store.commitErr = failure
			} else {
				store.faultAt = point
				store.faultErr = failure
			}
			service, err := NewService(store, fixedEventIDs("evt_unused"))
			if err != nil {
				t.Fatalf("new service: %v", err)
			}
			notified := false
			if _, err := service.Listen(func(domain.EventEnvelope) { notified = true }); err != nil {
				t.Fatalf("listen: %v", err)
			}
			definition := Definition{
				Type: "fixture.changed", Durable: &DurableDefinition{Version: 1, AggregateField: "fixtureID"},
			}
			replayed := domain.EventEnvelope{
				ID: "evt_replay_fault", Type: definition.Type,
				Data:    durableFixtureData("fixture-replay-fault", "fault"),
				Durable: &domain.EventDurable{AggregateID: "fixture-replay-fault", Sequence: 0, Version: 1},
			}
			_, err = service.Replay(context.Background(), definition, replayed, ReplayOptions{Publish: true})
			if !errors.Is(err, failure) {
				t.Fatalf("replay error = %v, want injected failure", err)
			}
			if store.eventCount() != 0 || store.sequenceCount() != 0 || notified {
				t.Fatalf("fault %s leaked state: events=%d sequences=%d notified=%v",
					point, store.eventCount(), store.sequenceCount(), notified)
			}
		})
	}
}

func TestReplayAllAcceptsContiguousChunksAndRejectsInvalidBatchBeforeMutation(t *testing.T) {
	t.Parallel()

	definition := Definition{
		Type: "fixture.changed", Durable: &DurableDefinition{Version: 1, AggregateField: "fixtureID"},
	}
	entry := func(id domain.EventID, aggregate string, sequence int64, text string) ReplayEntry {
		return ReplayEntry{Definition: definition, Event: domain.EventEnvelope{
			ID: id, Type: definition.Type, Data: durableFixtureData(aggregate, text),
			Durable: &domain.EventDurable{AggregateID: aggregate, Sequence: sequence, Version: 1},
		}}
	}
	store := newMemoryStore()
	service, err := NewService(store, fixedEventIDs("evt_unused"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	source, err := service.ReplayAll(context.Background(), []ReplayEntry{
		entry("evt_batch_zero", "fixture-batch", 0, "zero"),
		entry("evt_batch_one", "fixture-batch", 1, "one"),
	}, ReplayOptions{})
	if err != nil || source != "fixture-batch" {
		t.Fatalf("initial replay all source/error = %q/%v", source, err)
	}
	source, err = service.ReplayAll(context.Background(), []ReplayEntry{
		entry("evt_batch_two", "fixture-batch", 2, "two"),
	}, ReplayOptions{})
	if err != nil || source != "fixture-batch" || store.eventCount() != 3 {
		t.Fatalf("later replay chunk source/error/events = %q/%v/%d", source, err, store.eventCount())
	}

	for _, test := range []struct {
		name    string
		entries []ReplayEntry
		want    error
	}{
		{name: "mixed aggregates", entries: []ReplayEntry{
			entry("evt_mixed_zero", "fixture-mixed", 0, "zero"),
			entry("evt_mixed_one", "fixture-other", 1, "one"),
		}, want: ErrInvalidEvent},
		{name: "sequence gap", entries: []ReplayEntry{
			entry("evt_gap_zero", "fixture-gap", 0, "zero"),
			entry("evt_gap_two", "fixture-gap", 2, "two"),
		}, want: ErrSequenceConflict},
		{name: "conflicting exact definitions", entries: []ReplayEntry{
			entry("evt_definition_zero", "fixture-definition", 0, "zero"),
			{
				Definition: Definition{
					Type:    "fixture.changed",
					Durable: &DurableDefinition{Version: 1, AggregateField: "otherID"},
				},
				Event: domain.EventEnvelope{
					ID: "evt_definition_one", Type: "fixture.changed",
					Data: domain.JSONObject(map[string]domain.JSONValue{
						"otherID": domain.JSONString("fixture-definition"),
						"text":    domain.JSONString("one"),
					}),
					Durable: &domain.EventDurable{AggregateID: "fixture-definition", Sequence: 1, Version: 1},
				},
			},
		}, want: ErrInvalidDefinition},
	} {
		t.Run(test.name, func(t *testing.T) {
			isolatedStore := newMemoryStore()
			isolated, err := NewService(isolatedStore, fixedEventIDs("evt_unused"))
			if err != nil {
				t.Fatalf("new service: %v", err)
			}
			if _, err := isolated.ReplayAll(context.Background(), test.entries, ReplayOptions{}); !errors.Is(err, test.want) {
				t.Fatalf("replay all error = %v, want %v", err, test.want)
			}
			if isolatedStore.eventCount() != 0 || isolatedStore.sequenceCount() != 0 || isolatedStore.writeCount() != 0 {
				t.Fatalf("invalid batch mutated store: events=%d sequences=%d writes=%d",
					isolatedStore.eventCount(), isolatedStore.sequenceCount(), isolatedStore.writeCount())
			}
		})
	}
	empty, err := service.ReplayAll(context.Background(), nil, ReplayOptions{})
	if err != nil || empty != "" {
		t.Fatalf("empty replay all source/error = %q/%v", empty, err)
	}
}

func TestClaimFencesReplayCanTransferOwnerAndDoesNotCreateMissingAggregate(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	service, err := NewService(store, fixedEventIDs("evt_claim_seed"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	definition := Definition{
		Type: "fixture.changed", Durable: &DurableDefinition{Version: 1, AggregateField: "fixtureID"},
	}
	if _, err := service.Publish(context.Background(), definition,
		durableFixtureData("fixture-claim", "seed"), PublishOptions{}); err != nil {
		t.Fatalf("seed aggregate: %v", err)
	}
	if err := service.Claim(context.Background(), "fixture-claim", "owner-a"); err != nil {
		t.Fatalf("claim owner-a: %v", err)
	}
	ignored := domain.EventEnvelope{
		ID: "evt_claim_ignored", Type: definition.Type,
		Data:    durableFixtureData("fixture-claim", "ignored"),
		Durable: &domain.EventDurable{AggregateID: "fixture-claim", Sequence: 1, Version: 1},
	}
	inserted, err := service.Replay(context.Background(), definition, ignored, ReplayOptions{OwnerID: "owner-b"})
	if err != nil || inserted || store.eventCount() != 1 {
		t.Fatalf("foreign replay inserted/error/events = %v/%v/%d", inserted, err, store.eventCount())
	}
	if err := service.Claim(context.Background(), "fixture-claim", "owner-b"); err != nil {
		t.Fatalf("transfer claim: %v", err)
	}
	inserted, err = service.Replay(context.Background(), definition, ignored, ReplayOptions{OwnerID: "owner-b"})
	if err != nil || !inserted || store.eventCount() != 2 {
		t.Fatalf("transferred replay inserted/error/events = %v/%v/%d", inserted, err, store.eventCount())
	}
	if state := store.sequenceFor("fixture-claim"); state != (SequenceState{Latest: 1, OwnerID: "owner-b"}) {
		t.Fatalf("transferred sequence = %+v", state)
	}
	before := store.sequenceCount()
	if err := service.Claim(context.Background(), "fixture-missing", "owner-a"); err != nil {
		t.Fatalf("claim missing aggregate: %v", err)
	}
	if store.sequenceCount() != before {
		t.Fatalf("claim created missing aggregate: before=%d after=%d", before, store.sequenceCount())
	}
}

func TestRemoveClearsAggregateSoReplayCanRestartAtZero(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	service, err := NewService(store, fixedEventIDs("evt_remove_seed"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	definition := Definition{
		Type: "fixture.changed", Durable: &DurableDefinition{Version: 1, AggregateField: "fixtureID"},
	}
	if _, err := service.Publish(context.Background(), definition,
		durableFixtureData("fixture-remove", "seed"), PublishOptions{}); err != nil {
		t.Fatalf("seed aggregate: %v", err)
	}
	if err := service.Remove(context.Background(), "fixture-remove"); err != nil {
		t.Fatalf("remove aggregate: %v", err)
	}
	if store.eventCount() != 0 || store.sequenceCount() != 0 {
		t.Fatalf("remove retained state: events=%d sequences=%d", store.eventCount(), store.sequenceCount())
	}
	replayed := domain.EventEnvelope{
		ID: "evt_remove_replay", Type: definition.Type,
		Data:    durableFixtureData("fixture-remove", "replayed"),
		Durable: &domain.EventDurable{AggregateID: "fixture-remove", Sequence: 0, Version: 1},
	}
	inserted, err := service.Replay(context.Background(), definition, replayed, ReplayOptions{})
	if err != nil || !inserted {
		t.Fatalf("replay after remove inserted/error = %v/%v", inserted, err)
	}
}

func TestClaimAndRemoveValidateInputsAndRollbackEveryFault(t *testing.T) {
	t.Parallel()

	validationStore := newMemoryStore()
	validationService, err := NewService(validationStore, fixedEventIDs("evt_unused"))
	if err != nil {
		t.Fatalf("new validation service: %v", err)
	}
	for _, call := range []struct {
		name string
		run  func() error
	}{
		{name: "empty claim aggregate", run: func() error {
			return validationService.Claim(context.Background(), "", "owner")
		}},
		{name: "whitespace claim owner", run: func() error {
			return validationService.Claim(context.Background(), "fixture", " owner ")
		}},
		{name: "whitespace remove aggregate", run: func() error {
			return validationService.Remove(context.Background(), " fixture ")
		}},
	} {
		t.Run(call.name, func(t *testing.T) {
			if err := call.run(); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("validation error = %v, want ErrInvalidEvent", err)
			}
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validationService.Claim(canceled, "fixture", "owner"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled claim error = %v, want context.Canceled", err)
	}
	if validationStore.writeCount() != 0 {
		t.Fatalf("invalid operations entered %d transactions", validationStore.writeCount())
	}

	failure := errors.New("claim/remove fault")
	for _, operation := range []string{"claim", "remove"} {
		points := []string{"Commit"}
		if operation == "claim" {
			points = append([]string{"Sequence", "PutSequence"}, points...)
		} else {
			points = append([]string{"DeleteAggregate"}, points...)
		}
		for _, point := range points {
			t.Run(operation+"/"+point, func(t *testing.T) {
				store := newMemoryStore()
				store.sequences["fixture-fault"] = SequenceState{Latest: 0, OwnerID: "owner-before"}
				store.events = []StoredEvent{{
					ID: "evt_management_fault", AggregateID: "fixture-fault", Sequence: 0,
					Type: "fixture.changed.1", Data: []byte(`{"fixtureID":"fixture-fault"}`),
				}}
				if point == "Commit" {
					store.commitErr = failure
				} else {
					store.faultAt = point
					store.faultErr = failure
				}
				service, err := NewService(store, fixedEventIDs("evt_unused"))
				if err != nil {
					t.Fatalf("new service: %v", err)
				}
				if operation == "claim" {
					err = service.Claim(context.Background(), "fixture-fault", "owner-after")
				} else {
					err = service.Remove(context.Background(), "fixture-fault")
				}
				if !errors.Is(err, failure) {
					t.Fatalf("%s %s error = %v, want injected failure", operation, point, err)
				}
				if state := store.sequenceFor("fixture-fault"); state != (SequenceState{Latest: 0, OwnerID: "owner-before"}) {
					t.Fatalf("%s %s mutated sequence: %+v", operation, point, state)
				}
				if store.eventCount() != 1 {
					t.Fatalf("%s %s mutated events: %d", operation, point, store.eventCount())
				}
			})
		}
	}
}

func TestExactReplayEventLookupFailureDoesNotClaimOwner(t *testing.T) {
	t.Parallel()

	failure := errors.New("injected EventAt failure")
	store := newMemoryStore()
	store.sequences["fixture-exact"] = SequenceState{Latest: 0}
	store.events = []StoredEvent{{
		ID: "evt_exact", AggregateID: "fixture-exact", Sequence: 0,
		Type: "fixture.changed.1", Data: []byte(`{"fixtureID":"fixture-exact","text":"exact"}`),
	}}
	store.faultAt = "EventAt"
	store.faultErr = failure
	service, err := NewService(store, fixedEventIDs("evt_unused"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	definition := Definition{
		Type: "fixture.changed", Durable: &DurableDefinition{Version: 1, AggregateField: "fixtureID"},
	}
	_, err = service.Replay(context.Background(), definition, domain.EventEnvelope{
		ID: "evt_exact", Type: definition.Type, Data: durableFixtureData("fixture-exact", "exact"),
		Durable: &domain.EventDurable{AggregateID: "fixture-exact", Sequence: 0, Version: 1},
	}, ReplayOptions{OwnerID: "owner-after-read"})
	if !errors.Is(err, failure) {
		t.Fatalf("exact replay error = %v, want EventAt failure", err)
	}
	if state := store.sequenceFor("fixture-exact"); state.OwnerID != "" || state.Latest != 0 {
		t.Fatalf("failed exact replay claimed/mutated sequence: %+v", state)
	}
}

func waitDurableClosed(t *testing.T, subscription *DurableSubscription) {
	t.Helper()
	select {
	case _, open := <-subscription.Events():
		if open {
			t.Fatal("durable subscription emitted an unexpected event")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("durable subscription did not close")
	}
}

func assertNoDurableWakes(t *testing.T, service *Service) {
	t.Helper()
	service.mu.RLock()
	defer service.mu.RUnlock()
	if len(service.durableWakes) != 0 {
		t.Fatalf("durable wake registrations leaked: %+v", service.durableWakes)
	}
}

func durableFixtureData(aggregateID, text string) domain.JSONValue {
	return domain.JSONObject(map[string]domain.JSONValue{
		"fixtureID": domain.JSONString(aggregateID),
		"text":      domain.JSONString(text),
	})
}

func fixedEventIDs(ids ...domain.EventID) EventIDGenerator {
	var mu sync.Mutex
	index := 0
	return func() (domain.EventID, error) {
		mu.Lock()
		defer mu.Unlock()
		if index >= len(ids) {
			return "", errors.New("event ID fixture exhausted")
		}
		id := ids[index]
		index++
		return id, nil
	}
}

type memoryStore struct {
	writeMu     sync.Mutex
	mu          sync.Mutex
	sequences   map[string]SequenceState
	events      []StoredEvent
	historyHook func()
	historyErr  error
	faultAt     string
	faultErr    error
	commitErr   error
	writes      int
}

func newMemoryStore() *memoryStore {
	return &memoryStore{sequences: make(map[string]SequenceState)}
}

func (store *memoryStore) Write(ctx context.Context, run func(context.Context, Transaction) error) error {
	store.writeMu.Lock()
	defer store.writeMu.Unlock()

	store.mu.Lock()
	store.writes++
	sequences := make(map[string]SequenceState, len(store.sequences))
	for key, value := range store.sequences {
		sequences[key] = value
	}
	events := append([]StoredEvent(nil), store.events...)
	faultAt := store.faultAt
	faultErr := store.faultErr
	commitErr := store.commitErr
	store.mu.Unlock()

	tx := &memoryTransaction{sequences: sequences, events: events, faultAt: faultAt, faultErr: faultErr}
	if err := run(ctx, tx); err != nil {
		return err
	}
	if commitErr != nil {
		return commitErr
	}
	store.mu.Lock()
	store.sequences = tx.sequences
	store.events = tx.events
	store.mu.Unlock()
	return nil
}

func (store *memoryStore) History(_ context.Context, aggregateID string, after int64, limit int) ([]StoredEvent, error) {
	store.mu.Lock()
	if store.historyErr != nil {
		err := store.historyErr
		store.mu.Unlock()
		return nil, err
	}
	result := make([]StoredEvent, 0, limit)
	for _, event := range store.events {
		if event.AggregateID == aggregateID && event.Sequence > after {
			copy := event
			copy.Data = append([]byte(nil), event.Data...)
			result = append(result, copy)
			if len(result) == limit {
				break
			}
		}
	}
	hook := store.historyHook
	store.mu.Unlock()
	if hook != nil {
		hook()
	}
	return result, nil
}

func (store *memoryStore) eventCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.events)
}

func (store *memoryStore) sequenceCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.sequences)
}

func (store *memoryStore) writeCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.writes
}

func (store *memoryStore) sequenceFor(aggregateID string) SequenceState {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.sequences[aggregateID]
}

func (store *memoryStore) eventsFor(aggregateID string) []StoredEvent {
	store.mu.Lock()
	defer store.mu.Unlock()
	var result []StoredEvent
	for _, event := range store.events {
		if event.AggregateID == aggregateID {
			result = append(result, event)
		}
	}
	return result
}

type memoryTransaction struct {
	sequences map[string]SequenceState
	events    []StoredEvent
	faultAt   string
	faultErr  error
}

func (tx *memoryTransaction) fault(point string) error {
	if tx.faultAt == point {
		return tx.faultErr
	}
	return nil
}

func (tx *memoryTransaction) Sequence(_ context.Context, aggregateID string) (SequenceState, bool, error) {
	if err := tx.fault("Sequence"); err != nil {
		return SequenceState{}, false, err
	}
	sequence, ok := tx.sequences[aggregateID]
	return sequence, ok, nil
}

func (tx *memoryTransaction) EventByID(_ context.Context, id domain.EventID) (StoredEvent, bool, error) {
	if err := tx.fault("EventByID"); err != nil {
		return StoredEvent{}, false, err
	}
	for _, event := range tx.events {
		if event.ID == id {
			return event, true, nil
		}
	}
	return StoredEvent{}, false, nil
}

func (tx *memoryTransaction) EventAt(_ context.Context, aggregateID string, sequence int64) (StoredEvent, bool, error) {
	if err := tx.fault("EventAt"); err != nil {
		return StoredEvent{}, false, err
	}
	for _, event := range tx.events {
		if event.AggregateID == aggregateID && event.Sequence == sequence {
			return event, true, nil
		}
	}
	return StoredEvent{}, false, nil
}

func (tx *memoryTransaction) PutSequence(_ context.Context, aggregateID string, sequence SequenceState) error {
	if err := tx.fault("PutSequence"); err != nil {
		return err
	}
	tx.sequences[aggregateID] = sequence
	return nil
}

func (tx *memoryTransaction) InsertEvent(_ context.Context, event StoredEvent) error {
	if err := tx.fault("InsertEvent"); err != nil {
		return err
	}
	tx.events = append(tx.events, event)
	return nil
}

func (tx *memoryTransaction) DeleteAggregate(_ context.Context, aggregateID string) error {
	if err := tx.fault("DeleteAggregate"); err != nil {
		return err
	}
	delete(tx.sequences, aggregateID)
	retained := tx.events[:0]
	for _, event := range tx.events {
		if event.AggregateID != aggregateID {
			retained = append(retained, event)
		}
	}
	tx.events = retained
	return nil
}
