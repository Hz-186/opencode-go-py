package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/Hz-186/opencode-go-py/internal/codec"
	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

// EventEmitter couples the canonical stream validator to a bounded sink. An
// adapter must emit through this type so malformed native ordering cannot
// escape into Runner state.
type EventEmitter struct {
	ctx       context.Context
	sink      LLMEventSink
	validator *StreamValidator
	pending   llm.LLMEvent
	Started   bool
}

func NewEventEmitter(ctx context.Context, sink LLMEventSink) (*EventEmitter, error) {
	if ctx == nil || sink == nil {
		return nil, fmt.Errorf("%w: emitter requires context and sink", ErrInvalidRequest)
	}
	return &EventEmitter{ctx: ctx, sink: sink, validator: NewStreamValidator()}, nil
}

// EmitClassifiedHTTPFailure turns a safely classified pre-stream provider
// rejection into the same canonical terminal event used for native SSE error
// records. Unclassified HTTP/transport errors remain AttemptError values so
// RunWithRetry retains sole ownership of retry decisions.
func EmitClassifiedHTTPFailure(ctx context.Context, sink LLMEventSink, err error, providerName string) (bool, error) {
	var failure *ProviderHTTPFailure
	if !errors.As(err, &failure) || failure.Classification == nil {
		return false, nil
	}
	emitter, emitErr := NewEventEmitter(ctx, sink)
	if emitErr != nil {
		return true, emitErr
	}
	if emitErr := emitter.Emit(llm.StepStart{Index: 0}); emitErr != nil {
		return true, emitErr
	}
	fields := map[string]any{"source": "http"}
	var attempt *AttemptError
	if errors.As(err, &attempt) {
		fields["status"] = attempt.Status
	}
	if emitErr := emitter.Emit(llm.ProviderError{
		Message:          "provider rejected the request",
		Classification:   failure.Classification,
		Retryable:        failure.Retryable,
		ProviderMetadata: Metadata(providerName, fields),
	}); emitErr != nil {
		return true, emitErr
	}
	return true, emitter.Done()
}

func (emitter *EventEmitter) Emit(event llm.LLMEvent) error {
	if emitter == nil || emitter.sink == nil || emitter.validator == nil {
		return fmt.Errorf("%w: emitter is not initialized", ErrInvalidStream)
	}
	if event != nil && event.EventType() == llm.EventStepStart {
		if err := emitter.validator.Accept(event); err != nil {
			return err
		}
		emitter.pending = event
		return nil
	}
	if event != nil && event.EventType() != llm.EventFinish {
		// Mark the stream before the first sink call. If the pending StepStart
		// succeeds but the semantic event fails, retrying would duplicate the
		// already observed start.
		emitter.Started = true
	}
	if emitter.pending != nil {
		if err := emitter.sink.Send(asContext(emitter.ctx), emitter.pending); err != nil {
			return fmt.Errorf("%w: %w", ErrSink, err)
		}
		emitter.pending = nil
	}
	if err := emitter.validator.Accept(event); err != nil {
		return err
	}
	if err := emitter.sink.Send(asContext(emitter.ctx), event); err != nil {
		return fmt.Errorf("%w: %w", ErrSink, err)
	}
	return nil
}

func (emitter *EventEmitter) Done() error {
	if emitter == nil || emitter.validator == nil {
		return fmt.Errorf("%w: emitter is not initialized", ErrStreamMissingFinal)
	}
	return emitter.validator.Done()
}

// asContext keeps the helper's public constructor flexible while still
// handing the sink a real context. All production callers pass context.Context.
func asContext(value context.Context) context.Context { return value }

// JSONValueAny converts canonical JSON without using float64, preserving large
// integers in request/tool payloads.
func JSONValueAny(value domain.JSONValue) (any, error) {
	encoded, err := codec.EncodeJSONValue(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	var result any
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func AnyJSONValue(value any) (domain.JSONValue, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return domain.JSONValue{}, err
	}
	return codec.DecodeJSONValue(encoded)
}

func DecodeNativeJSON(value string) (domain.JSONValue, error) {
	decoded, err := codec.DecodeJSONValue([]byte(value))
	if err != nil {
		return domain.JSONValue{}, fmt.Errorf("%w: native JSON value", ErrMalformedFrame)
	}
	return decoded, nil
}

func Metadata(providerName string, fields map[string]any) llm.ProviderMetadata {
	metadata := llm.ProviderMetadata{}
	values := make(map[string]domain.JSONValue, len(fields))
	for key, value := range fields {
		converted, err := AnyJSONValue(value)
		if err == nil {
			values[key] = converted
		}
	}
	if len(values) > 0 {
		metadata[providerName] = values
	}
	return metadata
}

func BoolPtr(value bool) *bool { return &value }

func NumberPtr(value float64) *float64 { return &value }

// ClassifyProviderError derives canonical control fields without retaining the
// provider's raw message. Raw messages may echo prompts or credentials.
func ClassifyProviderError(code, typeName, message string) (*llm.ProviderFailureClassification, *bool) {
	value := strings.ToLower(code + " " + typeName + " " + message)
	overflowMarkers := []string{"context_length", "context window", "context_window", "maximum context", "prompt too long", "too many tokens"}
	for _, marker := range overflowMarkers {
		if strings.Contains(value, marker) {
			classification := llm.ProviderFailureContextOverflow
			return &classification, BoolPtr(false)
		}
	}
	retryMarkers := []string{"rate_limit", "rate limit", "overloaded", "server_error", "internal_error", "timeout", "temporarily unavailable"}
	for _, marker := range retryMarkers {
		if strings.Contains(value, marker) {
			return nil, BoolPtr(true)
		}
	}
	return nil, BoolPtr(false)
}

// MergeProviderOptions applies only explicitly selected provider option
// namespaces. Unknown namespaces remain out of a native request rather than
// silently changing protocol semantics.
func MergeProviderOptions(target map[string]any, options llm.ProviderMetadata, namespaces ...string) error {
	for _, namespace := range namespaces {
		for key, value := range options[namespace] {
			converted, err := JSONValueAny(value)
			if err != nil {
				return fmt.Errorf("provider option %s.%s: %w", namespace, key, err)
			}
			target[key] = converted
		}
	}
	return nil
}

// ApplyHTTPOptions adds explicit per-request headers/query/body extensions.
// Header and query values are used only for the request; they are never
// returned by adapter errors or diagnostics.
func ApplyHTTPOptions(request *http.Request, options *llm.HTTPOptions, body map[string]any) error {
	if options == nil {
		return nil
	}
	if body != nil {
		for key, value := range options.Body {
			converted, err := JSONValueAny(value)
			if err != nil {
				return fmt.Errorf("HTTP body option %s: %w", key, err)
			}
			body[key] = converted
		}
	}
	for key, value := range options.Headers {
		request.Header.Set(key, value)
	}
	query := request.URL.Query()
	for key, value := range options.Query {
		query.Set(key, value)
	}
	request.URL.RawQuery = query.Encode()
	return nil
}

// QueryValues is kept as a small test/helper API for callers that need to
// inspect projected URL options without mutating a request.
func QueryValues(values map[string]string) url.Values {
	result := url.Values{}
	for key, value := range values {
		result.Set(key, value)
	}
	return result
}

// SingleAttemptClient clones a client and disables automatic redirects. A
// redirect can replay POST bodies and credentials to a different endpoint,
// which is an implicit fallback outside ProviderPort retry policy.
func SingleAttemptClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &clone
}

func RequiredCapabilities(request ProviderTurnRequest) []string {
	set := map[string]bool{"text": true}
	if len(request.Request.Tools) > 0 {
		set["tool-calls"] = true
	}
	for _, message := range request.Request.Messages {
		for _, content := range message.Content {
			switch content.(type) {
			case llm.MediaContent:
				set["image-input"] = true
			case llm.ReasoningContent:
				set["reasoning"] = true
			case llm.ToolCallContent, llm.ToolResultContent:
				set["tool-calls"] = true
			}
		}
	}
	if request.Request.ResponseFormat != nil {
		switch request.Request.ResponseFormat.Type {
		case llm.ResponseFormatJSON:
			set["json-output"] = true
		case llm.ResponseFormatTool:
			set["tool-calls"] = true
		}
	}
	result := make([]string, 0, len(set))
	for capability := range set {
		result = append(result, capability)
	}
	sort.Strings(result)
	return result
}

// EffectiveToolsAndChoice applies the canonical tool response format before a
// protocol projects tools. ResponseFormatTool is represented by a real native
// function/tool definition and a forced named choice; adapters must never
// silently drop it or treat it as ordinary free-form text.
func EffectiveToolsAndChoice(request ProviderTurnRequest) ([]llm.ToolDefinition, *llm.ToolChoice) {
	tools := append([]llm.ToolDefinition(nil), request.Request.Tools...)
	choice := request.Request.ToolChoice
	format := request.Request.ResponseFormat
	if format == nil || format.Type != llm.ResponseFormatTool || format.Tool == nil {
		return tools, choice
	}
	tools = append(tools, *format.Tool)
	name := format.Tool.Name
	choice = &llm.ToolChoice{Type: llm.ToolChoiceNamed, Name: &name}
	return tools, choice
}
