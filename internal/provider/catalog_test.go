package provider

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

func TestCatalogResolvesExactCanonicalRouteWithoutFallback(t *testing.T) {
	catalog, err := NewCatalog([]Route{{
		Provider: "fixture", ModelID: "model-1", Name: "responses", API: APITypeOpenAIResponses,
		Capabilities: Capabilities{Text: true, Reasoning: true, Usage: true},
	}})
	if err != nil {
		t.Fatalf("new catalog: %v", err)
	}
	request := ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "fixture", ID: "model-1", Route: "responses"}}}
	route, err := catalog.Require(request, "text", "reasoning")
	if err != nil || route.API != APITypeOpenAIResponses {
		t.Fatalf("resolved route/error = %+v/%v", route, err)
	}
	request.Request.Model.Route = "compatible"
	if !errors.Is(func() error { _, err := catalog.ResolveRequest(request); return err }(), ErrUnsupportedRoute) {
		t.Fatal("unknown route silently fell back")
	}
	if _, err := catalog.Require(ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "fixture", ID: "model-1", Route: "responses"}}}, "tool-calls"); !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("missing capability error = %v", err)
	}
}

func TestCatalogRejectsUnknownAPIAndDuplicateRoutes(t *testing.T) {
	route := Route{Provider: "fixture", ModelID: "model-1", Name: "chat", API: APIType("future"), Capabilities: Capabilities{Text: true}}
	if _, err := NewCatalog([]Route{route}); !errors.Is(err, ErrUnsupportedRoute) {
		t.Fatalf("unknown API error = %v", err)
	}
	route.API = APITypeOpenAICompatible
	if _, err := NewCatalog([]Route{route, route}); !errors.Is(err, ErrDuplicateRoute) {
		t.Fatalf("duplicate route error = %v", err)
	}
	if _, err := NewCatalog([]Route{{Provider: "fixture", ModelID: "model-1", Name: "chat", API: APITypeOpenAICompatible}}); !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("text capability error = %v", err)
	}
}

func TestRequestPreviewIsDeterministicAndRedactsValues(t *testing.T) {
	catalog, err := NewCatalog([]Route{{
		Provider: "fixture", ModelID: "model-1", Name: "chat", API: APITypeAnthropicMessages,
		Capabilities: Capabilities{Text: true, ToolCalls: true},
	}})
	if err != nil {
		t.Fatalf("new catalog: %v", err)
	}
	request := ProviderTurnRequest{Request: llm.Request{
		Model:  llm.Model{Provider: "fixture", ID: "model-1", Route: "chat"},
		System: []llm.SystemPart{{Text: "do not put this prompt in preview"}},
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{
			llm.TextContent{Text: "secret prompt"}, llm.MediaContent{MediaType: "image/png", Data: "secret-bytes"},
		}}},
		Tools:           []llm.ToolDefinition{{Name: "shell", Description: "private description"}},
		ProviderOptions: llm.ProviderMetadata{"fixture": {"api_key": domain.JSONString("secret-token")}},
		HTTP:            &llm.HTTPOptions{Headers: map[string]string{"Authorization": "Bearer secret-token", "X-Trace": "trace"}, Query: map[string]string{"z": "secret", "a": "1"}},
		ResponseFormat:  &llm.ResponseFormat{Type: llm.ResponseFormatJSON},
	}}
	preview, err := catalog.Preview(request)
	if err != nil {
		t.Fatalf("request preview: %v", err)
	}
	first, err := preview.JSON()
	if err != nil {
		t.Fatalf("encode preview: %v", err)
	}
	second, err := preview.JSON()
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("preview bytes are not deterministic: %s / %s / %v", first, second, err)
	}
	for _, secret := range []string{"secret prompt", "secret-bytes", "secret-token", "private description"} {
		if strings.Contains(string(first), secret) {
			t.Fatalf("preview leaked %q: %s", secret, first)
		}
	}
	for _, name := range []string{"authorization", "x-trace", "a", "z", "fixture.api_key"} {
		if !strings.Contains(string(first), name) {
			t.Fatalf("preview omitted safe key %q: %s", name, first)
		}
	}
}

func TestRequiredCapabilitiesFollowRequestShape(t *testing.T) {
	request := ProviderTurnRequest{Request: llm.Request{
		Model: llm.Model{Provider: "fixture", ID: "model", Route: "route"},
		Tools: []llm.ToolDefinition{{Name: "lookup"}},
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{
			llm.MediaContent{MediaType: "image/png", Data: "data:image/png;base64,AA=="},
			llm.ReasoningContent{Text: "reason"},
		}}},
		ResponseFormat: &llm.ResponseFormat{Type: llm.ResponseFormatJSON},
	}}
	want := []string{"image-input", "json-output", "reasoning", "text", "tool-calls"}
	if got := RequiredCapabilities(request); !reflect.DeepEqual(got, want) {
		t.Fatalf("capabilities = %v, want %v", got, want)
	}
}

func TestResponseFormatToolBecomesEffectiveToolAndCapability(t *testing.T) {
	name := "ordinary"
	request := ProviderTurnRequest{Request: llm.Request{
		Model:      llm.Model{Provider: "fixture", ID: "model", Route: "route"},
		Tools:      []llm.ToolDefinition{{Name: name, InputSchema: llm.JSONSchema{"type": domain.JSONString("object")}}},
		ToolChoice: &llm.ToolChoice{Type: llm.ToolChoiceNone},
		ResponseFormat: &llm.ResponseFormat{Type: llm.ResponseFormatTool, Tool: &llm.ToolDefinition{
			Name: "emit_result", Description: "emit structured output",
			InputSchema: llm.JSONSchema{"type": domain.JSONString("object")},
		}},
	}}
	if err := request.Validate(); err != nil {
		t.Fatalf("valid tool response format: %v", err)
	}
	tools, choice := EffectiveToolsAndChoice(request)
	if len(tools) != 2 || tools[1].Name != "emit_result" || choice == nil || choice.Type != llm.ToolChoiceNamed || choice.Name == nil || *choice.Name != "emit_result" {
		t.Fatalf("effective tools/choice = %+v / %+v", tools, choice)
	}
	want := []string{"text", "tool-calls"}
	if got := RequiredCapabilities(request); !reflect.DeepEqual(got, want) {
		t.Fatalf("capabilities = %v, want %v", got, want)
	}
	// Effective projection must not mutate the caller's canonical request.
	if len(request.Request.Tools) != 1 || request.Request.ToolChoice.Type != llm.ToolChoiceNone {
		t.Fatalf("request mutated: %+v", request.Request)
	}
}

func TestResponseFormatToolValidationFailsClosed(t *testing.T) {
	base := ProviderTurnRequest{Request: llm.Request{Model: llm.Model{Provider: "fixture", ID: "model", Route: "route"}}}
	base.Request.ResponseFormat = &llm.ResponseFormat{Type: llm.ResponseFormatTool}
	if err := base.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing format tool error = %v", err)
	}
	base.Request.Tools = []llm.ToolDefinition{{Name: "emit"}}
	base.Request.ResponseFormat.Tool = &llm.ToolDefinition{Name: "emit"}
	if err := base.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("duplicate format tool error = %v", err)
	}
}
