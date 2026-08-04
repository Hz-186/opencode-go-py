package llm

import (
	"errors"
	"fmt"
	"math"
)

type Usage struct {
	InputTokens           *float64
	OutputTokens          *float64
	NonCachedInputTokens  *float64
	CacheReadInputTokens  *float64
	CacheWriteInputTokens *float64
	ReasoningTokens       *float64
	TotalTokens           *float64
	ProviderMetadata      ProviderMetadata
}

func (usage Usage) Validate() error {
	values := []struct {
		name  string
		value *float64
	}{
		{name: "inputTokens", value: usage.InputTokens},
		{name: "outputTokens", value: usage.OutputTokens},
		{name: "nonCachedInputTokens", value: usage.NonCachedInputTokens},
		{name: "cacheReadInputTokens", value: usage.CacheReadInputTokens},
		{name: "cacheWriteInputTokens", value: usage.CacheWriteInputTokens},
		{name: "reasoningTokens", value: usage.ReasoningTokens},
		{name: "totalTokens", value: usage.TotalTokens},
	}
	for _, item := range values {
		if item.value == nil {
			continue
		}
		if math.IsNaN(*item.value) || math.IsInf(*item.value, 0) || *item.value < 0 {
			return fmt.Errorf("usage %s must be finite and non-negative", item.name)
		}
	}
	if usage.InputTokens != nil && usage.NonCachedInputTokens != nil && usage.CacheReadInputTokens != nil && usage.CacheWriteInputTokens != nil {
		breakdown := *usage.NonCachedInputTokens + *usage.CacheReadInputTokens + *usage.CacheWriteInputTokens
		if !sameNumber(breakdown, *usage.InputTokens) {
			return errors.New("usage input token breakdown does not equal inputTokens")
		}
	}
	if usage.OutputTokens != nil && usage.ReasoningTokens != nil && *usage.ReasoningTokens > *usage.OutputTokens {
		return errors.New("usage reasoningTokens exceeds outputTokens")
	}
	if err := usage.ProviderMetadata.Validate(); err != nil {
		return err
	}
	return nil
}

func (usage Usage) VisibleOutputTokens() float64 {
	output := numberOrZero(usage.OutputTokens)
	reasoning := numberOrZero(usage.ReasoningTokens)
	return math.Max(0, output-reasoning)
}

func numberOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func sameNumber(left float64, right float64) bool {
	difference := math.Abs(left - right)
	scale := math.Max(1, math.Max(math.Abs(left), math.Abs(right)))
	return difference <= scale*1e-12
}
