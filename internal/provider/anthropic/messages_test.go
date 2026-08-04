package anthropic

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

	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
	"github.com/Hz-186/opencode-go-py/internal/provider"
)

func TestMessagesProjectsToolResponseFormatDeterministically(t *testing.T) {
	adapter := NewMessagesProvider(Config{})
	request := toolFormatRequest("anthropic", "messages")
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
	if len(tools) != 2 || choice["type"] != "tool" || choice["name"] != "emit_result" {
		t.Fatalf("effective tool projection = %s", first)
	}
	formatTool, _ := tools[1].(map[string]any)
	if formatTool["name"] != "emit_result" || formatTool["description"] != "structured output" || formatTool["input_schema"] == nil {
		t.Fatalf("format tool = %#v", formatTool)
	}
}

func TestMessagesProjectsCanonicalMessagesGenerationAndOptions(t *testing.T) {
	name := "lookup"
	maxTokens, temperature, topP, topK := 256.0, 0.2, 0.8, 40.0
	request := provider.ProviderTurnRequest{Request: llm.Request{
		Model:  llm.Model{Provider: "anthropic", ID: "claude-test", Route: "messages"},
		System: []llm.SystemPart{{Text: "system", Cache: &llm.CacheHint{Type: llm.CacheHintEphemeral}}},
		Messages: []llm.Message{
			{Role: llm.MessageRoleSystem, Content: []llm.Content{llm.TextContent{Text: "more system"}}},
			{Role: llm.MessageRoleUser, Content: []llm.Content{llm.TextContent{Text: "question"}}},
			{Role: llm.MessageRoleAssistant, Content: []llm.Content{llm.ToolCallContent{ID: "call_1", Name: name, Input: domain.JSONObject(map[string]domain.JSONValue{"q": domain.JSONString("x")})}}},
			{Role: llm.MessageRoleTool, Content: []llm.Content{llm.ToolResultContent{ID: "call_1", Name: name, Result: domain.JSONObject(map[string]domain.JSONValue{"answer": domain.JSONString("y")})}}},
		},
		Tools:      []llm.ToolDefinition{{Name: name, Description: "lookup", InputSchema: llm.JSONSchema{"type": domain.JSONString("object")}, Cache: &llm.CacheHint{Type: llm.CacheHintEphemeral}}},
		ToolChoice: &llm.ToolChoice{Type: llm.ToolChoiceNamed, Name: &name},
		Generation: &llm.GenerationOptions{MaxTokens: &maxTokens, Temperature: &temperature, TopP: &topP, TopK: &topK, Stop: []string{"stop"}},
		ResponseFormat: &llm.ResponseFormat{Type: llm.ResponseFormatJSON, Schema: llm.JSONSchema{
			"type": domain.JSONString("object"),
		}},
		ProviderOptions: llm.ProviderMetadata{"anthropic": {"metadata": domain.JSONObject(map[string]domain.JSONValue{"user_id": domain.JSONString("safe-id")})}},
		HTTP:            &llm.HTTPOptions{Body: llm.JSONSchema{"custom": domain.JSONBool(true)}},
	}}
	projected, err := NewMessagesProvider(Config{}).ProjectRequest(request)
	if err != nil {
		t.Fatalf("project request: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(projected, &body); err != nil {
		t.Fatal(err)
	}
	system, _ := body["system"].([]any)
	messages, _ := body["messages"].([]any)
	tools, _ := body["tools"].([]any)
	if len(system) != 2 || len(messages) != 3 || len(tools) != 1 || body["stream"] != true || body["max_tokens"] != float64(256) || body["top_k"] != float64(40) || body["custom"] != true || body["metadata"] == nil {
		t.Fatalf("canonical projection = %s", projected)
	}
	tool, _ := tools[0].(map[string]any)
	if tool["cache_control"] == nil || body["output_config"] == nil {
		t.Fatalf("cache/response format projection = %s", projected)
	}
}

func TestMessagesRejectsMissingToolCapability(t *testing.T) {
	catalog, err := provider.NewCatalog([]provider.Route{{
		Provider: "anthropic", ModelID: "model", Name: "messages", API: provider.APITypeAnthropicMessages,
		Capabilities: provider.Capabilities{Text: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	request := toolFormatRequest("anthropic", "messages")
	_, err = NewMessagesProvider(Config{Catalog: catalog}).ProjectRequest(request)
	if !errors.Is(err, provider.ErrUnsupportedCapability) {
		t.Fatalf("capability error = %v", err)
	}
}

func TestMessagesToolChoiceNoneOmitsNativeTools(t *testing.T) {
	adapter := NewMessagesProvider(Config{})
	request := toolFormatRequest("anthropic", "messages")
	request.Request.ResponseFormat = nil
	projected, err := adapter.ProjectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(projected, &body); err != nil {
		t.Fatal(err)
	}
	if _, exists := body["tools"]; exists {
		t.Fatalf("Anthropic none choice exposed tools: %s", projected)
	}
	if _, exists := body["tool_choice"]; exists {
		t.Fatalf("Anthropic none choice projected unsupported native choice: %s", projected)
	}
}

func TestMessagesProjectAndNormalizeText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("x-api-key") != "anthropic-secret" || request.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("headers = %v", request.Header)
		}
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), `"model":"claude-test"`) || !strings.Contains(string(body), `"max_tokens":4096`) {
			t.Errorf("request projection = %s", body)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":2,\"cache_read_input_tokens\":1,\"cache_creation_input_tokens\":1}}}\n\n")
		_, _ = io.WriteString(writer, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n")
		_, _ = io.WriteString(writer, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
		_, _ = io.WriteString(writer, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = io.WriteString(writer, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n")
		_, _ = io.WriteString(writer, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	adapter := NewMessagesProvider(Config{Client: server.Client(), Endpoint: server.URL, APIKey: "anthropic-secret"})
	request := provider.ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "anthropic", ID: "claude-test", Route: "messages"}, Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{llm.TextContent{Text: "hi"}}}}}}
	events := collectEvents(t, adapter, request)
	if err := provider.ValidateStream(events); err != nil {
		t.Fatalf("normalized stream: %v", err)
	}
	if got := events[2].(llm.TextDelta).Text; got != "hello" {
		t.Fatalf("text delta = %q", got)
	}
	usage := events[4].(llm.StepFinish).Usage
	if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 4 || usage.OutputTokens == nil || *usage.OutputTokens != 2 || usage.CacheReadInputTokens == nil || *usage.CacheReadInputTokens != 1 || usage.CacheWriteInputTokens == nil || *usage.CacheWriteInputTokens != 1 {
		t.Fatalf("usage = %+v", usage)
	}
	if metadata := events[4].(llm.StepFinish).ProviderMetadata["anthropic"]; metadata["id"].String != "msg_1" || metadata["model"].String != "claude-test" {
		t.Fatalf("provider metadata = %+v", metadata)
	}
}

func TestMessagesToolInputNormalizes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool_1\",\"name\":\"lookup\",\"input\":{}}}\n\n")
		_, _ = io.WriteString(writer, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"x\"}"}}`+"\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	adapter := NewMessagesProvider(Config{Client: server.Client(), Endpoint: server.URL})
	request := provider.ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "anthropic", ID: "claude-test", Route: "messages"}}}
	events := collectEvents(t, adapter, request)
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

func TestMessagesReasoningSignatureNormalizes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\"}}\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"plan\"}}\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig\"}}\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	events := collectEvents(t, NewMessagesProvider(Config{Client: server.Client(), Endpoint: server.URL}), provider.ProviderTurnRequest{Request: llm.Request{
		Model: llm.Model{Provider: "anthropic", ID: "claude-test", Route: "messages"},
	}})
	if err := provider.ValidateStream(events); err != nil {
		t.Fatalf("normalized stream: %v", err)
	}
	if events[2].(llm.ReasoningDelta).Text != "plan" || events[3].(llm.ReasoningEnd).ProviderMetadata["anthropic"]["signature"].String != "sig" {
		t.Fatalf("reasoning events = %#v", events)
	}
}

func TestMessagesErrorEventIsTerminalAndRedacted(t *testing.T) {
	const secret = "anthropic-secret-prompt"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, `event: error
data: {"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"`+secret+`"}}

`)
	}))
	defer server.Close()
	adapter := NewMessagesProvider(Config{Client: server.Client(), Endpoint: server.URL})
	request := provider.ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "anthropic", ID: "claude-test", Route: "messages"}}}
	events := collectEvents(t, adapter, request)
	if len(events) != 2 {
		t.Fatalf("events = %d, want step-start/provider-error", len(events))
	}
	failure := events[1].(llm.ProviderError)
	if strings.Contains(failure.Message, secret) || failure.Message == "" || failure.Classification == nil || *failure.Classification != llm.ProviderFailureContextOverflow || failure.Retryable == nil || *failure.Retryable {
		t.Fatalf("unsafe provider failure = %+v", failure)
	}
}

func TestMessagesMalformedFrameIsPreStreamAttempt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {bad\n\n")
	}))
	defer server.Close()
	adapter := NewMessagesProvider(Config{Client: server.Client(), Endpoint: server.URL})
	request := provider.ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "anthropic", ID: "claude-test", Route: "messages"}}}
	err := adapter.RunTurn(context.Background(), request, provider.LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return nil }))
	var attempt *provider.AttemptError
	if !errors.As(err, &attempt) || attempt.StreamStarted || !errors.Is(err, provider.ErrMalformedFrame) {
		t.Fatalf("error = %v", err)
	}
}

func TestMessagesDisconnectTerminalTailAndDuplicateBlockAreTyped(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		kind   error
	}{
		{name: "disconnect", stream: "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n", kind: provider.ErrStreamMissingFinal},
		{name: "terminal tail", stream: "data: {\"type\":\"message_stop\"}\n\ndata: {\"type\":\"ping\"}\n\n", kind: ErrProtocol},
		{name: "duplicate block", stream: "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n", kind: ErrProtocol},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(writer, test.stream)
			}))
			defer server.Close()
			err := NewMessagesProvider(Config{Client: server.Client(), Endpoint: server.URL}).RunTurn(context.Background(), provider.ProviderTurnRequest{Request: llm.Request{
				Model: llm.Model{Provider: "anthropic", ID: "claude-test", Route: "messages"},
			}}, provider.LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return nil }))
			if !errors.Is(err, test.kind) {
				t.Fatalf("error = %v, want %v", err, test.kind)
			}
		})
	}
}

func TestMessagesCancellationAndSinkFailure(t *testing.T) {
	t.Run("sink failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n")
		}))
		defer server.Close()
		failure := errors.New("sink stopped")
		err := NewMessagesProvider(Config{Client: server.Client(), Endpoint: server.URL}).RunTurn(context.Background(), provider.ProviderTurnRequest{Request: llm.Request{
			Model: llm.Model{Provider: "anthropic", ID: "claude-test", Route: "messages"},
		}}, provider.LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return failure }))
		if !errors.Is(err, provider.ErrSink) || !errors.Is(err, failure) {
			t.Fatalf("sink error = %v", err)
		}
	})
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
