package provider_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
	"github.com/Hz-186/opencode-go-py/internal/provider"
	"github.com/Hz-186/opencode-go-py/internal/provider/anthropic"
	"github.com/Hz-186/opencode-go-py/internal/provider/compatible"
	"github.com/Hz-186/opencode-go-py/internal/provider/openai"
)

func TestTier1AdaptersUseOneAttemptCredentialLeaseAndRedact401(t *testing.T) {
	tests := []struct {
		name       string
		model      llm.Model
		headerName string
		headerWant string
		newAdapter func(*http.Client, string, provider.CredentialSource) provider.ProviderPort
	}{
		{name: "OpenAI Responses", model: llm.Model{Provider: "openai", ID: "model", Route: "responses"}, headerName: "Authorization", headerWant: "Bearer lease-secret", newAdapter: func(client *http.Client, endpoint string, credential provider.CredentialSource) provider.ProviderPort {
			return openai.NewResponsesProvider(openai.Config{Client: client, Endpoint: endpoint, Credential: credential})
		}},
		{name: "Anthropic Messages", model: llm.Model{Provider: "anthropic", ID: "model", Route: "messages"}, headerName: "x-api-key", headerWant: "lease-secret", newAdapter: func(client *http.Client, endpoint string, credential provider.CredentialSource) provider.ProviderPort {
			return anthropic.NewMessagesProvider(anthropic.Config{Client: client, Endpoint: endpoint, Credential: credential})
		}},
		{name: "OpenAI compatible", model: llm.Model{Provider: "local", ID: "model", Route: "chat"}, headerName: "Authorization", headerWant: "Bearer lease-secret", newAdapter: func(client *http.Client, endpoint string, credential provider.CredentialSource) provider.ProviderPort {
			return compatible.NewChatProvider(compatible.Config{Client: client, Endpoint: endpoint, Credential: credential})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const errorSecret = "raw-error-body-secret"
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Header.Get(test.headerName) != test.headerWant || request.Header.Get("X-Route") != "route-value" || request.URL.Query().Get("api-version") != "v1" {
					t.Errorf("headers/query = %v / %s", request.Header, request.URL.RawQuery)
				}
				body, _ := io.ReadAll(request.Body)
				if !strings.Contains(string(body), `"custom":"body"`) {
					t.Errorf("HTTP body option missing: %s", body)
				}
				writer.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(writer, errorSecret)
			}))
			defer server.Close()

			secret := []byte("lease-secret")
			releases := 0
			credential := provider.CredentialSourceFunc(func(context.Context) ([]byte, func(), error) {
				return secret, func() { clear(secret); releases++ }, nil
			})
			request := provider.ProviderTurnRequest{Request: llm.Request{
				Model: test.model,
				HTTP: &llm.HTTPOptions{
					Body:    llm.JSONSchema{"custom": domain.JSONString("body")},
					Headers: map[string]string{"X-Route": "route-value"},
					Query:   map[string]string{"api-version": "v1"},
				},
			}}
			err := test.newAdapter(server.Client(), server.URL, credential).RunTurn(context.Background(), request, provider.LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return nil }))
			var attempt *provider.AttemptError
			if !errors.As(err, &attempt) || attempt.Status != http.StatusUnauthorized || releases != 1 {
				t.Fatalf("401/lease result: error=%v releases=%d", err, releases)
			}
			for index, value := range secret {
				if value != 0 {
					t.Fatalf("credential byte %d was not cleared", index)
				}
			}
			for _, forbidden := range []string{"lease-secret", errorSecret, "route-value", "api-version"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("diagnostic leaked %q: %v", forbidden, err)
				}
			}
		})
	}
}

func TestTier1AdaptersCancelInFlightHTTPStream(t *testing.T) {
	tests := []struct {
		name       string
		model      llm.Model
		newAdapter func(*http.Client, string) provider.ProviderPort
	}{
		{name: "OpenAI Responses", model: llm.Model{Provider: "openai", ID: "model", Route: "responses"}, newAdapter: func(client *http.Client, endpoint string) provider.ProviderPort {
			return openai.NewResponsesProvider(openai.Config{Client: client, Endpoint: endpoint})
		}},
		{name: "Anthropic Messages", model: llm.Model{Provider: "anthropic", ID: "model", Route: "messages"}, newAdapter: func(client *http.Client, endpoint string) provider.ProviderPort {
			return anthropic.NewMessagesProvider(anthropic.Config{Client: client, Endpoint: endpoint})
		}},
		{name: "OpenAI compatible", model: llm.Model{Provider: "local", ID: "model", Route: "chat"}, newAdapter: func(client *http.Client, endpoint string) provider.ProviderPort {
			return compatible.NewChatProvider(compatible.Config{Client: client, Endpoint: endpoint})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				writer.WriteHeader(http.StatusOK)
				writer.(http.Flusher).Flush()
				close(started)
				<-request.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				done <- test.newAdapter(server.Client(), server.URL).RunTurn(ctx, provider.ProviderTurnRequest{Request: llm.Request{Model: test.model}}, provider.LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return nil }))
			}()
			select {
			case <-started:
				cancel()
			case <-time.After(2 * time.Second):
				cancel()
				t.Fatal("HTTP stream did not start")
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel error = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("canceled adapter did not return")
			}
		})
	}
}

func TestTier1AdaptersNormalizePreStreamContextOverflow(t *testing.T) {
	tests := []struct {
		name       string
		model      llm.Model
		namespace  string
		newAdapter func(*http.Client, string) provider.ProviderPort
	}{
		{name: "OpenAI Responses", model: llm.Model{Provider: "openai", ID: "model", Route: "responses"}, namespace: "openai", newAdapter: func(client *http.Client, endpoint string) provider.ProviderPort {
			return openai.NewResponsesProvider(openai.Config{Client: client, Endpoint: endpoint})
		}},
		{name: "Anthropic Messages", model: llm.Model{Provider: "anthropic", ID: "model", Route: "messages"}, namespace: "anthropic", newAdapter: func(client *http.Client, endpoint string) provider.ProviderPort {
			return anthropic.NewMessagesProvider(anthropic.Config{Client: client, Endpoint: endpoint})
		}},
		{name: "OpenAI compatible", model: llm.Model{Provider: "local", ID: "model", Route: "chat"}, namespace: "compatible", newAdapter: func(client *http.Client, endpoint string) provider.ProviderPort {
			return compatible.NewChatProvider(compatible.Config{Client: client, Endpoint: endpoint})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const secret = "raw-overflow-prompt-secret"
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(writer, `{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"`+secret+`"}}`)
			}))
			defer server.Close()
			var events []llm.LLMEvent
			err := test.newAdapter(server.Client(), server.URL).RunTurn(context.Background(), provider.ProviderTurnRequest{Request: llm.Request{Model: test.model}}, provider.LLMEventSinkFunc(func(_ context.Context, event llm.LLMEvent) error {
				events = append(events, event)
				return nil
			}))
			if err != nil {
				t.Fatalf("run turn: %v", err)
			}
			if len(events) != 2 {
				t.Fatalf("events = %#v", events)
			}
			failure, ok := events[1].(llm.ProviderError)
			if !ok || failure.Classification == nil || *failure.Classification != llm.ProviderFailureContextOverflow || failure.Retryable == nil || *failure.Retryable || strings.Contains(failure.Message, secret) {
				t.Fatalf("provider failure = %#v", events[1])
			}
			metadata := failure.ProviderMetadata[test.namespace]
			if metadata["source"].String != "http" || metadata["status"].Number != "400" {
				t.Fatalf("failure metadata = %+v", failure.ProviderMetadata)
			}
		})
	}
}
