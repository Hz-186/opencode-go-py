// Package event owns canonical durable Event transaction semantics.
package event

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/Hz-186/opencode-go-py/internal/codec"
	"github.com/Hz-186/opencode-go-py/internal/domain"
)

var (
	ErrInvalidDefinition  = errors.New("invalid event definition")
	ErrInvalidEvent       = errors.New("invalid event")
	ErrEventConflict      = errors.New("event conflict")
	ErrReplayConflict     = errors.New("replay conflict")
	ErrSequenceConflict   = errors.New("event sequence conflict")
	ErrOwnerConflict      = errors.New("event owner conflict")
	ErrSubscriberOverflow = errors.New("event subscriber overflow")
	ErrObserverFailure    = errors.New("event observer failure")
)

// DurableDefinition freezes the storage identity of one durable Event type.
type DurableDefinition struct {
	Version        int64
	AggregateField string
}

// Definition describes one canonical Event payload contract. Payload shape is
// validated by its owning Domain codec before reaching this service; this layer
// additionally requires a JSON object and validates durable aggregate identity.
type Definition struct {
	Type    string
	Durable *DurableDefinition
}

// VersionedType is the immutable type stored in durable history.
func VersionedType(definition Definition) string {
	if definition.Durable == nil {
		return definition.Type
	}
	return fmt.Sprintf("%s.%d", definition.Type, definition.Durable.Version)
}

// SequenceState is the authoritative latest durable sequence for an aggregate.
type SequenceState struct {
	Latest  int64
	OwnerID string
}

// StoredEvent contains canonical bytes ready for the durable store.
type StoredEvent struct {
	ID          domain.EventID
	AggregateID string
	Sequence    int64
	Type        string
	Data        []byte
}

// Transaction is the minimum Event Store transaction surface. Concrete
// projectors receive the same transaction and may extend it with store-specific
// projection operations; all methods must roll back together when Write fails.
type Transaction interface {
	Sequence(context.Context, string) (SequenceState, bool, error)
	EventByID(context.Context, domain.EventID) (StoredEvent, bool, error)
	EventAt(context.Context, string, int64) (StoredEvent, bool, error)
	PutSequence(context.Context, string, SequenceState) error
	InsertEvent(context.Context, StoredEvent) error
	DeleteAggregate(context.Context, string) error
}

// Store serializes durable writers and atomically commits the supplied closure.
type Store interface {
	Write(context.Context, func(context.Context, Transaction) error) error
	History(context.Context, string, int64, int) ([]StoredEvent, error)
}

type EventIDGenerator func() (domain.EventID, error)
type Projector func(context.Context, Transaction, domain.EventEnvelope) error
type Listener func(domain.EventEnvelope)
type ObserverFailureHandler func(error)
type CommitHook func(context.Context, Transaction, int64) error

type PublishOptions struct {
	ID       domain.EventID
	Metadata map[string]domain.JSONValue
	Location *domain.LocationRef
	Commit   CommitHook
}

type ReplayOptions struct {
	OwnerID     string
	StrictOwner bool
	Publish     bool
}

type ReplayEntry struct {
	Definition Definition
	Event      domain.EventEnvelope
}

type SubscriberOverflowError struct {
	Capacity int
}

func (err *SubscriberOverflowError) Error() string {
	return fmt.Sprintf("%v: capacity %d", ErrSubscriberOverflow, err.Capacity)
}

func (err *SubscriberOverflowError) Unwrap() error {
	return ErrSubscriberOverflow
}

type Subscription struct {
	service  *Service
	id       uint64
	typeName string
	events   chan domain.EventEnvelope

	errMu sync.Mutex
	err   error
}

type DurableInput struct {
	AggregateID string
	After       int64
	Capacity    int
	BatchSize   int
}

type DurableSubscription struct {
	events chan domain.EventEnvelope
	cancel context.CancelFunc
	done   chan struct{}

	closeOnce sync.Once
	errMu     sync.Mutex
	err       error
	closing   bool
}

func (subscription *DurableSubscription) Events() <-chan domain.EventEnvelope {
	return subscription.events
}

func (subscription *DurableSubscription) Err() error {
	subscription.errMu.Lock()
	defer subscription.errMu.Unlock()
	return subscription.err
}

func (subscription *DurableSubscription) Close() {
	if subscription == nil {
		return
	}
	subscription.closeOnce.Do(func() {
		subscription.errMu.Lock()
		subscription.closing = true
		subscription.errMu.Unlock()
		subscription.cancel()
		<-subscription.done
	})
}

func (subscription *DurableSubscription) finish(err error) {
	subscription.errMu.Lock()
	if !(subscription.closing && errors.Is(err, context.Canceled)) {
		subscription.err = err
	}
	subscription.errMu.Unlock()
	close(subscription.events)
	close(subscription.done)
}

func (subscription *Subscription) Events() <-chan domain.EventEnvelope {
	return subscription.events
}

func (subscription *Subscription) Err() error {
	subscription.errMu.Lock()
	defer subscription.errMu.Unlock()
	return subscription.err
}

func (subscription *Subscription) Close() {
	if subscription == nil || subscription.service == nil {
		return
	}
	subscription.service.removeSubscription(subscription.id, nil)
}

func (subscription *Subscription) setError(err error) {
	subscription.errMu.Lock()
	subscription.err = err
	subscription.errMu.Unlock()
}

type Service struct {
	store      Store
	newEventID EventIDGenerator

	mu               sync.RWMutex
	definitions      map[string]Definition
	projectors       map[string][]Projector
	listeners        map[uint64]Listener
	nextListenerID   uint64
	observerFailure  ObserverFailureHandler
	subscriptions    map[uint64]*Subscription
	nextSubscriberID uint64
	durableWakes     map[string]map[uint64]chan struct{}
	nextDurableID    uint64
}

func NewService(store Store, newEventID EventIDGenerator) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: store is nil", ErrInvalidEvent)
	}
	if newEventID == nil {
		return nil, fmt.Errorf("%w: event ID generator is nil", ErrInvalidEvent)
	}
	return &Service{
		store: store, newEventID: newEventID,
		definitions: make(map[string]Definition), projectors: make(map[string][]Projector),
		listeners:     make(map[uint64]Listener),
		subscriptions: make(map[uint64]*Subscription), durableWakes: make(map[string]map[uint64]chan struct{}),
	}, nil
}

// Project registers a synchronous projector for one exact durable definition.
func (service *Service) Project(definition Definition, projector Projector) error {
	if err := validateDefinition(definition); err != nil {
		return err
	}
	if definition.Durable == nil {
		return fmt.Errorf("%w: live-only definition %q cannot have a synchronous projector", ErrInvalidDefinition, definition.Type)
	}
	if projector == nil {
		return fmt.Errorf("%w: projector is nil", ErrInvalidDefinition)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.bindDefinitionLocked(definition); err != nil {
		return err
	}
	key := VersionedType(definition)
	service.projectors[key] = append(service.projectors[key], projector)
	return nil
}

// SetObserverFailureHandler installs the process-local sink for recovered
// listener defects. Observer reporting is best effort: a defective handler is
// also isolated so it cannot turn a committed Event into a publish failure.
func (service *Service) SetObserverFailureHandler(handler ObserverFailureHandler) error {
	if handler == nil {
		return fmt.Errorf("%w: observer failure handler is nil", ErrInvalidEvent)
	}
	service.mu.Lock()
	service.observerFailure = handler
	service.mu.Unlock()
	return nil
}

// Listen registers an in-process observer. Durable events reach listeners only
// after their transaction commits. The returned function is concurrent-safe and
// idempotent.
func (service *Service) Listen(listener Listener) (func(), error) {
	if listener == nil {
		return nil, fmt.Errorf("%w: listener is nil", ErrInvalidEvent)
	}
	service.mu.Lock()
	service.nextListenerID++
	id := service.nextListenerID
	service.listeners[id] = listener
	service.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			service.mu.Lock()
			delete(service.listeners, id)
			service.mu.Unlock()
		})
	}, nil
}

// All returns an isolated bounded live Event subscription. A slow consumer is
// closed with SubscriberOverflowError without delaying publishers or peers.
func (service *Service) All(capacity int) (*Subscription, error) {
	return service.subscribe("", capacity)
}

// Subscribe returns an isolated bounded subscription for one canonical base
// Event type. Events using another durable version of the same base type still
// belong to this typed stream, matching the canonical Event API.
func (service *Service) Subscribe(definition Definition, capacity int) (*Subscription, error) {
	if err := validateDefinition(definition); err != nil {
		return nil, err
	}
	if capacity <= 0 {
		return nil, fmt.Errorf("%w: subscriber capacity must be positive", ErrInvalidEvent)
	}
	if err := service.bindDefinition(definition); err != nil {
		return nil, err
	}
	return service.subscribe(definition.Type, capacity)
}

func (service *Service) subscribe(typeName string, capacity int) (*Subscription, error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("%w: subscriber capacity must be positive", ErrInvalidEvent)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.nextSubscriberID++
	subscription := &Subscription{
		service:  service,
		id:       service.nextSubscriberID,
		typeName: typeName,
		events:   make(chan domain.EventEnvelope, capacity),
	}
	service.subscriptions[subscription.id] = subscription
	return subscription, nil
}

// Claim transfers an existing aggregate's replay owner. A missing aggregate is
// intentionally a no-op, matching the canonical update-only operation.
func (service *Service) Claim(ctx context.Context, aggregateID, ownerID string) error {
	if err := validateIdentity("aggregate ID", aggregateID); err != nil {
		return err
	}
	if err := validateIdentity("owner ID", ownerID); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return service.store.Write(ctx, func(txCtx context.Context, tx Transaction) error {
		if err := txCtx.Err(); err != nil {
			return err
		}
		state, found, err := tx.Sequence(txCtx, aggregateID)
		if err != nil {
			return fmt.Errorf("read aggregate to claim: %w", err)
		}
		if !found {
			return nil
		}
		state.OwnerID = ownerID
		if err := tx.PutSequence(txCtx, aggregateID, state); err != nil {
			return fmt.Errorf("claim aggregate: %w", err)
		}
		return nil
	})
}

// Remove atomically clears one aggregate's sequence and durable history.
func (service *Service) Remove(ctx context.Context, aggregateID string) error {
	if err := validateIdentity("aggregate ID", aggregateID); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return service.store.Write(ctx, func(txCtx context.Context, tx Transaction) error {
		if err := txCtx.Err(); err != nil {
			return err
		}
		if err := tx.DeleteAggregate(txCtx, aggregateID); err != nil {
			return fmt.Errorf("remove aggregate: %w", err)
		}
		return nil
	})
}

// Durable replays one aggregate after the supplied sequence and then tails new
// committed Events. A wake is registered before the first history read so the
// history/live handoff cannot lose a concurrent commit.
func (service *Service) Durable(ctx context.Context, input DurableInput) (*DurableSubscription, error) {
	if strings.TrimSpace(input.AggregateID) == "" || input.AggregateID != strings.TrimSpace(input.AggregateID) {
		return nil, fmt.Errorf("%w: durable aggregate ID must be non-empty without surrounding whitespace", ErrInvalidEvent)
	}
	if input.After < -1 {
		return nil, fmt.Errorf("%w: durable cursor must be at least -1", ErrInvalidEvent)
	}
	if input.Capacity <= 0 || input.BatchSize <= 0 {
		return nil, fmt.Errorf("%w: durable capacity and batch size must be positive", ErrInvalidEvent)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	subscription := &DurableSubscription{
		events: make(chan domain.EventEnvelope, input.Capacity),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	wake := make(chan struct{}, 1)
	service.mu.Lock()
	service.nextDurableID++
	id := service.nextDurableID
	wakes := service.durableWakes[input.AggregateID]
	if wakes == nil {
		wakes = make(map[uint64]chan struct{})
		service.durableWakes[input.AggregateID] = wakes
	}
	wakes[id] = wake
	service.mu.Unlock()
	go func() {
		err := service.runDurable(runCtx, input, wake, subscription.events)
		service.mu.Lock()
		if current := service.durableWakes[input.AggregateID]; current != nil {
			delete(current, id)
			if len(current) == 0 {
				delete(service.durableWakes, input.AggregateID)
			}
		}
		service.mu.Unlock()
		subscription.finish(err)
	}()
	return subscription, nil
}

// Publish commits a durable Event and its synchronous projections atomically.
// Live-only events skip the store and reject local commit hooks.
func (service *Service) Publish(
	ctx context.Context,
	definition Definition,
	data domain.JSONValue,
	options PublishOptions,
) (domain.EventEnvelope, error) {
	if err := validateDefinition(definition); err != nil {
		return domain.EventEnvelope{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.EventEnvelope{}, err
	}
	canonicalData, encoded, err := canonicalObject(data)
	if err != nil {
		return domain.EventEnvelope{}, err
	}
	aggregate := ""
	if definition.Durable == nil {
		if options.Commit != nil {
			return domain.EventEnvelope{}, fmt.Errorf("%w: local commit hooks require a durable event", ErrInvalidEvent)
		}
	} else {
		aggregate, err = aggregateID(definition, canonicalData)
		if err != nil {
			return domain.EventEnvelope{}, err
		}
	}
	metadata, err := cloneMetadata(options.Metadata)
	if err != nil {
		return domain.EventEnvelope{}, err
	}
	if err := service.bindDefinition(definition); err != nil {
		return domain.EventEnvelope{}, err
	}
	id := options.ID
	if id == "" {
		id, err = service.newEventID()
		if err != nil {
			return domain.EventEnvelope{}, fmt.Errorf("generate event ID: %w", err)
		}
	}
	if _, err := domain.ParseEventID(string(id)); err != nil {
		return domain.EventEnvelope{}, fmt.Errorf("%w: %v", ErrInvalidEvent, err)
	}
	event := domain.EventEnvelope{
		ID: id, Type: definition.Type, Data: canonicalData,
		Metadata: metadata, Location: cloneLocation(options.Location),
	}
	if definition.Durable == nil {
		service.notify(event)
		return event, nil
	}
	projectors := service.projectorSnapshot(VersionedType(definition))
	err = service.store.Write(ctx, func(txCtx context.Context, tx Transaction) error {
		if err := txCtx.Err(); err != nil {
			return err
		}
		if existing, found, err := tx.EventByID(txCtx, id); err != nil {
			return fmt.Errorf("read event ID: %w", err)
		} else if found {
			return fmt.Errorf("%w: event %q already belongs to aggregate %q sequence %d",
				ErrEventConflict, id, existing.AggregateID, existing.Sequence)
		}
		state, found, err := tx.Sequence(txCtx, aggregate)
		if err != nil {
			return fmt.Errorf("read aggregate sequence: %w", err)
		}
		latest := int64(-1)
		if found {
			latest = state.Latest
		}
		if latest == math.MaxInt64 {
			return fmt.Errorf("%w: aggregate %q sequence overflow", ErrEventConflict, aggregate)
		}
		sequence := latest + 1
		event.Durable = &domain.EventDurable{
			AggregateID: aggregate, Sequence: sequence, Version: definition.Durable.Version,
		}
		for _, projector := range projectors {
			if err := projector(txCtx, tx, event); err != nil {
				return fmt.Errorf("project %s: %w", VersionedType(definition), err)
			}
		}
		if options.Commit != nil {
			if err := options.Commit(txCtx, tx, sequence); err != nil {
				return fmt.Errorf("commit event operation: %w", err)
			}
		}
		if err := tx.PutSequence(txCtx, aggregate, SequenceState{Latest: sequence, OwnerID: state.OwnerID}); err != nil {
			return fmt.Errorf("store aggregate sequence: %w", err)
		}
		if err := tx.InsertEvent(txCtx, StoredEvent{
			ID: id, AggregateID: aggregate, Sequence: sequence,
			Type: VersionedType(definition), Data: append([]byte(nil), encoded...),
		}); err != nil {
			return fmt.Errorf("store event: %w", err)
		}
		return nil
	})
	if err != nil {
		return domain.EventEnvelope{}, err
	}
	service.notify(event)
	return event, nil
}

// Replay validates and inserts one externally sequenced durable Event. It
// returns true only when a new row was committed. Exact stale replay is
// idempotent and may claim a previously unowned aggregate without re-projecting
// or notifying observers.
func (service *Service) Replay(
	ctx context.Context,
	definition Definition,
	event domain.EventEnvelope,
	options ReplayOptions,
) (bool, error) {
	if err := validateDefinition(definition); err != nil {
		return false, err
	}
	if definition.Durable == nil {
		return false, fmt.Errorf("%w: replay requires a durable definition", ErrInvalidEvent)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if _, err := domain.ParseEventID(string(event.ID)); err != nil {
		return false, fmt.Errorf("%w: %v", ErrInvalidEvent, err)
	}
	if event.Type != definition.Type {
		return false, fmt.Errorf("%w: replay type %q does not match definition %q",
			ErrInvalidEvent, event.Type, definition.Type)
	}
	if event.Durable == nil || event.Durable.Sequence < 0 || event.Durable.Version != definition.Durable.Version {
		return false, fmt.Errorf("%w: replay durable identity does not match definition %q version %d",
			ErrInvalidEvent, definition.Type, definition.Durable.Version)
	}
	canonicalData, encoded, err := canonicalObject(event.Data)
	if err != nil {
		return false, err
	}
	aggregate, err := aggregateID(definition, canonicalData)
	if err != nil {
		return false, err
	}
	if aggregate != event.Durable.AggregateID {
		return false, fmt.Errorf("%w: replay aggregate %q does not match payload aggregate %q",
			ErrInvalidEvent, event.Durable.AggregateID, aggregate)
	}
	metadata, err := cloneMetadata(event.Metadata)
	if err != nil {
		return false, err
	}
	if err := service.bindDefinition(definition); err != nil {
		return false, err
	}
	committed := domain.EventEnvelope{
		ID: event.ID, Type: event.Type, Data: canonicalData,
		Durable: &domain.EventDurable{
			AggregateID: aggregate, Sequence: event.Durable.Sequence, Version: event.Durable.Version,
		},
		Metadata: metadata, Location: cloneLocation(event.Location),
	}
	inserted := false
	projectors := service.projectorSnapshot(VersionedType(definition))
	err = service.store.Write(ctx, func(txCtx context.Context, tx Transaction) error {
		if err := txCtx.Err(); err != nil {
			return err
		}
		state, found, err := tx.Sequence(txCtx, aggregate)
		if err != nil {
			return fmt.Errorf("read replay aggregate sequence: %w", err)
		}
		latest := int64(-1)
		if found {
			latest = state.Latest
		}
		if options.StrictOwner && state.OwnerID != "" && state.OwnerID != options.OwnerID {
			return fmt.Errorf("%w: aggregate %q belongs to %q, replay owner is %q",
				ErrOwnerConflict, aggregate, state.OwnerID, options.OwnerID)
		}
		if committed.Durable.Sequence <= latest {
			stored, exists, err := tx.EventAt(txCtx, aggregate, committed.Durable.Sequence)
			if err != nil {
				return fmt.Errorf("read replay sequence: %w", err)
			}
			if !exists || stored.ID != committed.ID || stored.Type != VersionedType(definition) ||
				!bytes.Equal(stored.Data, encoded) {
				return fmt.Errorf("%w: aggregate %q sequence %d diverged",
					ErrReplayConflict, aggregate, committed.Durable.Sequence)
			}
			if options.OwnerID != "" && state.OwnerID == "" {
				state.OwnerID = options.OwnerID
				if err := tx.PutSequence(txCtx, aggregate, state); err != nil {
					return fmt.Errorf("claim exact replay aggregate: %w", err)
				}
			}
			return nil
		}
		if state.OwnerID != "" && state.OwnerID != options.OwnerID {
			return nil
		}
		if committed.Durable.Sequence != latest+1 {
			return fmt.Errorf("%w: aggregate %q expected %d, got %d",
				ErrSequenceConflict, aggregate, latest+1, committed.Durable.Sequence)
		}
		if existing, exists, err := tx.EventByID(txCtx, committed.ID); err != nil {
			return fmt.Errorf("read replay event ID: %w", err)
		} else if exists {
			return fmt.Errorf("%w: event %q already belongs to aggregate %q sequence %d",
				ErrEventConflict, committed.ID, existing.AggregateID, existing.Sequence)
		}
		for _, projector := range projectors {
			if err := projector(txCtx, tx, committed); err != nil {
				return fmt.Errorf("project replay %s: %w", VersionedType(definition), err)
			}
		}
		ownerID := state.OwnerID
		if ownerID == "" {
			ownerID = options.OwnerID
		}
		if err := tx.PutSequence(txCtx, aggregate, SequenceState{
			Latest: committed.Durable.Sequence, OwnerID: ownerID,
		}); err != nil {
			return fmt.Errorf("store replay aggregate sequence: %w", err)
		}
		if err := tx.InsertEvent(txCtx, StoredEvent{
			ID: committed.ID, AggregateID: aggregate, Sequence: committed.Durable.Sequence,
			Type: VersionedType(definition), Data: append([]byte(nil), encoded...),
		}); err != nil {
			return fmt.Errorf("store replay event: %w", err)
		}
		inserted = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if inserted && options.Publish {
		service.notify(committed)
	}
	return inserted, nil
}

// ReplayAll validates one contiguous aggregate chunk before replaying its
// entries in order. Each entry retains Replay's idempotent checkpoint semantics,
// so a caller may safely retry a chunk after an operational failure.
func (service *Service) ReplayAll(
	ctx context.Context,
	entries []ReplayEntry,
	options ReplayOptions,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", nil
	}
	source := ""
	start := int64(0)
	for index, entry := range entries {
		if err := validateDefinition(entry.Definition); err != nil {
			return "", fmt.Errorf("replay entry %d: %w", index, err)
		}
		if entry.Definition.Durable == nil || entry.Event.Durable == nil {
			return "", fmt.Errorf("%w: replay entry %d requires a durable definition and envelope",
				ErrInvalidEvent, index)
		}
		if _, err := domain.ParseEventID(string(entry.Event.ID)); err != nil {
			return "", fmt.Errorf("%w: replay entry %d: %v", ErrInvalidEvent, index, err)
		}
		if entry.Event.Type != entry.Definition.Type ||
			entry.Event.Durable.Version != entry.Definition.Durable.Version ||
			entry.Event.Durable.Sequence < 0 {
			return "", fmt.Errorf("%w: replay entry %d identity does not match its definition",
				ErrInvalidEvent, index)
		}
		canonicalData, _, err := canonicalObject(entry.Event.Data)
		if err != nil {
			return "", fmt.Errorf("replay entry %d: %w", index, err)
		}
		aggregate, err := aggregateID(entry.Definition, canonicalData)
		if err != nil {
			return "", fmt.Errorf("replay entry %d: %w", index, err)
		}
		if aggregate != entry.Event.Durable.AggregateID {
			return "", fmt.Errorf("%w: replay entry %d aggregate %q does not match payload aggregate %q",
				ErrInvalidEvent, index, entry.Event.Durable.AggregateID, aggregate)
		}
		if _, err := cloneMetadata(entry.Event.Metadata); err != nil {
			return "", fmt.Errorf("replay entry %d: %w", index, err)
		}
		if index == 0 {
			source = aggregate
			start = entry.Event.Durable.Sequence
		} else if aggregate != source {
			return "", fmt.Errorf("%w: replay entries must belong to one aggregate, got %q and %q",
				ErrInvalidEvent, source, aggregate)
		}
		if int64(index) > math.MaxInt64-start || entry.Event.Durable.Sequence != start+int64(index) {
			return "", fmt.Errorf("%w: replay entry %d expected sequence %d, got %d",
				ErrSequenceConflict, index, start+int64(index), entry.Event.Durable.Sequence)
		}
	}
	definitions := make([]Definition, len(entries))
	for index, entry := range entries {
		definitions[index] = entry.Definition
	}
	if err := service.bindDefinitions(definitions); err != nil {
		return "", err
	}
	for index, entry := range entries {
		if _, err := service.Replay(ctx, entry.Definition, entry.Event, options); err != nil {
			return source, fmt.Errorf("replay entry %d: %w", index, err)
		}
	}
	return source, nil
}

func validateDefinition(definition Definition) error {
	if strings.TrimSpace(definition.Type) == "" || definition.Type != strings.TrimSpace(definition.Type) {
		return fmt.Errorf("%w: type must be non-empty without surrounding whitespace", ErrInvalidDefinition)
	}
	if definition.Durable == nil {
		return nil
	}
	if definition.Durable.Version <= 0 {
		return fmt.Errorf("%w: durable version must be positive", ErrInvalidDefinition)
	}
	if strings.TrimSpace(definition.Durable.AggregateField) == "" ||
		definition.Durable.AggregateField != strings.TrimSpace(definition.Durable.AggregateField) {
		return fmt.Errorf("%w: durable aggregate field must be non-empty without surrounding whitespace", ErrInvalidDefinition)
	}
	return nil
}

func validateIdentity(label, value string) error {
	if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%w: %s must be non-empty without surrounding whitespace", ErrInvalidEvent, label)
	}
	return nil
}

func (service *Service) bindDefinition(definition Definition) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.bindDefinitionLocked(definition)
}

func (service *Service) bindDefinitions(definitions []Definition) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	pending := make(map[string]Definition)
	for _, definition := range definitions {
		key := VersionedType(definition)
		existing, ok := service.definitions[key]
		if !ok {
			existing, ok = pending[key]
		}
		if ok && !definitionsEqual(existing, definition) {
			return fmt.Errorf("%w: exact event type %q is already bound to a different definition",
				ErrInvalidDefinition, key)
		}
		if !ok {
			pending[key] = cloneDefinition(definition)
		}
	}
	for key, definition := range pending {
		service.definitions[key] = definition
	}
	return nil
}

func (service *Service) bindDefinitionLocked(definition Definition) error {
	key := VersionedType(definition)
	if existing, ok := service.definitions[key]; ok {
		if !definitionsEqual(existing, definition) {
			return fmt.Errorf("%w: exact event type %q is already bound to a different definition",
				ErrInvalidDefinition, key)
		}
		return nil
	}
	service.definitions[key] = cloneDefinition(definition)
	return nil
}

func cloneDefinition(definition Definition) Definition {
	result := definition
	if definition.Durable != nil {
		durable := *definition.Durable
		result.Durable = &durable
	}
	return result
}

func definitionsEqual(left, right Definition) bool {
	if left.Type != right.Type || (left.Durable == nil) != (right.Durable == nil) {
		return false
	}
	return left.Durable == nil || *left.Durable == *right.Durable
}

func canonicalObject(data domain.JSONValue) (domain.JSONValue, []byte, error) {
	if data.Kind != domain.JSONKindObject {
		return domain.JSONValue{}, nil, fmt.Errorf("%w: event data must be an object", ErrInvalidEvent)
	}
	encoded, err := codec.EncodeJSONValue(data)
	if err != nil {
		return domain.JSONValue{}, nil, fmt.Errorf("%w: encode event data: %v", ErrInvalidEvent, err)
	}
	canonical, err := codec.DecodeJSONValue(encoded)
	if err != nil {
		return domain.JSONValue{}, nil, fmt.Errorf("%w: decode canonical event data: %v", ErrInvalidEvent, err)
	}
	return canonical, encoded, nil
}

func aggregateID(definition Definition, data domain.JSONValue) (string, error) {
	value, ok := data.Object[definition.Durable.AggregateField]
	if !ok || value.Kind != domain.JSONKindString || value.String == "" {
		return "", fmt.Errorf("%w: durable event %q requires non-empty string aggregate field %q",
			ErrInvalidEvent, definition.Type, definition.Durable.AggregateField)
	}
	return value.String, nil
}

func cloneMetadata(metadata map[string]domain.JSONValue) (map[string]domain.JSONValue, error) {
	if metadata == nil {
		return nil, nil
	}
	canonical, _, err := canonicalObject(domain.JSONObject(metadata))
	if err != nil {
		return nil, fmt.Errorf("%w: metadata: %v", ErrInvalidEvent, err)
	}
	return canonical.Object, nil
}

func cloneLocation(location *domain.LocationRef) *domain.LocationRef {
	if location == nil {
		return nil
	}
	result := *location
	if location.WorkspaceID != nil {
		workspaceID := *location.WorkspaceID
		result.WorkspaceID = &workspaceID
	}
	return &result
}

func (service *Service) projectorSnapshot(key string) []Projector {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return append([]Projector(nil), service.projectors[key]...)
}

func (service *Service) runDurable(
	ctx context.Context,
	input DurableInput,
	wake <-chan struct{},
	output chan<- domain.EventEnvelope,
) error {
	cursor := input.After
	drain := func() error {
		for {
			records, err := service.store.History(ctx, input.AggregateID, cursor, input.BatchSize)
			if err != nil {
				return fmt.Errorf("read durable history: %w", err)
			}
			if len(records) == 0 {
				return nil
			}
			for _, record := range records {
				if record.AggregateID != input.AggregateID || record.Sequence != cursor+1 {
					return fmt.Errorf("%w: aggregate %q expected history sequence %d, got aggregate %q sequence %d",
						ErrSequenceConflict, input.AggregateID, cursor+1, record.AggregateID, record.Sequence)
				}
				event, err := decodeStoredEvent(record)
				if err != nil {
					return err
				}
				select {
				case output <- event:
					cursor = record.Sequence
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if len(records) < input.BatchSize {
				return nil
			}
		}
	}
	if err := drain(); err != nil {
		return err
	}
	for {
		select {
		case <-wake:
			if err := drain(); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func decodeStoredEvent(record StoredEvent) (domain.EventEnvelope, error) {
	if _, err := domain.ParseEventID(string(record.ID)); err != nil {
		return domain.EventEnvelope{}, fmt.Errorf("%w: stored event ID: %v", ErrInvalidEvent, err)
	}
	separator := strings.LastIndexByte(record.Type, '.')
	if separator <= 0 || separator == len(record.Type)-1 {
		return domain.EventEnvelope{}, fmt.Errorf("%w: stored event type %q is not versioned", ErrInvalidEvent, record.Type)
	}
	version, err := strconv.ParseInt(record.Type[separator+1:], 10, 64)
	if err != nil || version <= 0 || record.Sequence < 0 || record.AggregateID == "" {
		return domain.EventEnvelope{}, fmt.Errorf("%w: stored event has invalid durable identity", ErrInvalidEvent)
	}
	data, err := codec.DecodeJSONValue(record.Data)
	if err != nil || data.Kind != domain.JSONKindObject {
		return domain.EventEnvelope{}, fmt.Errorf("%w: decode stored event data: %v", ErrInvalidEvent, err)
	}
	return domain.EventEnvelope{
		ID: record.ID, Type: record.Type[:separator], Data: data,
		Durable: &domain.EventDurable{
			AggregateID: record.AggregateID, Sequence: record.Sequence, Version: version,
		},
	}, nil
}

func (service *Service) notify(event domain.EventEnvelope) {
	service.mu.RLock()
	type registeredListener struct {
		id       uint64
		listener Listener
	}
	listeners := make([]registeredListener, 0, len(service.listeners))
	for id, listener := range service.listeners {
		listeners = append(listeners, registeredListener{id: id, listener: listener})
	}
	service.mu.RUnlock()
	sort.Slice(listeners, func(left, right int) bool {
		return listeners[left].id < listeners[right].id
	})
	for _, registered := range listeners {
		service.notifyListener(registered.id, registered.listener, event)
	}
	service.mu.Lock()
	for id, subscription := range service.subscriptions {
		if subscription.typeName != "" && subscription.typeName != event.Type {
			continue
		}
		select {
		case subscription.events <- cloneEnvelope(event):
		default:
			delete(service.subscriptions, id)
			subscription.setError(&SubscriberOverflowError{Capacity: cap(subscription.events)})
			close(subscription.events)
		}
	}
	if event.Durable != nil {
		for _, wake := range service.durableWakes[event.Durable.AggregateID] {
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}
	service.mu.Unlock()
}

func (service *Service) notifyListener(id uint64, listener Listener, event domain.EventEnvelope) {
	defer func() {
		if defect := recover(); defect != nil {
			service.reportObserverFailure(fmt.Errorf("%w: listener %d panicked: %v",
				ErrObserverFailure, id, defect))
		}
	}()
	listener(cloneEnvelope(event))
}

func (service *Service) reportObserverFailure(err error) {
	service.mu.RLock()
	handler := service.observerFailure
	service.mu.RUnlock()
	if handler == nil {
		return
	}
	func() {
		defer func() {
			_ = recover()
		}()
		handler(err)
	}()
}

func (service *Service) removeSubscription(id uint64, err error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	subscription, ok := service.subscriptions[id]
	if !ok {
		return
	}
	delete(service.subscriptions, id)
	if err != nil {
		subscription.setError(err)
	}
	close(subscription.events)
}

func cloneEnvelope(event domain.EventEnvelope) domain.EventEnvelope {
	result := event
	if event.Durable != nil {
		durable := *event.Durable
		result.Durable = &durable
	}
	result.Location = cloneLocation(event.Location)
	if data, _, err := canonicalObject(event.Data); err == nil {
		result.Data = data
	}
	if metadata, err := cloneMetadata(event.Metadata); err == nil {
		result.Metadata = metadata
	}
	return result
}
