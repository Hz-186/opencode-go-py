// Package compatible implements the OpenAI-compatible Chat Completions
// HTTP/SSE adapter used by canonical V2 routes.
package compatible

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
	"github.com/Hz-186/opencode-go-py/internal/provider"
)

var (
	ErrInvalidConfig = errors.New("invalid OpenAI-compatible adapter config")
	ErrProtocol      = errors.New("invalid OpenAI-compatible payload")
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

type ChatProvider struct {
	client       *http.Client
	endpoint     string
	apiKey       string
	credential   provider.CredentialSource
	headers      http.Header
	catalog      *provider.Catalog
	maxLineBytes int
	maxBodyBytes int64
}

type OpenAICompatibleProvider = ChatProvider

func NewChatProvider(config Config) *ChatProvider {
	client := config.Client
	client = provider.SingleAttemptClient(client)
	lineBytes := config.MaxLineBytes
	if lineBytes == 0 {
		lineBytes = 1 << 20
	}
	bodyBytes := config.MaxBodyBytes
	if bodyBytes == 0 {
		bodyBytes = 32 << 20
	}
	return &ChatProvider{client: client, endpoint: strings.TrimSpace(config.Endpoint), apiKey: config.APIKey, credential: config.Credential,
		headers: cloneHeaders(config.Headers), catalog: config.Catalog,
		maxLineBytes: lineBytes, maxBodyBytes: bodyBytes}
}

func NewOpenAICompatibleProvider(config Config) *ChatProvider { return NewChatProvider(config) }

func (p *ChatProvider) ProjectRequest(request provider.ProviderTurnRequest) ([]byte, error) {
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
		if route.API != provider.APITypeOpenAICompatible {
			return nil, fmt.Errorf("%w: route API is %s", provider.ErrUnsupportedRoute, route.API)
		}
	}
	body := map[string]any{"model": request.Request.Model.ID, "stream": true,
		"stream_options": map[string]any{"include_usage": true}}
	messages := make([]any, 0, len(request.Request.System)+len(request.Request.Messages))
	for _, system := range request.Request.System {
		messages = append(messages, map[string]any{"role": "system", "content": system.Text})
	}
	for _, message := range request.Request.Messages {
		projected, err := chatMessages(message)
		if err != nil {
			return nil, err
		}
		messages = append(messages, projected...)
	}
	body["messages"] = messages
	effectiveTools, effectiveChoice := provider.EffectiveToolsAndChoice(request)
	if len(effectiveTools) > 0 {
		tools := make([]any, 0, len(effectiveTools))
		for _, tool := range effectiveTools {
			schema, err := schemaAny(tool.InputSchema)
			if err != nil {
				return nil, err
			}
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": tool.Name, "description": tool.Description, "parameters": schema}})
		}
		body["tools"] = tools
	}
	if choice := effectiveChoice; choice != nil {
		body["tool_choice"] = chatToolChoice(*choice)
	}
	if generation := request.Request.Generation; generation != nil {
		if generation.MaxTokens != nil {
			body["max_tokens"] = *generation.MaxTokens
		}
		if generation.Temperature != nil {
			body["temperature"] = *generation.Temperature
		}
		if generation.TopP != nil {
			body["top_p"] = *generation.TopP
		}
		if generation.FrequencyPenalty != nil {
			body["frequency_penalty"] = *generation.FrequencyPenalty
		}
		if generation.PresencePenalty != nil {
			body["presence_penalty"] = *generation.PresencePenalty
		}
		if generation.Seed != nil {
			body["seed"] = *generation.Seed
		}
		if len(generation.Stop) > 0 {
			body["stop"] = generation.Stop
		}
	}
	if format := request.Request.ResponseFormat; format != nil {
		switch format.Type {
		case llm.ResponseFormatJSON:
			schema, err := schemaAny(format.Schema)
			if err != nil {
				return nil, err
			}
			body["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "response", "schema": schema}}
		case llm.ResponseFormatText:
			body["response_format"] = map[string]any{"type": "text"}
		}
	}
	if err := provider.MergeProviderOptions(body, request.Request.ProviderOptions, "compatible", request.Request.Model.Provider); err != nil {
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

func (p *ChatProvider) RunTurn(ctx context.Context, request provider.ProviderTurnRequest, sink provider.LLMEventSink) error {
	if ctx == nil || sink == nil {
		return fmt.Errorf("%w: context and sink are required", provider.ErrInvalidRequest)
	}
	if p == nil {
		return ErrInvalidConfig
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
	if endpoint == "" {
		return fmt.Errorf("%w: endpoint is required", ErrInvalidConfig)
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
		if handled, emitErr := provider.EmitClassifiedHTTPFailure(ctx, sink, err, "compatible"); handled {
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
	state := streamState{emitter: emitter, tools: map[string]*toolState{}, text: map[string]bool{}, reasoning: map[string]bool{}}
	readErr := provider.ReadSSE(ctx, response.Body, p.maxLineBytes, func(frame provider.SSEFrame) error { return state.frame(frame) })
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
	id, name string
	args     strings.Builder
	started  bool
	called   bool
}

type streamState struct {
	emitter   *provider.EventEmitter
	tools     map[string]*toolState
	text      map[string]bool
	reasoning map[string]bool
	usage     *llm.Usage
	reason    llm.FinishReason
	metadata  llm.ProviderMetadata
	terminal  bool
}

func (state *streamState) frame(frame provider.SSEFrame) error {
	data := strings.TrimSpace(frame.Data)
	if data == "" {
		return nil
	}
	if state.terminal {
		return fmt.Errorf("%w: event after terminal", ErrProtocol)
	}
	if data == "[DONE]" {
		if state.reason == "" {
			state.reason = llm.FinishStop
		}
		return state.finish(state.reason, state.usage)
	}
	object, err := provider.DecodeFrameJSON(frame.Data)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrProtocol, err)
	}
	if metadata := chunkMetadata(object); metadata != nil {
		state.metadata = metadata
	}
	if errorObject, _ := object["error"].(map[string]any); errorObject != nil {
		state.terminal = true
		classification, retryable := provider.ClassifyProviderError(stringValue(errorObject["code"]), stringValue(errorObject["type"]), stringValue(errorObject["message"]))
		return state.emitter.Emit(llm.ProviderError{Message: safeErrorMessage(errorObject), Classification: classification, Retryable: retryable,
			ProviderMetadata: provider.Metadata("compatible", map[string]any{"event": "error"})})
	}
	if usage, _ := object["usage"].(map[string]any); usage != nil {
		state.usage = parseUsage(usage)
	}
	choices, _ := object["choices"].([]any)
	for _, rawChoice := range choices {
		choice, _ := rawChoice.(map[string]any)
		choiceIndex := indexValue(choice["index"])
		delta, _ := choice["delta"].(map[string]any)
		if content := stringValue(delta["content"]); content != "" {
			id := "text-" + choiceIndex
			if !state.text[id] {
				if err := state.emitter.Emit(llm.TextStart{ID: id, ProviderMetadata: provider.Metadata("compatible", map[string]any{"choice": choiceIndex})}); err != nil {
					return err
				}
				state.text[id] = true
			}
			if err := state.emitter.Emit(llm.TextDelta{ID: id, Text: content}); err != nil {
				return err
			}
		}
		reasoning := firstString(delta, "reasoning_content", "reasoning")
		if reasoning != "" {
			id := "reasoning-" + choiceIndex
			if !state.reasoning[id] {
				if err := state.emitter.Emit(llm.ReasoningStart{ID: id}); err != nil {
					return err
				}
				state.reasoning[id] = true
			}
			if err := state.emitter.Emit(llm.ReasoningDelta{ID: id, Text: reasoning}); err != nil {
				return err
			}
		}
		toolCalls, _ := delta["tool_calls"].([]any)
		for _, rawCall := range toolCalls {
			call, _ := rawCall.(map[string]any)
			callIndex := indexValue(call["index"])
			key := choiceIndex + ":" + callIndex
			function, _ := call["function"].(map[string]any)
			tool := state.tools[key]
			if tool == nil {
				id := stringValue(call["id"])
				if id == "" {
					id = "tool-" + key
				}
				name := stringValue(function["name"])
				tool = &toolState{id: id, name: name}
				state.tools[key] = tool
			}
			if tool.id == "" {
				if id := stringValue(call["id"]); id != "" {
					tool.id = id
				}
			}
			if tool.name == "" {
				tool.name = stringValue(function["name"])
			}
			if !tool.started {
				if tool.id == "" || tool.name == "" {
					// Some compatible servers send id/name in a later chunk.
					continue
				}
				if err := state.emitter.Emit(llm.ToolInputStart{ID: tool.id, Name: tool.name}); err != nil {
					return err
				}
				tool.started = true
			}
			part := stringValue(function["arguments"])
			if part != "" {
				tool.args.WriteString(part)
				if err := state.emitter.Emit(llm.ToolInputDelta{ID: tool.id, Name: tool.name, Text: part}); err != nil {
					return err
				}
			}
		}
		if finish := stringValue(choice["finish_reason"]); finish != "" {
			state.reason = finishReason(finish)
		}
	}
	return nil
}

func (state *streamState) finish(reason llm.FinishReason, usage *llm.Usage) error {
	if state.terminal {
		return fmt.Errorf("%w: duplicate terminal", ErrProtocol)
	}
	textIDs := make([]string, 0, len(state.text))
	for id := range state.text {
		textIDs = append(textIDs, id)
	}
	sort.Strings(textIDs)
	for _, id := range textIDs {
		if err := state.emitter.Emit(llm.TextEnd{ID: id}); err != nil {
			return err
		}
		delete(state.text, id)
	}
	reasoningIDs := make([]string, 0, len(state.reasoning))
	for id := range state.reasoning {
		reasoningIDs = append(reasoningIDs, id)
	}
	sort.Strings(reasoningIDs)
	for _, id := range reasoningIDs {
		if err := state.emitter.Emit(llm.ReasoningEnd{ID: id}); err != nil {
			return err
		}
		delete(state.reasoning, id)
	}
	keys := make([]string, 0, len(state.tools))
	for key := range state.tools {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		tool := state.tools[key]
		if tool.called {
			continue
		}
		if !tool.started || tool.id == "" || tool.name == "" {
			return fmt.Errorf("%w: incomplete tool identity", ErrProtocol)
		}
		if err := state.emitter.Emit(llm.ToolInputEnd{ID: tool.id, Name: tool.name}); err != nil {
			return err
		}
		input := domain.JSONNull()
		if strings.TrimSpace(tool.args.String()) != "" {
			var err error
			input, err = provider.DecodeNativeJSON(tool.args.String())
			if err != nil {
				return fmt.Errorf("%w: tool input: %w", ErrProtocol, err)
			}
		}
		if err := state.emitter.Emit(llm.ToolCall{ID: tool.id, Name: tool.name, Input: input}); err != nil {
			return err
		}
		tool.called = true
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

func chatMessage(message llm.Message) (map[string]any, error) {
	result := map[string]any{"role": string(message.Role)}
	parts := make([]any, 0, len(message.Content))
	toolCalls := make([]any, 0)
	for _, content := range message.Content {
		switch value := content.(type) {
		case llm.TextContent:
			parts = append(parts, map[string]any{"type": "text", "text": value.Text})
		case llm.ReasoningContent:
			result["reasoning_content"] = value.Text
		case llm.MediaContent:
			data := value.Data
			if data == "" && len(value.Bytes) > 0 {
				data = "data:" + value.MediaType + ";base64," + base64.StdEncoding.EncodeToString(value.Bytes)
			}
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": data}})
		case llm.ToolCallContent:
			input, err := provider.JSONValueAny(value.Input)
			if err != nil {
				return nil, err
			}
			encoded, _ := provider.RequestJSON(input)
			toolCalls = append(toolCalls, map[string]any{"id": value.ID, "type": "function", "function": map[string]any{"name": value.Name, "arguments": string(encoded)}})
		case llm.ToolResultContent:
			valueAny, err := provider.JSONValueAny(value.Result)
			if err != nil {
				return nil, err
			}
			encoded, _ := provider.RequestJSON(valueAny)
			result["tool_call_id"] = value.ID
			result["role"] = "tool"
			result["content"] = string(encoded)
		default:
			return nil, fmt.Errorf("%w: unsupported content %T", provider.ErrInvalidRequest, content)
		}
	}
	if len(toolCalls) > 0 {
		result["tool_calls"] = toolCalls
	}
	if _, exists := result["content"]; !exists {
		if len(parts) == 1 {
			if textPart, ok := parts[0].(map[string]any); ok && textPart["type"] == "text" {
				result["content"] = textPart["text"]
			} else {
				result["content"] = parts
			}
		} else if len(parts) > 0 {
			result["content"] = parts
		} else {
			result["content"] = nil
		}
	}
	return result, nil
}

// Chat Completions requires one role=tool message per tool result. Canonical
// messages can contain multiple ordered content items, so split only at those
// result boundaries and retain the order of all other content.
func chatMessages(message llm.Message) ([]any, error) {
	result := make([]any, 0, len(message.Content))
	pending := make([]llm.Content, 0, len(message.Content))
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		projected, err := chatMessage(llm.Message{Role: message.Role, Content: pending})
		if err != nil {
			return err
		}
		result = append(result, projected)
		pending = nil
		return nil
	}
	for _, content := range message.Content {
		if _, ok := content.(llm.ToolResultContent); !ok {
			pending = append(pending, content)
			continue
		}
		if err := flush(); err != nil {
			return nil, err
		}
		projected, err := chatMessage(llm.Message{Role: llm.MessageRoleTool, Content: []llm.Content{content}})
		if err != nil {
			return nil, err
		}
		result = append(result, projected)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		projected, err := chatMessage(message)
		if err != nil {
			return nil, err
		}
		result = append(result, projected)
	}
	return result, nil
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

func chatToolChoice(choice llm.ToolChoice) any {
	switch choice.Type {
	case llm.ToolChoiceAuto:
		return "auto"
	case llm.ToolChoiceNone:
		return "none"
	case llm.ToolChoiceRequired:
		return "required"
	case llm.ToolChoiceNamed:
		if choice.Name != nil {
			return map[string]any{"type": "function", "function": map[string]any{"name": *choice.Name}}
		}
	}
	return "auto"
}

func parseUsage(usage map[string]any) *llm.Usage {
	result := &llm.Usage{InputTokens: numberPtr(usage["prompt_tokens"]), OutputTokens: numberPtr(usage["completion_tokens"]), TotalTokens: numberPtr(usage["total_tokens"])}
	if details, _ := usage["prompt_tokens_details"].(map[string]any); details != nil {
		result.CacheReadInputTokens = numberPtr(details["cached_tokens"])
	}
	if details, _ := usage["completion_tokens_details"].(map[string]any); details != nil {
		result.ReasoningTokens = numberPtr(details["reasoning_tokens"])
	}
	if result.InputTokens != nil && result.CacheReadInputTokens != nil {
		value := *result.InputTokens - *result.CacheReadInputTokens
		if value >= 0 {
			result.NonCachedInputTokens = &value
			result.CacheWriteInputTokens = provider.NumberPtr(0)
		}
	}
	result.ProviderMetadata = provider.Metadata("compatible", map[string]any{"usage": usage})
	return result
}

func chunkMetadata(object map[string]any) llm.ProviderMetadata {
	fields := map[string]any{}
	for _, key := range []string{"id", "model", "system_fingerprint", "service_tier"} {
		if value := object[key]; value != nil {
			fields[key] = value
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return provider.Metadata("compatible", fields)
}

func finishReason(value string) llm.FinishReason {
	switch value {
	case "stop":
		return llm.FinishStop
	case "length":
		return llm.FinishLength
	case "tool_calls", "function_call":
		return llm.FinishToolCalls
	case "content_filter":
		return llm.FinishContentFilter
	default:
		return llm.FinishUnknown
	}
}

func numberPtr(value any) *float64 {
	switch value := value.(type) {
	case float64:
		return &value
	case json.Number:
		result, err := value.Float64()
		if err == nil {
			return &result
		}
	case int:
		result := float64(value)
		return &result
	}
	return nil
}

func indexValue(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	case float64:
		return strconv.FormatInt(int64(value), 10)
	default:
		return "0"
	}
}

func stringValue(value any) string {
	if result, ok := value.(string); ok {
		return result
	}
	return ""
}

func firstString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if result := stringValue(object[key]); result != "" {
			return result
		}
	}
	return ""
}

func safeErrorMessage(object map[string]any) string {
	return "provider returned an error event"
}

func cloneHeaders(headers http.Header) http.Header {
	result := make(http.Header, len(headers))
	for key, values := range headers {
		result[key] = append([]string(nil), values...)
	}
	return result
}
