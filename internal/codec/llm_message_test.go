package codec

import (
	"strings"
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

func TestLLMMessageCodecCoversFrozenRolesAndContentUnion(t *testing.T) {
	roles := []llm.MessageRole{llm.MessageRoleSystem, llm.MessageRoleUser, llm.MessageRoleAssistant, llm.MessageRoleTool}
	for _, role := range roles {
		input := `{"role":"` + string(role) + `","content":[]}`
		message, err := DecodeLLMMessageJSON([]byte(input))
		if err != nil {
			t.Fatalf("decode role %s: %v", role, err)
		}
		if message.Role != role {
			t.Fatalf("message role = %q", message.Role)
		}
	}

	input := `{"id":"llm_msg_1","role":"user","content":[{"type":"text","text":"你好","cache":{"type":"ephemeral","ttlSeconds":60},"metadata":{"large":9007199254740993},"providerMetadata":{"openai":{"requestID":"req_1"}}},{"type":"media","mediaType":"image/png","data":"AAECAw==","filename":"图.png","metadata":{"source":"fixture"}},{"type":"tool-call","id":"call_1","name":"lookup","input":{"large":9007199254740993},"providerExecuted":false,"metadata":{"trace":true},"providerMetadata":{"openai":{"requestID":"req_2"}}},{"type":"tool-result","id":"call_1","name":"lookup","result":{"type":"content","value":[{"type":"text","text":"ok"}]},"providerExecuted":true,"cache":{"type":"persistent"},"metadata":{"trace":true},"providerMetadata":{"openai":{"requestID":"req_3"}}},{"type":"reasoning","text":"think","encrypted":"cipher","metadata":{"trace":true},"providerMetadata":{"openai":{"requestID":"req_4"}}}],"metadata":{"large":9007199254740993},"native":{"provider":"openai"}}`
	message, err := DecodeLLMMessageJSON([]byte(input))
	if err != nil {
		t.Fatalf("decode full LLM message: %v", err)
	}
	if len(message.Content) != 5 {
		t.Fatalf("content count = %d", len(message.Content))
	}
	wantTypes := []llm.ContentType{llm.ContentText, llm.ContentMedia, llm.ContentToolCall, llm.ContentToolResult, llm.ContentReasoning}
	for index, want := range wantTypes {
		if message.Content[index].ContentType() != want {
			t.Fatalf("content %d type = %q", index, message.Content[index].ContentType())
		}
	}
	first, err := EncodeLLMMessageJSON(message)
	if err != nil {
		t.Fatalf("encode full LLM message: %v", err)
	}
	roundTrip, err := DecodeLLMMessageJSON(first)
	if err != nil {
		t.Fatalf("decode canonical LLM message: %v", err)
	}
	second, err := EncodeLLMMessageJSON(roundTrip)
	if err != nil {
		t.Fatalf("re-encode full LLM message: %v", err)
	}
	if string(first) != string(second) || !strings.Contains(string(first), "9007199254740993") || !strings.HasSuffix(string(first), "\n") {
		t.Fatalf("LLM message was not deterministic/lossless: first=%q second=%q", first, second)
	}
}

func TestLLMMessageCodecRejectsFrozenShapeDrift(t *testing.T) {
	invalid := []string{
		`{"role":"future","content":[]}`,
		`{"role":"user","content":null}`,
		`{"role":"user","content":[],"id":null}`,
		`{"role":"user","content":[],"metadata":null}`,
		`{"role":"user","content":[],"extra":true}`,
		`{"role":"user","content":[{"type":"future"}]}`,
		`{"role":"user","content":[{"type":"media","mediaType":"image/png","data":[1,2,3]}]}`,
		`{"role":"user","content":[{"type":"text","text":"x","cache":null}]}`,
		`{"role":"user","content":[{"type":"text","text":"x","cache":{"type":"future"}}]}`,
		`{"role":"user","content":[{"type":"text","text":"x","providerMetadata":null}]}`,
		`{"role":"tool","content":[{"type":"tool-result","id":"c","name":"n","result":{"type":"content","value":{}}}]}`,
		`{"role":"assistant","content":[{"type":"reasoning","text":"x","encrypted":null}]}`,
	}
	for _, input := range invalid {
		if _, err := DecodeLLMMessageJSON([]byte(input)); err == nil {
			t.Fatalf("invalid LLM message unexpectedly succeeded: %s", input)
		}
	}
}

func TestLLMMessageCodecRejectsBinaryMediaAtJSONBoundary(t *testing.T) {
	message := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{llm.MediaContent{
			MediaType: "image/png",
			Bytes:     []byte{1, 2, 3},
		}},
	}
	if _, err := EncodeLLMMessageJSON(message); err == nil {
		t.Fatal("binary media unexpectedly encoded through canonical JSON codec")
	}
}
