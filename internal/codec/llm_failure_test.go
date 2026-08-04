package codec

import (
	"strings"
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

func TestLLMFailureCodecCoversFrozenReasonUnion(t *testing.T) {
	fixtures := []struct {
		tag       llm.FailureTag
		retryable bool
		json      string
	}{
		{llm.FailureInvalidRequest, false, `{"module":"provider","method":"stream","reason":{"_tag":"InvalidRequest","message":"bad","parameter":"model","classification":"context-overflow"}}`},
		{llm.FailureNoRoute, false, `{"module":"provider","method":"route","reason":{"_tag":"NoRoute","route":"responses","provider":"openai","model":"gpt-test"}}`},
		{llm.FailureAuthentication, false, `{"module":"provider","method":"stream","reason":{"_tag":"Authentication","message":"bad key","kind":"invalid"}}`},
		{llm.FailureRateLimit, true, `{"module":"provider","method":"stream","reason":{"_tag":"RateLimit","message":"slow down","retryAfterMs":1000,"rateLimit":{"retryAfterMs":1000,"limit":{"requests":"10"},"remaining":{"requests":"0"},"reset":{"requests":"1s"}},"providerMetadata":{"openai":{"requestID":"req_1","large":9007199254740993}},"http":{"request":{"method":"POST","url":"https://example.test/v1","headers":{"content-type":"application/json"}},"response":{"status":429,"headers":{"retry-after":"1"}},"body":"limited","bodyTruncated":false,"requestId":"req_1","rateLimit":{"retryAfterMs":1000}}}}`},
		{llm.FailureQuotaExceeded, false, `{"module":"provider","method":"stream","reason":{"_tag":"QuotaExceeded","message":"quota"}}`},
		{llm.FailureContentPolicy, false, `{"module":"provider","method":"stream","reason":{"_tag":"ContentPolicy","message":"blocked"}}`},
		{llm.FailureProviderInternal, true, `{"module":"provider","method":"stream","reason":{"_tag":"ProviderInternal","message":"down","status":503,"retryAfterMs":250}}`},
		{llm.FailureTransport, false, `{"module":"provider","method":"stream","reason":{"_tag":"Transport","message":"reset","kind":"ECONNRESET","url":"https://example.test"}}`},
		{llm.FailureInvalidProviderOutput, false, `{"module":"provider","method":"stream","reason":{"_tag":"InvalidProviderOutput","message":"bad frame","route":"responses","raw":"{}"}}`},
		{llm.FailureUnknownProvider, false, `{"module":"provider","method":"stream","reason":{"_tag":"UnknownProvider","message":"unknown","status":599}}`},
	}

	for _, fixture := range fixtures {
		t.Run(string(fixture.tag), func(t *testing.T) {
			failure, err := DecodeLLMFailureJSON([]byte(fixture.json))
			if err != nil {
				t.Fatalf("decode LLM failure: %v", err)
			}
			if failure.Reason.FailureTag() != fixture.tag || failure.Retryable() != fixture.retryable {
				t.Fatalf("reason tag/retryable = %q/%t", failure.Reason.FailureTag(), failure.Retryable())
			}
			first, err := EncodeLLMFailureJSON(failure)
			if err != nil {
				t.Fatalf("encode LLM failure: %v", err)
			}
			roundTrip, err := DecodeLLMFailureJSON(first)
			if err != nil {
				t.Fatalf("decode canonical LLM failure: %v", err)
			}
			second, err := EncodeLLMFailureJSON(roundTrip)
			if err != nil {
				t.Fatalf("re-encode LLM failure: %v", err)
			}
			if string(first) != string(second) || !strings.HasSuffix(string(first), "\n") {
				t.Fatalf("LLM failure encoding is not deterministic: first=%q second=%q", first, second)
			}
			if strings.Contains(fixture.json, "9007199254740993") && !strings.Contains(string(first), "9007199254740993") {
				t.Fatalf("provider metadata large integer was not preserved: %s", first)
			}
		})
	}
}

func TestLLMFailureCodecRejectsFrozenShapeDrift(t *testing.T) {
	invalid := []string{
		`{"module":"provider","method":"stream","reason":{"_tag":"Future","message":"bad"}}`,
		`{"module":"provider","method":"stream","reason":null}`,
		`{"module":"provider","method":"stream","reason":{"_tag":"Authentication","message":"bad","kind":"future"}}`,
		`{"module":"provider","method":"stream","reason":{"_tag":"InvalidRequest","message":"bad","parameter":null}}`,
		`{"module":"provider","method":"stream","reason":{"_tag":"RateLimit","message":"slow","retryAfterMs":null}}`,
		`{"module":"provider","method":"stream","reason":{"_tag":"RateLimit","message":"slow","providerMetadata":{"openai":true}}}`,
		`{"module":"provider","method":"stream","reason":{"_tag":"RateLimit","message":"slow","http":{"request":{"method":"POST","url":"https://example.test","headers":null}}}}`,
		`{"module":"provider","method":"stream","reason":{"_tag":"RateLimit","message":"slow","http":{"request":{"method":"POST","url":"https://example.test","headers":{},"extra":true}}}}`,
		`{"module":"provider","method":"stream","reason":{"_tag":"ProviderInternal","message":"bad"}}`,
		`{"module":"provider","method":"stream","reason":{"_tag":"UnknownProvider","message":"bad","extra":true}}`,
		`{"module":"provider","method":"stream","reason":{"_tag":"QuotaExceeded","message":"bad"},"extra":true}`,
	}
	for _, input := range invalid {
		if _, err := DecodeLLMFailureJSON([]byte(input)); err == nil {
			t.Fatalf("invalid LLM failure unexpectedly succeeded: %s", input)
		}
	}
}
