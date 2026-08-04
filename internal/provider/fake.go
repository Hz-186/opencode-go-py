package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Hz-186/opencode-go-py/internal/codec"
	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

// ScriptTurn is one deterministic cassette turn. Model matching is exact;
// route/model fallback is deliberately absent so unsupported combinations fail
// fast instead of silently changing protocol semantics.
type ScriptTurn struct {
	Model  llm.Model
	Events []llm.LLMEvent
	Error  error
	Delay  time.Duration
}

// Cassette is an ordered sequence of fake provider turns.
type Cassette struct {
	Turns []ScriptTurn
}

// Call is a redaction-safe record of a fake invocation. It intentionally does
// not retain prompts, headers, provider options, or credentials.
type Call struct {
	Model   llm.Model
	Attempt int
}

// FakeProvider replays a validated cassette in order. The cassette is consumed
// atomically when a call starts, while event delivery remains outside the lock
// so a slow sink cannot block unrelated inspection.
type FakeProvider struct {
	mu    sync.Mutex
	turns []ScriptTurn
	index int
	calls []Call
}

// NewFakeProvider validates and deep-clones a cassette. The caller may safely
// mutate its original request/event values after this function returns.
func NewFakeProvider(cassette Cassette) (*FakeProvider, error) {
	if len(cassette.Turns) == 0 {
		return nil, fmt.Errorf("%w: at least one turn is required", ErrInvalidCassette)
	}
	turns := make([]ScriptTurn, len(cassette.Turns))
	for index, turn := range cassette.Turns {
		if turn.Model.Provider == "" || turn.Model.ID == "" || turn.Model.Route == "" {
			return nil, fmt.Errorf("%w: turn %d has incomplete model identity", ErrInvalidCassette, index)
		}
		if turn.Delay < 0 {
			return nil, fmt.Errorf("%w: turn %d has negative delay", ErrInvalidCassette, index)
		}
		if turn.Error != nil && len(turn.Events) == 0 {
			// A failure-only turn is useful for retry tests and is intentionally valid.
		}
		clonedEvents := make([]llm.LLMEvent, len(turn.Events))
		for eventIndex, value := range turn.Events {
			cloned, err := cloneEvent(value)
			if err != nil {
				return nil, fmt.Errorf("%w: turn %d event %d: %v", ErrInvalidCassette, index, eventIndex, err)
			}
			clonedEvents[eventIndex] = cloned
		}
		turns[index] = ScriptTurn{
			Model: turn.Model, Events: clonedEvents, Error: turn.Error, Delay: turn.Delay,
		}
	}
	return &FakeProvider{turns: turns}, nil
}

func (provider *FakeProvider) RunTurn(
	ctx context.Context,
	request ProviderTurnRequest,
	sink LLMEventSink,
) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", ErrInvalidRequest)
	}
	if err := request.Validate(); err != nil {
		return err
	}
	if sink == nil {
		return fmt.Errorf("%w: event sink is nil", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	turn, err := provider.nextTurn(request)
	if err != nil {
		return err
	}
	for _, event := range turn.Events {
		if turn.Delay > 0 {
			timer := time.NewTimer(turn.Delay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// A fresh codec round-trip prevents a sink from mutating the cassette or
		// observing shared maps/slices across calls.
		cloned, err := cloneEvent(event)
		if err != nil {
			return fmt.Errorf("%w: clone scripted event: %v", ErrInvalidCassette, err)
		}
		if err := sink.Send(ctx, cloned); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return fmt.Errorf("%w: %w", ErrSink, err)
		}
	}
	return turn.Error
}

func (provider *FakeProvider) nextTurn(request ProviderTurnRequest) (ScriptTurn, error) {
	if provider == nil {
		return ScriptTurn{}, ErrNoCassette
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.index >= len(provider.turns) {
		return ScriptTurn{}, ErrNoCassette
	}
	turn := provider.turns[provider.index]
	if turn.Model != request.Request.Model {
		return ScriptTurn{}, fmt.Errorf("%w: cassette model %s/%s/%s does not match request %s/%s/%s",
			ErrNoCassette,
			turn.Model.Provider, turn.Model.ID, turn.Model.Route,
			request.Request.Model.Provider, request.Request.Model.ID, request.Request.Model.Route)
	}
	provider.index++
	provider.calls = append(provider.calls, Call{Model: request.Request.Model, Attempt: request.Attempt})
	return turn, nil
}

// Calls returns a stable copy of invocation metadata for assertions and
// diagnostics. It never exposes request content or cassette events.
func (provider *FakeProvider) Calls() []Call {
	if provider == nil {
		return nil
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]Call(nil), provider.calls...)
}

func cloneEvent(event llm.LLMEvent) (llm.LLMEvent, error) {
	encoded, err := codec.EncodeLLMEventJSON(event)
	if err != nil {
		return nil, err
	}
	return codec.DecodeLLMEventJSON(encoded)
}
