package provider

import (
	"errors"
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

func TestStreamValidatorAcceptsCanonicalTextReasoningAndToolLifecycle(t *testing.T) {
	toolInput := domain.JSONObject(map[string]domain.JSONValue{"path": domain.JSONString("/tmp/file")})
	events := []llm.LLMEvent{
		llm.StepStart{Index: 0},
		llm.ReasoningStart{ID: "reasoning-1"}, llm.ReasoningDelta{ID: "reasoning-1", Text: "plan"}, llm.ReasoningEnd{ID: "reasoning-1"},
		llm.TextStart{ID: "text-1"}, llm.TextDelta{ID: "text-1", Text: "answer"}, llm.TextEnd{ID: "text-1"},
		llm.ToolInputStart{ID: "tool-1", Name: "read"}, llm.ToolInputDelta{ID: "tool-1", Name: "read", Text: `{"path":"/tmp/file"}`}, llm.ToolInputEnd{ID: "tool-1", Name: "read"},
		llm.ToolCall{ID: "tool-1", Name: "read", Input: toolInput},
		llm.ToolResult{ID: "tool-1", Name: "read", Result: domain.JSONObject(map[string]domain.JSONValue{"type": domain.JSONString("text"), "value": domain.JSONString("contents")})},
		llm.StepFinish{Index: 0, Reason: llm.FinishToolCalls},
		llm.StepStart{Index: 1}, llm.TextStart{ID: "text-2"}, llm.TextEnd{ID: "text-2"}, llm.StepFinish{Index: 1, Reason: llm.FinishStop},
		llm.Finish{Reason: llm.FinishStop},
	}
	if err := ValidateStream(events); err != nil {
		t.Fatalf("valid canonical stream rejected: %v", err)
	}
}

func TestStreamValidatorRejectsInvalidOrderDuplicateIDsAndTerminalTail(t *testing.T) {
	tests := []struct {
		name   string
		events []llm.LLMEvent
		want   error
	}{
		{name: "delta before start", events: []llm.LLMEvent{llm.TextDelta{ID: "text-1", Text: "bad"}}, want: ErrStreamState},
		{name: "duplicate start", events: []llm.LLMEvent{llm.TextStart{ID: "text-1"}, llm.TextStart{ID: "text-1"}}, want: ErrStreamDuplicateID},
		{name: "text end before delta is okay but duplicate end is not", events: []llm.LLMEvent{llm.TextStart{ID: "text-1"}, llm.TextEnd{ID: "text-1"}, llm.TextEnd{ID: "text-1"}}, want: ErrStreamState},
		{name: "tool result before call", events: []llm.LLMEvent{llm.ToolResult{ID: "tool-1", Name: "read", Result: domain.JSONObject(map[string]domain.JSONValue{"type": domain.JSONString("text"), "value": domain.JSONString("bad")})}}, want: ErrStreamState},
		{name: "tool call before input end", events: []llm.LLMEvent{llm.ToolInputStart{ID: "tool-1", Name: "read"}, llm.ToolCall{ID: "tool-1", Name: "read", Input: domain.JSONObject(nil)}}, want: ErrStreamState},
		{name: "terminal tail", events: []llm.LLMEvent{llm.Finish{Reason: llm.FinishStop}, llm.TextStart{ID: "text-after"}}, want: ErrStreamTerminal},
		{name: "open block at finish", events: []llm.LLMEvent{llm.TextStart{ID: "text-1"}, llm.Finish{Reason: llm.FinishStop}}, want: ErrStreamState},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateStream(test.events); !errors.Is(err, test.want) {
				t.Fatalf("stream error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestStreamValidatorRejectsUsageInvariantAndMissingTerminal(t *testing.T) {
	input, output, nonCached, cacheRead, cacheWrite := 4.0, 2.0, 1.0, 1.0, 1.0
	invalid := llm.Usage{InputTokens: &input, OutputTokens: &output, NonCachedInputTokens: &nonCached, CacheReadInputTokens: &cacheRead, CacheWriteInputTokens: &cacheWrite}
	validator := NewStreamValidator()
	if err := validator.Accept(llm.StepStart{Index: 0}); err != nil {
		t.Fatalf("step start: %v", err)
	}
	if err := validator.Accept(llm.StepFinish{Index: 0, Reason: llm.FinishStop, Usage: &invalid}); !errors.Is(err, ErrInvalidStream) {
		t.Fatalf("invalid usage error = %v", err)
	}
	if err := NewStreamValidator().Done(); !errors.Is(err, ErrStreamMissingFinal) {
		t.Fatal("missing terminal was accepted")
	}
}

func TestStreamValidatorProviderErrorIsTerminalAndMetadataIsValidated(t *testing.T) {
	validator := NewStreamValidator()
	classification := llm.ProviderFailureContextOverflow
	if err := validator.Accept(llm.ProviderError{Message: "overflow", Classification: &classification}); err != nil {
		t.Fatalf("provider error: %v", err)
	}
	if err := validator.Done(); err != nil {
		t.Fatalf("provider error did not terminate stream: %v", err)
	}
	if err := validator.Accept(llm.TextStart{ID: "after"}); !errors.Is(err, ErrStreamTerminal) {
		t.Fatalf("provider error tail = %v, want ErrStreamTerminal", err)
	}
}
