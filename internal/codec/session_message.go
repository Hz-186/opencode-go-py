package codec

import (
	"errors"
	"fmt"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

func DecodeSessionMessageJSON(content []byte) (domain.SessionMessage, error) {
	object, err := decodeContractObject(content, "session message")
	if err != nil {
		return nil, err
	}
	typeName, err := requiredContractString(object, "type", "session message")
	if err != nil {
		return nil, err
	}
	switch domain.SessionMessageType(typeName) {
	case domain.SessionMessageAgentSwitched:
		return decodeAgentSwitchedMessage(object)
	case domain.SessionMessageModelSwitched:
		return decodeModelSwitchedMessage(object)
	case domain.SessionMessageUser:
		return decodeUserMessage(object)
	case domain.SessionMessageSynthetic:
		return decodeSyntheticMessage(object)
	case domain.SessionMessageSystem:
		return decodeSystemMessage(object)
	case domain.SessionMessageShell:
		return decodeShellMessage(object)
	case domain.SessionMessageAssistant:
		return decodeAssistantMessage(object)
	case domain.SessionMessageCompaction:
		return decodeCompactionMessage(object)
	default:
		return nil, fmt.Errorf("unknown session message type %q", typeName)
	}
}

func EncodeSessionMessageJSON(message domain.SessionMessage) ([]byte, error) {
	if message == nil {
		return nil, errors.New("session message is nil")
	}
	var object map[string]domain.JSONValue
	var err error
	switch message := message.(type) {
	case domain.AgentSwitchedMessage:
		object, err = encodeSessionMessageBase(message.SessionMessageBase, domain.SessionMessageAgentSwitched)
		if err == nil {
			object["agent"] = domain.JSONString(message.Agent)
			object["time"] = encodeCreatedTime(message.CreatedAt)
		}
	case domain.ModelSwitchedMessage:
		object, err = encodeSessionMessageBase(message.SessionMessageBase, domain.SessionMessageModelSwitched)
		if err == nil {
			object["model"] = encodeModelRef(message.Model)
			object["time"] = encodeCreatedTime(message.CreatedAt)
		}
	case domain.UserMessage:
		object, err = encodeSessionMessageBase(message.SessionMessageBase, domain.SessionMessageUser)
		if err == nil {
			object["text"] = domain.JSONString(message.Text)
			if message.Files != nil {
				object["files"] = encodeFileAttachments(message.Files)
			}
			if message.Agents != nil {
				object["agents"] = encodeAgentAttachments(message.Agents)
			}
			object["time"] = encodeCreatedTime(message.CreatedAt)
		}
	case domain.SyntheticMessage:
		object, err = encodeSessionMessageBase(message.SessionMessageBase, domain.SessionMessageSynthetic)
		if err == nil {
			if _, parseErr := domain.ParseSessionID(string(message.SessionID)); parseErr != nil {
				return nil, parseErr
			}
			object["sessionID"] = domain.JSONString(string(message.SessionID))
			object["text"] = domain.JSONString(message.Text)
			object["time"] = encodeCreatedTime(message.CreatedAt)
		}
	case domain.SystemMessage:
		object, err = encodeSessionMessageBase(message.SessionMessageBase, domain.SessionMessageSystem)
		if err == nil {
			object["text"] = domain.JSONString(message.Text)
			object["time"] = encodeCreatedTime(message.CreatedAt)
		}
	case domain.ShellMessage:
		object, err = encodeSessionMessageBase(message.SessionMessageBase, domain.SessionMessageShell)
		if err == nil {
			object["callID"] = domain.JSONString(message.CallID)
			object["command"] = domain.JSONString(message.Command)
			object["output"] = domain.JSONString(message.Output)
			object["time"] = encodeCreatedCompletedTime(message.CreatedAt, message.CompletedAt)
		}
	case domain.AssistantMessage:
		object, err = encodeAssistantMessage(message)
	case domain.CompactionMessage:
		object, err = encodeSessionMessageBase(message.SessionMessageBase, domain.SessionMessageCompaction)
		if err == nil {
			if message.Reason != domain.CompactionAuto && message.Reason != domain.CompactionManual {
				return nil, fmt.Errorf("invalid compaction reason %q", message.Reason)
			}
			object["reason"] = domain.JSONString(string(message.Reason))
			object["summary"] = domain.JSONString(message.Summary)
			object["recent"] = domain.JSONString(message.Recent)
			object["time"] = encodeCreatedTime(message.CreatedAt)
		}
	default:
		return nil, fmt.Errorf("unsupported session message value %T", message)
	}
	if err != nil {
		return nil, err
	}
	encoded, err := EncodeJSONValue(domain.JSONObject(object))
	if err != nil {
		return nil, err
	}
	if _, err := DecodeSessionMessageJSON(encoded); err != nil {
		return nil, fmt.Errorf("validate encoded session message: %w", err)
	}
	return encoded, nil
}

func decodeAgentSwitchedMessage(object map[string]domain.JSONValue) (domain.SessionMessage, error) {
	if err := rejectUnknownContractFields(object, "agent-switched message", "id", "metadata", "time", "type", "agent"); err != nil {
		return nil, err
	}
	base, err := decodeSessionMessageBase(object)
	if err != nil {
		return nil, err
	}
	agent, err := requiredContractString(object, "agent", "agent-switched message")
	if err != nil {
		return nil, err
	}
	created, err := decodeCreatedTime(object, "agent-switched message")
	return domain.AgentSwitchedMessage{SessionMessageBase: base, Agent: agent, CreatedAt: created}, err
}

func decodeModelSwitchedMessage(object map[string]domain.JSONValue) (domain.SessionMessage, error) {
	if err := rejectUnknownContractFields(object, "model-switched message", "id", "metadata", "time", "type", "model"); err != nil {
		return nil, err
	}
	base, err := decodeSessionMessageBase(object)
	if err != nil {
		return nil, err
	}
	model, err := decodeModelRefField(object, "model", "model-switched message")
	if err != nil {
		return nil, err
	}
	created, err := decodeCreatedTime(object, "model-switched message")
	return domain.ModelSwitchedMessage{SessionMessageBase: base, Model: model, CreatedAt: created}, err
}

func decodeUserMessage(object map[string]domain.JSONValue) (domain.SessionMessage, error) {
	if err := rejectUnknownContractFields(object, "user message", "id", "metadata", "time", "type", "text", "files", "agents"); err != nil {
		return nil, err
	}
	base, err := decodeSessionMessageBase(object)
	if err != nil {
		return nil, err
	}
	text, err := requiredContractString(object, "text", "user message")
	if err != nil {
		return nil, err
	}
	files, err := decodeOptionalFileAttachments(object, "files", "user message")
	if err != nil {
		return nil, err
	}
	agents, err := decodeOptionalAgentAttachments(object, "agents", "user message")
	if err != nil {
		return nil, err
	}
	created, err := decodeCreatedTime(object, "user message")
	return domain.UserMessage{SessionMessageBase: base, Text: text, Files: files, Agents: agents, CreatedAt: created}, err
}

func decodeSyntheticMessage(object map[string]domain.JSONValue) (domain.SessionMessage, error) {
	if err := rejectUnknownContractFields(object, "synthetic message", "id", "metadata", "time", "type", "sessionID", "text"); err != nil {
		return nil, err
	}
	base, err := decodeSessionMessageBase(object)
	if err != nil {
		return nil, err
	}
	sessionValue, err := requiredContractString(object, "sessionID", "synthetic message")
	if err != nil {
		return nil, err
	}
	sessionID, err := domain.ParseSessionID(sessionValue)
	if err != nil {
		return nil, err
	}
	text, err := requiredContractString(object, "text", "synthetic message")
	if err != nil {
		return nil, err
	}
	created, err := decodeCreatedTime(object, "synthetic message")
	return domain.SyntheticMessage{SessionMessageBase: base, SessionID: sessionID, Text: text, CreatedAt: created}, err
}

func decodeSystemMessage(object map[string]domain.JSONValue) (domain.SessionMessage, error) {
	if err := rejectUnknownContractFields(object, "system message", "id", "metadata", "time", "type", "text"); err != nil {
		return nil, err
	}
	base, err := decodeSessionMessageBase(object)
	if err != nil {
		return nil, err
	}
	text, err := requiredContractString(object, "text", "system message")
	if err != nil {
		return nil, err
	}
	created, err := decodeCreatedTime(object, "system message")
	return domain.SystemMessage{SessionMessageBase: base, Text: text, CreatedAt: created}, err
}

func decodeShellMessage(object map[string]domain.JSONValue) (domain.SessionMessage, error) {
	if err := rejectUnknownContractFields(object, "shell message", "id", "metadata", "time", "type", "callID", "command", "output"); err != nil {
		return nil, err
	}
	base, err := decodeSessionMessageBase(object)
	if err != nil {
		return nil, err
	}
	callID, err := requiredContractString(object, "callID", "shell message")
	if err != nil {
		return nil, err
	}
	command, err := requiredContractString(object, "command", "shell message")
	if err != nil {
		return nil, err
	}
	output, err := requiredContractString(object, "output", "shell message")
	if err != nil {
		return nil, err
	}
	created, completed, err := decodeCreatedCompletedTime(object, "shell message")
	return domain.ShellMessage{
		SessionMessageBase: base, CallID: callID, Command: command, Output: output,
		CreatedAt: created, CompletedAt: completed,
	}, err
}

func decodeCompactionMessage(object map[string]domain.JSONValue) (domain.SessionMessage, error) {
	if err := rejectUnknownContractFields(object, "compaction message", "id", "metadata", "time", "type", "reason", "summary", "recent"); err != nil {
		return nil, err
	}
	base, err := decodeSessionMessageBase(object)
	if err != nil {
		return nil, err
	}
	reasonValue, err := requiredContractString(object, "reason", "compaction message")
	if err != nil {
		return nil, err
	}
	reason := domain.CompactionReason(reasonValue)
	if reason != domain.CompactionAuto && reason != domain.CompactionManual {
		return nil, fmt.Errorf("invalid compaction reason %q", reason)
	}
	summary, err := requiredContractString(object, "summary", "compaction message")
	if err != nil {
		return nil, err
	}
	recent, err := requiredContractString(object, "recent", "compaction message")
	if err != nil {
		return nil, err
	}
	created, err := decodeCreatedTime(object, "compaction message")
	return domain.CompactionMessage{SessionMessageBase: base, Reason: reason, Summary: summary, Recent: recent, CreatedAt: created}, err
}

func decodeSessionMessageBase(object map[string]domain.JSONValue) (domain.SessionMessageBase, error) {
	idValue, err := requiredContractString(object, "id", "session message")
	if err != nil {
		return domain.SessionMessageBase{}, err
	}
	id, err := domain.ParseMessageID(idValue)
	if err != nil {
		return domain.SessionMessageBase{}, err
	}
	metadata, _, err := optionalContractObject(object, "metadata", "session message")
	if err != nil {
		return domain.SessionMessageBase{}, err
	}
	return domain.SessionMessageBase{ID: id, Metadata: metadata}, nil
}

func encodeSessionMessageBase(base domain.SessionMessageBase, typeName domain.SessionMessageType) (map[string]domain.JSONValue, error) {
	if _, err := domain.ParseMessageID(string(base.ID)); err != nil {
		return nil, err
	}
	object := map[string]domain.JSONValue{
		"id": domain.JSONString(string(base.ID)), "type": domain.JSONString(string(typeName)),
	}
	if base.Metadata != nil {
		object["metadata"] = domain.JSONObject(base.Metadata)
	}
	return object, nil
}

func decodeModelRefField(object map[string]domain.JSONValue, field string, label string) (domain.ModelRef, error) {
	modelObject, err := requiredContractObject(object, field, label)
	if err != nil {
		return domain.ModelRef{}, err
	}
	if err := rejectUnknownContractFields(modelObject, "model ref", "id", "providerID", "variant"); err != nil {
		return domain.ModelRef{}, err
	}
	id, err := requiredContractString(modelObject, "id", "model ref")
	if err != nil {
		return domain.ModelRef{}, err
	}
	providerID, err := requiredContractString(modelObject, "providerID", "model ref")
	if err != nil {
		return domain.ModelRef{}, err
	}
	variant, err := optionalContractString(modelObject, "variant", "model ref")
	if err != nil {
		return domain.ModelRef{}, err
	}
	return domain.ModelRef{ID: id, ProviderID: providerID, Variant: variant}, nil
}

func encodeModelRef(model domain.ModelRef) domain.JSONValue {
	object := map[string]domain.JSONValue{
		"id": domain.JSONString(model.ID), "providerID": domain.JSONString(model.ProviderID),
	}
	addOptionalContractString(object, "variant", model.Variant)
	return domain.JSONObject(object)
}

func decodeCreatedTime(object map[string]domain.JSONValue, label string) (float64, error) {
	timeObject, err := requiredContractObject(object, "time", label)
	if err != nil {
		return 0, err
	}
	if err := rejectUnknownContractFields(timeObject, label+" time", "created"); err != nil {
		return 0, err
	}
	return requiredContractNumber(timeObject, "created", label+" time")
}

func decodeCreatedCompletedTime(object map[string]domain.JSONValue, label string) (float64, *float64, error) {
	timeObject, err := requiredContractObject(object, "time", label)
	if err != nil {
		return 0, nil, err
	}
	if err := rejectUnknownContractFields(timeObject, label+" time", "created", "completed"); err != nil {
		return 0, nil, err
	}
	created, err := requiredContractNumber(timeObject, "created", label+" time")
	if err != nil {
		return 0, nil, err
	}
	completed, err := optionalContractNumber(timeObject, "completed", label+" time")
	return created, completed, err
}

func encodeCreatedTime(created float64) domain.JSONValue {
	return domain.JSONObject(map[string]domain.JSONValue{"created": jsonNumber(created)})
}

func encodeCreatedCompletedTime(created float64, completed *float64) domain.JSONValue {
	object := map[string]domain.JSONValue{"created": jsonNumber(created)}
	addOptionalContractNumber(object, "completed", completed)
	return domain.JSONObject(object)
}

func decodePromptSource(value domain.JSONValue, label string) (*domain.PromptSource, error) {
	if value.Kind != domain.JSONKindObject {
		return nil, fmt.Errorf("%s source must be an object", label)
	}
	object := value.Object
	if err := rejectUnknownContractFields(object, label+" source", "start", "end", "text"); err != nil {
		return nil, err
	}
	start, err := requiredContractNumber(object, "start", label+" source")
	if err != nil {
		return nil, err
	}
	end, err := requiredContractNumber(object, "end", label+" source")
	if err != nil {
		return nil, err
	}
	text, err := requiredContractString(object, "text", label+" source")
	if err != nil {
		return nil, err
	}
	return &domain.PromptSource{Start: start, End: end, Text: text}, nil
}

func encodePromptSource(source domain.PromptSource) domain.JSONValue {
	return domain.JSONObject(map[string]domain.JSONValue{
		"start": jsonNumber(source.Start), "end": jsonNumber(source.End), "text": domain.JSONString(source.Text),
	})
}

func decodeOptionalFileAttachments(object map[string]domain.JSONValue, field string, label string) ([]domain.FileAttachment, error) {
	value, present := object[field]
	if !present {
		return nil, nil
	}
	if value.Kind != domain.JSONKindArray {
		return nil, fmt.Errorf("%s %s must be an array when present", label, field)
	}
	result := make([]domain.FileAttachment, len(value.Array))
	for index, item := range value.Array {
		attachment, err := decodeFileAttachment(item, fmt.Sprintf("%s %s item %d", label, field, index))
		if err != nil {
			return nil, err
		}
		result[index] = attachment
	}
	return result, nil
}

func decodeFileAttachment(value domain.JSONValue, label string) (domain.FileAttachment, error) {
	if value.Kind != domain.JSONKindObject {
		return domain.FileAttachment{}, fmt.Errorf("%s must be an object", label)
	}
	object := value.Object
	if err := rejectUnknownContractFields(object, label, "uri", "mime", "name", "description", "source"); err != nil {
		return domain.FileAttachment{}, err
	}
	uri, err := requiredContractString(object, "uri", label)
	if err != nil {
		return domain.FileAttachment{}, err
	}
	mime, err := requiredContractString(object, "mime", label)
	if err != nil {
		return domain.FileAttachment{}, err
	}
	name, err := optionalContractString(object, "name", label)
	if err != nil {
		return domain.FileAttachment{}, err
	}
	description, err := optionalContractString(object, "description", label)
	if err != nil {
		return domain.FileAttachment{}, err
	}
	var source *domain.PromptSource
	if sourceValue, present := object["source"]; present {
		source, err = decodePromptSource(sourceValue, label)
		if err != nil {
			return domain.FileAttachment{}, err
		}
	}
	return domain.FileAttachment{URI: uri, MIME: mime, Name: name, Description: description, Source: source}, nil
}

func encodeFileAttachments(attachments []domain.FileAttachment) domain.JSONValue {
	items := make([]domain.JSONValue, len(attachments))
	for index, attachment := range attachments {
		object := map[string]domain.JSONValue{
			"uri": domain.JSONString(attachment.URI), "mime": domain.JSONString(attachment.MIME),
		}
		addOptionalContractString(object, "name", attachment.Name)
		addOptionalContractString(object, "description", attachment.Description)
		if attachment.Source != nil {
			object["source"] = encodePromptSource(*attachment.Source)
		}
		items[index] = domain.JSONObject(object)
	}
	return domain.JSONArray(items)
}

func decodeOptionalAgentAttachments(object map[string]domain.JSONValue, field string, label string) ([]domain.AgentAttachment, error) {
	value, present := object[field]
	if !present {
		return nil, nil
	}
	if value.Kind != domain.JSONKindArray {
		return nil, fmt.Errorf("%s %s must be an array when present", label, field)
	}
	result := make([]domain.AgentAttachment, len(value.Array))
	for index, item := range value.Array {
		if item.Kind != domain.JSONKindObject {
			return nil, fmt.Errorf("%s %s item %d must be an object", label, field, index)
		}
		if err := rejectUnknownContractFields(item.Object, "agent attachment", "name", "source"); err != nil {
			return nil, err
		}
		name, err := requiredContractString(item.Object, "name", "agent attachment")
		if err != nil {
			return nil, err
		}
		var source *domain.PromptSource
		if sourceValue, present := item.Object["source"]; present {
			source, err = decodePromptSource(sourceValue, "agent attachment")
			if err != nil {
				return nil, err
			}
		}
		result[index] = domain.AgentAttachment{Name: name, Source: source}
	}
	return result, nil
}

func encodeAgentAttachments(attachments []domain.AgentAttachment) domain.JSONValue {
	items := make([]domain.JSONValue, len(attachments))
	for index, attachment := range attachments {
		object := map[string]domain.JSONValue{"name": domain.JSONString(attachment.Name)}
		if attachment.Source != nil {
			object["source"] = encodePromptSource(*attachment.Source)
		}
		items[index] = domain.JSONObject(object)
	}
	return domain.JSONArray(items)
}
