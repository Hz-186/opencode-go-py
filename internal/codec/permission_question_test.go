package codec

import (
	"strings"
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

func TestPermissionRequestCodecRoundTripsFrozenContract(t *testing.T) {
	inputs := []string{
		`{"id":"per_test","sessionID":"ses_test","action":"read","resources":["src/**"]}`,
		`{"id":"per_test","sessionID":"ses_test","action":"write","resources":["src/a.go","src/b.go"],"save":[],"metadata":{"large":9007199254740993,"说明":"允许"},"source":{"type":"tool","messageID":"msg_test","callID":"call_1"}}`,
	}

	for _, input := range inputs {
		request, err := DecodePermissionRequestJSON([]byte(input))
		if err != nil {
			t.Fatalf("decode permission request %s: %v", input, err)
		}
		first, err := EncodePermissionRequestJSON(request)
		if err != nil {
			t.Fatalf("encode permission request: %v", err)
		}
		roundTrip, err := DecodePermissionRequestJSON(first)
		if err != nil {
			t.Fatalf("decode canonical permission request: %v", err)
		}
		second, err := EncodePermissionRequestJSON(roundTrip)
		if err != nil {
			t.Fatalf("re-encode permission request: %v", err)
		}
		if string(first) != string(second) || !strings.HasSuffix(string(first), "\n") {
			t.Fatalf("permission request encoding is not deterministic: first=%q second=%q", first, second)
		}
		if strings.Contains(input, "9007199254740993") && !strings.Contains(string(first), "9007199254740993") {
			t.Fatalf("large metadata integer was not preserved: %s", first)
		}
	}
}

func TestPermissionCodecRejectsFrozenShapeDrift(t *testing.T) {
	invalidRequests := []string{
		`{"id":"bad","sessionID":"ses_test","action":"read","resources":[]}`,
		`{"id":"per_test","sessionID":"bad","action":"read","resources":[]}`,
		`{"id":"per_test","sessionID":"ses_test","action":"read","resources":null}`,
		`{"id":"per_test","sessionID":"ses_test","action":"read","resources":[],"save":null}`,
		`{"id":"per_test","sessionID":"ses_test","action":"read","resources":[],"metadata":null}`,
		`{"id":"per_test","sessionID":"ses_test","action":"read","resources":[],"source":{"type":"other","messageID":"msg_test","callID":"call_1"}}`,
		`{"id":"per_test","sessionID":"ses_test","action":"read","resources":[],"extra":true}`,
	}
	for _, input := range invalidRequests {
		if _, err := DecodePermissionRequestJSON([]byte(input)); err == nil {
			t.Fatalf("invalid permission request unexpectedly succeeded: %s", input)
		}
	}

	for _, input := range []string{`"later"`, `null`, `1`} {
		if _, err := DecodePermissionReplyJSON([]byte(input)); err == nil {
			t.Fatalf("invalid permission reply unexpectedly succeeded: %s", input)
		}
	}
	for _, input := range []string{`[{"action":"read","resource":"*","effect":"later"}]`, `[{"action":"read","resource":"*","effect":"allow","extra":true}]`} {
		if _, err := DecodePermissionRulesetJSON([]byte(input)); err == nil {
			t.Fatalf("invalid permission ruleset unexpectedly succeeded: %s", input)
		}
	}
}

func TestPermissionReplyAndRulesetCodec(t *testing.T) {
	reply, err := DecodePermissionReplyJSON([]byte(`"always"`))
	if err != nil || reply != domain.PermissionReplyAlways {
		t.Fatalf("permission reply = %q, err = %v", reply, err)
	}
	encodedReply, err := EncodePermissionReplyJSON(reply)
	if err != nil || string(encodedReply) != `"always"`+"\n" {
		t.Fatalf("encoded permission reply = %q, err = %v", encodedReply, err)
	}

	rules, err := DecodePermissionRulesetJSON([]byte(`[{"action":"read","resource":"src/**","effect":"allow"},{"action":"write","resource":"*","effect":"ask"}]`))
	if err != nil {
		t.Fatalf("decode permission ruleset: %v", err)
	}
	encodedRules, err := EncodePermissionRulesetJSON(rules)
	if err != nil {
		t.Fatalf("encode permission ruleset: %v", err)
	}
	if string(encodedRules) != `[{"action":"read","effect":"allow","resource":"src/**"},{"action":"write","effect":"ask","resource":"*"}]`+"\n" {
		t.Fatalf("encoded permission ruleset = %s", encodedRules)
	}
}

func TestQuestionRequestAndReplyCodecRoundTrip(t *testing.T) {
	input := `{"id":"que_test","sessionID":"ses_test","questions":[{"question":"选择模型？","header":"模型","options":[{"label":"快速","description":"低延迟"},{"label":"精确","description":"高质量"}],"multiple":false,"custom":true}],"tool":{"messageID":"msg_test","callID":"call_1"}}`
	request, err := DecodeQuestionRequestJSON([]byte(input))
	if err != nil {
		t.Fatalf("decode question request: %v", err)
	}
	encoded, err := EncodeQuestionRequestJSON(request)
	if err != nil {
		t.Fatalf("encode question request: %v", err)
	}
	if !strings.Contains(string(encoded), "选择模型？") || !strings.HasSuffix(string(encoded), "\n") {
		t.Fatalf("question request Unicode/trailing newline lost: %q", encoded)
	}
	if _, err := DecodeQuestionRequestJSON(encoded); err != nil {
		t.Fatalf("decode canonical question request: %v", err)
	}

	reply, err := DecodeQuestionReplyJSON([]byte(`{"answers":[["快速"],["自定义答案"]]}`))
	if err != nil {
		t.Fatalf("decode question reply: %v", err)
	}
	encodedReply, err := EncodeQuestionReplyJSON(reply)
	if err != nil || string(encodedReply) != `{"answers":[["快速"],["自定义答案"]]}`+"\n" {
		t.Fatalf("encoded question reply = %q, err = %v", encodedReply, err)
	}
}

func TestQuestionCodecRejectsFrozenShapeDrift(t *testing.T) {
	invalidRequests := []string{
		`{"id":"bad","sessionID":"ses_test","questions":[]}`,
		`{"id":"que_test","sessionID":"ses_test","questions":null}`,
		`{"id":"que_test","sessionID":"ses_test","questions":[{"question":"Q","header":"H","options":[],"multiple":null}]}`,
		`{"id":"que_test","sessionID":"ses_test","questions":[{"question":"Q","header":"H","options":[{"label":"A","description":"B","extra":true}]}]}`,
		`{"id":"que_test","sessionID":"ses_test","questions":[],"tool":null}`,
		`{"id":"que_test","sessionID":"ses_test","questions":[],"extra":true}`,
	}
	for _, input := range invalidRequests {
		if _, err := DecodeQuestionRequestJSON([]byte(input)); err == nil {
			t.Fatalf("invalid question request unexpectedly succeeded: %s", input)
		}
	}

	for _, input := range []string{`{"answers":null}`, `{"answers":["not-an-array"]}`, `{"answers":[],"extra":true}`} {
		if _, err := DecodeQuestionReplyJSON([]byte(input)); err == nil {
			t.Fatalf("invalid question reply unexpectedly succeeded: %s", input)
		}
	}
}
