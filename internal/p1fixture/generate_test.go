package p1fixture

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/codec"
	"github.com/Hz-186/opencode-go-py/internal/domain"
)

func TestGenerateIsDeterministicAndComplete(t *testing.T) {
	first, err := Generate()
	if err != nil {
		t.Fatalf("generate first manifest: %v", err)
	}
	second, err := Generate()
	if err != nil {
		t.Fatalf("generate second manifest: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("P1 fixture generation is not byte-for-byte deterministic")
	}
	if len(first) == 0 || first[len(first)-1] != '\n' {
		t.Fatalf("P1 fixture manifest has no trailing newline: %q", first)
	}
	manifest, err := codec.DecodeJSONValue(first)
	if err != nil {
		t.Fatalf("decode generated manifest: %v", err)
	}
	if manifest.Kind != domain.JSONKindObject {
		t.Fatalf("manifest kind = %q", manifest.Kind)
	}
	wantCount := strconv.Itoa(len(Catalog()))
	if manifest.Object["count"].Number != wantCount || len(manifest.Object["fixtures"].Array) != len(Catalog()) {
		t.Fatalf("manifest count = %s/%d, want %s", manifest.Object["count"].Number, len(manifest.Object["fixtures"].Array), wantCount)
	}
}

func TestCatalogCoversFrozenTaggedUnions(t *testing.T) {
	want := map[string]int{"llm-event": 16, "llm-failure": 10, "session-message": 11, "session-event": 32}
	counts := make(map[string]int)
	seen := make(map[string]struct{})
	for _, definition := range Catalog() {
		key := DefinitionKey(definition)
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate fixture key %q", key)
		}
		seen[key] = struct{}{}
		counts[definition.Contract]++
	}
	for contract, expected := range want {
		if counts[contract] != expected {
			t.Fatalf("fixture count for %s = %d, want %d", contract, counts[contract], expected)
		}
	}
}

func TestWriteVerifyAndDetectDrift(t *testing.T) {
	directory := t.TempDir()
	if _, err := Write(directory); err != nil {
		t.Fatalf("write P1 fixtures: %v", err)
	}
	if err := Verify(directory); err != nil {
		t.Fatalf("verify P1 fixtures: %v", err)
	}
	manifestPath := filepath.Join(directory, ManifestName)
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read generated fixture: %v", err)
	}
	content[0] = '['
	if err := os.WriteFile(manifestPath, content, 0o644); err != nil {
		t.Fatalf("corrupt generated fixture: %v", err)
	}
	if err := Verify(directory); err == nil {
		t.Fatal("drifted P1 fixture unexpectedly verified")
	}
}

func TestStructuredContractsRejectTopLevelUnknownFields(t *testing.T) {
	structured := map[string]bool{
		"location-ref": true, "llm-usage": true, "llm-event": true, "llm-message": true,
		"llm-failure": true, "permission-request": true, "question-request": true,
		"question-reply": true, "event-envelope": true, "session-message": true, "session-event": true,
	}
	for _, definition := range Catalog() {
		if !structured[definition.Contract] {
			continue
		}
		value, err := codec.DecodeJSONValue([]byte(definition.Input))
		if err != nil || value.Kind != domain.JSONKindObject {
			t.Fatalf("decode structured fixture %s: kind=%q err=%v", DefinitionKey(definition), value.Kind, err)
		}
		mutated := cloneJSONValue(value)
		mutated.Object["__unexpected"] = domain.JSONBool(true)
		definition.Input = encodeMutation(t, mutated)
		if _, err := canonicalize(definition); err == nil {
			t.Fatalf("structured fixture %s accepted a top-level unknown field", DefinitionKey(definition))
		}
	}
}

func TestCatalogMutationMatrixRemainsSafeAndCanonical(t *testing.T) {
	for _, definition := range Catalog() {
		value, err := codec.DecodeJSONValue([]byte(definition.Input))
		if err != nil {
			continue
		}
		mutations := jsonMutations(value, 2, 200)
		for index, mutation := range mutations {
			mutatedDefinition := definition
			mutatedDefinition.Input = encodeMutation(t, mutation)
			first, err := canonicalize(mutatedDefinition)
			if err != nil {
				continue
			}
			mutatedDefinition.Input = string(first)
			second, err := canonicalize(mutatedDefinition)
			if err != nil {
				t.Fatalf("canonical mutation %s/%d failed on second pass: %v", DefinitionKey(definition), index, err)
			}
			if !bytes.Equal(first, second) {
				t.Fatalf("canonical mutation %s/%d is not idempotent: first=%q second=%q", DefinitionKey(definition), index, first, second)
			}
		}
	}
}

func jsonMutations(value domain.JSONValue, depth int, limit int) []domain.JSONValue {
	if depth <= 0 || limit <= 0 {
		return nil
	}
	result := make([]domain.JSONValue, 0)
	appendMutation := func(mutation domain.JSONValue) bool {
		if len(result) >= limit {
			return false
		}
		result = append(result, mutation)
		return true
	}
	switch value.Kind {
	case domain.JSONKindObject:
		for field, item := range value.Object {
			deleted := cloneJSONValue(value)
			delete(deleted.Object, field)
			if !appendMutation(deleted) {
				break
			}
			for _, replacement := range []domain.JSONValue{
				domain.JSONNull(), domain.JSONBool(true), domain.JSONString("mutation"),
				domain.JSONNumber("1.5"), domain.JSONArray(nil), domain.JSONObject(nil),
			} {
				mutated := cloneJSONValue(value)
				mutated.Object[field] = replacement
				if !appendMutation(mutated) {
					break
				}
			}
			for _, nested := range jsonMutations(item, depth-1, limit-len(result)) {
				mutated := cloneJSONValue(value)
				mutated.Object[field] = nested
				if !appendMutation(mutated) {
					break
				}
			}
		}
	case domain.JSONKindArray:
		if len(value.Array) > 0 {
			for _, nested := range jsonMutations(value.Array[0], depth-1, limit) {
				mutated := cloneJSONValue(value)
				mutated.Array[0] = nested
				if !appendMutation(mutated) {
					break
				}
			}
		}
	}
	return result
}

func cloneJSONValue(value domain.JSONValue) domain.JSONValue {
	result := value
	if value.Array != nil {
		result.Array = make([]domain.JSONValue, len(value.Array))
		for index, item := range value.Array {
			result.Array[index] = cloneJSONValue(item)
		}
	}
	if value.Object != nil {
		result.Object = make(map[string]domain.JSONValue, len(value.Object))
		for field, item := range value.Object {
			result.Object[field] = cloneJSONValue(item)
		}
	}
	return result
}

func encodeMutation(t *testing.T, value domain.JSONValue) string {
	t.Helper()
	encoded, err := codec.EncodeJSONValue(value)
	if err != nil {
		t.Fatalf("encode fixture mutation: %v", err)
	}
	return string(encoded)
}
