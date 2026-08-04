// Package provider defines the canonical Go boundary between a Runner and a
// model provider. Providers stream normalized LLM events; they never own
// tool settlement, retry policy, session state, or persistence.
package provider

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

var (
	ErrInvalidRequest  = errors.New("invalid provider turn request")
	ErrInvalidCassette = errors.New("invalid provider cassette")
	ErrNoCassette      = errors.New("provider cassette has no matching turn")
	ErrSink            = errors.New("provider event sink failed")
)

// ProviderTurnRequest is the complete input for one provider turn. Attempt is
// observable to the adapter but retry decisions remain owned by the Runner.
type ProviderTurnRequest struct {
	Request llm.Request
	Attempt int
}

func (request ProviderTurnRequest) Validate() error {
	if request.Attempt < 0 {
		return fmt.Errorf("%w: attempt must be non-negative", ErrInvalidRequest)
	}
	if request.Request.Model.Provider == "" || request.Request.Model.ID == "" ||
		request.Request.Model.Route == "" {
		return fmt.Errorf("%w: model provider, id, and route are required", ErrInvalidRequest)
	}
	for _, value := range []string{request.Request.Model.Provider, request.Request.Model.ID, request.Request.Model.Route} {
		if strings.TrimSpace(value) != value || strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("%w: model identity is not canonical", ErrInvalidRequest)
		}
	}
	if err := request.Request.ProviderOptions.Validate(); err != nil {
		return fmt.Errorf("%w: provider options: %v", ErrInvalidRequest, err)
	}
	for index, system := range request.Request.System {
		for key, value := range system.Metadata {
			if err := value.Validate(); err != nil {
				return fmt.Errorf("%w: system %d metadata %s", ErrInvalidRequest, index, key)
			}
		}
	}
	for messageIndex, message := range request.Request.Messages {
		if message.Role != llm.MessageRoleSystem && message.Role != llm.MessageRoleUser &&
			message.Role != llm.MessageRoleAssistant && message.Role != llm.MessageRoleTool {
			return fmt.Errorf("%w: message %d has unsupported role", ErrInvalidRequest, messageIndex)
		}
		for contentIndex, content := range message.Content {
			if content == nil {
				return fmt.Errorf("%w: message %d content %d is nil", ErrInvalidRequest, messageIndex, contentIndex)
			}
			switch value := content.(type) {
			case llm.MediaContent:
				if strings.TrimSpace(value.MediaType) == "" || (value.Data == "" && len(value.Bytes) == 0) {
					return fmt.Errorf("%w: message %d media content is incomplete", ErrInvalidRequest, messageIndex)
				}
			case llm.ToolCallContent:
				if strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.Name) == "" || value.Input.Validate() != nil {
					return fmt.Errorf("%w: message %d tool call is invalid", ErrInvalidRequest, messageIndex)
				}
			case llm.ToolResultContent:
				if strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.Name) == "" || value.Result.Validate() != nil {
					return fmt.Errorf("%w: message %d tool result is invalid", ErrInvalidRequest, messageIndex)
				}
			}
		}
	}
	toolNames := map[string]bool{}
	for index, tool := range request.Request.Tools {
		if err := validateToolDefinition(tool, toolNames); err != nil {
			return fmt.Errorf("%w: tool %d: %v", ErrInvalidRequest, index, err)
		}
		toolNames[tool.Name] = true
	}
	if format := request.Request.ResponseFormat; format != nil {
		switch format.Type {
		case llm.ResponseFormatText:
		case llm.ResponseFormatJSON:
			if err := validateSchema(format.Schema); err != nil {
				return fmt.Errorf("%w: response JSON schema: %v", ErrInvalidRequest, err)
			}
		case llm.ResponseFormatTool:
			if format.Tool == nil {
				return fmt.Errorf("%w: tool response format has no tool", ErrInvalidRequest)
			}
			if err := validateToolDefinition(*format.Tool, toolNames); err != nil {
				return fmt.Errorf("%w: response format tool: %v", ErrInvalidRequest, err)
			}
			toolNames[format.Tool.Name] = true
		default:
			return fmt.Errorf("%w: unsupported response format", ErrInvalidRequest)
		}
	}
	if choice := request.Request.ToolChoice; choice != nil {
		if choice.Type != llm.ToolChoiceAuto && choice.Type != llm.ToolChoiceNone &&
			choice.Type != llm.ToolChoiceRequired && choice.Type != llm.ToolChoiceNamed {
			return fmt.Errorf("%w: unsupported tool choice", ErrInvalidRequest)
		}
		if choice.Type == llm.ToolChoiceNamed && (choice.Name == nil || strings.TrimSpace(*choice.Name) == "") {
			return fmt.Errorf("%w: named tool choice has no name", ErrInvalidRequest)
		}
		if choice.Type == llm.ToolChoiceNamed && choice.Name != nil && !toolNames[*choice.Name] {
			return fmt.Errorf("%w: named tool choice is not defined", ErrInvalidRequest)
		}
	}
	if generation := request.Request.Generation; generation != nil {
		values := []*float64{generation.MaxTokens, generation.Temperature, generation.TopP, generation.TopK,
			generation.FrequencyPenalty, generation.PresencePenalty, generation.Seed}
		for _, value := range values {
			if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0)) {
				return fmt.Errorf("%w: generation value is not finite", ErrInvalidRequest)
			}
		}
		if generation.MaxTokens != nil && *generation.MaxTokens < 0 {
			return fmt.Errorf("%w: max tokens is negative", ErrInvalidRequest)
		}
	}
	return nil
}

func validateToolDefinition(tool llm.ToolDefinition, names map[string]bool) error {
	if strings.TrimSpace(tool.Name) == "" || strings.TrimSpace(tool.Name) != tool.Name || names[tool.Name] {
		return errors.New("invalid or duplicate name")
	}
	if err := validateSchema(tool.InputSchema); err != nil {
		return fmt.Errorf("input schema: %v", err)
	}
	if err := validateSchema(tool.OutputSchema); err != nil {
		return fmt.Errorf("output schema: %v", err)
	}
	for key, value := range tool.Metadata {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("metadata %s", key)
		}
	}
	for key, value := range tool.Native {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("native field %s", key)
		}
	}
	return nil
}

func validateSchema(schema llm.JSONSchema) error {
	for key, value := range schema {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("field %s", key)
		}
	}
	return nil
}

// LLMEventSink receives normalized events in provider order. Implementations
// must return context cancellation or backpressure errors to stop the turn.
type LLMEventSink interface {
	Send(context.Context, llm.LLMEvent) error
}

// LLMEventSinkFunc adapts a function to LLMEventSink.
type LLMEventSinkFunc func(context.Context, llm.LLMEvent) error

func (sink LLMEventSinkFunc) Send(ctx context.Context, event llm.LLMEvent) error {
	if sink == nil {
		return ErrSink
	}
	return sink(ctx, event)
}

// ProviderPort is the only Runner-to-provider contract. A provider must emit
// normalized events and return only transport/provider/sink failures; it may
// not execute tools or decide whether another attempt is allowed.
type ProviderPort interface {
	RunTurn(context.Context, ProviderTurnRequest, LLMEventSink) error
}
