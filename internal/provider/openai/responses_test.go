package openai

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

func TestResponsesProjectsToolResponseFormatDeterministically(t *testing.T) {
	adapter := NewResponsesProvider(Config{})
	request := toolFormatRequest("openai", "responses")
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
	if len(tools) != 2 || choice["type"] != "function" || choice["name"] != "emit_result" {
		t.Fatalf("effective tool projection = %s", first)
	}
	formatTool, _ := tools[1].(map[string]any)
	if formatTool["name"] != "emit_result" || formatTool["description"] != "structured output" || formatTool["parameters"] == nil {
		t.Fatalf("format tool = %#v", formatTool)
	}
}

func TestResponsesProjectsCanonicalMessagesGenerationAndOptions(t *testing.T) {
	name := "lookup"
	maxTokens, temperature, topP := 128.0, 0.25, 0.9
	request := provider.ProviderTurnRequest{Request: llm.Request{
		Model:  llm.Model{Provider: "openai", ID: "gpt-test", Route: "responses"},
		System: []llm.SystemPart{{Text: "system"}},
		Messages: []llm.Message{
			{Role: llm.MessageRoleUser, Content: []llm.Content{llm.TextContent{Text: "question"}, llm.MediaContent{MediaType: "image/png", Data: "data:image/png;base64,AA=="}}},
			{Role: llm.MessageRoleAssistant, Content: []llm.Content{llm.TextContent{Text: "calling"}, llm.ToolCallContent{ID: "call_1", Name: name, Input: domain.JSONObject(map[string]domain.JSONValue{"q": domain.JSONString("x")})}}},
			{Role: llm.MessageRoleTool, Content: []llm.Content{llm.ToolResultContent{ID: "call_1", Name: name, Result: domain.JSONObject(map[string]domain.JSONValue{"answer": domain.JSONString("y")})}}},
		},
		Tools:      []llm.ToolDefinition{{Name: name, Description: "lookup", InputSchema: llm.JSONSchema{"type": domain.JSONString("object")}}},
		ToolChoice: &llm.ToolChoice{Type: llm.ToolChoiceNamed, Name: &name},
		Generation: &llm.GenerationOptions{MaxTokens: &maxTokens, Temperature: &temperature, TopP: &topP, Stop: []string{"stop"}},
		ResponseFormat: &llm.ResponseFormat{Type: llm.ResponseFormatJSON, Schema: llm.JSONSchema{
			"type": domain.JSONString("object"),
		}},
		ProviderOptions: llm.ProviderMetadata{"openai": {"store": domain.JSONBool(false)}},
		HTTP:            &llm.HTTPOptions{Body: llm.JSONSchema{"custom": domain.JSONString("body")}},
	}}
	projected, err := NewResponsesProvider(Config{}).ProjectRequest(request)
	if err != nil {
		t.Fatalf("project request: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(projected, &body); err != nil {
		t.Fatal(err)
	}
	input, _ := body["input"].([]any)
	if len(input) != 5 || body["stream"] != true || body["max_output_tokens"] != float64(128) || body["temperature"] != 0.25 || body["top_p"] != 0.9 || body["store"] != false || body["custom"] != "body" {
		t.Fatalf("canonical projection = %s", projected)
	}
	functionCall, _ := input[3].(map[string]any)
	functionOutput, _ := input[4].(map[string]any)
	if functionCall["type"] != "function_call" || functionCall["call_id"] != "call_1" || functionOutput["type"] != "function_call_output" || functionOutput["call_id"] != "call_1" {
		t.Fatalf("Responses items are not top-level: %s", projected)
	}
	text, _ := body["text"].(map[string]any)
	format, _ := text["format"].(map[string]any)
	if format["type"] != "json_schema" || format["schema"] == nil {
		t.Fatalf("response format = %#v", text)
	}
}

func TestResponsesRejectsCatalogAPIMismatch(t *testing.T) {
	catalog, err := provider.NewCatalog([]provider.Route{{
		Provider: "openai", ModelID: "gpt-test", Name: "responses", API: provider.APITypeAnthropicMessages,
		Capabilities: provider.Capabilities{Text: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewResponsesProvider(Config{Catalog: catalog}).ProjectRequest(provider.ProviderTurnRequest{Request: llm.Request{
		Model: llm.Model{Provider: "openai", ID: "gpt-test", Route: "responses"},
	}})
	if !errors.Is(err, provider.ErrUnsupportedRoute) {
		t.Fatalf("API mismatch error = %v", err)
	}
}

func TestResponsesProjectAndNormalizeText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("authorization header = %q", request.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), `"model":"gpt-test"`) || !strings.Contains(string(body), `"input"`) {
			t.Errorf("request projection = %s", body)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"hello\"}\n\n")
		_, _ = io.WriteString(writer, "event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"item_id\":\"msg_1\"}\n\n")
		_, _ = io.WriteString(writer, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-test\",\"status\":\"completed\",\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3,\"input_tokens_details\":{\"cached_tokens\":1},\"output_tokens_details\":{\"reasoning_tokens\":1}}}}\n\n")
	}))
	defer server.Close()
	adapter := NewResponsesProvider(Config{Client: server.Client(), Endpoint: server.URL, APIKey: "test-secret"})
	request := provider.ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "openai", ID: "gpt-test", Route: "responses"}, Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{llm.TextContent{Text: "hi"}}}}}}
	events := collectEvents(t, adapter, request)
	if err := provider.ValidateStream(events); err != nil {
		t.Fatalf("normalized stream: %v", err)
	}
	if len(events) != 6 {
		t.Fatalf("events = %d, want step/text start+delta/end/step finish/finish", len(events))
	}
	if got := events[2].(llm.TextDelta).Text; got != "hello" {
		t.Fatalf("text delta = %q", got)
	}
	usage := events[4].(llm.StepFinish).Usage
	if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 2 || usage.CacheReadInputTokens == nil || *usage.CacheReadInputTokens != 1 || usage.ReasoningTokens == nil || *usage.ReasoningTokens != 1 {
		t.Fatalf("usage = %+v", usage)
	}
	if metadata := events[4].(llm.StepFinish).ProviderMetadata["openai"]; metadata["id"].String != "resp_1" || metadata["model"].String != "gpt-test" {
		t.Fatalf("provider metadata = %+v", metadata)
	}
}

func TestResponsesToolArgumentsAreNotDuplicated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"item_1\",\"call_id\":\"call_1\",\"name\":\"lookup\"}}\n\n")
		_, _ = io.WriteString(writer, `data: {"type":"response.function_call_arguments.delta","item_id":"item_1","delta":"{\"q\":\"x\"}"}`+"\n\n")
		_, _ = io.WriteString(writer, `data: {"type":"response.function_call_arguments.done","item_id":"item_1","arguments":"{\"q\":\"x\"}"}`+"\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()
	adapter := NewResponsesProvider(Config{Client: server.Client(), Endpoint: server.URL})
	request := provider.ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "openai", ID: "gpt-test", Route: "responses"}}}
	events := collectEvents(t, adapter, request)
	var call llm.ToolCall
	for _, event := range events {
		if value, ok := event.(llm.ToolCall); ok {
			call = value
		}
	}
	if call.Name != "lookup" || call.Input.Kind != domain.JSONKindObject || call.Input.Object["q"].String != "x" {
		t.Fatalf("tool call = %+v", call)
	}
	if finish := events[len(events)-1].(llm.Finish); finish.Reason != llm.FinishToolCalls {
		t.Fatalf("tool finish reason = %q", finish.Reason)
	}
}

func TestResponsesReasoningAndImplicitZeroIDsNormalize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"think\"}\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.reasoning_summary_text.done\"}\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_text.done\"}\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()
	events := collectEvents(t, NewResponsesProvider(Config{Client: server.Client(), Endpoint: server.URL}), provider.ProviderTurnRequest{Request: llm.Request{
		Model: llm.Model{Provider: "openai", ID: "gpt-test", Route: "responses"},
	}})
	if err := provider.ValidateStream(events); err != nil {
		t.Fatalf("normalized stream: %v", err)
	}
	if events[2].(llm.ReasoningDelta).Text != "think" || events[5].(llm.TextDelta).Text != "answer" {
		t.Fatalf("events = %#v", events)
	}
}

func TestResponsesErrorEventClassifiesOverflowAndRedacts(t *testing.T) {
	const secret = "prompt-and-tool-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, `data: {"type":"response.failed","response":{"error":{"code":"context_length_exceeded","type":"invalid_request_error","message":"`+secret+`"}}}`+"\n\n")
	}))
	defer server.Close()
	events := collectEvents(t, NewResponsesProvider(Config{Client: server.Client(), Endpoint: server.URL}), provider.ProviderTurnRequest{Request: llm.Request{
		Model: llm.Model{Provider: "openai", ID: "gpt-test", Route: "responses"},
	}})
	failure := events[1].(llm.ProviderError)
	if failure.Classification == nil || *failure.Classification != llm.ProviderFailureContextOverflow || failure.Retryable == nil || *failure.Retryable || strings.Contains(failure.Message, secret) {
		t.Fatalf("provider failure = %+v", failure)
	}
}

func TestResponsesTerminalTailAndSinkFailureAreTyped(t *testing.T) {
	t.Run("terminal tail", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, "data: {\"type\":\"response.completed\",\"response\":{}}\n\ndata: {\"type\":\"response.created\"}\n\n")
		}))
		defer server.Close()
		err := NewResponsesProvider(Config{Client: server.Client(), Endpoint: server.URL}).RunTurn(context.Background(), provider.ProviderTurnRequest{Request: llm.Request{
			Model: llm.Model{Provider: "openai", ID: "gpt-test", Route: "responses"},
		}}, provider.LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return nil }))
		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("terminal tail error = %v", err)
		}
	})
	t.Run("sink failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg\",\"delta\":\"text\"}\n\n")
		}))
		defer server.Close()
		failure := errors.New("backpressure")
		err := NewResponsesProvider(Config{Client: server.Client(), Endpoint: server.URL}).RunTurn(context.Background(), provider.ProviderTurnRequest{Request: llm.Request{
			Model: llm.Model{Provider: "openai", ID: "gpt-test", Route: "responses"},
		}}, provider.LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return failure }))
		if !errors.Is(err, provider.ErrSink) || !errors.Is(err, failure) {
			t.Fatalf("sink error = %v", err)
		}
	})
}

func TestResponsesHTTPStatusIsRetryableAttempt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Retry-After", "2")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(writer, `{"error":{"message":"secret body"}}`)
	}))
	defer server.Close()
	adapter := NewResponsesProvider(Config{Client: server.Client(), Endpoint: server.URL})
	request := provider.ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "openai", ID: "gpt-test", Route: "responses"}}}
	err := adapter.RunTurn(context.Background(), request, provider.LLMEventSinkFunc(func(context.Context, llm.LLMEvent) error { return nil }))
	var attempt *provider.AttemptError
	if !errors.As(err, &attempt) || attempt.Status != http.StatusTooManyRequests || attempt.RetryAfter <= 0 {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "secret body") {
		t.Fatalf("HTTP body leaked in error: %v", err)
	}
}

func TestResponsesMalformedAndDisconnectAreTyped(t *testing.T) {
	for _, test := range []struct {
		name    string
		stream  string
		started bool
		kind    error
	}{
		{name: "malformed", stream: "data: {bad\n\n", started: false, kind: provider.ErrMalformedFrame},
		{name: "disconnect", stream: "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"partial\"}\n\n", started: true, kind: provider.ErrStreamMissingFinal},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(writer, test.stream)
			}))
			defer server.Close()
			adapter := NewResponsesProvider(Config{Client: server.Client(), Endpoint: server.URL})
			request := provider.ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "openai", ID: "gpt-test", Route: "responses"}}}
			var events []llm.LLMEvent
			err := adapter.RunTurn(context.Background(), request, provider.LLMEventSinkFunc(func(_ context.Context, event llm.LLMEvent) error {
				events = append(events, event)
				return nil
			}))
			var attempt *provider.AttemptError
			if !errors.As(err, &attempt) || attempt.StreamStarted != test.started || !errors.Is(err, test.kind) {
				t.Fatalf("error = %v", err)
			}
			if !test.started && len(events) != 0 {
				t.Fatalf("pre-stream failure emitted %d events", len(events))
			}
		})
	}
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
