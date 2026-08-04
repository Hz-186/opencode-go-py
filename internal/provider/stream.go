package provider

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/Hz-186/opencode-go-py/internal/codec"
	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

var (
	ErrInvalidStream      = errors.New("invalid provider event stream")
	ErrStreamTerminal     = errors.New("provider event stream is already terminal")
	ErrStreamState        = errors.New("invalid provider block state")
	ErrStreamDuplicateID  = errors.New("duplicate provider block ID")
	ErrStreamMissingFinal = errors.New("provider event stream has no terminal event")
	ErrStreamUnknownEvent = errors.New("unknown provider event cannot be validated")
)

type blockKind uint8

const (
	blockText blockKind = iota + 1
	blockReasoning
	blockToolInput
	blockToolCall
)

type block struct {
	kind     blockKind
	name     string
	closed   bool
	called   bool
	terminal bool
}

// StreamValidator enforces canonical ordering without imposing provider
// protocol framing. It can be fed incrementally by HTTP/SSE adapters and is
// safe to discard after Done returns.
type StreamValidator struct {
	blocks      map[string]block
	stepActive  bool
	stepIndex   float64
	terminal    bool
	eventNumber int
}

func NewStreamValidator() *StreamValidator {
	return &StreamValidator{blocks: make(map[string]block)}
}

// Accept validates and records one event. On error the validator remains
// terminally unusable; callers must discard it rather than continue a stream
// after an invalid provider response.
func (validator *StreamValidator) Accept(event llm.LLMEvent) error {
	if validator == nil {
		return fmt.Errorf("%w: validator is nil", ErrInvalidStream)
	}
	index := validator.eventNumber
	validator.eventNumber++
	if validator.terminal {
		return streamError(index, event, ErrStreamTerminal, "event follows terminal")
	}
	if event == nil {
		validator.terminal = true
		return streamError(index, event, ErrInvalidStream, "event is nil")
	}
	if _, err := codec.EncodeLLMEventJSON(event); err != nil {
		validator.terminal = true
		return streamError(index, event, ErrInvalidStream, err.Error())
	}
	if unknown, ok := event.(llm.UnknownEvent); ok {
		validator.terminal = true
		return streamError(index, event, ErrStreamUnknownEvent, unknown.Type)
	}

	var err error
	switch value := event.(type) {
	case llm.StepStart:
		err = validator.stepStart(value)
	case llm.TextStart:
		err = validator.startBlock(value.ID, "text", blockText, "", value.ProviderMetadata)
	case llm.TextDelta:
		err = validator.deltaBlock(value.ID, "text", blockText, "", false)
	case llm.TextEnd:
		err = validator.endBlock(value.ID, "text", blockText, "")
	case llm.ReasoningStart:
		err = validator.startBlock(value.ID, "reasoning", blockReasoning, "", value.ProviderMetadata)
	case llm.ReasoningDelta:
		err = validator.deltaBlock(value.ID, "reasoning", blockReasoning, "", false)
	case llm.ReasoningEnd:
		err = validator.endBlock(value.ID, "reasoning", blockReasoning, "")
	case llm.ToolInputStart:
		err = validator.startBlock(value.ID, "tool-input", blockToolInput, value.Name, value.ProviderMetadata)
	case llm.ToolInputDelta:
		err = validator.deltaBlock(value.ID, "tool-input", blockToolInput, value.Name, true)
	case llm.ToolInputEnd:
		err = validator.endBlock(value.ID, "tool-input", blockToolInput, value.Name)
	case llm.ToolCall:
		err = validator.toolCall(value)
	case llm.ToolResult:
		err = validator.toolTerminal(value.ID, value.Name, "tool-result")
	case llm.ToolError:
		err = validator.toolTerminal(value.ID, value.Name, "tool-error")
	case llm.StepFinish:
		err = validator.stepFinish(value)
	case llm.Finish:
		err = validator.finish()
	case llm.ProviderError:
		validator.terminal = true
	default:
		err = fmt.Errorf("%w: unsupported event type %T", ErrInvalidStream, event)
	}
	if err != nil {
		validator.terminal = true
		return streamError(index, event, err, "state transition rejected")
	}
	return nil
}

// Done requires a terminal Finish or ProviderError. It is intentionally
// separate from Accept so a streaming transport can distinguish disconnect
// from a provider-declared terminal event.
func (validator *StreamValidator) Done() error {
	if validator == nil {
		return fmt.Errorf("%w: validator is nil", ErrStreamMissingFinal)
	}
	if !validator.terminal {
		return ErrStreamMissingFinal
	}
	return nil
}

func ValidateStream(events []llm.LLMEvent) error {
	validator := NewStreamValidator()
	for _, event := range events {
		if err := validator.Accept(event); err != nil {
			return err
		}
	}
	return validator.Done()
}

func (validator *StreamValidator) stepStart(event llm.StepStart) error {
	if !validIndex(event.Index) {
		return fmt.Errorf("%w: step index must be finite and non-negative", ErrInvalidStream)
	}
	if validator.stepActive {
		return fmt.Errorf("%w: step %g is already active", ErrStreamState, validator.stepIndex)
	}
	validator.stepActive = true
	validator.stepIndex = event.Index
	return nil
}

func (validator *StreamValidator) stepFinish(event llm.StepFinish) error {
	if !validIndex(event.Index) {
		return fmt.Errorf("%w: step index must be finite and non-negative", ErrInvalidStream)
	}
	if !validator.stepActive || event.Index != validator.stepIndex {
		return fmt.Errorf("%w: step finish index %g does not match active step %g", ErrStreamState, event.Index, validator.stepIndex)
	}
	if err := validateUsage(event.Usage); err != nil {
		return err
	}
	if err := validator.noOpenBlocks(); err != nil {
		return err
	}
	validator.stepActive = false
	return nil
}

func (validator *StreamValidator) finish() error {
	if validator.stepActive {
		return fmt.Errorf("%w: finish arrived before step finish", ErrStreamState)
	}
	if err := validator.noOpenBlocks(); err != nil {
		return err
	}
	validator.terminal = true
	return nil
}

func (validator *StreamValidator) startBlock(id, label string, kind blockKind, name string, metadata llm.ProviderMetadata) error {
	if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) || (kind == blockToolInput && strings.TrimSpace(name) == "") {
		return fmt.Errorf("%w: %s has empty identity", ErrInvalidStream, label)
	}
	if err := metadata.Validate(); err != nil {
		return fmt.Errorf("%w: %s metadata: %v", ErrInvalidStream, label, err)
	}
	if _, exists := validator.blocks[id]; exists {
		return fmt.Errorf("%w: %s", ErrStreamDuplicateID, id)
	}
	validator.blocks[id] = block{kind: kind, name: name}
	return nil
}

func (validator *StreamValidator) deltaBlock(id, label string, kind blockKind, name string, named bool) error {
	current, ok := validator.blocks[id]
	if !ok || current.kind != kind || current.closed || (named && current.name != name) {
		return fmt.Errorf("%w: %s delta for %q", ErrStreamState, label, id)
	}
	return nil
}

func (validator *StreamValidator) endBlock(id, label string, kind blockKind, name string) error {
	current, ok := validator.blocks[id]
	if !ok || current.kind != kind || current.closed || (name != "" && current.name != name) {
		return fmt.Errorf("%w: %s end for %q", ErrStreamState, label, id)
	}
	current.closed = true
	validator.blocks[id] = current
	return nil
}

func (validator *StreamValidator) toolCall(event llm.ToolCall) error {
	if strings.TrimSpace(event.ID) == "" || event.ID != strings.TrimSpace(event.ID) || strings.TrimSpace(event.Name) == "" {
		return fmt.Errorf("%w: tool call has empty identity", ErrInvalidStream)
	}
	if err := event.ProviderMetadata.Validate(); err != nil {
		return fmt.Errorf("%w: tool call metadata: %v", ErrInvalidStream, err)
	}
	current, exists := validator.blocks[event.ID]
	if !exists {
		validator.blocks[event.ID] = block{kind: blockToolCall, name: event.Name, closed: true, called: true}
		return nil
	}
	if current.kind != blockToolInput || !current.closed || current.called || current.name != event.Name {
		return fmt.Errorf("%w: tool call for %q", ErrStreamState, event.ID)
	}
	current.called = true
	validator.blocks[event.ID] = current
	return nil
}

func (validator *StreamValidator) toolTerminal(id, name, label string) error {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: %s has empty identity", ErrInvalidStream, label)
	}
	current, exists := validator.blocks[id]
	if !exists || !current.called || current.terminal || current.name != name {
		return fmt.Errorf("%w: %s for %q", ErrStreamState, label, id)
	}
	current.terminal = true
	validator.blocks[id] = current
	return nil
}

func (validator *StreamValidator) noOpenBlocks() error {
	for id, current := range validator.blocks {
		if (current.kind == blockText || current.kind == blockReasoning || current.kind == blockToolInput) && !current.closed {
			return fmt.Errorf("%w: block %q remains open", ErrStreamState, id)
		}
	}
	return nil
}

func validateUsage(usage *llm.Usage) error {
	if usage == nil {
		return nil
	}
	if err := usage.Validate(); err != nil {
		return fmt.Errorf("%w: usage: %v", ErrInvalidStream, err)
	}
	return nil
}

func validIndex(index float64) bool {
	return !math.IsNaN(index) && !math.IsInf(index, 0) && index >= 0 && math.Trunc(index) == index
}

func streamError(index int, event llm.LLMEvent, kind error, detail string) error {
	typeName := "<nil>"
	if event != nil {
		typeName = string(event.EventType())
	}
	return fmt.Errorf("%w: event=%d type=%s: %w: %s", ErrInvalidStream, index, typeName, kind, detail)
}
