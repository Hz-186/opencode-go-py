package codec

import (
	"strings"
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

func TestSessionMessageCodecCoversFrozenTaggedUnions(t *testing.T) {
	fixtures := []struct {
		typeName domain.SessionMessageType
		json     string
	}{
		{domain.SessionMessageAgentSwitched, `{"id":"msg_agent","type":"agent-switched","agent":"build","time":{"created":1}}`},
		{domain.SessionMessageModelSwitched, `{"id":"msg_model","type":"model-switched","model":{"id":"gpt-test","providerID":"openai","variant":"fast"},"time":{"created":2}}`},
		{domain.SessionMessageUser, `{"id":"msg_user","type":"user","text":"检查项目","files":[{"uri":"file:///tmp/a.txt","mime":"text/plain","name":"a.txt","description":"说明","source":{"start":0,"end":3,"text":"abc"}}],"agents":[{"name":"review","source":{"start":4,"end":7,"text":"def"}}],"metadata":{"large":9007199254740993},"time":{"created":3}}`},
		{domain.SessionMessageSynthetic, `{"id":"msg_synthetic","type":"synthetic","sessionID":"ses_test","text":"synthetic","time":{"created":4}}`},
		{domain.SessionMessageSystem, `{"id":"msg_system","type":"system","text":"operator update","time":{"created":5}}`},
		{domain.SessionMessageShell, `{"id":"msg_shell","type":"shell","callID":"call_1","command":"pwd","output":"/tmp","time":{"created":6,"completed":7}}`},
		{domain.SessionMessageAssistant, `{"id":"msg_assistant","type":"assistant","agent":"build","model":{"id":"gpt-test","providerID":"openai","variant":"fast"},"content":[{"type":"text","id":"text_1","text":"done"},{"type":"reasoning","id":"reason_1","text":"think","providerMetadata":{"openai":{"large":9007199254740993}},"time":{"created":8,"completed":9}},{"type":"tool","id":"call_1","name":"lookup","provider":{"executed":true,"metadata":{"openai":{"requestID":"req_1"}},"resultMetadata":{"openai":{"requestID":"req_2"}}},"state":{"status":"running","input":{"id":9007199254740993},"structured":{"phase":"running"},"content":[{"type":"text","text":"working"},{"type":"file","uri":"file:///tmp/a","mime":"text/plain","name":"a"}]},"time":{"created":10,"ran":11}}],"snapshot":{"start":"a","end":"b","files":["src/a.go"]},"finish":"stop","cost":0.25,"tokens":{"input":3,"output":2,"reasoning":1,"cache":{"read":1,"write":0}},"time":{"created":8,"completed":12}}`},
		{domain.SessionMessageAssistant, `{"id":"msg_pending","type":"assistant","agent":"build","model":{"id":"gpt-test","providerID":"openai"},"content":[{"type":"tool","id":"call_pending","name":"lookup","state":{"status":"pending","input":"{\"id\":"},"time":{"created":1}}],"time":{"created":1}}`},
		{domain.SessionMessageAssistant, `{"id":"msg_completed","type":"assistant","agent":"build","model":{"id":"gpt-test","providerID":"openai"},"content":[{"type":"tool","id":"call_completed","name":"lookup","state":{"status":"completed","input":{"id":1},"attachments":[],"content":[],"outputPaths":["/tmp/out"],"structured":{"ok":true},"result":{"large":9007199254740993}},"time":{"created":1,"completed":2,"pruned":3}}],"time":{"created":1}}`},
		{domain.SessionMessageAssistant, `{"id":"msg_error","type":"assistant","agent":"build","model":{"id":"gpt-test","providerID":"openai"},"content":[{"type":"tool","id":"call_error","name":"lookup","state":{"status":"error","input":{},"content":[],"structured":{},"error":{"type":"unknown","message":"failed"},"result":{"retry":false}},"time":{"created":1,"completed":2}}],"error":{"type":"unknown","message":"turn failed"},"time":{"created":1,"completed":2}}`},
		{domain.SessionMessageCompaction, `{"id":"msg_compaction","type":"compaction","reason":"auto","summary":"summary","recent":"recent","time":{"created":13}}`},
	}

	for _, fixture := range fixtures {
		t.Run(string(fixture.typeName)+fixture.json[7:16], func(t *testing.T) {
			message, err := DecodeSessionMessageJSON([]byte(fixture.json))
			if err != nil {
				t.Fatalf("decode session message: %v", err)
			}
			if message.SessionMessageType() != fixture.typeName {
				t.Fatalf("message type = %q", message.SessionMessageType())
			}
			first, err := EncodeSessionMessageJSON(message)
			if err != nil {
				t.Fatalf("encode session message: %v", err)
			}
			roundTrip, err := DecodeSessionMessageJSON(first)
			if err != nil {
				t.Fatalf("decode canonical session message: %v", err)
			}
			second, err := EncodeSessionMessageJSON(roundTrip)
			if err != nil {
				t.Fatalf("re-encode session message: %v", err)
			}
			if string(first) != string(second) || !strings.HasSuffix(string(first), "\n") {
				t.Fatalf("session message encoding is not deterministic: first=%q second=%q", first, second)
			}
			if strings.Contains(fixture.json, "9007199254740993") && !strings.Contains(string(first), "9007199254740993") {
				t.Fatalf("large unknown integer was not preserved: %s", first)
			}
		})
	}
}

func TestSessionMessageCodecRejectsFrozenShapeDrift(t *testing.T) {
	invalid := []string{
		`{"id":"bad","type":"system","text":"x","time":{"created":1}}`,
		`{"id":"msg_test","type":"future","time":{"created":1}}`,
		`{"id":"msg_test","type":"system","text":"x"}`,
		`{"id":"msg_test","type":"system","text":"x","metadata":null,"time":{"created":1}}`,
		`{"id":"msg_test","type":"system","text":"x","time":{"created":1},"extra":true}`,
		`{"id":"msg_test","type":"user","text":"x","files":null,"time":{"created":1}}`,
		`{"id":"msg_test","type":"model-switched","model":{"id":"x","providerID":"p","variant":null},"time":{"created":1}}`,
		`{"id":"msg_test","type":"shell","callID":"c","command":"x","output":"y","time":{"created":1,"completed":null}}`,
		`{"id":"msg_test","type":"assistant","agent":"a","model":{"id":"m","providerID":"p"},"content":[{"type":"future"}],"time":{"created":1}}`,
		`{"id":"msg_test","type":"assistant","agent":"a","model":{"id":"m","providerID":"p"},"content":[{"type":"tool","id":"c","name":"n","state":{"status":"future"},"time":{"created":1}}],"time":{"created":1}}`,
		`{"id":"msg_test","type":"assistant","agent":"a","model":{"id":"m","providerID":"p"},"content":[{"type":"tool","id":"c","name":"n","state":{"status":"completed","input":{},"content":[],"structured":{},"result":null},"time":{"created":1}}],"time":{"created":1}}`,
		`{"id":"msg_test","type":"assistant","agent":"a","model":{"id":"m","providerID":"p"},"content":[],"cost":1e400,"time":{"created":1}}`,
		`{"id":"msg_test","type":"assistant","agent":"a","model":{"id":"m","providerID":"p"},"content":[],"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0}},"time":{"created":1}}`,
		`{"id":"msg_test","type":"compaction","reason":"future","summary":"s","recent":"r","time":{"created":1}}`,
	}
	for _, input := range invalid {
		if _, err := DecodeSessionMessageJSON([]byte(input)); err == nil {
			t.Fatalf("invalid session message unexpectedly succeeded: %s", input)
		}
	}
}
