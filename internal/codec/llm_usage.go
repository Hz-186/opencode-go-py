package codec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"unicode/utf8"

	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

var usageFields = map[string]struct{}{
	"inputTokens": {}, "outputTokens": {}, "nonCachedInputTokens": {},
	"cacheReadInputTokens": {}, "cacheWriteInputTokens": {}, "reasoningTokens": {},
	"totalTokens": {}, "providerMetadata": {},
}

func DecodeUsageJSON(content []byte) (llm.Usage, error) {
	if !utf8.Valid(content) {
		return llm.Usage{}, errors.New("usage JSON is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil {
		return llm.Usage{}, fmt.Errorf("decode usage JSON: %w", err)
	}
	if object == nil {
		return llm.Usage{}, errors.New("usage JSON must be an object")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return llm.Usage{}, err
	}
	for field := range object {
		if _, known := usageFields[field]; !known {
			return llm.Usage{}, fmt.Errorf("unknown usage field %q", field)
		}
	}
	usage := llm.Usage{}
	var err error
	if usage.InputTokens, err = decodeOptionalNumber(object, "inputTokens"); err != nil {
		return llm.Usage{}, err
	}
	if usage.OutputTokens, err = decodeOptionalNumber(object, "outputTokens"); err != nil {
		return llm.Usage{}, err
	}
	if usage.NonCachedInputTokens, err = decodeOptionalNumber(object, "nonCachedInputTokens"); err != nil {
		return llm.Usage{}, err
	}
	if usage.CacheReadInputTokens, err = decodeOptionalNumber(object, "cacheReadInputTokens"); err != nil {
		return llm.Usage{}, err
	}
	if usage.CacheWriteInputTokens, err = decodeOptionalNumber(object, "cacheWriteInputTokens"); err != nil {
		return llm.Usage{}, err
	}
	if usage.ReasoningTokens, err = decodeOptionalNumber(object, "reasoningTokens"); err != nil {
		return llm.Usage{}, err
	}
	if usage.TotalTokens, err = decodeOptionalNumber(object, "totalTokens"); err != nil {
		return llm.Usage{}, err
	}
	if raw, present := object["providerMetadata"]; present {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return llm.Usage{}, errors.New("usage providerMetadata must not be null")
		}
		usage.ProviderMetadata, err = decodeProviderMetadata(raw)
		if err != nil {
			return llm.Usage{}, err
		}
	}
	if err := usage.Validate(); err != nil {
		return llm.Usage{}, err
	}
	return usage, nil
}

func EncodeUsageJSON(usage llm.Usage) ([]byte, error) {
	if err := usage.Validate(); err != nil {
		return nil, err
	}
	type usageWire struct {
		InputTokens           *float64         `json:"inputTokens,omitempty"`
		OutputTokens          *float64         `json:"outputTokens,omitempty"`
		NonCachedInputTokens  *float64         `json:"nonCachedInputTokens,omitempty"`
		CacheReadInputTokens  *float64         `json:"cacheReadInputTokens,omitempty"`
		CacheWriteInputTokens *float64         `json:"cacheWriteInputTokens,omitempty"`
		ReasoningTokens       *float64         `json:"reasoningTokens,omitempty"`
		TotalTokens           *float64         `json:"totalTokens,omitempty"`
		ProviderMetadata      *json.RawMessage `json:"providerMetadata,omitempty"`
	}
	providerMetadata, err := encodeProviderMetadata(usage.ProviderMetadata)
	if err != nil {
		return nil, err
	}
	wire := usageWire{
		InputTokens: normalizeZero(usage.InputTokens), OutputTokens: normalizeZero(usage.OutputTokens),
		NonCachedInputTokens:  normalizeZero(usage.NonCachedInputTokens),
		CacheReadInputTokens:  normalizeZero(usage.CacheReadInputTokens),
		CacheWriteInputTokens: normalizeZero(usage.CacheWriteInputTokens),
		ReasoningTokens:       normalizeZero(usage.ReasoningTokens), TotalTokens: normalizeZero(usage.TotalTokens),
		ProviderMetadata: providerMetadata,
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encode usage JSON: %w", err)
	}
	return append(encoded, '\n'), nil
}

func encodeProviderMetadata(value llm.ProviderMetadata) (*json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	object := make(map[string]domain.JSONValue, len(value))
	for provider, metadata := range value {
		object[provider] = domain.JSONObject(metadata)
	}
	encoded, err := EncodeJSONValue(domain.JSONObject(object))
	if err != nil {
		return nil, fmt.Errorf("encode provider metadata: %w", err)
	}
	raw := json.RawMessage(bytes.TrimSuffix(encoded, []byte{'\n'}))
	return &raw, nil
}

func decodeProviderMetadata(content []byte) (llm.ProviderMetadata, error) {
	value, err := DecodeJSONValue(content)
	if err != nil {
		return nil, fmt.Errorf("decode provider metadata: %w", err)
	}
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

func decodeOptionalNumber(object map[string]json.RawMessage, field string) (*float64, error) {
	raw, present := object[field]
	if !present {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("usage %s must not be null", field)
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return nil, fmt.Errorf("usage %s must be a number", field)
	}
	value, err := number.Float64()
	if err != nil {
		return nil, fmt.Errorf("usage %s must be finite: %w", field, err)
	}
	if value == 0 {
		value = 0
	}
	return &value, nil
}

func normalizeZero(value *float64) *float64 {
	if value == nil || *value != 0 || !math.Signbit(*value) {
		return value
	}
	normalized := float64(0)
	return &normalized
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	return errors.New("multiple JSON values are not allowed")
}
