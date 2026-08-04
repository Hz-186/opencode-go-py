package codec

import (
	"math"
	"testing"
)

func TestDecodeUsageJSONDistinguishesMissingFromNullAndRejectsUnknown(t *testing.T) {
	empty, err := DecodeUsageJSON([]byte(`{}`))
	if err != nil {
		t.Fatalf("decode empty usage: %v", err)
	}
	if empty.InputTokens != nil || empty.OutputTokens != nil {
		t.Fatalf("empty usage = %#v", empty)
	}
	withMetadata, err := DecodeUsageJSON([]byte(`{"providerMetadata":{}}`))
	if err != nil {
		t.Fatalf("decode empty provider metadata: %v", err)
	}
	if withMetadata.ProviderMetadata == nil {
		t.Fatal("present empty provider metadata was collapsed to missing")
	}
	encodedMetadata, err := EncodeUsageJSON(withMetadata)
	if err != nil {
		t.Fatalf("encode empty provider metadata: %v", err)
	}
	if string(encodedMetadata) != `{"providerMetadata":{}}`+"\n" {
		t.Fatalf("encoded provider metadata = %s", encodedMetadata)
	}

	invalid := []string{
		`{"inputTokens":null}`,
		`{"inputTokens":-1}`,
		`{"inputTokens":1,"unknown":true}`,
		`{"inputTokens":1} trailing`,
	}
	for _, input := range invalid {
		if _, err := DecodeUsageJSON([]byte(input)); err == nil {
			t.Fatalf("invalid input %s unexpectedly succeeded", input)
		}
	}
}

func TestUsageJSONNormalizesNegativeZeroAndRoundTripsCanonicalFields(t *testing.T) {
	usage, err := DecodeUsageJSON([]byte(`{"inputTokens":-0,"outputTokens":8,"reasoningTokens":3}`))
	if err != nil {
		t.Fatalf("decode usage: %v", err)
	}
	if usage.InputTokens == nil || math.Signbit(*usage.InputTokens) {
		t.Fatalf("input tokens were not normalized: %#v", usage.InputTokens)
	}
	encoded, err := EncodeUsageJSON(usage)
	if err != nil {
		t.Fatalf("encode usage: %v", err)
	}
	if string(encoded) != `{"inputTokens":0,"outputTokens":8,"reasoningTokens":3}`+"\n" {
		t.Fatalf("encoded usage = %s", encoded)
	}
	decoded, err := DecodeUsageJSON(encoded)
	if err != nil {
		t.Fatalf("round-trip usage: %v", err)
	}
	if decoded.VisibleOutputTokens() != 5 {
		t.Fatalf("visible output = %v", decoded.VisibleOutputTokens())
	}
}
