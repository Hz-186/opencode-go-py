package codec

import (
	"strings"
	"testing"
)

func TestEventEnvelopeCodecRoundTripsFrozenContract(t *testing.T) {
	inputs := []string{
		`{"id":"evt_test","type":"session.message.created","data":{"sessionID":"ses_test","message":{"type":"future"}}}`,
		`{"id":"evt_test","type":"future.event","data":{"large":9007199254740993,"text":"你好"},"durable":{"aggregateID":"ses_test","seq":9007199254740993,"version":2},"location":{"directory":"/tmp/项目","workspaceID":"wrk_test"},"metadata":{"trace":{"large":9007199254740993}}}`,
	}

	for _, input := range inputs {
		event, err := DecodeEventEnvelopeJSON([]byte(input))
		if err != nil {
			t.Fatalf("decode event envelope %s: %v", input, err)
		}
		first, err := EncodeEventEnvelopeJSON(event)
		if err != nil {
			t.Fatalf("encode event envelope: %v", err)
		}
		roundTrip, err := DecodeEventEnvelopeJSON(first)
		if err != nil {
			t.Fatalf("decode canonical event envelope: %v", err)
		}
		second, err := EncodeEventEnvelopeJSON(roundTrip)
		if err != nil {
			t.Fatalf("re-encode event envelope: %v", err)
		}
		if string(first) != string(second) || !strings.HasSuffix(string(first), "\n") {
			t.Fatalf("event envelope encoding is not deterministic: first=%q second=%q", first, second)
		}
		if strings.Contains(input, "9007199254740993") && strings.Count(string(first), "9007199254740993") != strings.Count(input, "9007199254740993") {
			t.Fatalf("large integers were not preserved: %s", first)
		}
	}
}

func TestEventEnvelopeCodecRejectsFrozenShapeDrift(t *testing.T) {
	invalid := []string{
		`{"id":"bad","type":"future.event","data":{}}`,
		`{"id":"evt_test","type":"","data":{}}`,
		`{"id":"evt_test","type":"future.event"}`,
		`{"id":"evt_test","type":"future.event","data":{},"durable":null}`,
		`{"id":"evt_test","type":"future.event","data":{},"durable":{"aggregateID":"ses_test","seq":1.5,"version":1}}`,
		`{"id":"evt_test","type":"future.event","data":{},"durable":{"aggregateID":"ses_test","seq":1,"version":1,"extra":true}}`,
		`{"id":"evt_test","type":"future.event","data":{},"location":null}`,
		`{"id":"evt_test","type":"future.event","data":{},"location":{"directory":"/tmp","workspaceID":"bad"}}`,
		`{"id":"evt_test","type":"future.event","data":{},"location":{"directory":"/tmp","extra":true}}`,
		`{"id":"evt_test","type":"future.event","data":{},"metadata":null}`,
		`{"id":"evt_test","type":"future.event","data":{},"extra":true}`,
	}
	for _, input := range invalid {
		if _, err := DecodeEventEnvelopeJSON([]byte(input)); err == nil {
			t.Fatalf("invalid event envelope unexpectedly succeeded: %s", input)
		}
	}
}
