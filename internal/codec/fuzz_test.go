package codec

import (
	"bytes"
	"testing"
)

func FuzzJSONValueCanonicalRoundTrip(f *testing.F) {
	for _, seed := range []string{
		`null`, `{"large":9007199254740993,"text":"你好"}`, `[-0,true,"x"]`,
		`{"duplicate":1,"duplicate":2}`, `{`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		value, err := DecodeJSONValue(input)
		if err != nil {
			return
		}
		first, err := EncodeJSONValue(value)
		if err != nil {
			t.Fatalf("encode decoded JSON value: %v", err)
		}
		roundTrip, err := DecodeJSONValue(first)
		if err != nil {
			t.Fatalf("decode canonical JSON value: %v", err)
		}
		second, err := EncodeJSONValue(roundTrip)
		if err != nil {
			t.Fatalf("re-encode canonical JSON value: %v", err)
		}
		if !bytes.Equal(first, second) {
			t.Fatalf("JSON canonicalization is not idempotent: first=%q second=%q", first, second)
		}
	})
}

func FuzzSessionMessageCanonicalRoundTrip(f *testing.F) {
	for _, seed := range []string{
		`{"id":"msg_test","type":"system","text":"你好","time":{"created":1}}`,
		`{"id":"msg_test","type":"assistant","agent":"a","model":{"id":"m","providerID":"p"},"content":[],"time":{"created":1}}`,
		`{"id":"bad","type":"future"}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		message, err := DecodeSessionMessageJSON(input)
		if err != nil {
			return
		}
		first, err := EncodeSessionMessageJSON(message)
		if err != nil {
			t.Fatalf("encode decoded session message: %v", err)
		}
		roundTrip, err := DecodeSessionMessageJSON(first)
		if err != nil {
			t.Fatalf("decode canonical session message: %v", err)
		}
		second, err := EncodeSessionMessageJSON(roundTrip)
		if err != nil {
			t.Fatalf("re-encode canonical session message: %v", err)
		}
		if !bytes.Equal(first, second) {
			t.Fatalf("session message canonicalization is not idempotent: first=%q second=%q", first, second)
		}
	})
}

func FuzzSessionEventCanonicalRoundTrip(f *testing.F) {
	for _, seed := range []string{
		`{"id":"evt_test","type":"session.next.revert.cleared","data":{"timestamp":1,"sessionID":"ses_test"}}`,
		`{"id":"evt_test","type":"session.next.future","data":{"large":9007199254740993}}`,
		`{"id":"bad","type":"session.next.future","data":{}}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		event, err := DecodeSessionEventJSON(input)
		if err != nil {
			return
		}
		first, err := EncodeSessionEventJSON(event)
		if err != nil {
			t.Fatalf("encode decoded session event: %v", err)
		}
		roundTrip, err := DecodeSessionEventJSON(first)
		if err != nil {
			t.Fatalf("decode canonical session event: %v", err)
		}
		second, err := EncodeSessionEventJSON(roundTrip)
		if err != nil {
			t.Fatalf("re-encode canonical session event: %v", err)
		}
		if !bytes.Equal(first, second) {
			t.Fatalf("session event canonicalization is not idempotent: first=%q second=%q", first, second)
		}
	})
}
