package provider

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

type retryFixtureProvider struct {
	errors []error
	calls  []ProviderTurnRequest
}

func (provider *retryFixtureProvider) RunTurn(_ context.Context, request ProviderTurnRequest, _ LLMEventSink) error {
	provider.calls = append(provider.calls, request)
	index := len(provider.calls) - 1
	if index < len(provider.errors) {
		return provider.errors[index]
	}
	return nil
}

func retryRequest() ProviderTurnRequest {
	return ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "fixture", ID: "model-1", Route: "chat"}}}
}

func TestRunWithRetryRetriesOnlyUnstartedWhitelistedTransportAndReportsDiagnostic(t *testing.T) {
	provider := &retryFixtureProvider{errors: []error{
		mustAttemptError(t, 429, false, 3*time.Second),
		nil,
	}}
	policy := RetryPolicy{MaxAttempts: 3, BaseDelay: 20 * time.Millisecond, MaxDelay: 50 * time.Millisecond,
		Sleep: func(_ context.Context, delay time.Duration) error {
			if delay != 3*time.Second && delay != 50*time.Millisecond {
				return errors.New("unexpected retry delay")
			}
			return nil
		}}
	var diagnostics []RetryDiagnostic
	if err := RunWithRetry(context.Background(), provider, retryRequest(), LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return nil }), policy, func(value RetryDiagnostic) { diagnostics = append(diagnostics, value) }); err != nil {
		t.Fatalf("retry run: %v", err)
	}
	if len(provider.calls) != 2 || provider.calls[0].Attempt != 0 || provider.calls[1].Attempt != 1 {
		t.Fatalf("provider calls = %+v", provider.calls)
	}
	want := []RetryDiagnostic{{Attempt: 1, NextAttempt: 2, Status: 429, Delay: 50 * time.Millisecond, Reason: "retryable transport before stream start"}}
	if !reflect.DeepEqual(diagnostics, want) {
		t.Fatalf("diagnostics = %+v, want %+v", diagnostics, want)
	}
}

func TestRunWithRetryDoesNotRetryNonWhitelistedOrStartedFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "bad request", err: mustAttemptError(t, 400, false, 0)},
		{name: "stream started", err: mustAttemptError(t, 500, true, 0)},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &retryFixtureProvider{errors: []error{test.err, nil}}
			if err := RunWithRetry(context.Background(), provider, retryRequest(), LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return nil }), RetryPolicy{MaxAttempts: 3}, nil); !errors.Is(err, test.err) {
				t.Fatalf("retry error = %v, want original attempt error", err)
			}
			if len(provider.calls) != 1 {
				t.Fatalf("non-retryable failure calls = %d, want 1", len(provider.calls))
			}
		})
	}
}

func TestRunWithRetryAttemptNumbersAreLinearFromCallerBase(t *testing.T) {
	fixture := &retryFixtureProvider{errors: []error{
		mustAttemptError(t, 503, false, 0),
		mustAttemptError(t, 503, false, 0),
		nil,
	}}
	request := retryRequest()
	request.Attempt = 7
	policy := RetryPolicy{MaxAttempts: 3, BaseDelay: 0, MaxDelay: 0}
	if err := RunWithRetry(context.Background(), fixture, request, LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return nil }), policy, nil); err != nil {
		t.Fatalf("retry run: %v", err)
	}
	want := []int{7, 8, 9}
	for index, call := range fixture.calls {
		if call.Attempt != want[index] {
			t.Fatalf("attempts = %+v, want %v", fixture.calls, want)
		}
	}
}

func TestRunWithRetryCancellationStopsBackoff(t *testing.T) {
	provider := &retryFixtureProvider{errors: []error{mustAttemptError(t, 503, false, 0), nil}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	policy := RetryPolicy{MaxAttempts: 2, BaseDelay: time.Second, MaxDelay: time.Second, Sleep: func(ctx context.Context, _ time.Duration) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}
	done := make(chan error, 1)
	go func() {
		done <- RunWithRetry(ctx, provider, retryRequest(), LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return nil }), policy, nil)
	}()
	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("retry backoff did not start")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled retry = %v, want context.Canceled", err)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("canceled retry calls = %d, want 1", len(provider.calls))
	}
}

func TestNewAttemptErrorRejectsInvalidBounds(t *testing.T) {
	if !errors.Is(mustAttemptError(t, 500, false, 0), ErrTransportAttempt) {
		t.Fatal("valid attempt error does not unwrap ErrTransportAttempt")
	}
	if !errors.Is(func() error { return NewAttemptError(700, false, 0, nil) }(), ErrInvalidRetryPolicy) {
		t.Fatal("invalid status was accepted")
	}
}

func mustAttemptError(t *testing.T, status int, started bool, retryAfter time.Duration) error {
	t.Helper()
	return NewAttemptError(status, started, retryAfter, errors.New("fixture transport failure"))
}
