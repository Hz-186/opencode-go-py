package codec

import (
	"errors"
	"fmt"

	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

func decodeAssistantMessage(object map[string]domain.JSONValue) (domain.SessionMessage, error) {
	if err := rejectUnknownContractFields(object, "assistant message", "id", "metadata", "time", "type", "agent", "model", "content", "snapshot", "finish", "cost", "tokens", "error"); err != nil {
		return nil, err
	}
	base, err := decodeSessionMessageBase(object)
	if err != nil {
		return nil, err
	}
	agent, err := requiredContractString(object, "agent", "assistant message")
	if err != nil {
		return nil, err
	}
	model, err := decodeModelRefField(object, "model", "assistant message")
	if err != nil {
		return nil, err
	}
	contentValues, err := requiredContractArray(object, "content", "assistant message")
	if err != nil {
		return nil, err
	}
	content := make([]domain.AssistantContent, len(contentValues))
	for index, value := range contentValues {
		content[index], err = decodeAssistantContent(value, index)
		if err != nil {
			return nil, err
		}
	}
	snapshot, err := decodeAssistantSnapshot(object)
	if err != nil {
		return nil, err
	}
	finish, err := optionalContractString(object, "finish", "assistant message")
	if err != nil {
		return nil, err
	}
	cost, err := optionalContractNumber(object, "cost", "assistant message")
	if err != nil {
		return nil, err
	}
	tokens, err := decodeAssistantTokens(object)
	if err != nil {
		return nil, err
	}
	messageError, err := decodeOptionalUnknownError(object, "error", "assistant message")
	if err != nil {
		return nil, err
	}
	created, completed, err := decodeCreatedCompletedTime(object, "assistant message")
	if err != nil {
		return nil, err
	}
	return domain.AssistantMessage{
		SessionMessageBase: base, Agent: agent, Model: model, Content: content, Snapshot: snapshot,
		Finish: finish, Cost: cost, Tokens: tokens, Error: messageError, CreatedAt: created, CompletedAt: completed,
	}, nil
}

func encodeAssistantMessage(message domain.AssistantMessage) (map[string]domain.JSONValue, error) {
	object, err := encodeSessionMessageBase(message.SessionMessageBase, domain.SessionMessageAssistant)
	if err != nil {
		return nil, err
	}
	content := make([]domain.JSONValue, len(message.Content))
	for index, item := range message.Content {
		content[index], err = encodeAssistantContent(item)
		if err != nil {
			return nil, err
		}
	}
	object["agent"] = domain.JSONString(message.Agent)
	object["model"] = encodeModelRef(message.Model)
	object["content"] = domain.JSONArray(content)
	if message.Snapshot != nil {
		snapshot := make(map[string]domain.JSONValue)
		addOptionalContractString(snapshot, "start", message.Snapshot.Start)
		addOptionalContractString(snapshot, "end", message.Snapshot.End)
		if message.Snapshot.Files != nil {
			snapshot["files"] = contractStringArray(message.Snapshot.Files)
		}
		object["snapshot"] = domain.JSONObject(snapshot)
	}
	addOptionalContractString(object, "finish", message.Finish)
	addOptionalContractNumber(object, "cost", message.Cost)
	if message.Tokens != nil {
		object["tokens"] = domain.JSONObject(map[string]domain.JSONValue{
			"input":     domain.JSONNumber(numberString(message.Tokens.Input)),
			"output":    domain.JSONNumber(numberString(message.Tokens.Output)),
			"reasoning": domain.JSONNumber(numberString(message.Tokens.Reasoning)),
			"cache": domain.JSONObject(map[string]domain.JSONValue{
				"read":  domain.JSONNumber(numberString(message.Tokens.Cache.Read)),
				"write": domain.JSONNumber(numberString(message.Tokens.Cache.Write)),
			}),
		})
	}
	if message.Error != nil {
		object["error"] = encodeUnknownError(*message.Error)
	}
	object["time"] = encodeCreatedCompletedTime(message.CreatedAt, message.CompletedAt)
	return object, nil
}

func numberString(value float64) string {
	return jsonNumber(value).Number
}

func decodeAssistantSnapshot(object map[string]domain.JSONValue) (*domain.AssistantSnapshot, error) {
	snapshotObject, present, err := optionalContractObject(object, "snapshot", "assistant message")
	if err != nil || !present {
		return nil, err
	}
	if err := rejectUnknownContractFields(snapshotObject, "assistant snapshot", "start", "end", "files"); err != nil {
		return nil, err
	}
	start, err := optionalContractString(snapshotObject, "start", "assistant snapshot")
	if err != nil {
		return nil, err
	}
	end, err := optionalContractString(snapshotObject, "end", "assistant snapshot")
	if err != nil {
		return nil, err
	}
	files, err := optionalContractStringArray(snapshotObject, "files", "assistant snapshot")
	if err != nil {
		return nil, err
	}
	return &domain.AssistantSnapshot{Start: start, End: end, Files: files}, nil
}

func decodeAssistantTokens(object map[string]domain.JSONValue) (*domain.AssistantTokens, error) {
	tokenObject, present, err := optionalContractObject(object, "tokens", "assistant message")
	if err != nil || !present {
		return nil, err
	}
	if err := rejectUnknownContractFields(tokenObject, "assistant tokens", "input", "output", "reasoning", "cache"); err != nil {
		return nil, err
	}
	input, err := requiredContractNumber(tokenObject, "input", "assistant tokens")
	if err != nil {
		return nil, err
	}
	output, err := requiredContractNumber(tokenObject, "output", "assistant tokens")
	if err != nil {
		return nil, err
	}
	reasoning, err := requiredContractNumber(tokenObject, "reasoning", "assistant tokens")
	if err != nil {
		return nil, err
	}
	cacheObject, err := requiredContractObject(tokenObject, "cache", "assistant tokens")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownContractFields(cacheObject, "assistant token cache", "read", "write"); err != nil {
		return nil, err
	}
	read, err := requiredContractNumber(cacheObject, "read", "assistant token cache")
	if err != nil {
		return nil, err
	}
	write, err := requiredContractNumber(cacheObject, "write", "assistant token cache")
	if err != nil {
		return nil, err
	}
	return &domain.AssistantTokens{Input: input, Output: output, Reasoning: reasoning, Cache: domain.AssistantTokenCache{Read: read, Write: write}}, nil
}

func decodeAssistantContent(value domain.JSONValue, index int) (domain.AssistantContent, error) {
	if value.Kind != domain.JSONKindObject {
		return nil, fmt.Errorf("assistant content %d must be an object", index)
	}
	object := value.Object
	typeName, err := requiredContractString(object, "type", "assistant content")
	if err != nil {
		return nil, err
	}
	switch domain.AssistantContentType(typeName) {
	case domain.AssistantContentText:
		if err := rejectUnknownContractFields(object, "assistant text", "type", "id", "text"); err != nil {
			return nil, err
		}
		id, err := requiredContractString(object, "id", "assistant text")
		if err != nil {
			return nil, err
		}
		text, err := requiredContractString(object, "text", "assistant text")
		return domain.AssistantText{ID: id, Text: text}, err
	case domain.AssistantContentReasoning:
		return decodeAssistantReasoning(object)
	case domain.AssistantContentTool:
		return decodeAssistantTool(object)
	default:
		return nil, fmt.Errorf("unknown assistant content type %q", typeName)
	}
}

func encodeAssistantContent(content domain.AssistantContent) (domain.JSONValue, error) {
	if content == nil {
		return domain.JSONValue{}, errors.New("assistant content is nil")
	}
	switch content := content.(type) {
	case domain.AssistantText:
		return domain.JSONObject(map[string]domain.JSONValue{
			"type": domain.JSONString(string(domain.AssistantContentText)), "id": domain.JSONString(content.ID), "text": domain.JSONString(content.Text),
		}), nil
	case domain.AssistantReasoning:
		object := map[string]domain.JSONValue{
			"type": domain.JSONString(string(domain.AssistantContentReasoning)), "id": domain.JSONString(content.ID), "text": domain.JSONString(content.Text),
		}
		if err := addProviderMetadata(object, content.ProviderMetadata); err != nil {
			return domain.JSONValue{}, err
		}
		if content.CreatedAt != nil || content.CompletedAt != nil {
			timeObject := make(map[string]domain.JSONValue)
			addOptionalContractNumber(timeObject, "created", content.CreatedAt)
			addOptionalContractNumber(timeObject, "completed", content.CompletedAt)
			object["time"] = domain.JSONObject(timeObject)
		}
		return domain.JSONObject(object), nil
	case domain.AssistantTool:
		return encodeAssistantTool(content)
	default:
		return domain.JSONValue{}, fmt.Errorf("unsupported assistant content value %T", content)
	}
}

func decodeAssistantReasoning(object map[string]domain.JSONValue) (domain.AssistantContent, error) {
	if err := rejectUnknownContractFields(object, "assistant reasoning", "type", "id", "text", "providerMetadata", "time"); err != nil {
		return nil, err
	}
	id, err := requiredContractString(object, "id", "assistant reasoning")
	if err != nil {
		return nil, err
	}
	text, err := requiredContractString(object, "text", "assistant reasoning")
	if err != nil {
		return nil, err
	}
	metadata, err := optionalEventMetadata(object)
	if err != nil {
		return nil, err
	}
	var created *float64
	var completed *float64
	if timeObject, present, err := optionalContractObject(object, "time", "assistant reasoning"); err != nil {
		return nil, err
	} else if present {
		if err := rejectUnknownContractFields(timeObject, "assistant reasoning time", "created", "completed"); err != nil {
			return nil, err
		}
		createdValue, err := requiredContractNumber(timeObject, "created", "assistant reasoning time")
		if err != nil {
			return nil, err
		}
		created = &createdValue
		completed, err = optionalContractNumber(timeObject, "completed", "assistant reasoning time")
		if err != nil {
			return nil, err
		}
	}
	return domain.AssistantReasoning{ID: id, Text: text, ProviderMetadata: metadata, CreatedAt: created, CompletedAt: completed}, nil
}

func decodeAssistantTool(object map[string]domain.JSONValue) (domain.AssistantContent, error) {
	if err := rejectUnknownContractFields(object, "assistant tool", "type", "id", "name", "provider", "state", "time"); err != nil {
		return nil, err
	}
	id, err := requiredContractString(object, "id", "assistant tool")
	if err != nil {
		return nil, err
	}
	name, err := requiredContractString(object, "name", "assistant tool")
	if err != nil {
		return nil, err
	}
	provider, err := decodeAssistantToolProvider(object)
	if err != nil {
		return nil, err
	}
	stateObject, err := requiredContractObject(object, "state", "assistant tool")
	if err != nil {
		return nil, err
	}
	state, err := decodeSessionToolState(stateObject)
	if err != nil {
		return nil, err
	}
	created, ran, completed, pruned, err := decodeAssistantToolTime(object)
	if err != nil {
		return nil, err
	}
	return domain.AssistantTool{ID: id, Name: name, Provider: provider, State: state, CreatedAt: created, RanAt: ran, CompletedAt: completed, PrunedAt: pruned}, nil
}

func encodeAssistantTool(tool domain.AssistantTool) (domain.JSONValue, error) {
	if tool.State == nil {
		return domain.JSONValue{}, errors.New("assistant tool state is nil")
	}
	state, err := encodeSessionToolState(tool.State)
	if err != nil {
		return domain.JSONValue{}, err
	}
	object := map[string]domain.JSONValue{
		"type": domain.JSONString(string(domain.AssistantContentTool)), "id": domain.JSONString(tool.ID),
		"name": domain.JSONString(tool.Name), "state": state,
	}
	if tool.Provider != nil {
		provider := map[string]domain.JSONValue{"executed": domain.JSONBool(tool.Provider.Executed)}
		if tool.Provider.Metadata != nil {
			value, err := providerMetadataToJSONValue(tool.Provider.Metadata)
			if err != nil {
				return domain.JSONValue{}, err
			}
			provider["metadata"] = value
		}
		if tool.Provider.ResultMetadata != nil {
			value, err := providerMetadataToJSONValue(tool.Provider.ResultMetadata)
			if err != nil {
				return domain.JSONValue{}, err
			}
			provider["resultMetadata"] = value
		}
		object["provider"] = domain.JSONObject(provider)
	}
	timeObject := map[string]domain.JSONValue{"created": jsonNumber(tool.CreatedAt)}
	addOptionalContractNumber(timeObject, "ran", tool.RanAt)
	addOptionalContractNumber(timeObject, "completed", tool.CompletedAt)
	addOptionalContractNumber(timeObject, "pruned", tool.PrunedAt)
	object["time"] = domain.JSONObject(timeObject)
	return domain.JSONObject(object), nil
}

func decodeAssistantToolProvider(object map[string]domain.JSONValue) (*domain.AssistantToolProvider, error) {
	providerObject, present, err := optionalContractObject(object, "provider", "assistant tool")
	if err != nil || !present {
		return nil, err
	}
	if err := rejectUnknownContractFields(providerObject, "assistant tool provider", "executed", "metadata", "resultMetadata"); err != nil {
		return nil, err
	}
	executed, err := requiredContractBool(providerObject, "executed", "assistant tool provider")
	if err != nil {
		return nil, err
	}
	metadata, err := decodeOptionalProviderMetadataField(providerObject, "metadata", "assistant tool provider")
	if err != nil {
		return nil, err
	}
	resultMetadata, err := decodeOptionalProviderMetadataField(providerObject, "resultMetadata", "assistant tool provider")
	if err != nil {
		return nil, err
	}
	return &domain.AssistantToolProvider{Executed: executed, Metadata: metadata, ResultMetadata: resultMetadata}, nil
}

func decodeOptionalProviderMetadataField(object map[string]domain.JSONValue, field string, label string) (llm.ProviderMetadata, error) {
	value, present := object[field]
	if !present {
		return nil, nil
	}
	if value.Kind == domain.JSONKindNull {
		return nil, fmt.Errorf("%s %s must not be null", label, field)
	}
	return providerMetadataFromJSONValue(value)
}

func decodeAssistantToolTime(object map[string]domain.JSONValue) (float64, *float64, *float64, *float64, error) {
	timeObject, err := requiredContractObject(object, "time", "assistant tool")
	if err != nil {
		return 0, nil, nil, nil, err
	}
	if err := rejectUnknownContractFields(timeObject, "assistant tool time", "created", "ran", "completed", "pruned"); err != nil {
		return 0, nil, nil, nil, err
	}
	created, err := requiredContractNumber(timeObject, "created", "assistant tool time")
	if err != nil {
		return 0, nil, nil, nil, err
	}
	ran, err := optionalContractNumber(timeObject, "ran", "assistant tool time")
	if err != nil {
		return 0, nil, nil, nil, err
	}
	completed, err := optionalContractNumber(timeObject, "completed", "assistant tool time")
	if err != nil {
		return 0, nil, nil, nil, err
	}
	pruned, err := optionalContractNumber(timeObject, "pruned", "assistant tool time")
	return created, ran, completed, pruned, err
}

func decodeSessionToolState(object map[string]domain.JSONValue) (domain.SessionToolState, error) {
	statusValue, err := requiredContractString(object, "status", "session tool state")
	if err != nil {
		return nil, err
	}
	switch domain.SessionToolStateStatus(statusValue) {
	case domain.SessionToolPending:
		if err := rejectUnknownContractFields(object, "pending tool state", "status", "input"); err != nil {
			return nil, err
		}
		input, err := requiredContractString(object, "input", "pending tool state")
		return domain.SessionToolPendingState{Input: input}, err
	case domain.SessionToolRunning:
		if err := rejectUnknownContractFields(object, "running tool state", "status", "input", "structured", "content"); err != nil {
			return nil, err
		}
		input, err := requiredContractObject(object, "input", "running tool state")
		if err != nil {
			return nil, err
		}
		structured, err := requiredContractObject(object, "structured", "running tool state")
		if err != nil {
			return nil, err
		}
		content, err := decodeRequiredToolContent(object, "content", "running tool state")
		if err != nil {
			return nil, err
		}
		return domain.SessionToolRunningState{Input: input, Structured: structured, Content: content}, nil
	case domain.SessionToolCompleted:
		return decodeCompletedToolState(object)
	case domain.SessionToolError:
		return decodeErrorToolState(object)
	default:
		return nil, fmt.Errorf("unknown session tool state %q", statusValue)
	}
}

func decodeCompletedToolState(object map[string]domain.JSONValue) (domain.SessionToolState, error) {
	if err := rejectUnknownContractFields(object, "completed tool state", "status", "input", "attachments", "content", "outputPaths", "structured", "result"); err != nil {
		return nil, err
	}
	input, err := requiredContractObject(object, "input", "completed tool state")
	if err != nil {
		return nil, err
	}
	attachments, err := decodeOptionalFileAttachments(object, "attachments", "completed tool state")
	if err != nil {
		return nil, err
	}
	content, err := decodeRequiredToolContent(object, "content", "completed tool state")
	if err != nil {
		return nil, err
	}
	outputPaths, err := optionalContractStringArray(object, "outputPaths", "completed tool state")
	if err != nil {
		return nil, err
	}
	structured, err := requiredContractObject(object, "structured", "completed tool state")
	if err != nil {
		return nil, err
	}
	result, err := decodeOptionalUnknownValue(object, "result", "completed tool state")
	if err != nil {
		return nil, err
	}
	return domain.SessionToolCompletedState{Input: input, Attachments: attachments, Content: content, OutputPaths: outputPaths, Structured: structured, Result: result}, nil
}

func decodeErrorToolState(object map[string]domain.JSONValue) (domain.SessionToolState, error) {
	if err := rejectUnknownContractFields(object, "error tool state", "status", "input", "content", "structured", "error", "result"); err != nil {
		return nil, err
	}
	input, err := requiredContractObject(object, "input", "error tool state")
	if err != nil {
		return nil, err
	}
	content, err := decodeRequiredToolContent(object, "content", "error tool state")
	if err != nil {
		return nil, err
	}
	structured, err := requiredContractObject(object, "structured", "error tool state")
	if err != nil {
		return nil, err
	}
	stateError, err := decodeRequiredUnknownError(object, "error", "error tool state")
	if err != nil {
		return nil, err
	}
	result, err := decodeOptionalUnknownValue(object, "result", "error tool state")
	if err != nil {
		return nil, err
	}
	return domain.SessionToolErrorState{Input: input, Content: content, Structured: structured, Error: stateError, Result: result}, nil
}

func encodeSessionToolState(state domain.SessionToolState) (domain.JSONValue, error) {
	switch state := state.(type) {
	case domain.SessionToolPendingState:
		return domain.JSONObject(map[string]domain.JSONValue{"status": domain.JSONString(string(domain.SessionToolPending)), "input": domain.JSONString(state.Input)}), nil
	case domain.SessionToolRunningState:
		return domain.JSONObject(map[string]domain.JSONValue{
			"status": domain.JSONString(string(domain.SessionToolRunning)), "input": domain.JSONObject(state.Input),
			"structured": domain.JSONObject(state.Structured), "content": encodeToolContent(state.Content),
		}), nil
	case domain.SessionToolCompletedState:
		object := map[string]domain.JSONValue{
			"status": domain.JSONString(string(domain.SessionToolCompleted)), "input": domain.JSONObject(state.Input),
			"content": encodeToolContent(state.Content), "structured": domain.JSONObject(state.Structured),
		}
		if state.Attachments != nil {
			object["attachments"] = encodeFileAttachments(state.Attachments)
		}
		if state.OutputPaths != nil {
			object["outputPaths"] = contractStringArray(state.OutputPaths)
		}
		if state.Result != nil {
			if state.Result.Kind == domain.JSONKindNull {
				return domain.JSONValue{}, errors.New("completed tool state result must not be null")
			}
			object["result"] = *state.Result
		}
		return domain.JSONObject(object), nil
	case domain.SessionToolErrorState:
		object := map[string]domain.JSONValue{
			"status": domain.JSONString(string(domain.SessionToolError)), "input": domain.JSONObject(state.Input),
			"content": encodeToolContent(state.Content), "structured": domain.JSONObject(state.Structured),
			"error": encodeUnknownError(state.Error),
		}
		if state.Result != nil {
			if state.Result.Kind == domain.JSONKindNull {
				return domain.JSONValue{}, errors.New("error tool state result must not be null")
			}
			object["result"] = *state.Result
		}
		return domain.JSONObject(object), nil
	default:
		return domain.JSONValue{}, fmt.Errorf("unsupported session tool state %T", state)
	}
}

func decodeRequiredToolContent(object map[string]domain.JSONValue, field string, label string) ([]domain.ToolContent, error) {
	values, err := requiredContractArray(object, field, label)
	if err != nil {
		return nil, err
	}
	result := make([]domain.ToolContent, len(values))
	for index, value := range values {
		if value.Kind != domain.JSONKindObject {
			return nil, fmt.Errorf("%s %s item %d must be an object", label, field, index)
		}
		typeName, err := requiredContractString(value.Object, "type", "tool content")
		if err != nil {
			return nil, err
		}
		switch domain.ToolContentType(typeName) {
		case domain.ToolContentText:
			if err := rejectUnknownContractFields(value.Object, "tool text content", "type", "text"); err != nil {
				return nil, err
			}
			text, err := requiredContractString(value.Object, "text", "tool text content")
			if err != nil {
				return nil, err
			}
			result[index] = domain.ToolContent{Type: domain.ToolContentText, Text: text}
		case domain.ToolContentFile:
			if err := rejectUnknownContractFields(value.Object, "tool file content", "type", "uri", "mime", "name"); err != nil {
				return nil, err
			}
			uri, err := requiredContractString(value.Object, "uri", "tool file content")
			if err != nil {
				return nil, err
			}
			mime, err := requiredContractString(value.Object, "mime", "tool file content")
			if err != nil {
				return nil, err
			}
			name, err := optionalContractString(value.Object, "name", "tool file content")
			if err != nil {
				return nil, err
			}
			result[index] = domain.ToolContent{Type: domain.ToolContentFile, URI: uri, MIME: mime, Name: name}
		default:
			return nil, fmt.Errorf("unknown tool content type %q", typeName)
		}
	}
	return result, nil
}

func encodeToolContent(content []domain.ToolContent) domain.JSONValue {
	items := make([]domain.JSONValue, len(content))
	for index, item := range content {
		switch item.Type {
		case domain.ToolContentText:
			items[index] = domain.JSONObject(map[string]domain.JSONValue{"type": domain.JSONString(string(item.Type)), "text": domain.JSONString(item.Text)})
		case domain.ToolContentFile:
			object := map[string]domain.JSONValue{
				"type": domain.JSONString(string(item.Type)), "uri": domain.JSONString(item.URI), "mime": domain.JSONString(item.MIME),
			}
			addOptionalContractString(object, "name", item.Name)
			items[index] = domain.JSONObject(object)
		default:
			items[index] = domain.JSONObject(map[string]domain.JSONValue{"type": domain.JSONString(string(item.Type))})
		}
	}
	return domain.JSONArray(items)
}

func decodeOptionalUnknownValue(object map[string]domain.JSONValue, field string, label string) (*domain.JSONValue, error) {
	value, present := object[field]
	if !present {
		return nil, nil
	}
	if value.Kind == domain.JSONKindNull {
		return nil, fmt.Errorf("%s %s must not be null", label, field)
	}
	return &value, nil
}

func decodeOptionalUnknownError(object map[string]domain.JSONValue, field string, label string) (*domain.UnknownError, error) {
	value, present := object[field]
	if !present {
		return nil, nil
	}
	if value.Kind != domain.JSONKindObject {
		return nil, fmt.Errorf("%s %s must be an object when present", label, field)
	}
	result, err := decodeUnknownErrorObject(value.Object, label+" "+field)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func decodeRequiredUnknownError(object map[string]domain.JSONValue, field string, label string) (domain.UnknownError, error) {
	value, err := requiredContractObject(object, field, label)
	if err != nil {
		return domain.UnknownError{}, err
	}
	return decodeUnknownErrorObject(value, label+" "+field)
}

func decodeUnknownErrorObject(object map[string]domain.JSONValue, label string) (domain.UnknownError, error) {
	if err := rejectUnknownContractFields(object, label, "type", "message"); err != nil {
		return domain.UnknownError{}, err
	}
	typeName, err := requiredContractString(object, "type", label)
	if err != nil {
		return domain.UnknownError{}, err
	}
	if typeName != "unknown" {
		return domain.UnknownError{}, fmt.Errorf("%s type must be unknown", label)
	}
	message, err := requiredContractString(object, "message", label)
	if err != nil {
		return domain.UnknownError{}, err
	}
	return domain.UnknownError{Message: message}, nil
}

func encodeUnknownError(value domain.UnknownError) domain.JSONValue {
	return domain.JSONObject(map[string]domain.JSONValue{"type": domain.JSONString("unknown"), "message": domain.JSONString(value.Message)})
}
