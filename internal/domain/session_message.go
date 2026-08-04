package domain

type SessionMessageType string

const (
	SessionMessageAgentSwitched SessionMessageType = "agent-switched"
	SessionMessageModelSwitched SessionMessageType = "model-switched"
	SessionMessageUser          SessionMessageType = "user"
	SessionMessageSynthetic     SessionMessageType = "synthetic"
	SessionMessageSystem        SessionMessageType = "system"
	SessionMessageShell         SessionMessageType = "shell"
	SessionMessageAssistant     SessionMessageType = "assistant"
	SessionMessageCompaction    SessionMessageType = "compaction"
)

type SessionMessage interface {
	SessionMessageType() SessionMessageType
}

type SessionMessageBase struct {
	ID       MessageID
	Metadata map[string]JSONValue
}

type ModelRef struct {
	ID         string
	ProviderID string
	Variant    *string
}

type PromptSource struct {
	Start float64
	End   float64
	Text  string
}

type FileAttachment struct {
	URI         string
	MIME        string
	Name        *string
	Description *string
	Source      *PromptSource
}

type AgentAttachment struct {
	Name   string
	Source *PromptSource
}

type UnknownError struct {
	Message string
}

type AgentSwitchedMessage struct {
	SessionMessageBase
	Agent     string
	CreatedAt float64
}

func (AgentSwitchedMessage) SessionMessageType() SessionMessageType {
	return SessionMessageAgentSwitched
}

type ModelSwitchedMessage struct {
	SessionMessageBase
	Model     ModelRef
	CreatedAt float64
}

func (ModelSwitchedMessage) SessionMessageType() SessionMessageType {
	return SessionMessageModelSwitched
}

type UserMessage struct {
	SessionMessageBase
	Text      string
	Files     []FileAttachment
	Agents    []AgentAttachment
	CreatedAt float64
}

func (UserMessage) SessionMessageType() SessionMessageType { return SessionMessageUser }

type SyntheticMessage struct {
	SessionMessageBase
	SessionID SessionID
	Text      string
	CreatedAt float64
}

func (SyntheticMessage) SessionMessageType() SessionMessageType { return SessionMessageSynthetic }

type SystemMessage struct {
	SessionMessageBase
	Text      string
	CreatedAt float64
}

func (SystemMessage) SessionMessageType() SessionMessageType { return SessionMessageSystem }

type ShellMessage struct {
	SessionMessageBase
	CallID      string
	Command     string
	Output      string
	CreatedAt   float64
	CompletedAt *float64
}

func (ShellMessage) SessionMessageType() SessionMessageType { return SessionMessageShell }

type ToolContentType string

const (
	ToolContentText ToolContentType = "text"
	ToolContentFile ToolContentType = "file"
)

type ToolContent struct {
	Type ToolContentType
	Text string
	URI  string
	MIME string
	Name *string
}

type SessionToolStateStatus string

const (
	SessionToolPending   SessionToolStateStatus = "pending"
	SessionToolRunning   SessionToolStateStatus = "running"
	SessionToolCompleted SessionToolStateStatus = "completed"
	SessionToolError     SessionToolStateStatus = "error"
)

type SessionToolState interface {
	SessionToolStateStatus() SessionToolStateStatus
}

type SessionToolPendingState struct {
	Input string
}

func (SessionToolPendingState) SessionToolStateStatus() SessionToolStateStatus {
	return SessionToolPending
}

type SessionToolRunningState struct {
	Input      map[string]JSONValue
	Structured map[string]JSONValue
	Content    []ToolContent
}

func (SessionToolRunningState) SessionToolStateStatus() SessionToolStateStatus {
	return SessionToolRunning
}

type SessionToolCompletedState struct {
	Input       map[string]JSONValue
	Attachments []FileAttachment
	Content     []ToolContent
	OutputPaths []string
	Structured  map[string]JSONValue
	Result      *JSONValue
}

func (SessionToolCompletedState) SessionToolStateStatus() SessionToolStateStatus {
	return SessionToolCompleted
}

type SessionToolErrorState struct {
	Input      map[string]JSONValue
	Content    []ToolContent
	Structured map[string]JSONValue
	Error      UnknownError
	Result     *JSONValue
}

func (SessionToolErrorState) SessionToolStateStatus() SessionToolStateStatus {
	return SessionToolError
}

type AssistantContentType string

const (
	AssistantContentText      AssistantContentType = "text"
	AssistantContentReasoning AssistantContentType = "reasoning"
	AssistantContentTool      AssistantContentType = "tool"
)

type AssistantContent interface {
	AssistantContentType() AssistantContentType
}

type AssistantText struct {
	ID   string
	Text string
}

func (AssistantText) AssistantContentType() AssistantContentType { return AssistantContentText }

type AssistantReasoning struct {
	ID               string
	Text             string
	ProviderMetadata ProviderMetadata
	CreatedAt        *float64
	CompletedAt      *float64
}

func (AssistantReasoning) AssistantContentType() AssistantContentType {
	return AssistantContentReasoning
}

type AssistantToolProvider struct {
	Executed       bool
	Metadata       ProviderMetadata
	ResultMetadata ProviderMetadata
}

type AssistantTool struct {
	ID          string
	Name        string
	Provider    *AssistantToolProvider
	State       SessionToolState
	CreatedAt   float64
	RanAt       *float64
	CompletedAt *float64
	PrunedAt    *float64
}

func (AssistantTool) AssistantContentType() AssistantContentType { return AssistantContentTool }

type AssistantSnapshot struct {
	Start *string
	End   *string
	Files []string
}

type AssistantTokenCache struct {
	Read  float64
	Write float64
}

type AssistantTokens struct {
	Input     float64
	Output    float64
	Reasoning float64
	Cache     AssistantTokenCache
}

type AssistantMessage struct {
	SessionMessageBase
	Agent       string
	Model       ModelRef
	Content     []AssistantContent
	Snapshot    *AssistantSnapshot
	Finish      *string
	Cost        *float64
	Tokens      *AssistantTokens
	Error       *UnknownError
	CreatedAt   float64
	CompletedAt *float64
}

func (AssistantMessage) SessionMessageType() SessionMessageType { return SessionMessageAssistant }

type CompactionReason string

const (
	CompactionAuto   CompactionReason = "auto"
	CompactionManual CompactionReason = "manual"
)

type CompactionMessage struct {
	SessionMessageBase
	Reason    CompactionReason
	Summary   string
	Recent    string
	CreatedAt float64
}

func (CompactionMessage) SessionMessageType() SessionMessageType { return SessionMessageCompaction }
