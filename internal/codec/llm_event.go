package codec

import (
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

func DecodeLLMEventJSON(content []byte) (llm.LLMEvent, error) {
	value, err := DecodeJSONValue(content)
	if err != nil {
		return nil, err
	}
	if value.Kind != domain.JSONKindObject {
		return nil, errors.New("LLM event must be a JSON object")
	}
	typeName, err := requiredEventString(value.Object, "type")
	if err != nil || typeName == "" {
		return nil, errors.New("LLM event type is required")
	}
	object := value.Object
	switch llm.EventType(typeName) {
	case llm.EventStepStart:
		if err := onlyEventFields(object, "index"); err != nil {
			return nil, err
		}
		index, err := requiredEventNumber(object, "index")
		return llm.StepStart{Index: index}, err
	case llm.EventTextStart:
		return decodeTextStart(object)
	case llm.EventTextDelta:
		return decodeTextDelta(object)
	case llm.EventTextEnd:
		return decodeTextEnd(object)
	case llm.EventReasoningStart:
		return decodeReasoningStart(object)
	case llm.EventReasoningDelta:
		return decodeReasoningDelta(object)
	case llm.EventReasoningEnd:
		return decodeReasoningEnd(object)
	case llm.EventToolInputStart:
		return decodeToolInputStart(object)
	case llm.EventToolInputDelta:
		return decodeToolInputDelta(object)
	case llm.EventToolInputEnd:
		return decodeToolInputEnd(object)
	case llm.EventToolCall:
		return decodeToolCall(object)
	case llm.EventToolResult:
		return decodeToolResult(object)
	case llm.EventToolError:
		return decodeToolError(object)
	case llm.EventStepFinish:
		return decodeStepFinish(object)
	case llm.EventFinish:
		return decodeFinish(object)
	case llm.EventProviderError:
		return decodeProviderError(object)
	default:
		return llm.UnknownEvent{Type: typeName, Raw: value}, nil
	}
}

func EncodeLLMEventJSON(event llm.LLMEvent) ([]byte, error) {
	if event == nil {
		return nil, errors.New("LLM event is nil")
	}
	object := map[string]domain.JSONValue{"type": domain.JSONString(string(event.EventType()))}
	var err error
	switch event := event.(type) {
	case llm.StepStart:
		object["index"] = jsonNumber(event.Index)
	case llm.TextStart:
		object["id"] = domain.JSONString(event.ID)
		err = addProviderMetadata(object, event.ProviderMetadata)
	case llm.TextDelta:
		object["id"] = domain.JSONString(event.ID)
		object["text"] = domain.JSONString(event.Text)
		err = addProviderMetadata(object, event.ProviderMetadata)
	case llm.TextEnd:
		object["id"] = domain.JSONString(event.ID)
		err = addProviderMetadata(object, event.ProviderMetadata)
	case llm.ReasoningStart:
		object["id"] = domain.JSONString(event.ID)
		err = addProviderMetadata(object, event.ProviderMetadata)
	case llm.ReasoningDelta:
		object["id"] = domain.JSONString(event.ID)
		object["text"] = domain.JSONString(event.Text)
		err = addProviderMetadata(object, event.ProviderMetadata)
	case llm.ReasoningEnd:
		object["id"] = domain.JSONString(event.ID)
		err = addProviderMetadata(object, event.ProviderMetadata)
	case llm.ToolInputStart:
		object["id"] = domain.JSONString(event.ID)
		object["name"] = domain.JSONString(event.Name)
		err = addProviderMetadata(object, event.ProviderMetadata)
	case llm.ToolInputDelta:
		object["id"] = domain.JSONString(event.ID)
		object["name"] = domain.JSONString(event.Name)
		object["text"] = domain.JSONString(event.Text)
	case llm.ToolInputEnd:
		object["id"] = domain.JSONString(event.ID)
		object["name"] = domain.JSONString(event.Name)
		err = addProviderMetadata(object, event.ProviderMetadata)
	case llm.ToolCall:
		object["id"] = domain.JSONString(event.ID)
		object["name"] = domain.JSONString(event.Name)
		object["input"] = event.Input
		addOptionalBool(object, "providerExecuted", event.ProviderExecuted)
		err = addProviderMetadata(object, event.ProviderMetadata)
	case llm.ToolResult:
		object["id"] = domain.JSONString(event.ID)
		object["name"] = domain.JSONString(event.Name)
		object["result"] = event.Result
		if event.Output != nil {
			object["output"] = *event.Output
		}
		addOptionalBool(object, "providerExecuted", event.ProviderExecuted)
		err = addProviderMetadata(object, event.ProviderMetadata)
	case llm.ToolError:
		object["id"] = domain.JSONString(event.ID)
		object["name"] = domain.JSONString(event.Name)
		object["message"] = domain.JSONString(event.Message)
		if event.Error != nil {
			object["error"] = *event.Error
		}
		err = addProviderMetadata(object, event.ProviderMetadata)
	case llm.StepFinish:
		object["index"] = jsonNumber(event.Index)
		object["reason"] = domain.JSONString(string(event.Reason))
		err = addUsageAndMetadata(object, event.Usage, event.ProviderMetadata)
	case llm.Finish:
		object["reason"] = domain.JSONString(string(event.Reason))
		err = addUsageAndMetadata(object, event.Usage, event.ProviderMetadata)
	case llm.ProviderError:
		object["message"] = domain.JSONString(event.Message)
		if event.Classification != nil {
			object["classification"] = domain.JSONString(string(*event.Classification))
		}
		addOptionalBool(object, "retryable", event.Retryable)
		err = addProviderMetadata(object, event.ProviderMetadata)
	case llm.UnknownEvent:
		if event.Raw.Kind != domain.JSONKindObject {
			return nil, errors.New("unknown LLM event raw value must be an object")
		}
		if isKnownLLMEventType(llm.EventType(event.Type)) {
			return nil, fmt.Errorf("known LLM event type %q must use its typed domain value", event.Type)
		}
		rawType, typeErr := requiredEventString(event.Raw.Object, "type")
		if typeErr != nil || rawType != event.Type {
			return nil, errors.New("unknown LLM event type does not match raw payload")
		}
		return EncodeJSONValue(event.Raw)
	default:
		return nil, fmt.Errorf("unsupported LLM event value %T", event)
	}
	if err != nil {
		return nil, err
	}
	encoded, err := EncodeJSONValue(domain.JSONObject(object))
	if err != nil {
		return nil, err
	}
	if _, err := DecodeLLMEventJSON(encoded); err != nil {
		return nil, fmt.Errorf("validate encoded LLM event: %w", err)
	}
	return encoded, nil
}

func decodeTextStart(object map[string]domain.JSONValue) (llm.LLMEvent, error) {
	if err := onlyEventFields(object, "id", "providerMetadata"); err != nil {
		return nil, err
	}
	id, err := requiredEventString(object, "id")
	if err != nil {
		return nil, err
	}
	metadata, err := optionalEventMetadata(object)
	return llm.TextStart{ID: id, ProviderMetadata: metadata}, err
}

func decodeTextDelta(object map[string]domain.JSONValue) (llm.LLMEvent, error) {
	if err := onlyEventFields(object, "id", "text", "providerMetadata"); err != nil {
		return nil, err
	}
	id, err := requiredEventString(object, "id")
	if err != nil {
		return nil, err
	}
	text, err := requiredEventString(object, "text")
	if err != nil {
		return nil, err
	}
	metadata, err := optionalEventMetadata(object)
	return llm.TextDelta{ID: id, Text: text, ProviderMetadata: metadata}, err
}

func decodeTextEnd(object map[string]domain.JSONValue) (llm.LLMEvent, error) {
	if err := onlyEventFields(object, "id", "providerMetadata"); err != nil {
		return nil, err
	}
	id, err := requiredEventString(object, "id")
	if err != nil {
		return nil, err
	}
	metadata, err := optionalEventMetadata(object)
	return llm.TextEnd{ID: id, ProviderMetadata: metadata}, err
}

func decodeReasoningStart(object map[string]domain.JSONValue) (llm.LLMEvent, error) {
	event, err := decodeTextStart(object)
	if err != nil {
		return nil, err
	}
	text := event.(llm.TextStart)
	return llm.ReasoningStart{ID: text.ID, ProviderMetadata: text.ProviderMetadata}, nil
}

func decodeReasoningDelta(object map[string]domain.JSONValue) (llm.LLMEvent, error) {
	event, err := decodeTextDelta(object)
	if err != nil {
		return nil, err
	}
	text := event.(llm.TextDelta)
	return llm.ReasoningDelta{ID: text.ID, Text: text.Text, ProviderMetadata: text.ProviderMetadata}, nil
}

func decodeReasoningEnd(object map[string]domain.JSONValue) (llm.LLMEvent, error) {
	event, err := decodeTextEnd(object)
	if err != nil {
		return nil, err
	}
	text := event.(llm.TextEnd)
	return llm.ReasoningEnd{ID: text.ID, ProviderMetadata: text.ProviderMetadata}, nil
}

func decodeToolInputStart(object map[string]domain.JSONValue) (llm.LLMEvent, error) {
	if err := onlyEventFields(object, "id", "name", "providerMetadata"); err != nil {
		return nil, err
	}
	id, name, metadata, err := decodeNamedEvent(object, true)
	return llm.ToolInputStart{ID: id, Name: name, ProviderMetadata: metadata}, err
}

func decodeToolInputDelta(object map[string]domain.JSONValue) (llm.LLMEvent, error) {
	if err := onlyEventFields(object, "id", "name", "text"); err != nil {
		return nil, err
	}
	id, name, _, err := decodeNamedEvent(object, false)
	if err != nil {
		return nil, err
	}
	text, err := requiredEventString(object, "text")
	return llm.ToolInputDelta{ID: id, Name: name, Text: text}, err
}

func decodeToolInputEnd(object map[string]domain.JSONValue) (llm.LLMEvent, error) {
	if err := onlyEventFields(object, "id", "name", "providerMetadata"); err != nil {
		return nil, err
	}
	id, name, metadata, err := decodeNamedEvent(object, true)
	return llm.ToolInputEnd{ID: id, Name: name, ProviderMetadata: metadata}, err
}

func decodeToolCall(object map[string]domain.JSONValue) (llm.LLMEvent, error) {
	if err := onlyEventFields(object, "id", "name", "input", "providerExecuted", "providerMetadata"); err != nil {
		return nil, err
	}
	id, name, metadata, err := decodeNamedEvent(object, true)
	if err != nil {
		return nil, err
	}
	input, ok := object["input"]
	if !ok {
		return nil, errors.New("LLM event input is required")
	}
	providerExecuted, err := optionalEventBool(object, "providerExecuted")
	return llm.ToolCall{ID: id, Name: name, Input: input, ProviderExecuted: providerExecuted, ProviderMetadata: metadata}, err
}

func decodeToolResult(object map[string]domain.JSONValue) (llm.LLMEvent, error) {
	if err := onlyEventFields(object, "id", "name", "result", "output", "providerExecuted", "providerMetadata"); err != nil {
		return nil, err
	}
	id, name, metadata, err := decodeNamedEvent(object, true)
	if err != nil {
		return nil, err
	}
	result, ok := object["result"]
	if !ok {
		return nil, errors.New("LLM event result is required")
	}
	if err := validateToolResultValue(result); err != nil {
		return nil, err
	}
	var output *domain.JSONValue
	if value, present := object["output"]; present {
		if value.Kind == domain.JSONKindNull {
			return nil, errors.New("LLM event output must not be null")
		}
		if err := validateToolOutput(value); err != nil {
			return nil, err
		}
		output = &value
	}
	providerExecuted, err := optionalEventBool(object, "providerExecuted")
	return llm.ToolResult{ID: id, Name: name, Result: result, Output: output, ProviderExecuted: providerExecuted, ProviderMetadata: metadata}, err
}

func decodeToolError(object map[string]domain.JSONValue) (llm.LLMEvent, error) {
	if err := onlyEventFields(object, "id", "name", "message", "error", "providerMetadata"); err != nil {
		return nil, err
	}
	id, name, metadata, err := decodeNamedEvent(object, true)
	if err != nil {
		return nil, err
	}
	message, err := requiredEventString(object, "message")
	if err != nil {
		return nil, err
	}
	var eventError *domain.JSONValue
	if value, present := object["error"]; present {
		if value.Kind == domain.JSONKindNull {
			return nil, errors.New("LLM event error must not be null")
		}
		eventError = &value
	}
	return llm.ToolError{ID: id, Name: name, Message: message, Error: eventError, ProviderMetadata: metadata}, nil
}

func decodeStepFinish(object map[string]domain.JSONValue) (llm.LLMEvent, error) {
	if err := onlyEventFields(object, "index", "reason", "usage", "providerMetadata"); err != nil {
		return nil, err
	}
	index, err := requiredEventNumber(object, "index")
	if err != nil {
		return nil, err
	}
	reason, usage, metadata, err := decodeFinishFields(object)
	return llm.StepFinish{Index: index, Reason: reason, Usage: usage, ProviderMetadata: metadata}, err
}

func decodeFinish(object map[string]domain.JSONValue) (llm.LLMEvent, error) {
	if err := onlyEventFields(object, "reason", "usage", "providerMetadata"); err != nil {
		return nil, err
	}
	reason, usage, metadata, err := decodeFinishFields(object)
	return llm.Finish{Reason: reason, Usage: usage, ProviderMetadata: metadata}, err
}

func decodeProviderError(object map[string]domain.JSONValue) (llm.LLMEvent, error) {
	if err := onlyEventFields(object, "message", "classification", "retryable", "providerMetadata"); err != nil {
		return nil, err
	}
	message, err := requiredEventString(object, "message")
	if err != nil {
		return nil, err
	}
	var classification *llm.ProviderFailureClassification
	if value, present := object["classification"]; present {
		if value.Kind != domain.JSONKindString || value.String != string(llm.ProviderFailureContextOverflow) {
			return nil, errors.New("invalid provider failure classification")
		}
		parsed := llm.ProviderFailureClassification(value.String)
		classification = &parsed
	}
	retryable, err := optionalEventBool(object, "retryable")
	if err != nil {
		return nil, err
	}
	metadata, err := optionalEventMetadata(object)
	return llm.ProviderError{Message: message, Classification: classification, Retryable: retryable, ProviderMetadata: metadata}, err
}

func decodeNamedEvent(object map[string]domain.JSONValue, metadataAllowed bool) (string, string, llm.ProviderMetadata, error) {
	id, err := requiredEventString(object, "id")
	if err != nil {
		return "", "", nil, err
	}
	name, err := requiredEventString(object, "name")
	if err != nil {
		return "", "", nil, err
	}
	if !metadataAllowed {
		return id, name, nil, nil
	}
	metadata, err := optionalEventMetadata(object)
	return id, name, metadata, err
}

func decodeFinishFields(object map[string]domain.JSONValue) (llm.FinishReason, *llm.Usage, llm.ProviderMetadata, error) {
	reasonValue, err := requiredEventString(object, "reason")
	if err != nil {
		return "", nil, nil, err
	}
	reason := llm.FinishReason(reasonValue)
	if !validFinishReason(reason) {
		return "", nil, nil, fmt.Errorf("invalid finish reason %q", reason)
	}
	usage, err := optionalEventUsage(object)
	if err != nil {
		return "", nil, nil, err
	}
	metadata, err := optionalEventMetadata(object)
	return reason, usage, metadata, err
}

func optionalEventUsage(object map[string]domain.JSONValue) (*llm.Usage, error) {
	value, present := object["usage"]
	if !present {
		return nil, nil
	}
	if value.Kind == domain.JSONKindNull {
		return nil, errors.New("LLM event usage must not be null")
	}
	encoded, err := EncodeJSONValue(value)
	if err != nil {
		return nil, err
	}
	usage, err := DecodeUsageJSON(encoded)
	if err != nil {
		return nil, err
	}
	return &usage, nil
}

func optionalEventMetadata(object map[string]domain.JSONValue) (llm.ProviderMetadata, error) {
	value, present := object["providerMetadata"]
	if !present {
		return nil, nil
	}
	if value.Kind == domain.JSONKindNull {
		return nil, errors.New("LLM event providerMetadata must not be null")
	}
	return providerMetadataFromJSONValue(value)
}

func providerMetadataFromJSONValue(value domain.JSONValue) (llm.ProviderMetadata, error) {
	if value.Kind != domain.JSONKindObject {
		return nil, errors.New("provider metadata must be a JSON object")
	}
	metadata := make(llm.ProviderMetadata, len(value.Object))
	for provider, providerValue := range value.Object {
		if providerValue.Kind != domain.JSONKindObject {
			return nil, fmt.Errorf("provider metadata %q must be a JSON object", provider)
		}
		metadata[provider] = providerValue.Object
	}
	return metadata, nil
}

func providerMetadataToJSONValue(metadata llm.ProviderMetadata) (domain.JSONValue, error) {
	if err := metadata.Validate(); err != nil {
		return domain.JSONValue{}, err
	}
	object := make(map[string]domain.JSONValue, len(metadata))
	for provider, values := range metadata {
		object[provider] = domain.JSONObject(values)
	}
	return domain.JSONObject(object), nil
}

func addProviderMetadata(object map[string]domain.JSONValue, metadata llm.ProviderMetadata) error {
	if metadata == nil {
		return nil
	}
	value, err := providerMetadataToJSONValue(metadata)
	if err != nil {
		return err
	}
	object["providerMetadata"] = value
	return nil
}

func addUsageAndMetadata(object map[string]domain.JSONValue, usage *llm.Usage, metadata llm.ProviderMetadata) error {
	if usage != nil {
		encoded, err := EncodeUsageJSON(*usage)
		if err != nil {
			return err
		}
		value, err := DecodeJSONValue(encoded)
		if err != nil {
			return err
		}
		object["usage"] = value
	}
	return addProviderMetadata(object, metadata)
}

func addOptionalBool(object map[string]domain.JSONValue, field string, value *bool) {
	if value != nil {
		object[field] = domain.JSONBool(*value)
	}
}

func onlyEventFields(object map[string]domain.JSONValue, fields ...string) error {
	allowed := map[string]struct{}{"type": {}}
	for _, field := range fields {
		allowed[field] = struct{}{}
	}
	for field := range object {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("unknown %s event field %q", object["type"].String, field)
		}
	}
	return nil
}

func requiredEventString(object map[string]domain.JSONValue, field string) (string, error) {
	value, present := object[field]
	if !present || value.Kind != domain.JSONKindString {
		return "", fmt.Errorf("LLM event %s must be a string", field)
	}
	return value.String, nil
}

func requiredEventNumber(object map[string]domain.JSONValue, field string) (float64, error) {
	value, present := object[field]
	if !present || value.Kind != domain.JSONKindNumber {
		return 0, fmt.Errorf("LLM event %s must be a number", field)
	}
	parsed, err := strconv.ParseFloat(value.Number, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, fmt.Errorf("LLM event %s must be finite", field)
	}
	return normalizeNumber(parsed), nil
}

func optionalEventBool(object map[string]domain.JSONValue, field string) (*bool, error) {
	value, present := object[field]
	if !present {
		return nil, nil
	}
	if value.Kind != domain.JSONKindBool {
		return nil, fmt.Errorf("LLM event %s must be a boolean when present", field)
	}
	result := value.Bool
	return &result, nil
}

func validFinishReason(reason llm.FinishReason) bool {
	switch reason {
	case llm.FinishStop, llm.FinishLength, llm.FinishToolCalls, llm.FinishContentFilter, llm.FinishError, llm.FinishUnknown:
		return true
	default:
		return false
	}
}

func isKnownLLMEventType(eventType llm.EventType) bool {
	switch eventType {
	case llm.EventStepStart,
		llm.EventTextStart,
		llm.EventTextDelta,
		llm.EventTextEnd,
		llm.EventReasoningStart,
		llm.EventReasoningDelta,
		llm.EventReasoningEnd,
		llm.EventToolInputStart,
		llm.EventToolInputDelta,
		llm.EventToolInputEnd,
		llm.EventToolCall,
		llm.EventToolResult,
		llm.EventToolError,
		llm.EventStepFinish,
		llm.EventFinish,
		llm.EventProviderError:
		return true
	default:
		return false
	}
}

func validateToolResultValue(value domain.JSONValue) error {
	if value.Kind != domain.JSONKindObject {
		return errors.New("tool result value must be an object")
	}
	if err := onlyFields(value.Object, "type", "value"); err != nil {
		return err
	}
	typeName, err := requiredEventString(value.Object, "type")
	if err != nil {
		return err
	}
	if _, present := value.Object["value"]; !present {
		return errors.New("tool result value is required")
	}
	switch typeName {
	case "json", "text", "error":
		return nil
	case "content":
		if value.Object["value"].Kind != domain.JSONKindArray {
			return errors.New("tool content result value must be an array")
		}
		for _, content := range value.Object["value"].Array {
			if err := validateToolContent(content); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("invalid tool result type %q", typeName)
	}
}

func validateToolOutput(value domain.JSONValue) error {
	if value.Kind != domain.JSONKindObject {
		return errors.New("tool output must be an object")
	}
	if err := onlyFields(value.Object, "structured", "content"); err != nil {
		return err
	}
	if _, present := value.Object["structured"]; !present {
		return errors.New("tool output structured value is required")
	}
	content, present := value.Object["content"]
	if !present || content.Kind != domain.JSONKindArray {
		return errors.New("tool output content must be an array")
	}
	for _, item := range content.Array {
		if err := validateToolContent(item); err != nil {
			return err
		}
	}
	return nil
}

func validateToolContent(value domain.JSONValue) error {
	if value.Kind != domain.JSONKindObject {
		return errors.New("tool content must be an object")
	}
	typeName, err := requiredEventString(value.Object, "type")
	if err != nil {
		return err
	}
	switch typeName {
	case "text":
		if err := onlyFields(value.Object, "type", "text"); err != nil {
			return err
		}
		_, err = requiredEventString(value.Object, "text")
		return err
	case "file":
		if err := onlyFields(value.Object, "type", "uri", "mime", "name"); err != nil {
			return err
		}
		if _, err := requiredEventString(value.Object, "uri"); err != nil {
			return err
		}
		if _, err := requiredEventString(value.Object, "mime"); err != nil {
			return err
		}
		if name, present := value.Object["name"]; present && name.Kind != domain.JSONKindString {
			return errors.New("tool file content name must be a string when present")
		}
		return nil
	default:
		return fmt.Errorf("invalid tool content type %q", typeName)
	}
}

func onlyFields(object map[string]domain.JSONValue, fields ...string) error {
	allowed := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		allowed[field] = struct{}{}
	}
	for field := range object {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("unknown field %q", field)
		}
	}
	return nil
}

func jsonNumber(value float64) domain.JSONValue {
	return domain.JSONNumber(strconv.FormatFloat(normalizeNumber(value), 'g', -1, 64))
}
