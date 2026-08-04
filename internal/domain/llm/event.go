package llm

import "github.com/Hz-186/opencode-go-py/internal/domain"

type EventType string

const (
	EventStepStart      EventType = "step-start"
	EventTextStart      EventType = "text-start"
	EventTextDelta      EventType = "text-delta"
	EventTextEnd        EventType = "text-end"
	EventReasoningStart EventType = "reasoning-start"
	EventReasoningDelta EventType = "reasoning-delta"
	EventReasoningEnd   EventType = "reasoning-end"
	EventToolInputStart EventType = "tool-input-start"
	EventToolInputDelta EventType = "tool-input-delta"
	EventToolInputEnd   EventType = "tool-input-end"
	EventToolCall       EventType = "tool-call"
	EventToolResult     EventType = "tool-result"
	EventToolError      EventType = "tool-error"
	EventStepFinish     EventType = "step-finish"
	EventFinish         EventType = "finish"
	EventProviderError  EventType = "provider-error"
)

type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishToolCalls     FinishReason = "tool-calls"
	FinishContentFilter FinishReason = "content-filter"
	FinishError         FinishReason = "error"
	FinishUnknown       FinishReason = "unknown"
)

type ProviderFailureClassification string

const ProviderFailureContextOverflow ProviderFailureClassification = "context-overflow"

type LLMEvent interface {
	EventType() EventType
}

type StepStart struct {
	Index float64
}

func (StepStart) EventType() EventType { return EventStepStart }

type TextStart struct {
	ID               string
	ProviderMetadata ProviderMetadata
}

func (TextStart) EventType() EventType { return EventTextStart }

type TextDelta struct {
	ID               string
	Text             string
	ProviderMetadata ProviderMetadata
}

func (TextDelta) EventType() EventType { return EventTextDelta }

type TextEnd struct {
	ID               string
	ProviderMetadata ProviderMetadata
}

func (TextEnd) EventType() EventType { return EventTextEnd }

type ReasoningStart struct {
	ID               string
	ProviderMetadata ProviderMetadata
}

func (ReasoningStart) EventType() EventType { return EventReasoningStart }

type ReasoningDelta struct {
	ID               string
	Text             string
	ProviderMetadata ProviderMetadata
}

func (ReasoningDelta) EventType() EventType { return EventReasoningDelta }

type ReasoningEnd struct {
	ID               string
	ProviderMetadata ProviderMetadata
}

func (ReasoningEnd) EventType() EventType { return EventReasoningEnd }

type ToolInputStart struct {
	ID               string
	Name             string
	ProviderMetadata ProviderMetadata
}

func (ToolInputStart) EventType() EventType { return EventToolInputStart }

type ToolInputDelta struct {
	ID   string
	Name string
	Text string
}

func (ToolInputDelta) EventType() EventType { return EventToolInputDelta }

type ToolInputEnd struct {
	ID               string
	Name             string
	ProviderMetadata ProviderMetadata
}

func (ToolInputEnd) EventType() EventType { return EventToolInputEnd }

type ToolCall struct {
	ID               string
	Name             string
	Input            domain.JSONValue
	ProviderExecuted *bool
	ProviderMetadata ProviderMetadata
}

func (ToolCall) EventType() EventType { return EventToolCall }

type ToolResult struct {
	ID               string
	Name             string
	Result           domain.JSONValue
	Output           *domain.JSONValue
	ProviderExecuted *bool
	ProviderMetadata ProviderMetadata
}

func (ToolResult) EventType() EventType { return EventToolResult }

type ToolError struct {
	ID               string
	Name             string
	Message          string
	Error            *domain.JSONValue
	ProviderMetadata ProviderMetadata
}

func (ToolError) EventType() EventType { return EventToolError }

type StepFinish struct {
	Index            float64
	Reason           FinishReason
	Usage            *Usage
	ProviderMetadata ProviderMetadata
}

func (StepFinish) EventType() EventType { return EventStepFinish }

type Finish struct {
	Reason           FinishReason
	Usage            *Usage
	ProviderMetadata ProviderMetadata
}

func (Finish) EventType() EventType { return EventFinish }

type ProviderError struct {
	Message          string
	Classification   *ProviderFailureClassification
	Retryable        *bool
	ProviderMetadata ProviderMetadata
}

func (ProviderError) EventType() EventType { return EventProviderError }

type UnknownEvent struct {
	Type string
	Raw  domain.JSONValue
}

func (event UnknownEvent) EventType() EventType { return EventType(event.Type) }
