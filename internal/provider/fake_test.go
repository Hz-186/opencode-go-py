package provider

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

func TestFakeProviderStreamsCanonicalCassetteAndRecordsOnlySafeCallMetadata(t *testing.T) {
	metadata := llm.ProviderMetadata{"fixture": map[string]domain.JSONValue{
		"request": domain.JSONString("not retained"),
	}}
	input := []llm.LLMEvent{
		llm.TextStart{ID: "text-1", ProviderMetadata: metadata},
		llm.TextDelta{ID: "text-1", Text: "hello"},
		llm.TextEnd{ID: "text-1"},
		llm.Finish{Reason: llm.FinishStop},
	}
	provider, err := NewFakeProvider(Cassette{Turns: []ScriptTurn{{
		Model: llm.Model{Provider: "fixture", ID: "model-1", Route: "chat"}, Events: input,
	}}})
	if err != nil {
		t.Fatalf("new fake provider: %v", err)
	}
	input[0] = llm.TextDelta{ID: "mutated", Text: "caller mutation"}
	var got []llm.LLMEvent
	err = provider.RunTurn(context.Background(), ProviderTurnRequest{
		Request: llm.Request{Model: llm.Model{Provider: "fixture", ID: "model-1", Route: "chat"}}, Attempt: 2,
	}, LLMEventSinkFunc(func(_ context.Context, event llm.LLMEvent) error {
		got = append(got, event)
		return nil
	}))
	if err != nil {
		t.Fatalf("run fake turn: %v", err)
	}
	if len(got) != 4 || got[0].(llm.TextStart).ID != "text-1" || got[1].(llm.TextDelta).Text != "hello" {
		t.Fatalf("streamed events = %#v", got)
	}
	calls := provider.Calls()
	wantCall := []Call{{Model: llm.Model{Provider: "fixture", ID: "model-1", Route: "chat"}, Attempt: 2}}
	if !reflect.DeepEqual(calls, wantCall) {
		t.Fatalf("safe calls = %#v, want %#v", calls, wantCall)
	}
	if strings.Contains(strings.Join([]string{calls[0].Model.Provider, calls[0].Model.ID}, " "), "not retained") {
		t.Fatal("provider call metadata retained request content")
	}
}

func TestFakeProviderFailsFastOnUnsupportedModelAndDoesNotConsumeCassette(t *testing.T) {
	provider, err := NewFakeProvider(Cassette{Turns: []ScriptTurn{{
		Model: llm.Model{Provider: "fixture", ID: "model-1", Route: "chat"},
	}}})
	if err != nil {
		t.Fatalf("new fake provider: %v", err)
	}
	request := ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "fixture", ID: "other", Route: "chat"}}}
	if !errors.Is(provider.RunTurn(context.Background(), request, LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return nil })), ErrNoCassette) {
		t.Fatal("unsupported model did not fail with ErrNoCassette")
	}
	request.Request.Model.ID = "model-1"
	if err := provider.RunTurn(context.Background(), request, LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return nil })); err != nil {
		t.Fatalf("matching turn after failed lookup: %v", err)
	}
	if got := len(provider.Calls()); got != 1 {
		t.Fatalf("calls after failed lookup = %d, want 1", got)
	}
}

func TestFakeProviderCancellationAndSinkFailureStopStream(t *testing.T) {
	provider, err := NewFakeProvider(Cassette{Turns: []ScriptTurn{{
		Model:  llm.Model{Provider: "fixture", ID: "model-1", Route: "chat"},
		Events: []llm.LLMEvent{llm.TextStart{ID: "text-1"}, llm.TextDelta{ID: "text-1", Text: "late"}}, Delay: 30 * time.Millisecond,
	}}})
	if err != nil {
		t.Fatalf("new fake provider: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if err := provider.RunTurn(ctx, ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "fixture", ID: "model-1", Route: "chat"}}}, LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return nil })); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled fake turn error = %v, want deadline", err)
	}

	provider, err = NewFakeProvider(Cassette{Turns: []ScriptTurn{{
		Model: llm.Model{Provider: "fixture", ID: "model-1", Route: "chat"}, Events: []llm.LLMEvent{llm.TextStart{ID: "text-1"}},
	}}})
	if err != nil {
		t.Fatalf("new sink failure provider: %v", err)
	}
	failure := errors.New("slow sink")
	if err := provider.RunTurn(context.Background(), ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "fixture", ID: "model-1", Route: "chat"}}}, LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return failure })); !errors.Is(err, ErrSink) || !errors.Is(err, failure) {
		t.Fatalf("sink failure = %v, want ErrSink and cause", err)
	}
}

func TestFakeProviderRejectsInvalidCassetteAndRequest(t *testing.T) {
	if _, err := NewFakeProvider(Cassette{}); !errors.Is(err, ErrInvalidCassette) {
		t.Fatalf("empty cassette error = %v", err)
	}
	provider, err := NewFakeProvider(Cassette{Turns: []ScriptTurn{{
		Model: llm.Model{Provider: "fixture", ID: "model-1", Route: "chat"}, Events: []llm.LLMEvent{llm.TextStart{ID: "text-1"}},
	}}})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	request := ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "fixture", ID: "model-1"}}, Attempt: -1}
	if !errors.Is(provider.RunTurn(context.Background(), request, LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return nil })), ErrInvalidRequest) {
		t.Fatal("invalid request was accepted")
	}
}
