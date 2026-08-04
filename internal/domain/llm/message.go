package llm

import "github.com/Hz-186/opencode-go-py/internal/domain"

type MessageRole string

const (
	MessageRoleSystem    MessageRole = "system"
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleTool      MessageRole = "tool"
)

type CacheHintType string

const (
	CacheHintEphemeral  CacheHintType = "ephemeral"
	CacheHintPersistent CacheHintType = "persistent"
)

type CacheHint struct {
	Type       CacheHintType
	TTLSeconds *float64
}

type ContentType string

const (
	ContentText       ContentType = "text"
	ContentMedia      ContentType = "media"
	ContentToolCall   ContentType = "tool-call"
	ContentToolResult ContentType = "tool-result"
	ContentReasoning  ContentType = "reasoning"
)

type Content interface {
	ContentType() ContentType
}

type TextContent struct {
	Text             string
	Cache            *CacheHint
	Metadata         map[string]domain.JSONValue
	ProviderMetadata ProviderMetadata
}

func (TextContent) ContentType() ContentType { return ContentText }

type MediaContent struct {
	MediaType string
	Data      string
	Bytes     []byte
	Filename  *string
	Metadata  map[string]domain.JSONValue
}

func (MediaContent) ContentType() ContentType { return ContentMedia }

type ToolCallContent struct {
	ID               string
	Name             string
	Input            domain.JSONValue
	ProviderExecuted *bool
	Metadata         map[string]domain.JSONValue
	ProviderMetadata ProviderMetadata
}

func (ToolCallContent) ContentType() ContentType { return ContentToolCall }

type ToolResultContent struct {
	ID               string
	Name             string
	Result           domain.JSONValue
	ProviderExecuted *bool
	Cache            *CacheHint
	Metadata         map[string]domain.JSONValue
	ProviderMetadata ProviderMetadata
}

func (ToolResultContent) ContentType() ContentType { return ContentToolResult }

type ReasoningContent struct {
	Text             string
	Encrypted        *string
	Metadata         map[string]domain.JSONValue
	ProviderMetadata ProviderMetadata
}

func (ReasoningContent) ContentType() ContentType { return ContentReasoning }

type Message struct {
	ID       *string
	Role     MessageRole
	Content  []Content
	Metadata map[string]domain.JSONValue
	Native   map[string]domain.JSONValue
}
