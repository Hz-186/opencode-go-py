package provider

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/Hz-186/opencode-go-py/internal/codec"
	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

var (
	ErrUnsupportedRoute      = errors.New("unsupported provider route")
	ErrUnsupportedCapability = errors.New("provider capability is unavailable")
	ErrDuplicateRoute        = errors.New("duplicate provider route")
)

// APIType identifies the three canonical V2 provider protocols. Long-tail
// protocols must not silently map to one of these values.
type APIType string

const (
	APITypeOpenAIResponses   APIType = "openai-responses"
	APITypeAnthropicMessages APIType = "anthropic-messages"
	APITypeOpenAICompatible  APIType = "openai-compatible"
)

func (api APIType) valid() bool {
	return api == APITypeOpenAIResponses || api == APITypeAnthropicMessages || api == APITypeOpenAICompatible
}

// Capabilities describes only protocol/model affordances. It is deliberately
// a value, not a mutable map, so a resolved route can be held by a turn.
type Capabilities struct {
	Text         bool
	Reasoning    bool
	ToolCalls    bool
	ImageInput   bool
	JSONOutput   bool
	Usage        bool
	ProviderMeta bool
}

func (capabilities Capabilities) Supports(capability string) bool {
	switch capability {
	case "text":
		return capabilities.Text
	case "reasoning":
		return capabilities.Reasoning
	case "tool-calls":
		return capabilities.ToolCalls
	case "image-input":
		return capabilities.ImageInput
	case "json-output":
		return capabilities.JSONOutput
	case "usage":
		return capabilities.Usage
	case "provider-metadata":
		return capabilities.ProviderMeta
	default:
		return false
	}
}

// Route is a resolved provider/model/API combination. Endpoint is optional in
// the catalog because a native adapter may choose its documented default.
type Route struct {
	Provider     string
	ModelID      string
	Name         string
	API          APIType
	Endpoint     string
	Capabilities Capabilities
}

func (route Route) key() routeKey {
	return routeKey{Provider: route.Provider, ModelID: route.ModelID, Name: route.Name}
}

func (route Route) validate(index int) error {
	if strings.TrimSpace(route.Provider) == "" || route.Provider != strings.TrimSpace(route.Provider) ||
		strings.TrimSpace(route.ModelID) == "" || route.ModelID != strings.TrimSpace(route.ModelID) ||
		strings.TrimSpace(route.Name) == "" || route.Name != strings.TrimSpace(route.Name) {
		return fmt.Errorf("%w: route %d has invalid provider/model/name", ErrUnsupportedRoute, index)
	}
	if !route.API.valid() {
		return fmt.Errorf("%w: route %d has unsupported API %q", ErrUnsupportedRoute, index, route.API)
	}
	if route.Endpoint != "" {
		parsed, err := url.Parse(route.Endpoint)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return fmt.Errorf("%w: route %d endpoint must be an absolute HTTP(S) URL", ErrUnsupportedRoute, index)
		}
	}
	if !route.Capabilities.Text {
		return fmt.Errorf("%w: route %d must support text", ErrUnsupportedCapability, index)
	}
	return nil
}

type routeKey struct {
	Provider string
	ModelID  string
	Name     string
}

// Catalog resolves routes without fallback. A caller must explicitly register
// every provider/model/API combination it intends to use.
type Catalog struct {
	routes map[routeKey]Route
}

func NewCatalog(routes []Route) (*Catalog, error) {
	if len(routes) == 0 {
		return nil, fmt.Errorf("%w: catalog is empty", ErrUnsupportedRoute)
	}
	resolved := make(map[routeKey]Route, len(routes))
	for index, route := range routes {
		if err := route.validate(index); err != nil {
			return nil, err
		}
		key := route.key()
		if _, exists := resolved[key]; exists {
			return nil, fmt.Errorf("%w: %s/%s/%s", ErrDuplicateRoute, key.Provider, key.ModelID, key.Name)
		}
		resolved[key] = route
	}
	return &Catalog{routes: resolved}, nil
}

func (catalog *Catalog) Resolve(model llm.Model) (Route, error) {
	if catalog == nil {
		return Route{}, ErrUnsupportedRoute
	}
	route, ok := catalog.routes[routeKey{Provider: model.Provider, ModelID: model.ID, Name: model.Route}]
	if !ok {
		return Route{}, fmt.Errorf("%w: %s/%s/%s", ErrUnsupportedRoute, model.Provider, model.ID, model.Route)
	}
	return route, nil
}

func (catalog *Catalog) ResolveRequest(request ProviderTurnRequest) (Route, error) {
	if err := request.Validate(); err != nil {
		return Route{}, err
	}
	return catalog.Resolve(request.Request.Model)
}

// Require rejects a turn whose request shape needs an unavailable capability.
func (catalog *Catalog) Require(request ProviderTurnRequest, capabilities ...string) (Route, error) {
	route, err := catalog.ResolveRequest(request)
	if err != nil {
		return Route{}, err
	}
	for _, capability := range capabilities {
		if !route.Capabilities.Supports(capability) {
			return Route{}, fmt.Errorf("%w: %s for %s/%s/%s", ErrUnsupportedCapability,
				capability, route.Provider, route.ModelID, route.Name)
		}
	}
	return route, nil
}

// MessagePreview contains no message content. It is safe to log as a raw
// request preview while retaining enough shape to audit protocol projection.
type MessagePreview struct {
	Role         string   `json:"role"`
	ContentTypes []string `json:"contentTypes"`
}

// RequestPreview is a redaction-safe request projection. Header and option
// values are intentionally absent; only sorted names are retained.
type RequestPreview struct {
	API                APIType
	Provider           string
	Model              string
	Route              string
	Messages           []MessagePreview
	ToolCount          int
	ProviderOptionKeys []string
	HTTPHeaderNames    []string
	HTTPQueryNames     []string
	ResponseFormat     *string
}

func (catalog *Catalog) Preview(request ProviderTurnRequest) (RequestPreview, error) {
	route, err := catalog.ResolveRequest(request)
	if err != nil {
		return RequestPreview{}, err
	}
	preview := RequestPreview{
		API: route.API, Provider: route.Provider, Model: route.ModelID, Route: route.Name,
		Messages:  make([]MessagePreview, 0, len(request.Request.Messages)),
		ToolCount: len(request.Request.Tools),
	}
	if tools, _ := EffectiveToolsAndChoice(request); len(tools) != preview.ToolCount {
		preview.ToolCount = len(tools)
	}
	for _, message := range request.Request.Messages {
		contentTypes := make([]string, 0, len(message.Content))
		for _, content := range message.Content {
			if content == nil {
				return RequestPreview{}, fmt.Errorf("%w: message contains nil content", ErrInvalidRequest)
			}
			contentTypes = append(contentTypes, string(content.ContentType()))
		}
		sort.Strings(contentTypes)
		preview.Messages = append(preview.Messages, MessagePreview{Role: string(message.Role), ContentTypes: contentTypes})
	}
	preview.ProviderOptionKeys = nestedKeys(request.Request.ProviderOptions)
	if request.Request.HTTP != nil {
		preview.HTTPHeaderNames = mapKeys(request.Request.HTTP.Headers)
		preview.HTTPQueryNames = mapKeys(request.Request.HTTP.Query)
	}
	if request.Request.ResponseFormat != nil {
		value := string(request.Request.ResponseFormat.Type)
		preview.ResponseFormat = &value
	}
	return preview, nil
}

// JSON returns deterministic canonical bytes for diagnostics/cassettes.
func (preview RequestPreview) JSON() ([]byte, error) {
	messages := make([]domain.JSONValue, len(preview.Messages))
	for index, message := range preview.Messages {
		contentTypes := make([]domain.JSONValue, len(message.ContentTypes))
		for contentIndex, contentType := range message.ContentTypes {
			contentTypes[contentIndex] = domain.JSONString(contentType)
		}
		messages[index] = domain.JSONObject(map[string]domain.JSONValue{
			"role":         domain.JSONString(message.Role),
			"contentTypes": domain.JSONArray(contentTypes),
		})
	}
	object := map[string]domain.JSONValue{
		"api":                domain.JSONString(string(preview.API)),
		"provider":           domain.JSONString(preview.Provider),
		"model":              domain.JSONString(preview.Model),
		"route":              domain.JSONString(preview.Route),
		"messages":           domain.JSONArray(messages),
		"toolCount":          domain.JSONNumber(fmt.Sprintf("%d", preview.ToolCount)),
		"providerOptionKeys": stringArray(preview.ProviderOptionKeys),
		"httpHeaderNames":    stringArray(preview.HTTPHeaderNames),
		"httpQueryNames":     stringArray(preview.HTTPQueryNames),
	}
	if preview.ResponseFormat != nil {
		object["responseFormat"] = domain.JSONString(*preview.ResponseFormat)
	}
	encoded, err := codec.EncodeJSONValue(domain.JSONObject(object))
	if err != nil {
		return nil, fmt.Errorf("encode request preview: %w", err)
	}
	return encoded, nil
}

func mapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, strings.ToLower(key))
	}
	sort.Strings(keys)
	return uniqueStrings(keys)
}

func nestedKeys(values llm.ProviderMetadata) []string {
	keys := make([]string, 0)
	for provider, metadata := range values {
		for key := range metadata {
			keys = append(keys, provider+"."+key)
		}
	}
	sort.Strings(keys)
	return keys
}

func uniqueStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func stringArray(values []string) domain.JSONValue {
	items := make([]domain.JSONValue, len(values))
	for index, value := range values {
		items[index] = domain.JSONString(value)
	}
	return domain.JSONArray(items)
}
