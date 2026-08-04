package config

import (
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

func objectField(t *testing.T, object map[string]domain.JSONValue, key string) map[string]domain.JSONValue {
	t.Helper()
	value, ok := object[key]
	if !ok || value.Kind != domain.JSONKindObject {
		t.Fatalf("field %q = %#v, want object", key, value)
	}
	return value.Object
}

func assertStringField(t *testing.T, object map[string]domain.JSONValue, key, want string) {
	t.Helper()
	value, ok := object[key]
	if !ok || value.Kind != domain.JSONKindString || value.String != want {
		t.Fatalf("field %q = %#v, want string %q", key, value, want)
	}
}

func assertNumberField(t *testing.T, object map[string]domain.JSONValue, key, want string) {
	t.Helper()
	value, ok := object[key]
	if !ok || value.Kind != domain.JSONKindNumber || value.Number != want {
		t.Fatalf("field %q = %#v, want number %q", key, value, want)
	}
}

func assertStringArrayField(t *testing.T, object map[string]domain.JSONValue, key string, want []string) {
	t.Helper()
	value, ok := object[key]
	if !ok || value.Kind != domain.JSONKindArray || len(value.Array) != len(want) {
		t.Fatalf("field %q = %#v, want string array %v", key, value, want)
	}
	for i := range want {
		if value.Array[i].Kind != domain.JSONKindString || value.Array[i].String != want[i] {
			t.Fatalf("field %q = %#v, want string array %v", key, value, want)
		}
	}
}

func jsonString(value string) domain.JSONValue {
	return domain.JSONString(value)
}
