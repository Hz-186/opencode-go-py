package llm

import (
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

func TestRequestDomainOwnsFrozenCanonicalFieldsWithoutTransportDTOs(t *testing.T) {
	name := "lookup"
	request := Request{
		Model:  Model{ID: "gpt-test", Provider: "openai", Route: "responses"},
		System: []SystemPart{{Text: "system"}},
		Messages: []Message{{
			Role: MessageRoleUser,
			Content: []Content{MediaContent{
				MediaType: "image/png", Bytes: []byte{1, 2, 3}, Filename: &name,
			}},
		}},
		Tools: []ToolDefinition{{
			Name: "lookup", Description: "lookup", InputSchema: JSONSchema{"type": domain.JSONString("object")},
		}},
		ToolChoice:      &ToolChoice{Type: ToolChoiceNamed, Name: &name},
		Generation:      &GenerationOptions{Stop: []string{"stop"}},
		ProviderOptions: ProviderMetadata{"openai": {"store": domain.JSONBool(false)}},
		HTTP:            &HTTPOptions{Headers: map[string]string{"x-test": "1"}},
		ResponseFormat:  &ResponseFormat{Type: ResponseFormatText},
		Cache:           &CachePolicy{Mode: stringPointer("auto")},
		Metadata:        map[string]domain.JSONValue{"trace": domain.JSONString("test")},
	}
	if request.Model.Route != "responses" || request.ToolChoice == nil || request.ToolChoice.Name == nil || len(request.Messages) != 1 {
		t.Fatalf("request domain lost canonical fields: %#v", request)
	}
}

func stringPointer(value string) *string { return &value }
