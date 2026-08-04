package domain

type PermissionSourceType string

const PermissionSourceTool PermissionSourceType = "tool"

type PermissionSource struct {
	Type      PermissionSourceType
	MessageID string
	CallID    string
}

type PermissionRequest struct {
	ID        PermissionID
	SessionID SessionID
	Action    string
	Resources []string
	Save      []string
	Metadata  map[string]JSONValue
	Source    *PermissionSource
}

type PermissionReply string

const (
	PermissionReplyOnce   PermissionReply = "once"
	PermissionReplyAlways PermissionReply = "always"
	PermissionReplyReject PermissionReply = "reject"
)

type PermissionEffect string

const (
	PermissionEffectAllow PermissionEffect = "allow"
	PermissionEffectDeny  PermissionEffect = "deny"
	PermissionEffectAsk   PermissionEffect = "ask"
)

type PermissionRule struct {
	Action   string
	Resource string
	Effect   PermissionEffect
}

type PermissionRuleset []PermissionRule
