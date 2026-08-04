package codec

import (
	"errors"
	"fmt"

	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

func DecodeLLMMessageJSON(content []byte) (llm.Message, error) {
	object, err := decodeContractObject(content, "LLM message")
	if err != nil {
		return llm.Message{}, err
	}
	if err := rejectUnknownContractFields(object, "LLM message", "id", "role", "content", "metadata", "native"); err != nil {
		return llm.Message{}, err
	}
	id, err := optionalContractString(object, "id", "LLM message")
	if err != nil {
		return llm.Message{}, err
	}
	roleValue, err := requiredContractString(object, "role", "LLM message")
	if err != nil {
		return llm.Message{}, err
	}
	role := llm.MessageRole(roleValue)
	if err := validateMessageRole(role); err != nil {
		return llm.Message{}, err
	}
	contentValues, err := requiredContractArray(object, "content", "LLM message")
	if err != nil {
		return llm.Message{}, err
	}
	contents := make([]llm.Content, len(contentValues))
	for index, value := range contentValues {
		contents[index], err = decodeLLMContent(value, index)
		if err != nil {
			return llm.Message{}, err
		}
	}
	metadata, _, err := optionalContractObject(object, "metadata", "LLM message")
	if err != nil {
		return llm.Message{}, err
	}
	native, _, err := optionalContractObject(object, "native", "LLM message")
	if err != nil {
		return llm.Message{}, err
	}
	return llm.Message{ID: id, Role: role, Content: contents, Metadata: metadata, Native: native}, nil
}

func EncodeLLMMessageJSON(message llm.Message) ([]byte, error) {
	if err := validateMessageRole(message.Role); err != nil {
		return nil, err
	}
	contents := make([]domain.JSONValue, len(message.Content))
	for index, content := range message.Content {
		value, err := encodeLLMContent(content)
		if err != nil {
			return nil, err
		}
		contents[index] = value
	}
	object := map[string]domain.JSONValue{
		"role": domain.JSONString(string(message.Role)), "content": domain.JSONArray(contents),
	}
	addOptionalContractString(object, "id", message.ID)
	if message.Metadata != nil {
		object["metadata"] = domain.JSONObject(message.Metadata)
	}
	if message.Native != nil {
		object["native"] = domain.JSONObject(message.Native)
	}
	encoded, err := EncodeJSONValue(domain.JSONObject(object))
	if err != nil {
		return nil, err
	}
	if _, err := DecodeLLMMessageJSON(encoded); err != nil {
		return nil, fmt.Errorf("validate encoded LLM message: %w", err)
	}
	return encoded, nil
}

func validateMessageRole(role llm.MessageRole) error {
	return validateContractEnum(string(role), "LLM message role",
		string(llm.MessageRoleSystem), string(llm.MessageRoleUser), string(llm.MessageRoleAssistant), string(llm.MessageRoleTool))
}

func decodeLLMContent(value domain.JSONValue, index int) (llm.Content, error) {
	if value.Kind != domain.JSONKindObject {
		return nil, fmt.Errorf("LLM content %d must be an object", index)
	}
	object := value.Object
	typeName, err := requiredContractString(object, "type", "LLM content")
	if err != nil {
		return nil, err
	}
	switch llm.ContentType(typeName) {
	case llm.ContentText:
		return decodeLLMTextContent(object)
	case llm.ContentMedia:
		return decodeLLMMediaContent(object)
	case llm.ContentToolCall:
		return decodeLLMToolCallContent(object)
	case llm.ContentToolResult:
		return decodeLLMToolResultContent(object)
	case llm.ContentReasoning:
		return decodeLLMReasoningContent(object)
	default:
		return nil, fmt.Errorf("unknown LLM content type %q", typeName)
	}
}

func decodeLLMTextContent(object map[string]domain.JSONValue) (llm.Content, error) {
	if err := rejectUnknownContractFields(object, "LLM text content", "type", "text", "cache", "metadata", "providerMetadata"); err != nil {
		return nil, err
	}
	text, err := requiredContractString(object, "text", "LLM text content")
	if err != nil {
		return nil, err
	}
	cache, err := decodeCacheHint(object, "LLM text content")
	if err != nil {
		return nil, err
	}
	metadata, _, err := optionalContractObject(object, "metadata", "LLM text content")
	if err != nil {
		return nil, err
	}
	providerMetadata, err := decodeOptionalProviderMetadataField(object, "providerMetadata", "LLM text content")
	if err != nil {
		return nil, err
	}
	return llm.TextContent{Text: text, Cache: cache, Metadata: metadata, ProviderMetadata: providerMetadata}, nil
}

func decodeLLMMediaContent(object map[string]domain.JSONValue) (llm.Content, error) {
	if err := rejectUnknownContractFields(object, "LLM media content", "type", "mediaType", "data", "filename", "metadata"); err != nil {
		return nil, err
	}
	mediaType, err := requiredContractString(object, "mediaType", "LLM media content")
	if err != nil {
		return nil, err
	}
	data, err := requiredContractString(object, "data", "LLM media content")
	if err != nil {
		return nil, errors.New("LLM media content data must be a string in canonical JSON")
	}
	filename, err := optionalContractString(object, "filename", "LLM media content")
	if err != nil {
		return nil, err
	}
	metadata, _, err := optionalContractObject(object, "metadata", "LLM media content")
	if err != nil {
		return nil, err
	}
	return llm.MediaContent{MediaType: mediaType, Data: data, Filename: filename, Metadata: metadata}, nil
}

func decodeLLMToolCallContent(object map[string]domain.JSONValue) (llm.Content, error) {
	if err := rejectUnknownContractFields(object, "LLM tool-call content", "type", "id", "name", "input", "providerExecuted", "metadata", "providerMetadata"); err != nil {
		return nil, err
	}
	id, err := requiredContractString(object, "id", "LLM tool-call content")
	if err != nil {
		return nil, err
	}
	name, err := requiredContractString(object, "name", "LLM tool-call content")
	if err != nil {
		return nil, err
	}
	input, err := requireContractValue(object, "input", "LLM tool-call content")
	if err != nil {
		return nil, err
	}
	providerExecuted, err := optionalContractBool(object, "providerExecuted", "LLM tool-call content")
	if err != nil {
		return nil, err
	}
	metadata, _, err := optionalContractObject(object, "metadata", "LLM tool-call content")
	if err != nil {
		return nil, err
	}
	providerMetadata, err := decodeOptionalProviderMetadataField(object, "providerMetadata", "LLM tool-call content")
	if err != nil {
		return nil, err
	}
	return llm.ToolCallContent{ID: id, Name: name, Input: input, ProviderExecuted: providerExecuted, Metadata: metadata, ProviderMetadata: providerMetadata}, nil
}

func decodeLLMToolResultContent(object map[string]domain.JSONValue) (llm.Content, error) {
	if err := rejectUnknownContractFields(object, "LLM tool-result content", "type", "id", "name", "result", "providerExecuted", "cache", "metadata", "providerMetadata"); err != nil {
		return nil, err
	}
	id, err := requiredContractString(object, "id", "LLM tool-result content")
	if err != nil {
		return nil, err
	}
	name, err := requiredContractString(object, "name", "LLM tool-result content")
	if err != nil {
		return nil, err
	}
	result, err := requireContractValue(object, "result", "LLM tool-result content")
	if err != nil {
		return nil, err
	}
	if err := validateToolResultValue(result); err != nil {
		return nil, err
	}
	providerExecuted, err := optionalContractBool(object, "providerExecuted", "LLM tool-result content")
	if err != nil {
		return nil, err
	}
	cache, err := decodeCacheHint(object, "LLM tool-result content")
	if err != nil {
		return nil, err
	}
	metadata, _, err := optionalContractObject(object, "metadata", "LLM tool-result content")
	if err != nil {
		return nil, err
	}
	providerMetadata, err := decodeOptionalProviderMetadataField(object, "providerMetadata", "LLM tool-result content")
	if err != nil {
		return nil, err
	}
	return llm.ToolResultContent{ID: id, Name: name, Result: result, ProviderExecuted: providerExecuted, Cache: cache, Metadata: metadata, ProviderMetadata: providerMetadata}, nil
}

func decodeLLMReasoningContent(object map[string]domain.JSONValue) (llm.Content, error) {
	if err := rejectUnknownContractFields(object, "LLM reasoning content", "type", "text", "encrypted", "metadata", "providerMetadata"); err != nil {
		return nil, err
	}
	text, err := requiredContractString(object, "text", "LLM reasoning content")
	if err != nil {
		return nil, err
	}
	encrypted, err := optionalContractString(object, "encrypted", "LLM reasoning content")
	if err != nil {
		return nil, err
	}
	metadata, _, err := optionalContractObject(object, "metadata", "LLM reasoning content")
	if err != nil {
		return nil, err
	}
	providerMetadata, err := decodeOptionalProviderMetadataField(object, "providerMetadata", "LLM reasoning content")
	if err != nil {
		return nil, err
	}
	return llm.ReasoningContent{Text: text, Encrypted: encrypted, Metadata: metadata, ProviderMetadata: providerMetadata}, nil
}

func encodeLLMContent(content llm.Content) (domain.JSONValue, error) {
	if content == nil {
		return domain.JSONValue{}, errors.New("LLM content is nil")
	}
	object := make(map[string]domain.JSONValue)
	var metadata map[string]domain.JSONValue
	var providerMetadata llm.ProviderMetadata
	switch content := content.(type) {
	case llm.TextContent:
		object["type"] = domain.JSONString(string(llm.ContentText))
		object["text"] = domain.JSONString(content.Text)
		if content.Cache != nil {
			object["cache"] = encodeCacheHint(*content.Cache)
		}
		metadata, providerMetadata = content.Metadata, content.ProviderMetadata
	case llm.MediaContent:
		if content.Bytes != nil {
			return domain.JSONValue{}, errors.New("binary LLM media has no canonical JSON representation")
		}
		object["type"] = domain.JSONString(string(llm.ContentMedia))
		object["mediaType"] = domain.JSONString(content.MediaType)
		object["data"] = domain.JSONString(content.Data)
		addOptionalContractString(object, "filename", content.Filename)
		metadata = content.Metadata
	case llm.ToolCallContent:
		object["type"] = domain.JSONString(string(llm.ContentToolCall))
		object["id"] = domain.JSONString(content.ID)
		object["name"] = domain.JSONString(content.Name)
		object["input"] = content.Input
		addOptionalBool(object, "providerExecuted", content.ProviderExecuted)
		metadata, providerMetadata = content.Metadata, content.ProviderMetadata
	case llm.ToolResultContent:
		if err := validateToolResultValue(content.Result); err != nil {
			return domain.JSONValue{}, err
		}
		object["type"] = domain.JSONString(string(llm.ContentToolResult))
		object["id"] = domain.JSONString(content.ID)
		object["name"] = domain.JSONString(content.Name)
		object["result"] = content.Result
		addOptionalBool(object, "providerExecuted", content.ProviderExecuted)
		if content.Cache != nil {
			object["cache"] = encodeCacheHint(*content.Cache)
		}
		metadata, providerMetadata = content.Metadata, content.ProviderMetadata
	case llm.ReasoningContent:
		object["type"] = domain.JSONString(string(llm.ContentReasoning))
		object["text"] = domain.JSONString(content.Text)
		addOptionalContractString(object, "encrypted", content.Encrypted)
		metadata, providerMetadata = content.Metadata, content.ProviderMetadata
	default:
		return domain.JSONValue{}, fmt.Errorf("unsupported LLM content value %T", content)
	}
	if metadata != nil {
		object["metadata"] = domain.JSONObject(metadata)
	}
	if err := addProviderMetadata(object, providerMetadata); err != nil {
		return domain.JSONValue{}, err
	}
	return domain.JSONObject(object), nil
}

func decodeCacheHint(object map[string]domain.JSONValue, label string) (*llm.CacheHint, error) {
	cacheObject, present, err := optionalContractObject(object, "cache", label)
	if err != nil || !present {
		return nil, err
	}
	if err := rejectUnknownContractFields(cacheObject, "cache hint", "type", "ttlSeconds"); err != nil {
		return nil, err
	}
	typeValue, err := requiredContractString(cacheObject, "type", "cache hint")
	if err != nil {
		return nil, err
	}
	typeName := llm.CacheHintType(typeValue)
	if typeName != llm.CacheHintEphemeral && typeName != llm.CacheHintPersistent {
		return nil, fmt.Errorf("invalid cache hint type %q", typeName)
	}
	ttl, err := optionalContractNumber(cacheObject, "ttlSeconds", "cache hint")
	if err != nil {
		return nil, err
	}
	return &llm.CacheHint{Type: typeName, TTLSeconds: ttl}, nil
}

func encodeCacheHint(cache llm.CacheHint) domain.JSONValue {
	object := map[string]domain.JSONValue{"type": domain.JSONString(string(cache.Type))}
	addOptionalContractNumber(object, "ttlSeconds", cache.TTLSeconds)
	return domain.JSONObject(object)
}
