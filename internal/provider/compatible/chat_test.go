package compatible

import (
	"bytes"
	"context"
	"encoding/json"
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
)

func TestChatProjectsToolResponseFormatDeterministically(t *testing.T) {
	adapter := NewChatProvider(Config{Endpoint: "https://example.invalid/v1/chat/completions"})
	request := toolFormatRequest("local", "chat")
	first, err := adapter.ProjectRequest(request)
	if err != nil {
		t.Fatalf("project request: %v", err)
	}
	second, err := adapter.ProjectRequest(request)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("projection is not deterministic: %s / %s / %v", first, second, err)
	}
	var body map[string]any
	if err := json.Unmarshal(first, &body); err != nil {
		t.Fatal(err)
	}
	tools, _ := body["tools"].([]any)
	choice, _ := body["tool_choice"].(map[string]any)
	function, _ := choice["function"].(map[string]any)
	if len(tools) != 2 || choice["type"] != "function" || function["name"] != "emit_result" {
		t.Fatalf("effective tool projection = %s", first)
	}
	formatTool, _ := tools[1].(map[string]any)
	formatFunction, _ := formatTool["function"].(map[string]any)
	if formatFunction["name"] != "emit_result" || formatFunction["description"] != "structured output" || formatFunction["parameters"] == nil {
		t.Fatalf("format tool = %#v", formatTool)
	}
}

func TestChatProjectsCanonicalMessagesGenerationAndOptions(t *testing.T) {
	name := "lookup"
	maxTokens, temperature, topP, frequency, presence, seed := 64.0, 0.3, 0.7, 0.1, 0.2, 42.0
	request := provider.ProviderTurnRequest{Request: llm.Request{
		Model:  llm.Model{Provider: "local", ID: "local-model", Route: "chat"},
		System: []llm.SystemPart{{Text: "system"}},
		Messages: []llm.Message{
			{Role: llm.MessageRoleAssistant, Content: []llm.Content{llm.TextContent{Text: "calling"}, llm.ToolCallContent{ID: "call_1", Name: name, Input: domain.JSONObject(map[string]domain.JSONValue{"q": domain.JSONString("x")})}}},
			{Role: llm.MessageRoleTool, Content: []llm.Content{
				llm.ToolResultContent{ID: "call_1", Name: name, Result: domain.JSONObject(map[string]domain.JSONValue{"answer": domain.JSONString("one")})},
				llm.ToolResultContent{ID: "call_2", Name: name, Result: domain.JSONObject(map[string]domain.JSONValue{"answer": domain.JSONString("two")})},
			}},
		},
		Tools:      []llm.ToolDefinition{{Name: name, Description: "lookup", InputSchema: llm.JSONSchema{"type": domain.JSONString("object")}}},
		ToolChoice: &llm.ToolChoice{Type: llm.ToolChoiceNamed, Name: &name},
		Generation: &llm.GenerationOptions{MaxTokens: &maxTokens, Temperature: &temperature, TopP: &topP, FrequencyPenalty: &frequency, PresencePenalty: &presence, Seed: &seed, Stop: []string{"stop"}},
		ResponseFormat: &llm.ResponseFormat{Type: llm.ResponseFormatJSON, Schema: llm.JSONSchema{
			"type": domain.JSONString("object"),
		}},
		ProviderOptions: llm.ProviderMetadata{"compatible": {"parallel_tool_calls": domain.JSONBool(false)}},
		HTTP:            &llm.HTTPOptions{Body: llm.JSONSchema{"custom": domain.JSONString("body")}},
	}}
	projected, err := NewChatProvider(Config{Endpoint: "https://example.invalid/v1/chat/completions"}).ProjectRequest(request)
	if err != nil {
		t.Fatalf("project request: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(projected, &body); err != nil {
		t.Fatal(err)
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 4 || body["max_tokens"] != float64(64) || body["frequency_penalty"] != 0.1 || body["presence_penalty"] != 0.2 || body["seed"] != float64(42) || body["parallel_tool_calls"] != false || body["custom"] != "body" {
		t.Fatalf("canonical projection = %s", projected)
	}
	for _, index := range []int{2, 3} {
		message, _ := messages[index].(map[string]any)
		if message["role"] != "tool" || message["tool_call_id"] == nil {
			t.Fatalf("tool result message %d = %#v", index, message)
		}
	}
	if body["response_format"] == nil {
		t.Fatalf("response format missing: %s", projected)
	}
}

func TestChatRejectsUnknownCatalogRoute(t *testing.T) {
	catalog, err := provider.NewCatalog([]provider.Route{{
		Provider: "local", ModelID: "other", Name: "chat", API: provider.APITypeOpenAICompatible,
		Capabilities: provider.Capabilities{Text: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewChatProvider(Config{Catalog: catalog, Endpoint: "https://example.invalid"}).ProjectRequest(provider.ProviderTurnRequest{Request: llm.Request{
		Model: llm.Model{Provider: "local", ID: "local-model", Route: "chat"},
	}})
	if !errors.Is(err, provider.ErrUnsupportedRoute) {
		t.Fatalf("route error = %v", err)
	}
}

func TestChatProjectAndNormalizeText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer compatible-secret" {
			t.Errorf("authorization header = %q", request.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), `"model":"local-model"`) || !strings.Contains(string(body), `"stream":true`) {
			t.Errorf("request projection = %s", body)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"id\":\"chat_1\",\"model\":\"local-model\",\"system_fingerprint\":\"fp_1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(writer, "data: {\"id\":\"chat_1\",\"model\":\"local-model\",\"system_fingerprint\":\"fp_1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3,\"prompt_tokens_details\":{\"cached_tokens\":1},\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\n")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()
	adapter := NewChatProvider(Config{Client: server.Client(), Endpoint: server.URL, APIKey: "compatible-secret"})
	request := provider.ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "local", ID: "local-model", Route: "chat"}, Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{llm.TextContent{Text: "hi"}}}}}}
	events := collectEvents(t, adapter, request)
	if err := provider.ValidateStream(events); err != nil {
		t.Fatalf("normalized stream: %v", err)
	}
	if events[2].(llm.TextDelta).Text != "hello" || events[4].(llm.StepFinish).Usage == nil {
		t.Fatalf("events = %#v", events)
	}
	usage := events[4].(llm.StepFinish).Usage
	if usage.CacheReadInputTokens == nil || *usage.CacheReadInputTokens != 1 || usage.ReasoningTokens == nil || *usage.ReasoningTokens != 1 {
		t.Fatalf("usage = %+v", usage)
	}
	if metadata := events[4].(llm.StepFinish).ProviderMetadata["compatible"]; metadata["id"].String != "chat_1" || metadata["system_fingerprint"].String != "fp_1" {
		t.Fatalf("provider metadata = %+v", metadata)
	}
}

func TestChatErrorEventClassifiesOverflowAndRedacts(t *testing.T) {
	const secret = "prompt-tool-credential-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, `data: {"error":{"code":"context_length_exceeded","type":"`+secret+`","message":"`+secret+`"}}`+"\n\n")
	}))
	defer server.Close()
	events := collectEvents(t, NewChatProvider(Config{Client: server.Client(), Endpoint: server.URL}), provider.ProviderTurnRequest{Request: llm.Request{
		Model: llm.Model{Provider: "local", ID: "local-model", Route: "chat"},
	}})
	failure := events[1].(llm.ProviderError)
	if failure.Classification == nil || *failure.Classification != llm.ProviderFailureContextOverflow || failure.Retryable == nil || *failure.Retryable || strings.Contains(failure.Message, secret) {
		t.Fatalf("provider failure = %+v", failure)
	}
	if encoded := failure.ProviderMetadata["compatible"]["event"].String; encoded != "error" {
		t.Fatalf("safe error metadata = %+v", failure.ProviderMetadata)
	}
}

func TestChatMalformedDisconnectTerminalTailAndSinkFailure(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		kind   error
	}{
		{name: "malformed", stream: "data: {bad\n\n", kind: provider.ErrMalformedFrame},
		{name: "disconnect", stream: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n", kind: provider.ErrStreamMissingFinal},
		{name: "terminal tail", stream: "data: [DONE]\n\ndata: [DONE]\n\n", kind: ErrProtocol},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(writer, test.stream)
			}))
			defer server.Close()
			err := NewChatProvider(Config{Client: server.Client(), Endpoint: server.URL}).RunTurn(context.Background(), provider.ProviderTurnRequest{Request: llm.Request{
				Model: llm.Model{Provider: "local", ID: "local-model", Route: "chat"},
			}}, provider.LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return nil }))
			if !errors.Is(err, test.kind) {
				t.Fatalf("error = %v, want %v", err, test.kind)
			}
		})
	}
	t.Run("sink failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"text\"}}]}\n\n")
		}))
		defer server.Close()
		failure := errors.New("sink stopped")
		err := NewChatProvider(Config{Client: server.Client(), Endpoint: server.URL}).RunTurn(context.Background(), provider.ProviderTurnRequest{Request: llm.Request{
			Model: llm.Model{Provider: "local", ID: "local-model", Route: "chat"},
		}}, provider.LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return failure }))
		if !errors.Is(err, provider.ErrSink) || !errors.Is(err, failure) {
			t.Fatalf("sink error = %v", err)
		}
	})
}

func TestChatToolArgumentsAndReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"think\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(writer, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()
	adapter := NewChatProvider(Config{Client: server.Client(), Endpoint: server.URL})
	request := provider.ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "local", ID: "local-model", Route: "chat"}}}
	events := collectEvents(t, adapter, request)
	if err := provider.ValidateStream(events); err != nil {
		t.Fatalf("normalized stream: %v", err)
	}
	var call llm.ToolCall
	for _, event := range events {
		if value, ok := event.(llm.ToolCall); ok {
			call = value
		}
	}
	if call.Input.Kind != domain.JSONKindObject || call.Input.Object["q"].String != "x" {
		t.Fatalf("tool call = %+v", call)
	}
}

func TestChatCancellationInterruptsHTTPStream(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()
	adapter := NewChatProvider(Config{Client: server.Client(), Endpoint: server.URL})
	request := provider.ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "local", ID: "local-model", Route: "chat"}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- adapter.RunTurn(ctx, request, provider.LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return nil }))
	}()
	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("HTTP stream did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled HTTP stream did not return")
	}
}

func TestChatAppliesExplicitHTTPOptionsWithoutPreviewLeak(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("api-version") != "v1" || request.Header.Get("X-Route") != "route-value" {
			t.Errorf("options: query=%s header=%s", request.URL.RawQuery, request.Header.Get("X-Route"))
		}
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), `"custom":true`) {
			t.Errorf("custom body option missing: %s", body)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()
	adapter := NewChatProvider(Config{Client: server.Client(), Endpoint: server.URL})
	request := provider.ProviderTurnRequest{Request: llm.Request{
		Model: llm.Model{Provider: "local", ID: "local-model", Route: "chat"},
		HTTP: &llm.HTTPOptions{Body: llm.JSONSchema{"custom": domain.JSONBool(true)},
			Headers: map[string]string{"X-Route": "route-value"}, Query: map[string]string{"api-version": "v1"}},
	}}
	collectEvents(t, adapter, request)
}

func collectEvents(t *testing.T, adapter provider.ProviderPort, request provider.ProviderTurnRequest) []llm.LLMEvent {
	t.Helper()
	events := make([]llm.LLMEvent, 0)
	err := adapter.RunTurn(context.Background(), request, provider.LLMEventSinkFunc(func(_ context.Context, event llm.LLMEvent) error {
		events = append(events, event)
		return nil
	}))
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	return events
}

func toolFormatRequest(providerName, route string) provider.ProviderTurnRequest {
	return provider.ProviderTurnRequest{Request: llm.Request{
		Model:      llm.Model{Provider: providerName, ID: "model", Route: route},
		Tools:      []llm.ToolDefinition{{Name: "lookup", InputSchema: llm.JSONSchema{"type": domain.JSONString("object")}}},
		ToolChoice: &llm.ToolChoice{Type: llm.ToolChoiceNone},
		ResponseFormat: &llm.ResponseFormat{Type: llm.ResponseFormatTool, Tool: &llm.ToolDefinition{
			Name: "emit_result", Description: "structured output",
			InputSchema: llm.JSONSchema{"type": domain.JSONString("object"), "required": domain.JSONArray([]domain.JSONValue{domain.JSONString("answer")})},
		}},
	}}
}
