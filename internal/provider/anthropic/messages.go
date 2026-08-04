// Package anthropic implements the native Anthropic Messages HTTP/SSE adapter.
package anthropic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
	"github.com/Hz-186/opencode-go-py/internal/provider"
)

const defaultEndpoint = "https://api.anthropic.com/v1/messages"

var (
	ErrInvalidConfig = errors.New("invalid Anthropic Messages adapter config")
	ErrProtocol      = errors.New("invalid Anthropic Messages payload")
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

type MessagesProvider struct {
	client       *http.Client
	endpoint     string
	apiKey       string
	credential   provider.CredentialSource
	headers      http.Header
	catalog      *provider.Catalog
	maxLineBytes int
	maxBodyBytes int64
}

type AnthropicMessagesProvider = MessagesProvider

func NewMessagesProvider(config Config) *MessagesProvider {
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
	return &MessagesProvider{client: client, endpoint: endpoint, apiKey: config.APIKey, credential: config.Credential,
		headers: cloneHeaders(config.Headers), catalog: config.Catalog,
		maxLineBytes: lineBytes, maxBodyBytes: bodyBytes}
}

func NewAnthropicMessagesProvider(config Config) *MessagesProvider {
	return NewMessagesProvider(config)
}

func (p *MessagesProvider) ProjectRequest(request provider.ProviderTurnRequest) ([]byte, error) {
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
		if route.API != provider.APITypeAnthropicMessages {
			return nil, fmt.Errorf("%w: route API is %s", provider.ErrUnsupportedRoute, route.API)
		}
	}
	body := map[string]any{"model": request.Request.Model.ID, "max_tokens": float64(4096), "stream": true}
	systemParts := make([]any, 0, len(request.Request.System))
	for _, system := range request.Request.System {
		part := map[string]any{"type": "text", "text": system.Text}
		if system.Cache != nil {
			part["cache_control"] = map[string]any{"type": string(system.Cache.Type)}
		}
		systemParts = append(systemParts, part)
	}
	messages := make([]any, 0, len(request.Request.Messages))
	for _, message := range request.Request.Messages {
		if message.Role == llm.MessageRoleSystem {
			parts, err := anthropicSystemParts(message)
			if err != nil {
				return nil, err
			}
			systemParts = append(systemParts, parts...)
			continue
		}
		content := make([]any, 0, len(message.Content))
		for _, item := range message.Content {
			part, err := messageContent(item)
			if err != nil {
				return nil, err
			}
			content = append(content, part)
		}
		role := string(message.Role)
		if role != "user" && role != "assistant" {
			role = "user"
		}
		messages = append(messages, map[string]any{"role": role, "content": content})
	}
	if len(systemParts) > 0 {
		body["system"] = systemParts
	}
	body["messages"] = messages
	effectiveTools, effectiveChoice := provider.EffectiveToolsAndChoice(request)
	// Anthropic has no native "none" tool choice. Omitting definitions is the
	// exact projection: the model cannot call a tool that is not in the turn.
	if effectiveChoice == nil || effectiveChoice.Type != llm.ToolChoiceNone {
		tools := make([]any, 0, len(effectiveTools))
		for _, tool := range effectiveTools {
			schema, err := schemaAny(tool.InputSchema)
			if err != nil {
				return nil, err
			}
			projected := map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": schema}
			if tool.Cache != nil {
				projected["cache_control"] = map[string]any{"type": string(tool.Cache.Type)}
			}
			tools = append(tools, projected)
		}
		if len(tools) > 0 {
			body["tools"] = tools
		}
	}
	if choice := effectiveChoice; choice != nil && choice.Type != llm.ToolChoiceNone {
		body["tool_choice"] = anthropicToolChoice(*choice)
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
		if generation.TopK != nil {
			body["top_k"] = *generation.TopK
		}
		if len(generation.Stop) > 0 {
			body["stop_sequences"] = generation.Stop
		}
	}
	if format := request.Request.ResponseFormat; format != nil && format.Type == llm.ResponseFormatJSON {
		body["output_config"] = map[string]any{"format": map[string]any{"type": "json_schema", "schema": mustSchema(format.Schema)}}
	}
	if err := provider.MergeProviderOptions(body, request.Request.ProviderOptions, "anthropic", request.Request.Model.Provider); err != nil {
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

func (p *MessagesProvider) RunTurn(ctx context.Context, request provider.ProviderTurnRequest, sink provider.LLMEventSink) error {
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
	httpRequest.Header.Set("anthropic-version", "2023-06-01")
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
		httpRequest.Header.Set("x-api-key", string(secret))
	} else if p.apiKey != "" {
		httpRequest.Header.Set("x-api-key", p.apiKey)
	}
	for key, values := range p.headers {
		httpRequest.Header[key] = append([]string(nil), values...)
	}
	if err := provider.ApplyHTTPOptions(httpRequest, request.Request.HTTP, nil); err != nil {
		releaseCredential()
		return err
	}
	response, err := provider.DoHTTP(ctx, p.client, httpRequest, p.maxBodyBytes)
	httpRequest.Header.Del("x-api-key")
	releaseCredential()
	if err != nil {
		if handled, emitErr := provider.EmitClassifiedHTTPFailure(ctx, sink, err, "anthropic"); handled {
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
	state := streamState{emitter: emitter, blocks: map[string]*blockState{}}
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

type blockState struct {
	id, name, kind string
	args           strings.Builder
	signature      strings.Builder
	closed, called bool
}

type streamState struct {
	emitter  *provider.EventEmitter
	blocks   map[string]*blockState
	usage    *llm.Usage
	reason   llm.FinishReason
	metadata llm.ProviderMetadata
	terminal bool
}

func (state *streamState) frame(frame provider.SSEFrame) error {
	if strings.TrimSpace(frame.Data) == "" {
		return nil
	}
	if state.terminal {
		return fmt.Errorf("%w: event after terminal", ErrProtocol)
	}
	object, err := provider.DecodeFrameJSON(frame.Data)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrProtocol, err)
	}
	typeName := stringValue(object["type"])
	if typeName == "" {
		typeName = frame.Event
	}
	switch typeName {
	case "message_start":
		message, _ := object["message"].(map[string]any)
		state.usage = usageFromMessageStart(message)
		state.metadata = messageMetadata(message)
		return nil
	case "content_block_start":
		index := indexValue(object["index"])
		if _, exists := state.blocks[index]; exists {
			return fmt.Errorf("%w: duplicate content block", ErrProtocol)
		}
		content, _ := object["content_block"].(map[string]any)
		kind := stringValue(content["type"])
		id := firstString(content, "id")
		if id == "" {
			id = kind + "-" + index
		}
		block := &blockState{id: id, kind: kind, name: stringValue(content["name"])}
		if initial := content["input"]; initial != nil && !emptyJSONObject(initial) {
			if encoded, encodeErr := provider.RequestJSON(initial); encodeErr == nil {
				block.args.Write(encoded)
			}
		}
		state.blocks[index] = block
		switch kind {
		case "text":
			return state.emitter.Emit(llm.TextStart{ID: id, ProviderMetadata: provider.Metadata("anthropic", map[string]any{"index": index})})
		case "thinking", "redacted_thinking":
			block.kind = "thinking"
			return state.emitter.Emit(llm.ReasoningStart{ID: id})
		case "tool_use":
			if block.name == "" {
				return fmt.Errorf("%w: tool name missing", ErrProtocol)
			}
			return state.emitter.Emit(llm.ToolInputStart{ID: id, Name: block.name})
		default:
			return fmt.Errorf("%w: unsupported content block", ErrProtocol)
		}
	case "content_block_delta":
		index := indexValue(object["index"])
		block := state.blocks[index]
		if block == nil || block.closed {
			return fmt.Errorf("%w: delta for unknown block", ErrProtocol)
		}
		delta, _ := object["delta"].(map[string]any)
		deltaType := stringValue(delta["type"])
		switch deltaType {
		case "text_delta":
			return state.emitter.Emit(llm.TextDelta{ID: block.id, Text: stringValue(delta["text"])})
		case "thinking_delta":
			return state.emitter.Emit(llm.ReasoningDelta{ID: block.id, Text: stringValue(delta["thinking"])})
		case "signature_delta":
			block.signature.WriteString(stringValue(delta["signature"]))
			return nil
		case "input_json_delta":
			part := stringValue(delta["partial_json"])
			block.args.WriteString(part)
			return state.emitter.Emit(llm.ToolInputDelta{ID: block.id, Name: block.name, Text: part})
		default:
			return fmt.Errorf("%w: unsupported content delta", ErrProtocol)
		}
	case "content_block_stop":
		index := indexValue(object["index"])
		block := state.blocks[index]
		if block == nil || block.closed {
			return fmt.Errorf("%w: duplicate block stop", ErrProtocol)
		}
		block.closed = true
		switch block.kind {
		case "text":
			return state.emitter.Emit(llm.TextEnd{ID: block.id})
		case "thinking":
			return state.emitter.Emit(llm.ReasoningEnd{ID: block.id, ProviderMetadata: provider.Metadata("anthropic", map[string]any{"signature": block.signature.String()})})
		case "tool_use":
			if err := state.emitter.Emit(llm.ToolInputEnd{ID: block.id, Name: block.name}); err != nil {
				return err
			}
			input := domain.JSONNull()
			if strings.TrimSpace(block.args.String()) != "" {
				input, err = provider.DecodeNativeJSON(block.args.String())
				if err != nil {
					return fmt.Errorf("%w: tool input: %w", ErrProtocol, err)
				}
			}
			if err := state.emitter.Emit(llm.ToolCall{ID: block.id, Name: block.name, Input: input}); err != nil {
				return err
			}
			block.called = true
		}
		return nil
	case "message_delta":
		delta, _ := object["delta"].(map[string]any)
		state.reason = finishReason(stringValue(delta["stop_reason"]))
		if usage, _ := object["usage"].(map[string]any); usage != nil {
			if state.usage == nil {
				state.usage = &llm.Usage{}
			}
			state.usage.OutputTokens = numberPtr(usage["output_tokens"])
			state.usage.TotalTokens = sumTokens(state.usage.InputTokens, state.usage.OutputTokens)
		}
		return nil
	case "message_stop":
		if state.reason == "" {
			state.reason = llm.FinishStop
		}
		return state.finish(state.reason, state.usage)
	case "error":
		state.terminal = true
		errorObject, _ := object["error"].(map[string]any)
		classification, retryable := provider.ClassifyProviderError(stringValue(errorObject["code"]), stringValue(errorObject["type"]), stringValue(errorObject["message"]))
		return state.emitter.Emit(llm.ProviderError{Message: safeErrorMessage(object), Classification: classification, Retryable: retryable, ProviderMetadata: provider.Metadata("anthropic", map[string]any{"event": typeName})})
	default:
		switch typeName {
		case "ping":
			return nil
		}
		return fmt.Errorf("%w: unsupported event", ErrProtocol)
	}
}

func (state *streamState) finish(reason llm.FinishReason, usage *llm.Usage) error {
	if state.terminal {
		return fmt.Errorf("%w: duplicate terminal", ErrProtocol)
	}
	for _, block := range state.blocks {
		if !block.closed {
			return fmt.Errorf("%w: content block did not stop", ErrProtocol)
		}
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

func emptyJSONObject(value any) bool {
	object, ok := value.(map[string]any)
	return ok && len(object) == 0
}

func messageContent(content llm.Content) (map[string]any, error) {
	switch value := content.(type) {
	case llm.TextContent:
		result := map[string]any{"type": "text", "text": value.Text}
		if value.Cache != nil {
			result["cache_control"] = map[string]any{"type": string(value.Cache.Type)}
		}
		return result, nil
	case llm.ReasoningContent:
		return map[string]any{"type": "text", "text": value.Text}, nil
	case llm.MediaContent:
		data := value.Data
		if data == "" && len(value.Bytes) > 0 {
			data = base64.StdEncoding.EncodeToString(value.Bytes)
		}
		if strings.HasPrefix(data, "data:") {
			if comma := strings.IndexByte(data, ','); comma >= 0 {
				data = data[comma+1:]
			}
			return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": value.MediaType, "data": data}}, nil
		}
		if strings.HasPrefix(data, "http://") || strings.HasPrefix(data, "https://") {
			return map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": data}}, nil
		}
		return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": value.MediaType, "data": data}}, nil
	case llm.ToolCallContent:
		input, err := provider.JSONValueAny(value.Input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "tool_use", "id": value.ID, "name": value.Name, "input": input}, nil
	case llm.ToolResultContent:
		result, err := provider.JSONValueAny(value.Result)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "tool_result", "tool_use_id": value.ID, "content": result}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported content %T", provider.ErrInvalidRequest, content)
	}
}

func anthropicSystemParts(message llm.Message) ([]any, error) {
	parts := make([]any, 0, len(message.Content))
	for _, content := range message.Content {
		switch value := content.(type) {
		case llm.TextContent:
			part := map[string]any{"type": "text", "text": value.Text}
			if value.Cache != nil {
				part["cache_control"] = map[string]any{"type": string(value.Cache.Type)}
			}
			parts = append(parts, part)
		case llm.ReasoningContent:
			parts = append(parts, map[string]any{"type": "text", "text": value.Text})
		default:
			return nil, fmt.Errorf("%w: Anthropic system content %T", provider.ErrInvalidRequest, content)
		}
	}
	return parts, nil
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

func mustSchema(schema llm.JSONSchema) any {
	value, _ := schemaAny(schema)
	return value
}

func anthropicToolChoice(choice llm.ToolChoice) any {
	switch choice.Type {
	case llm.ToolChoiceAuto:
		return map[string]any{"type": "auto"}
	case llm.ToolChoiceRequired:
		return map[string]any{"type": "any"}
	case llm.ToolChoiceNamed:
		if choice.Name != nil {
			return map[string]any{"type": "tool", "name": *choice.Name}
		}
	}
	return map[string]any{"type": "auto"}
}

func usageFromMessageStart(message map[string]any) *llm.Usage {
	usage, _ := message["usage"].(map[string]any)
	if usage == nil {
		return nil
	}
	result := &llm.Usage{NonCachedInputTokens: numberPtr(usage["input_tokens"]), OutputTokens: numberPtr(usage["output_tokens"])}
	if cache := numberPtr(usage["cache_read_input_tokens"]); cache != nil {
		result.CacheReadInputTokens = cache
	}
	if cache := numberPtr(usage["cache_creation_input_tokens"]); cache != nil {
		result.CacheWriteInputTokens = cache
	}
	if result.NonCachedInputTokens != nil {
		input := *result.NonCachedInputTokens
		if result.CacheReadInputTokens != nil {
			input += *result.CacheReadInputTokens
		} else {
			result.CacheReadInputTokens = provider.NumberPtr(0)
		}
		if result.CacheWriteInputTokens != nil {
			input += *result.CacheWriteInputTokens
		} else {
			result.CacheWriteInputTokens = provider.NumberPtr(0)
		}
		result.InputTokens = &input
	}
	result.TotalTokens = sumTokens(result.InputTokens, result.OutputTokens)
	result.ProviderMetadata = provider.Metadata("anthropic", map[string]any{"model": message["model"], "id": message["id"]})
	return result
}

func messageMetadata(message map[string]any) llm.ProviderMetadata {
	fields := map[string]any{}
	for _, key := range []string{"id", "model", "type"} {
		if value := message[key]; value != nil {
			fields[key] = value
		}
	}
	return provider.Metadata("anthropic", fields)
}

func sumTokens(input, output *float64) *float64 {
	if input == nil || output == nil {
		return nil
	}
	value := *input + *output
	return &value
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

func finishReason(value string) llm.FinishReason {
	switch value {
	case "end_turn", "stop_sequence":
		return llm.FinishStop
	case "max_tokens":
		return llm.FinishLength
	case "tool_use":
		return llm.FinishToolCalls
	default:
		return llm.FinishUnknown
	}
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
		if value := stringValue(object[key]); value != "" {
			return value
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
