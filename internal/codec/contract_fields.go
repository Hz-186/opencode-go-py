package codec

import (
	"fmt"
	"math"
	"strconv"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

func decodeContractObject(content []byte, label string) (map[string]domain.JSONValue, error) {
	value, err := DecodeJSONValue(content)
	if err != nil {
		return nil, err
	}
	if value.Kind != domain.JSONKindObject {
		return nil, fmt.Errorf("%s must be a JSON object", label)
	}
	return value.Object, nil
}

func rejectUnknownContractFields(object map[string]domain.JSONValue, label string, fields ...string) error {
	allowed := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		allowed[field] = struct{}{}
	}
	for field := range object {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("unknown %s field %q", label, field)
		}
	}
	return nil
}

func requiredContractString(object map[string]domain.JSONValue, field string, label string) (string, error) {
	value, present := object[field]
	if !present || value.Kind != domain.JSONKindString {
		return "", fmt.Errorf("%s %s must be a string", label, field)
	}
	return value.String, nil
}

func requiredContractArray(object map[string]domain.JSONValue, field string, label string) ([]domain.JSONValue, error) {
	value, present := object[field]
	if !present || value.Kind != domain.JSONKindArray {
		return nil, fmt.Errorf("%s %s must be an array", label, field)
	}
	return value.Array, nil
}

func requiredContractObject(object map[string]domain.JSONValue, field string, label string) (map[string]domain.JSONValue, error) {
	value, present := object[field]
	if !present || value.Kind != domain.JSONKindObject {
		return nil, fmt.Errorf("%s %s must be an object", label, field)
	}
	return value.Object, nil
}

func optionalContractBool(object map[string]domain.JSONValue, field string, label string) (*bool, error) {
	value, present := object[field]
	if !present {
		return nil, nil
	}
	if value.Kind != domain.JSONKindBool {
		return nil, fmt.Errorf("%s %s must be a boolean when present", label, field)
	}
	result := value.Bool
	return &result, nil
}

func requiredContractBool(object map[string]domain.JSONValue, field string, label string) (bool, error) {
	value, present := object[field]
	if !present || value.Kind != domain.JSONKindBool {
		return false, fmt.Errorf("%s %s must be a boolean", label, field)
	}
	return value.Bool, nil
}

func optionalContractString(object map[string]domain.JSONValue, field string, label string) (*string, error) {
	value, present := object[field]
	if !present {
		return nil, nil
	}
	if value.Kind != domain.JSONKindString {
		return nil, fmt.Errorf("%s %s must be a string when present", label, field)
	}
	result := value.String
	return &result, nil
}

func requiredContractNumber(object map[string]domain.JSONValue, field string, label string) (float64, error) {
	value, present := object[field]
	if !present || value.Kind != domain.JSONKindNumber {
		return 0, fmt.Errorf("%s %s must be a number", label, field)
	}
	parsed, err := strconv.ParseFloat(value.Number, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, fmt.Errorf("%s %s must be finite", label, field)
	}
	return normalizeNumber(parsed), nil
}

func optionalContractNumber(object map[string]domain.JSONValue, field string, label string) (*float64, error) {
	if _, present := object[field]; !present {
		return nil, nil
	}
	value, err := requiredContractNumber(object, field, label)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func optionalContractObject(object map[string]domain.JSONValue, field string, label string) (map[string]domain.JSONValue, bool, error) {
	value, present := object[field]
	if !present {
		return nil, false, nil
	}
	if value.Kind != domain.JSONKindObject {
		return nil, false, fmt.Errorf("%s %s must be an object when present", label, field)
	}
	return value.Object, true, nil
}

func decodeContractStringArray(value domain.JSONValue, label string) ([]string, error) {
	if value.Kind != domain.JSONKindArray {
		return nil, fmt.Errorf("%s must be an array", label)
	}
	result := make([]string, len(value.Array))
	for index, item := range value.Array {
		if item.Kind != domain.JSONKindString {
			return nil, fmt.Errorf("%s item %d must be a string", label, index)
		}
		result[index] = item.String
	}
	return result, nil
}

func optionalContractStringArray(object map[string]domain.JSONValue, field string, label string) ([]string, error) {
	value, present := object[field]
	if !present {
		return nil, nil
	}
	return decodeContractStringArray(value, label+" "+field)
}

func contractStringArray(values []string) domain.JSONValue {
	items := make([]domain.JSONValue, len(values))
	for index, value := range values {
		items[index] = domain.JSONString(value)
	}
	return domain.JSONArray(items)
}

func decodeContractStringMap(value domain.JSONValue, label string) (map[string]string, error) {
	if value.Kind != domain.JSONKindObject {
		return nil, fmt.Errorf("%s must be an object", label)
	}
	result := make(map[string]string, len(value.Object))
	for key, item := range value.Object {
		if item.Kind != domain.JSONKindString {
			return nil, fmt.Errorf("%s field %q must be a string", label, key)
		}
		result[key] = item.String
	}
	return result, nil
}

func contractStringMap(values map[string]string) domain.JSONValue {
	object := make(map[string]domain.JSONValue, len(values))
	for key, value := range values {
		object[key] = domain.JSONString(value)
	}
	return domain.JSONObject(object)
}

func contractOptionalBool(object map[string]domain.JSONValue, field string, value *bool) {
	if value != nil {
		object[field] = domain.JSONBool(*value)
	}
}

func validateContractEnum(value string, label string, allowed ...string) error {
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return fmt.Errorf("invalid %s %q", label, value)
}

func requireContractValue(object map[string]domain.JSONValue, field string, label string) (domain.JSONValue, error) {
	value, present := object[field]
	if !present {
		return domain.JSONValue{}, fmt.Errorf("%s %s is required", label, field)
	}
	return value, nil
}
