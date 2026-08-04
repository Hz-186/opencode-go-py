package codec

import (
	"math"
	"strings"
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

func TestLLMEventCodecCoversFrozenTaggedUnion(t *testing.T) {
	fixtures := []struct {
		typeName llm.EventType
		json     string
	}{
		{llm.EventStepStart, `{"type":"step-start","index":0}`},
		{llm.EventTextStart, `{"type":"text-start","id":"text_1"}`},
		{llm.EventTextDelta, `{"type":"text-delta","id":"text_1","text":"你"}`},
		{llm.EventTextEnd, `{"type":"text-end","id":"text_1"}`},
		{llm.EventReasoningStart, `{"type":"reasoning-start","id":"reason_1"}`},
		{llm.EventReasoningDelta, `{"type":"reasoning-delta","id":"reason_1","text":"think"}`},
		{llm.EventReasoningEnd, `{"type":"reasoning-end","id":"reason_1"}`},
		{llm.EventToolInputStart, `{"type":"tool-input-start","id":"call_1","name":"lookup"}`},
		{llm.EventToolInputDelta, `{"type":"tool-input-delta","id":"call_1","name":"lookup","text":"{\"id\":"}`},
		{llm.EventToolInputEnd, `{"type":"tool-input-end","id":"call_1","name":"lookup"}`},
		{llm.EventToolCall, `{"type":"tool-call","id":"call_1","name":"lookup","input":{"id":9007199254740993}}`},
		{llm.EventToolResult, `{"type":"tool-result","id":"call_1","name":"lookup","result":{"type":"json","value":{"ok":true}}}`},
		{llm.EventToolError, `{"type":"tool-error","id":"call_1","name":"lookup","message":"failed"}`},
		{llm.EventStepFinish, `{"type":"step-finish","index":0,"reason":"tool-calls","usage":{"inputTokens":3}}`},
		{llm.EventFinish, `{"type":"finish","reason":"stop","usage":{"outputTokens":2}}`},
		{llm.EventProviderError, `{"type":"provider-error","message":"overflow","classification":"context-overflow","retryable":false}`},
	}

	for _, fixture := range fixtures {
		t.Run(string(fixture.typeName), func(t *testing.T) {
			event, err := DecodeLLMEventJSON([]byte(fixture.json))
			if err != nil {
				t.Fatalf("decode event: %v", err)
			}
			if event.EventType() != fixture.typeName {
				t.Fatalf("event type = %q", event.EventType())
			}
			encoded, err := EncodeLLMEventJSON(event)
			if err != nil {
				t.Fatalf("encode event: %v", err)
			}
			roundTrip, err := DecodeLLMEventJSON(encoded)
			if err != nil {
				t.Fatalf("round-trip event: %v", err)
			}
			if roundTrip.EventType() != fixture.typeName {
				t.Fatalf("round-trip type = %q", roundTrip.EventType())
			}
		})
	}
}

func TestLLMEventCodecPreservesUnknownEventsWithoutTreatingThemAsKnown(t *testing.T) {
	event, err := DecodeLLMEventJSON([]byte(`{"type":"future-event","payload":{"big":9007199254740993}}`))
	if err != nil {
		t.Fatalf("decode unknown event: %v", err)
	}
	unknown, ok := event.(llm.UnknownEvent)
	if !ok {
		t.Fatalf("event type = %T, want UnknownEvent", event)
	}
	if unknown.Raw.Kind != domain.JSONKindObject || unknown.Raw.Object["payload"].Object["big"].Number != "9007199254740993" {
		t.Fatalf("unknown raw event = %#v", unknown.Raw)
	}
	encoded, err := EncodeLLMEventJSON(unknown)
	if err != nil {
		t.Fatalf("encode unknown event: %v", err)
	}
	if string(encoded) != `{"payload":{"big":9007199254740993},"type":"future-event"}`+"\n" {
		t.Fatalf("encoded unknown event = %s", encoded)
	}
}

func TestLLMEventCodecRejectsKnownShapeDrift(t *testing.T) {
	invalid := []string{
		`{"type":"finish","reason":"stop","usage":null}`,
		`{"type":"finish","reason":"stop","providerMetadata":null}`,
		`{"type":"finish","reason":"stop","providerMetadata":{"openai":true}}`,
		`{"type":"finish","reason":"bogus"}`,
		`{"type":"finish","reason":"stop","extra":true}`,
		`{"type":"step-start"}`,
		`{"type":"step-start","index":1e400}`,
		`{"type":"tool-call","id":"call_1","name":"lookup"}`,
		`{"type":"tool-call","id":"call_1","name":"lookup","input":{},"providerExecuted":null}`,
		`{"type":"tool-result","id":"call_1","name":"lookup","result":{"type":"content","value":{}}}`,
		`{"type":"tool-result","id":"call_1","name":"lookup","result":{"type":"content","value":[{"type":"file","uri":"file:///tmp/a","mime":"text/plain","name":null}]}}`,
		`{"type":"tool-result","id":"call_1","name":"lookup","result":{"type":"json","value":{}},"output":{"content":[]}}`,
		`{"type":"tool-error","id":"call_1","name":"lookup","message":"failed","error":null}`,
		`{"type":"provider-error","message":"failed","classification":null}`,
		`{"type":"finish","reason":"stop","usage":{"inputTokens":2,"nonCachedInputTokens":1,"cacheReadInputTokens":1,"cacheWriteInputTokens":1}}`,
	}
	for _, input := range invalid {
		if _, err := DecodeLLMEventJSON([]byte(input)); err == nil {
			t.Fatalf("invalid event %s unexpectedly succeeded", input)
		}
	}
}

func TestLLMEventCodecRoundTripsFrozenNestedContractsDeterministically(t *testing.T) {
	inputs := []string{
		`{"type":"text-delta","id":"text_1","text":"你好","providerMetadata":{"openai":{"requestID":"req_1","large":9007199254740993}}}`,
		`{"type":"tool-result","id":"call_1","name":"lookup","result":{"type":"content","value":[{"type":"text","text":"ok"},{"type":"file","uri":"file:///tmp/result.txt","mime":"text/plain","name":"result.txt"}]},"output":{"structured":{"large":9007199254740993},"content":[{"type":"text","text":"ok"}]},"providerExecuted":false,"providerMetadata":{"openai":{"requestID":"req_2"}}}`,
		`{"type":"step-finish","index":1,"reason":"stop","usage":{"inputTokens":3,"outputTokens":2,"nonCachedInputTokens":1,"cacheReadInputTokens":1,"cacheWriteInputTokens":1,"reasoningTokens":1,"totalTokens":5,"providerMetadata":{"openai":{"cached":true}}},"providerMetadata":{"openai":{"finishReason":"stop"}}}`,
	}

	for _, input := range inputs {
		event, err := DecodeLLMEventJSON([]byte(input))
		if err != nil {
			t.Fatalf("decode nested event %s: %v", input, err)
		}
		first, err := EncodeLLMEventJSON(event)
		if err != nil {
			t.Fatalf("encode nested event %s: %v", input, err)
		}
		if !strings.HasSuffix(string(first), "\n") {
			t.Fatalf("encoded event has no trailing newline: %q", first)
		}
		roundTrip, err := DecodeLLMEventJSON(first)
		if err != nil {
			t.Fatalf("decode canonical event %s: %v", first, err)
		}
		second, err := EncodeLLMEventJSON(roundTrip)
		if err != nil {
			t.Fatalf("re-encode canonical event %s: %v", first, err)
		}
		if string(first) != string(second) {
			t.Fatalf("event encoding is not deterministic:\nfirst:  %ssecond: %s", first, second)
		}
		if strings.Contains(input, "9007199254740993") && !strings.Contains(string(first), "9007199254740993") {
			t.Fatalf("large integer was not preserved: %s", first)
		}
	}
}

func TestLLMEventCodecRejectsInvalidDomainValues(t *testing.T) {
	invalid := []llm.LLMEvent{
		llm.StepStart{Index: math.NaN()},
		llm.StepStart{Index: math.Inf(1)},
		llm.Finish{Reason: llm.FinishReason("bogus")},
		llm.UnknownEvent{
			Type: string(llm.EventFinish),
			Raw: domain.JSONObject(map[string]domain.JSONValue{
				"type":  domain.JSONString(string(llm.EventFinish)),
				"extra": domain.JSONBool(true),
			}),
		},
	}

	for _, event := range invalid {
		if encoded, err := EncodeLLMEventJSON(event); err == nil {
			t.Fatalf("invalid domain event %#v encoded as %s", event, encoded)
		}
	}
}
