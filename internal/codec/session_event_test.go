package codec

import (
	"fmt"
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

func TestSessionEventCodecCoversFrozenDefinitions(t *testing.T) {
	base := `"timestamp":1,"sessionID":"ses_test"`
	fixtures := []struct {
		typeName string
		data     string
		durable  bool
		version  int64
	}{
		{"session.next.agent.switched", base + `,"messageID":"msg_test","agent":"build"`, true, 1},
		{"session.next.model.switched", base + `,"messageID":"msg_test","model":{"id":"m","providerID":"p"}`, true, 1},
		{"session.next.moved", base + `,"location":{"directory":"/tmp"}`, true, 1},
		{"session.next.prompted", base + `,"messageID":"msg_test","prompt":{"text":"hello"},"delivery":"steer"`, true, 1},
		{"session.next.prompt.admitted", base + `,"messageID":"msg_test","prompt":{"text":"hello"},"delivery":"queue"`, true, 1},
		{"session.next.context.updated", base + `,"messageID":"msg_test","text":"context"`, true, 1},
		{"session.next.synthetic", base + `,"messageID":"msg_test","text":"synthetic"`, true, 1},
		{"session.next.shell.started", base + `,"messageID":"msg_test","callID":"call_1","command":"pwd"`, true, 1},
		{"session.next.shell.ended", base + `,"callID":"call_1","output":"/tmp"`, true, 1},
		{"session.next.step.started", base + `,"assistantMessageID":"msg_test","agent":"build","model":{"id":"m","providerID":"p"}`, true, 1},
		{"session.next.step.ended", base + `,"assistantMessageID":"msg_test","finish":"stop","cost":0,"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}`, true, 2},
		{"session.next.step.failed", base + `,"assistantMessageID":"msg_test","error":{"type":"unknown","message":"failed"}`, true, 2},
		{"session.next.text.started", base + `,"assistantMessageID":"msg_test","textID":"text_1"`, true, 1},
		{"session.next.text.delta", base + `,"assistantMessageID":"msg_test","textID":"text_1","delta":"a"`, false, 0},
		{"session.next.text.ended", base + `,"assistantMessageID":"msg_test","textID":"text_1","text":"all"`, true, 1},
		{"session.next.reasoning.started", base + `,"assistantMessageID":"msg_test","reasoningID":"reason_1"`, true, 1},
		{"session.next.reasoning.delta", base + `,"assistantMessageID":"msg_test","reasoningID":"reason_1","delta":"a"`, false, 0},
		{"session.next.reasoning.ended", base + `,"assistantMessageID":"msg_test","reasoningID":"reason_1","text":"all"`, true, 1},
		{"session.next.tool.input.started", base + `,"assistantMessageID":"msg_test","callID":"call_1","name":"lookup"`, true, 1},
		{"session.next.tool.input.delta", base + `,"assistantMessageID":"msg_test","callID":"call_1","delta":"{}"`, false, 0},
		{"session.next.tool.input.ended", base + `,"assistantMessageID":"msg_test","callID":"call_1","text":"{}"`, true, 1},
		{"session.next.tool.called", base + `,"assistantMessageID":"msg_test","callID":"call_1","tool":"lookup","input":{},"provider":{"executed":false}`, true, 1},
		{"session.next.tool.progress", base + `,"assistantMessageID":"msg_test","callID":"call_1","structured":{},"content":[]`, true, 1},
		{"session.next.tool.success", base + `,"assistantMessageID":"msg_test","callID":"call_1","structured":{},"content":[],"provider":{"executed":true}`, true, 1},
		{"session.next.tool.failed", base + `,"assistantMessageID":"msg_test","callID":"call_1","error":{"type":"unknown","message":"failed"},"provider":{"executed":true}`, true, 1},
		{"session.next.retried", base + `,"attempt":1,"error":{"message":"retry","isRetryable":true}`, true, 1},
		{"session.next.compaction.started", base + `,"messageID":"msg_test","reason":"auto"`, true, 1},
		{"session.next.compaction.delta", base + `,"messageID":"msg_test","text":"part"`, false, 0},
		{"session.next.compaction.ended", base + `,"messageID":"msg_test","reason":"manual","text":"summary","recent":"recent"`, true, 1},
		{"session.next.revert.staged", base + `,"revert":{"messageID":"msg_test"}`, true, 1},
		{"session.next.revert.cleared", base, true, 1},
		{"session.next.revert.committed", base + `,"messageID":"msg_test"`, true, 1},
	}

	for _, fixture := range fixtures {
		t.Run(fixture.typeName, func(t *testing.T) {
			input := fmt.Sprintf(`{"id":"evt_test","type":%q,"data":{%s}}`, fixture.typeName, fixture.data)
			event, err := DecodeSessionEventJSON([]byte(input))
			if err != nil {
				t.Fatalf("decode session event: %v", err)
			}
			known, ok := event.(domain.KnownSessionEvent)
			if !ok {
				t.Fatalf("event type = %T, want KnownSessionEvent", event)
			}
			if known.Type() != fixture.typeName || known.DurableDefinition != fixture.durable || known.DefinitionVersion != fixture.version {
				t.Fatalf("known event metadata = %q durable=%t version=%d", known.Type(), known.DurableDefinition, known.DefinitionVersion)
			}
			encoded, err := EncodeSessionEventJSON(known)
			if err != nil {
				t.Fatalf("encode session event: %v", err)
			}
			if _, err := DecodeSessionEventJSON(encoded); err != nil {
				t.Fatalf("decode canonical session event: %v", err)
			}
		})
	}
}

func TestSessionEventCodecPreservesUnknownAndRejectsKnownDrift(t *testing.T) {
	unknownInput := `{"id":"evt_test","type":"session.next.future","data":{"large":9007199254740993}}`
	event, err := DecodeSessionEventJSON([]byte(unknownInput))
	if err != nil {
		t.Fatalf("decode unknown session event: %v", err)
	}
	unknown, ok := event.(domain.UnknownSessionEvent)
	if !ok {
		t.Fatalf("event type = %T, want UnknownSessionEvent", event)
	}
	encoded, err := EncodeSessionEventJSON(unknown)
	if err != nil {
		t.Fatalf("encode unknown session event: %v", err)
	}
	if string(encoded) != `{"data":{"large":9007199254740993},"id":"evt_test","type":"session.next.future"}`+"\n" {
		t.Fatalf("encoded unknown session event = %s", encoded)
	}

	invalid := []string{
		`{"id":"evt_test","type":"session.next.agent.switched","data":{"timestamp":1,"sessionID":"ses_test","messageID":"bad","agent":"a"}}`,
		`{"id":"evt_test","type":"session.next.agent.switched","data":{"timestamp":1,"sessionID":"ses_test","messageID":"msg_test","agent":"a","extra":true}}`,
		`{"id":"evt_test","type":"session.next.prompted","data":{"timestamp":1,"sessionID":"ses_test","messageID":"msg_test","prompt":{"text":"x","files":null},"delivery":"steer"}}`,
		`{"id":"evt_test","type":"session.next.prompted","data":{"timestamp":1,"sessionID":"ses_test","messageID":"msg_test","prompt":{"text":"x"},"delivery":"later"}}`,
		`{"id":"evt_test","type":"session.next.reasoning.started","data":{"timestamp":1,"sessionID":"ses_test","assistantMessageID":"msg_test","reasoningID":"r","providerMetadata":null}}`,
		`{"id":"evt_test","type":"session.next.tool.success","data":{"timestamp":1,"sessionID":"ses_test","assistantMessageID":"msg_test","callID":"c","structured":{},"content":[],"provider":{"executed":true,"metadata":null}}}`,
		`{"id":"evt_test","type":"session.next.revert.staged","data":{"timestamp":1,"sessionID":"ses_test","revert":{"messageID":"msg_test","files":[{"path":"a","status":"added","additions":-1,"deletions":0,"patch":""}]}}}`,
	}
	for _, input := range invalid {
		if _, err := DecodeSessionEventJSON([]byte(input)); err == nil {
			t.Fatalf("invalid known session event unexpectedly succeeded: %s", input)
		}
	}

	knownAsUnknown := domain.UnknownSessionEvent{Value: domain.EventEnvelope{
		ID: "evt_test", Type: "session.next.revert.cleared", Data: domain.JSONObject(map[string]domain.JSONValue{
			"timestamp": domain.JSONNumber("1"), "sessionID": domain.JSONString("ses_test"),
		}),
	}}
	if _, err := EncodeSessionEventJSON(knownAsUnknown); err == nil {
		t.Fatal("known type encoded through UnknownSessionEvent")
	}
}
