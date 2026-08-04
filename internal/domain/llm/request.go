package llm

import "github.com/Hz-186/opencode-go-py/internal/domain"

type JSONSchema map[string]domain.JSONValue

type Model struct {
	ID       string
	Provider string
	Route    string
}

type SystemPart struct {
	Text     string
	Cache    *CacheHint
	Metadata map[string]domain.JSONValue
}

type ToolDefinition struct {
	Name         string
	Description  string
	InputSchema  JSONSchema
	OutputSchema JSONSchema
	Cache        *CacheHint
	Metadata     map[string]domain.JSONValue
	Native       map[string]domain.JSONValue
}

type ToolChoiceType string

const (
	ToolChoiceAuto     ToolChoiceType = "auto"
	ToolChoiceNone     ToolChoiceType = "none"
	ToolChoiceRequired ToolChoiceType = "required"
	ToolChoiceNamed    ToolChoiceType = "tool"
)

type ToolChoice struct {
	Type ToolChoiceType
	Name *string
}

type GenerationOptions struct {
	MaxTokens        *float64
	Temperature      *float64
	TopP             *float64
	TopK             *float64
	FrequencyPenalty *float64
	PresencePenalty  *float64
	Seed             *float64
	Stop             []string
}

type HTTPOptions struct {
	Body    JSONSchema
	Headers map[string]string
	Query   map[string]string
}

type ResponseFormatType string

const (
	ResponseFormatText ResponseFormatType = "text"
	ResponseFormatJSON ResponseFormatType = "json"
	ResponseFormatTool ResponseFormatType = "tool"
)

type ResponseFormat struct {
	Type   ResponseFormatType
	Schema JSONSchema
	Tool   *ToolDefinition
}

type CachePolicyMessageMode string

const (
	CacheLatestUserMessage CachePolicyMessageMode = "latest-user-message"
	CacheLatestAssistant   CachePolicyMessageMode = "latest-assistant"
)

type CachePolicy struct {
	Mode       *string
	Tools      *bool
	System     *bool
	Messages   *CachePolicyMessageMode
	Tail       *float64
	TTLSeconds *float64
}

type Request struct {
	ID              *string
	Model           Model
	System          []SystemPart
	Messages        []Message
	Tools           []ToolDefinition
	ToolChoice      *ToolChoice
	Generation      *GenerationOptions
	ProviderOptions ProviderMetadata
	HTTP            *HTTPOptions
	ResponseFormat  *ResponseFormat
	Cache           *CachePolicy
	Metadata        map[string]domain.JSONValue
}
