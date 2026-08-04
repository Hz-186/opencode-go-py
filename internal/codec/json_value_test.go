package codec

import "testing"

func TestJSONValuePreservesLargeNumbersAndProducesStableObjectOrder(t *testing.T) {
	value, err := DecodeJSONValue([]byte(`{"z":9007199254740993,"negativeZero":-0.00e+2,"a":[null,true,"你好"]}`))
	if err != nil {
		t.Fatalf("decode JSON value: %v", err)
	}
	if value.Object["z"].Number != "9007199254740993" {
		t.Fatalf("large number = %q", value.Object["z"].Number)
	}
	if value.Object["negativeZero"].Number != "0" {
		t.Fatalf("negative zero = %q", value.Object["negativeZero"].Number)
	}
	encoded, err := EncodeJSONValue(value)
	if err != nil {
		t.Fatalf("encode JSON value: %v", err)
	}
	if string(encoded) != `{"a":[null,true,"你好"],"negativeZero":0,"z":9007199254740993}`+"\n" {
		t.Fatalf("encoded JSON = %s", encoded)
	}
	second, err := DecodeJSONValue(encoded)
	if err != nil {
		t.Fatalf("decode canonical JSON: %v", err)
	}
	secondEncoded, err := EncodeJSONValue(second)
	if err != nil {
		t.Fatalf("re-encode canonical JSON: %v", err)
	}
	if string(secondEncoded) != string(encoded) {
		t.Fatalf("JSON encoding is not deterministic:\n%s\n%s", encoded, secondEncoded)
	}
}

func TestJSONValueRejectsDuplicateKeysInvalidUTF8AndTrailingValues(t *testing.T) {
	invalid := [][]byte{
		[]byte(`{"duplicate":1,"duplicate":2}`),
		[]byte(`{"ok":true} {"trailing":true}`),
		{'"', 0xff, '"'},
	}
	for _, input := range invalid {
		if _, err := DecodeJSONValue(input); err == nil {
			t.Fatalf("invalid JSON %q unexpectedly succeeded", input)
		}
	}
}
