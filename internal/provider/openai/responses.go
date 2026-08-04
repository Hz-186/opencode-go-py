// Package openai implements the native OpenAI Responses HTTP/SSE adapter.
package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
	"github.com/Hz-186/opencode-go-py/internal/provider"
)

const defaultEndpoint = "https://api.openai.com/v1/responses"

var (
	ErrInvalidConfig = errors.New("invalid OpenAI Responses adapter config")
	ErrProtocol      = errors.New("invalid OpenAI Responses payload")
)

type Config struct {
	Client       *http.Client
	Endpoint     string
	APIKey       string
	Credential   provider.CredentialSource
	Headers      http.Header
	Catalog      *provider.Catalog
	MaxLineBytes int
	MaxBodyBytes int64
}

type ResponsesProvider struct {
	client       *http.Client
	endpoint     string
	apiKey       string
	credential   provider.CredentialSource
	headers      http.Header
	catalog      *provider.Catalog
	maxLineBytes int
	maxBodyBytes int64
}

// OpenAIResponsesProvider is the descriptive name used by the phase plan.
type OpenAIResponsesProvider = ResponsesProvider

func NewResponsesProvider(config Config) *ResponsesProvider {
	client := config.Client
	client = provider.SingleAttemptClient(client)
	endpoint := strings.TrimSpace(config.Endpoint)
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	lineBytes := config.MaxLineBytes
	if lineBytes == 0 {
		lineBytes = 1 << 20
	}
	bodyBytes := config.MaxBodyBytes
	if bodyBytes == 0 {
		bodyBytes = 32 << 20
	}
	return &ResponsesProvider{client: client, endpoint: endpoint, apiKey: config.APIKey, credential: config.Credential,
		headers: cloneHeaders(config.Headers), catalog: config.Catalog,
		maxLineBytes: lineBytes, maxBodyBytes: bodyBytes}
}

func NewOpenAIResponsesProvider(config Config) *ResponsesProvider {
	return NewResponsesProvider(config)
}

func (p *ResponsesProvider) ProjectRequest(request provider.ProviderTurnRequest) ([]byte, error) {
	if p == nil {
		return nil, ErrInvalidConfig
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if p.catalog != nil {
		route, err := p.catalog.Require(request, provider.RequiredCapabilities(request)...)
		if err != nil {
			return nil, err
		}
		if route.API != provider.APITypeOpenAIResponses {
			return nil, fmt.Errorf("%w: route API is %s", provider.ErrUnsupportedRoute, route.API)
		}
	}
	body := map[string]any{"model": request.Request.Model.ID, "stream": true}
	input := make([]any, 0, len(request.Request.System)+len(request.Request.Messages))
	for _, system := range request.Request.System {
		input = append(input, map[string]any{"role": "system", "content": []any{map[string]any{"type": "input_text", "text": system.Text}}})
	}
	for _, message := range request.Request.Messages {
		items, err := responseInput(message)
		if err != nil {
			return nil, err
		}
		input = append(input, items...)
	}
	body["input"] = input
	effectiveTools, effectiveChoice := provider.EffectiveToolsAndChoice(request)
	if len(effectiveTools) > 0 {
		tools := make([]any, 0, len(effectiveTools))
		for _, tool := range effectiveTools {
			schema, err := schemaAny(tool.InputSchema)
			if err != nil {
				return nil, err
			}
			tools = append(tools, map[string]any{"type": "function", "name": tool.Name,
				"description": tool.Description, "parameters": schema})
		}
		body["tools"] = tools
	}
	if choice := effectiveChoice; choice != nil {
		body["tool_choice"] = openAIToolChoice(*choice)
	}
	if generation := request.Request.Generation; generation != nil {
		if generation.MaxTokens != nil {
			body["max_output_tokens"] = *generation.MaxTokens
		}
		if generation.Temperature != nil {
			body["temperature"] = *generation.Temperature
		}
		if generation.TopP != nil {
			body["top_p"] = *generation.TopP
		}
		if len(generation.Stop) > 0 {
			body["stop"] = generation.Stop
		}
	}
	if format := request.Request.ResponseFormat; format != nil {
		if format.Type == llm.ResponseFormatJSON {
			schema, err := schemaAny(format.Schema)
			if err != nil {
				return nil, err
			}
			body["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "name": "response", "schema": schema}}
		} else if format.Type == llm.ResponseFormatText {
			body["text"] = map[string]any{"format": map[string]any{"type": "text"}}
		}
	}
	if err := provider.MergeProviderOptions(body, request.Request.ProviderOptions, "openai", request.Request.Model.Provider); err != nil {
		return nil, err
	}
	if options := request.Request.HTTP; options != nil {
		for key, value := range options.Body {
			converted, err := provider.JSONValueAny(value)
			if err != nil {
				return nil, err
			}
			body[key] = converted
		}
	}
	return provider.RequestJSON(body)
}

func (p *ResponsesProvider) RunTurn(ctx context.Context, request provider.ProviderTurnRequest, sink provider.LLMEventSink) error {
	if ctx == nil || sink == nil {
		return fmt.Errorf("%w: context and sink are required", provider.ErrInvalidRequest)
	}
	body, err := p.ProjectRequest(request)
	if err != nil {
		return err
	}
	endpoint := p.endpoint
	if p.catalog != nil {
		route, resolveErr := p.catalog.ResolveRequest(request)
		if resolveErr != nil {
			return resolveErr
		}
		if route.Endpoint != "" {
			endpoint = route.Endpoint
		}
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("%w: request: %v", provider.ErrProviderHTTP, err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	releaseCredential := func() {}
	if p.credential != nil {
		secret, release, acquireErr := p.credential.Acquire(ctx)
		if acquireErr != nil {
			return acquireErr
		}
		if len(secret) == 0 {
			if release != nil {
				release()
			}
			return provider.ErrCredential
		}
		if release != nil {
			releaseCredential = release
		}
		httpRequest.Header.Set("Authorization", "Bearer "+string(secret))
	} else if p.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	for key, values := range p.headers {
		httpRequest.Header[key] = append([]string(nil), values...)
	}
	if err := provider.ApplyHTTPOptions(httpRequest, request.Request.HTTP, nil); err != nil {
		releaseCredential()
		return err
	}
	response, err := provider.DoHTTP(ctx, p.client, httpRequest, p.maxBodyBytes)
	httpRequest.Header.Del("Authorization")
	releaseCredential()
	if err != nil {
		if handled, emitErr := provider.EmitClassifiedHTTPFailure(ctx, sink, err, "openai"); handled {
			return emitErr
		}
		return err
	}
	defer response.Body.Close()
	if err := provider.ValidateSSEResponse(response); err != nil {
		return provider.NewAttemptError(response.StatusCode, false, 0, err)
	}
	emitter, err := provider.NewEventEmitter(ctx, sink)
	if err != nil {
		return err
	}
	if err := emitter.Emit(llm.StepStart{Index: 0}); err != nil {
		return err
	}
	state := streamState{emitter: emitter, textIDs: map[string]bool{}, reasoningIDs: map[string]bool{}, tools: map[string]*toolState{}}
	readErr := provider.ReadSSE(ctx, response.Body, p.maxLineBytes, func(frame provider.SSEFrame) error {
		return state.frame(frame)
	})
	if readErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return provider.NewAttemptError(response.StatusCode, emitter.Started, 0, readErr)
	}
	if !state.terminal {
		return provider.NewAttemptError(response.StatusCode, emitter.Started, 0, provider.ErrStreamMissingFinal)
	}
	if err := emitter.Done(); err != nil {
		return provider.NewAttemptError(response.StatusCode, emitter.Started, 0, err)
	}
	return nil
}

type toolState struct {
	id, itemID, name string
	args             strings.Builder
	started          bool
	ended            bool
	called           bool
}

type streamState struct {
	emitter      *provider.EventEmitter
	textIDs      map[string]bool
	reasoningIDs map[string]bool
	tools        map[string]*toolState
	metadata     llm.ProviderMetadata
	terminal     bool
}

func (state *streamState) frame(frame provider.SSEFrame) error {
	if strings.TrimSpace(frame.Data) == "" {
		return nil
	}
	if state.terminal {
		return fmt.Errorf("%w: event after terminal", ErrProtocol)
	}
	if strings.TrimSpace(frame.Data) == "[DONE]" {
		return state.finish(llm.FinishStop, nil)
	}
	object, err := provider.DecodeFrameJSON(frame.Data)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrProtocol, err)
	}
	if metadata := responseMetadata(object); metadata != nil {
		state.metadata = metadata
	}
	typeName := stringValue(object["type"])
	if typeName == "" {
		typeName = frame.Event
	}
	switch typeName {
	case "response.output_text.delta":
		id := firstString(object, "item_id", "id")
		if id == "" {
			id = "text-" + firstString(object, "output_index")
		}
		if id == "text-" {
			id = "text-0"
		}
		if !state.textIDs[id] {
			if err := state.emitter.Emit(llm.TextStart{ID: id, ProviderMetadata: provider.Metadata("openai", map[string]any{"event": typeName})}); err != nil {
				return err
			}
			state.textIDs[id] = true
		}
		return state.emitter.Emit(llm.TextDelta{ID: id, Text: stringValue(object["delta"])})
	case "response.output_text.done":
		id := firstString(object, "item_id", "id")
		if id == "" {
			id = "text-" + firstString(object, "output_index")
		}
		if id == "text-" {
			id = "text-0"
		}
		if !state.textIDs[id] {
			return fmt.Errorf("%w: text done without start", ErrProtocol)
		}
		delete(state.textIDs, id)
		return state.emitter.Emit(llm.TextEnd{ID: id})
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		id := firstString(object, "item_id", "id")
		if id == "" {
			id = "reasoning-" + firstString(object, "output_index")
		}
		if id == "reasoning-" {
			id = "reasoning-0"
		}
		if !state.reasoningIDs[id] {
			if err := state.emitter.Emit(llm.ReasoningStart{ID: id, ProviderMetadata: provider.Metadata("openai", map[string]any{"event": typeName})}); err != nil {
				return err
			}
			state.reasoningIDs[id] = true
		}
		return state.emitter.Emit(llm.ReasoningDelta{ID: id, Text: stringValue(object["delta"])})
	case "response.reasoning_summary_text.done", "response.reasoning_text.done":
		id := firstString(object, "item_id", "id")
		if id == "" {
			id = "reasoning-" + firstString(object, "output_index")
		}
		if id == "reasoning-" {
			id = "reasoning-0"
		}
		if !state.reasoningIDs[id] {
			return fmt.Errorf("%w: reasoning done without start", ErrProtocol)
		}
		delete(state.reasoningIDs, id)
		return state.emitter.Emit(llm.ReasoningEnd{ID: id})
	case "response.output_item.added":
		item, _ := object["item"].(map[string]any)
		if stringValue(item["type"]) != "function_call" {
			return nil
		}
		id := firstString(item, "call_id", "id")
		name := stringValue(item["name"])
		if id == "" || name == "" {
			return fmt.Errorf("%w: function call identity missing", ErrProtocol)
		}
		tool := &toolState{id: id, itemID: firstString(item, "id"), name: name}
		state.tools[id] = tool
		if tool.itemID != "" && tool.itemID != id {
			state.tools[tool.itemID] = tool
		}
		tool.started = true
		return state.emitter.Emit(llm.ToolInputStart{ID: id, Name: name})
	case "response.function_call_arguments.delta":
		id := firstString(object, "item_id", "call_id", "id")
		tool := state.findTool(id)
		if tool == nil {
			return fmt.Errorf("%w: function argument delta without call", ErrProtocol)
		}
		part := stringValue(object["delta"])
		tool.args.WriteString(part)
		return state.emitter.Emit(llm.ToolInputDelta{ID: tool.id, Name: tool.name, Text: part})
	case "response.function_call_arguments.done":
		id := firstString(object, "item_id", "call_id", "id")
		tool := state.findTool(id)
		if tool == nil {
			return fmt.Errorf("%w: function argument done without call", ErrProtocol)
		}
		if tool.args.Len() == 0 {
			if value := stringValue(object["arguments"]); value != "" {
				tool.args.WriteString(value)
			}
		}
		return state.completeTool(tool)
	case "response.output_item.done":
		item, _ := object["item"].(map[string]any)
		if stringValue(item["type"]) != "function_call" {
			return nil
		}
		id := firstString(item, "call_id", "id")
		tool := state.findTool(id)
		if tool == nil {
			return fmt.Errorf("%w: function call completed before start", ErrProtocol)
		}
		if !tool.called {
			if tool.args.Len() == 0 {
				if value := stringValue(item["arguments"]); value != "" {
					tool.args.WriteString(value)
				}
			}
			return state.completeTool(tool)
		}
		return nil
	case "response.completed":
		return state.finish(llm.FinishStop, usageFromResponse(object))
	case "response.incomplete":
		return state.finish(llm.FinishLength, usageFromResponse(object))
	case "response.failed", "error":
		state.terminal = true
		errorObject, _ := object["error"].(map[string]any)
		if response, _ := object["response"].(map[string]any); response != nil {
			if nested, _ := response["error"].(map[string]any); nested != nil {
				errorObject = nested
			}
		}
		classification, retryable := provider.ClassifyProviderError(stringValue(errorObject["code"]), stringValue(errorObject["type"]), stringValue(errorObject["message"]))
		return state.emitter.Emit(llm.ProviderError{Message: safeErrorMessage(object), Classification: classification, Retryable: retryable, ProviderMetadata: provider.Metadata("openai", map[string]any{"event": typeName})})
	default:
		// Lifecycle notifications carry no canonical content and are safe to
		// ignore. Unknown error-shaped records fail closed.
		switch typeName {
		case "response.created", "response.in_progress", "response.content_part.added", "response.content_part.done",
			"response.output_text.annotation.added", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
			"response.web_search_call.in_progress", "response.web_search_call.searching", "response.web_search_call.completed", "ping":
			return nil
		}
		return fmt.Errorf("%w: unsupported event", ErrProtocol)
	}
}

func (state *streamState) completeTool(tool *toolState) error {
	if tool.ended {
		return fmt.Errorf("%w: duplicate function arguments terminal", ErrProtocol)
	}
	if err := state.emitter.Emit(llm.ToolInputEnd{ID: tool.id, Name: tool.name}); err != nil {
		return err
	}
	tool.ended = true
	input := domain.JSONNull()
	if strings.TrimSpace(tool.args.String()) != "" {
		value, err := provider.DecodeNativeJSON(tool.args.String())
		if err != nil {
			return fmt.Errorf("%w: tool arguments: %w", ErrProtocol, err)
		}
		input = value
	}
	if err := state.emitter.Emit(llm.ToolCall{ID: tool.id, Name: tool.name, Input: input,
		ProviderMetadata: provider.Metadata("openai", map[string]any{"itemID": tool.itemID})}); err != nil {
		return err
	}
	tool.called = true
	return nil
}

func (state *streamState) findTool(id string) *toolState {
	if tool := state.tools[id]; tool != nil {
		return tool
	}
	for _, tool := range state.tools {
		if tool.itemID == id || tool.id == id {
			return tool
		}
	}
	return nil
}

func (state *streamState) finish(reason llm.FinishReason, usage *llm.Usage) error {
	if state.terminal {
		return fmt.Errorf("%w: duplicate terminal", ErrProtocol)
	}
	seen := map[*toolState]bool{}
	toolList := make([]*toolState, 0, len(state.tools))
	for _, tool := range state.tools {
		if !seen[tool] {
			seen[tool] = true
			toolList = append(toolList, tool)
		}
	}
	sort.Slice(toolList, func(i, j int) bool { return toolList[i].id < toolList[j].id })
	for _, tool := range toolList {
		if !tool.called {
			return fmt.Errorf("%w: function call did not finish", ErrProtocol)
		}
	}
	if len(toolList) > 0 && reason == llm.FinishStop {
		reason = llm.FinishToolCalls
	}
	if len(state.textIDs) > 0 || len(state.reasoningIDs) > 0 {
		return fmt.Errorf("%w: response completed with open content block", ErrProtocol)
	}
	if err := state.emitter.Emit(llm.StepFinish{Index: 0, Reason: reason, Usage: usage, ProviderMetadata: state.metadata}); err != nil {
		return err
	}
	if err := state.emitter.Emit(llm.Finish{Reason: reason, Usage: usage, ProviderMetadata: state.metadata}); err != nil {
		return err
	}
	state.terminal = true
	return nil
}

func responseContent(content llm.Content, role llm.MessageRole) (map[string]any, error) {
	switch value := content.(type) {
	case llm.TextContent:
		typeName := "input_text"
		if role == llm.MessageRoleAssistant {
			typeName = "output_text"
		}
		return map[string]any{"type": typeName, "text": value.Text}, nil
	case llm.ReasoningContent:
		return map[string]any{"type": "input_text", "text": value.Text}, nil
	case llm.MediaContent:
		data := value.Data
		if data == "" && len(value.Bytes) > 0 {
			data = "data:" + value.MediaType + ";base64," + base64.StdEncoding.EncodeToString(value.Bytes)
		}
		return map[string]any{"type": "input_image", "image_url": data}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported content %T", provider.ErrInvalidRequest, content)
	}
}

// responseInput keeps Responses API item types at the input-array level.
// function_call and function_call_output are not valid message content parts.
func responseInput(message llm.Message) ([]any, error) {
	items := make([]any, 0, len(message.Content))
	parts := make([]any, 0, len(message.Content))
	flushParts := func() {
		if len(parts) == 0 {
			return
		}
		items = append(items, map[string]any{"role": string(message.Role), "content": parts})
		parts = nil
	}
	for _, content := range message.Content {
		switch value := content.(type) {
		case llm.ToolCallContent:
			flushParts()
			input, err := provider.JSONValueAny(value.Input)
			if err != nil {
				return nil, err
			}
			encoded, err := provider.RequestJSON(input)
			if err != nil {
				return nil, err
			}
			items = append(items, map[string]any{"type": "function_call", "call_id": value.ID, "name": value.Name, "arguments": string(encoded)})
		case llm.ToolResultContent:
			flushParts()
			result, err := provider.JSONValueAny(value.Result)
			if err != nil {
				return nil, err
			}
			encoded, err := provider.RequestJSON(result)
			if err != nil {
				return nil, err
			}
			items = append(items, map[string]any{"type": "function_call_output", "call_id": value.ID, "output": string(encoded)})
		default:
			part, err := responseContent(content, message.Role)
			if err != nil {
				return nil, err
			}
			parts = append(parts, part)
		}
	}
	flushParts()
	if len(items) == 0 {
		items = append(items, map[string]any{"role": string(message.Role), "content": []any{}})
	}
	return items, nil
}

func schemaAny(schema llm.JSONSchema) (any, error) {
	object := make(map[string]any, len(schema))
	for key, value := range schema {
		converted, err := provider.JSONValueAny(value)
		if err != nil {
			return nil, err
		}
		object[key] = converted
	}
	return object, nil
}

func openAIToolChoice(choice llm.ToolChoice) any {
	switch choice.Type {
	case llm.ToolChoiceAuto:
		return "auto"
	case llm.ToolChoiceNone:
		return "none"
	case llm.ToolChoiceRequired:
		return "required"
	case llm.ToolChoiceNamed:
		if choice.Name != nil {
			return map[string]any{"type": "function", "name": *choice.Name}
		}
	}
	return "auto"
}

func usageFromResponse(object map[string]any) *llm.Usage {
	response, _ := object["response"].(map[string]any)
	usage, _ := response["usage"].(map[string]any)
	if usage == nil {
		usage, _ = object["usage"].(map[string]any)
	}
	if usage == nil {
		return nil
	}
	result := &llm.Usage{}
	result.InputTokens = numberPtr(usage["input_tokens"])
	result.OutputTokens = numberPtr(usage["output_tokens"])
	result.TotalTokens = numberPtr(usage["total_tokens"])
	if details, _ := usage["input_tokens_details"].(map[string]any); details != nil {
		result.CacheReadInputTokens = numberPtr(details["cached_tokens"])
	}
	if details, _ := usage["output_tokens_details"].(map[string]any); details != nil {
		result.ReasoningTokens = numberPtr(details["reasoning_tokens"])
	}
	if result.InputTokens != nil && result.CacheReadInputTokens != nil {
		value := *result.InputTokens - *result.CacheReadInputTokens
		if value >= 0 {
			result.NonCachedInputTokens = &value
			result.CacheWriteInputTokens = provider.NumberPtr(0)
		}
	}
	result.ProviderMetadata = provider.Metadata("openai", map[string]any{"usage": usage})
	return result
}

func responseMetadata(object map[string]any) llm.ProviderMetadata {
	response, _ := object["response"].(map[string]any)
	if response == nil {
		return nil
	}
	fields := map[string]any{}
	for _, key := range []string{"id", "model", "status", "service_tier"} {
		if value := response[key]; value != nil {
			fields[key] = value
		}
	}
	return provider.Metadata("openai", fields)
}

func numberPtr(value any) *float64 {
	var number float64
	switch value := value.(type) {
	case float64:
		number = value
	case json.Number:
		number, _ = value.Float64()
	case int:
		number = float64(value)
	default:
		return nil
	}
	return &number
}

func cloneHeaders(headers http.Header) http.Header {
	result := make(http.Header, len(headers))
	for key, values := range headers {
		result[key] = append([]string(nil), values...)
	}
	return result
}

func stringValue(value any) string {
	if result, ok := value.(string); ok {
		return result
	}
	if result, ok := value.(fmt.Stringer); ok {
		return result.String()
	}
	return ""
}

func firstString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(object[key]); value != "" {
			return value
		}
	}
	return ""
}

func safeErrorMessage(object map[string]any) string {
	return "provider returned an error event"
}
