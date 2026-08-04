package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

func TestReadSSEJoinsDataAndIgnoresExtensionFields(t *testing.T) {
	input := "id: 1\nevent: chunk\nx-provider: ignored\ndata: first\ndata: second\n\n: heartbeat\n\n"
	var frames []SSEFrame
	if err := ReadSSE(context.Background(), strings.NewReader(input), 1024, func(frame SSEFrame) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		t.Fatalf("read SSE: %v", err)
	}
	want := []SSEFrame{{Event: "chunk", Data: "first\nsecond"}}
	if !reflect.DeepEqual(frames, want) {
		t.Fatalf("frames = %+v, want %+v", frames, want)
	}
}

func TestDoHTTPClassifies401WithoutLeakingBody(t *testing.T) {
	const secret = "provider-error-body-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(writer, secret)
	}))
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL, nil)
	_, err := DoHTTP(context.Background(), server.Client(), request, 1024)
	var attempt *AttemptError
	if !errors.As(err, &attempt) || attempt.Status != http.StatusUnauthorized || attempt.StreamStarted || strings.Contains(err.Error(), secret) {
		t.Fatalf("401 error = %v", err)
	}
}

func TestDoHTTPClassifiesBoundedContextOverflowWithoutRetainingBody(t *testing.T) {
	const secret = "prompt-and-tool-body-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(writer, `{"error":{"code":"context_length_exceeded","message":"`+secret+`"}}`)
	}))
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL, nil)
	_, err := DoHTTP(context.Background(), server.Client(), request, 1024)
	var failure *ProviderHTTPFailure
	if !errors.As(err, &failure) || failure.Classification == nil || *failure.Classification != llm.ProviderFailureContextOverflow || failure.Retryable == nil || *failure.Retryable || !errors.Is(err, ErrProviderReject) || strings.Contains(err.Error(), secret) {
		t.Fatalf("overflow HTTP error = %v / %+v", err, failure)
	}
}

func TestSingleAttemptClientDoesNotFollowRedirectFallback(t *testing.T) {
	targetHits := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetHits++ }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", target.URL)
		writer.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	request, _ := http.NewRequest(http.MethodPost, redirect.URL, strings.NewReader(`{"prompt":"secret"}`))
	_, err := DoHTTP(context.Background(), SingleAttemptClient(redirect.Client()), request, 1024)
	var attempt *AttemptError
	if !errors.As(err, &attempt) || attempt.Status != http.StatusTemporaryRedirect || targetHits != 0 {
		t.Fatalf("redirect error=%v targetHits=%d", err, targetHits)
	}
}

func TestBoundedBodyFailureRemainsTypedAndRedacted(t *testing.T) {
	const secret = "response-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: "+strings.Repeat(secret, 16)+"\n\n")
	}))
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL, nil)
	response, err := DoHTTP(context.Background(), server.Client(), request, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	err = ReadSSE(context.Background(), response.Body, 1024, func(SSEFrame) error { return nil })
	if !errors.Is(err, ErrMalformedFrame) || !errors.Is(err, ErrBodyLimit) || strings.Contains(err.Error(), secret) {
		t.Fatalf("bounded body error = %v", err)
	}
}

func TestReadSSERejectsOversizedLineWithoutEchoingIt(t *testing.T) {
	secret := strings.Repeat("secret-token", 20)
	err := ReadSSE(context.Background(), strings.NewReader("data: "+secret+"\n\n"), 32, func(SSEFrame) error { return nil })
	if !errors.Is(err, ErrMalformedFrame) || strings.Contains(err.Error(), secret) {
		t.Fatalf("bounded SSE error = %v", err)
	}
}

func TestEventEmitterDoesNotExposeStepStartBeforeSemanticEvent(t *testing.T) {
	var events []llm.LLMEvent
	emitter, err := NewEventEmitter(context.Background(), LLMEventSinkFunc(func(_ context.Context, event llm.LLMEvent) error {
		events = append(events, event)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := emitter.Emit(llm.StepStart{Index: 0}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 || emitter.Started {
		t.Fatalf("step start escaped before semantic event: events=%d started=%t", len(events), emitter.Started)
	}
	if err := emitter.Emit(llm.TextStart{ID: "text-0"}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || !emitter.Started {
		t.Fatalf("emitter state: events=%d started=%t", len(events), emitter.Started)
	}
}

func TestClassifyProviderErrorWithoutRetainingMessage(t *testing.T) {
	classification, retryable := ClassifyProviderError("context_length_exceeded", "invalid_request", "prompt contains a secret")
	if classification == nil || *classification != llm.ProviderFailureContextOverflow || retryable == nil || *retryable {
		t.Fatalf("overflow classification = %v retryable=%v", classification, retryable)
	}
	classification, retryable = ClassifyProviderError("rate_limit_error", "", "")
	if classification != nil || retryable == nil || !*retryable {
		t.Fatalf("rate limit classification = %v retryable=%v", classification, retryable)
	}
}
