package llm

import (
	"math"
	"testing"
)

func TestUsageValidatesNonNegativeBreakdownInvariants(t *testing.T) {
	tests := []struct {
		name    string
		usage   Usage
		wantErr bool
	}{
		{name: "empty", usage: Usage{}},
		{name: "complete", usage: Usage{
			InputTokens: ptr(10), OutputTokens: ptr(8), NonCachedInputTokens: ptr(5),
			CacheReadInputTokens: ptr(3), CacheWriteInputTokens: ptr(2), ReasoningTokens: ptr(3), TotalTokens: ptr(18),
		}},
		{name: "partial breakdown", usage: Usage{InputTokens: ptr(10), CacheReadInputTokens: ptr(3)}},
		{name: "negative", usage: Usage{InputTokens: ptr(-1)}, wantErr: true},
		{name: "non finite", usage: Usage{OutputTokens: ptr(inf())}, wantErr: true},
		{name: "breakdown mismatch", usage: Usage{
			InputTokens: ptr(10), NonCachedInputTokens: ptr(5), CacheReadInputTokens: ptr(3), CacheWriteInputTokens: ptr(1),
		}, wantErr: true},
		{name: "reasoning exceeds output", usage: Usage{OutputTokens: ptr(4), ReasoningTokens: ptr(5)}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.usage.Validate()
			if test.wantErr && err == nil {
				t.Fatal("validation unexpectedly succeeded")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("validate: %v", err)
			}
		})
	}
}

func TestUsageVisibleOutputTokensClampsAtZero(t *testing.T) {
	if got := (Usage{OutputTokens: ptr(10), ReasoningTokens: ptr(4)}).VisibleOutputTokens(); got != 6 {
		t.Fatalf("visible output = %v", got)
	}
	if got := (Usage{OutputTokens: ptr(4), ReasoningTokens: ptr(10)}).VisibleOutputTokens(); got != 0 {
		t.Fatalf("clamped visible output = %v", got)
	}
	if got := (Usage{}).VisibleOutputTokens(); got != 0 {
		t.Fatalf("empty visible output = %v", got)
	}
}

func ptr(value float64) *float64 {
	return &value
}

func inf() float64 {
	return math.Inf(1)
}
