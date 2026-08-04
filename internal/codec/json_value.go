package codec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

const maxJSONValueDepth = 100

func DecodeJSONValue(content []byte) (domain.JSONValue, error) {
	if !utf8.Valid(content) {
		return domain.JSONValue{}, errors.New("JSON value is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	value, err := decodeJSONToken(decoder, 0)
	if err != nil {
		return domain.JSONValue{}, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return domain.JSONValue{}, errors.New("multiple JSON values are not allowed")
		}
		return domain.JSONValue{}, fmt.Errorf("decode trailing JSON token: %w", err)
	}
	if err := value.Validate(); err != nil {
		return domain.JSONValue{}, err
	}
	return value, nil
}

func EncodeJSONValue(value domain.JSONValue) ([]byte, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := encodeJSONValue(&output, value, 0); err != nil {
		return nil, err
	}
	output.WriteByte('\n')
	return output.Bytes(), nil
}

func decodeJSONToken(decoder *json.Decoder, depth int) (domain.JSONValue, error) {
	if depth > maxJSONValueDepth {
		return domain.JSONValue{}, fmt.Errorf("JSON value exceeds maximum depth %d", maxJSONValueDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return domain.JSONValue{}, fmt.Errorf("decode JSON token: %w", err)
	}
	switch token := token.(type) {
	case nil:
		return domain.JSONNull(), nil
	case bool:
		return domain.JSONBool(token), nil
	case string:
		return domain.JSONString(token), nil
	case json.Number:
		return domain.JSONNumber(normalizeJSONNumber(token.String())), nil
	case json.Delim:
		switch token {
		case '[':
			items := make([]domain.JSONValue, 0)
			for decoder.More() {
				item, err := decodeJSONToken(decoder, depth+1)
				if err != nil {
					return domain.JSONValue{}, err
				}
				items = append(items, item)
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return domain.JSONValue{}, errors.New("JSON array is not closed")
			}
			return domain.JSONArray(items), nil
		case '{':
			object := make(map[string]domain.JSONValue)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return domain.JSONValue{}, fmt.Errorf("decode JSON object key: %w", err)
				}
				key, ok := keyToken.(string)
				if !ok {
					return domain.JSONValue{}, errors.New("JSON object key is not a string")
				}
				if _, duplicate := object[key]; duplicate {
					return domain.JSONValue{}, fmt.Errorf("duplicate JSON object key %q", key)
				}
				item, err := decodeJSONToken(decoder, depth+1)
				if err != nil {
					return domain.JSONValue{}, err
				}
				object[key] = item
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return domain.JSONValue{}, errors.New("JSON object is not closed")
			}
			return domain.JSONObject(object), nil
		default:
			return domain.JSONValue{}, fmt.Errorf("unexpected JSON delimiter %q", token)
		}
	default:
		return domain.JSONValue{}, fmt.Errorf("unsupported JSON token %T", token)
	}
}

func encodeJSONValue(output *bytes.Buffer, value domain.JSONValue, depth int) error {
	if depth > maxJSONValueDepth {
		return fmt.Errorf("JSON value exceeds maximum depth %d", maxJSONValueDepth)
	}
	switch value.Kind {
	case domain.JSONKindNull:
		output.WriteString("null")
	case domain.JSONKindBool:
		if value.Bool {
			output.WriteString("true")
		} else {
			output.WriteString("false")
		}
	case domain.JSONKindNumber:
		output.WriteString(normalizeJSONNumber(value.Number))
	case domain.JSONKindString:
		encoded, err := encodeJSONString(value.String)
		if err != nil {
			return err
		}
		output.Write(encoded)
	case domain.JSONKindArray:
		output.WriteByte('[')
		for index, item := range value.Array {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := encodeJSONValue(output, item, depth+1); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case domain.JSONKindObject:
		output.WriteByte('{')
		keys := make([]string, 0, len(value.Object))
		for key := range value.Object {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for index, key := range keys {
			if index > 0 {
				output.WriteByte(',')
			}
			encoded, err := encodeJSONString(key)
			if err != nil {
				return err
			}
			output.Write(encoded)
			output.WriteByte(':')
			if err := encodeJSONValue(output, value.Object[key], depth+1); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return errors.New("JSON value kind is invalid")
	}
	return nil
}

func encodeJSONString(value string) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("encode JSON string: %w", err)
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

func normalizeJSONNumber(value string) string {
	if !strings.HasPrefix(value, "-") {
		return value
	}
	mantissa := value[1:]
	if exponent := strings.IndexAny(mantissa, "eE"); exponent >= 0 {
		mantissa = mantissa[:exponent]
	}
	mantissa = strings.ReplaceAll(mantissa, ".", "")
	if mantissa != "" && strings.Trim(mantissa, "0") == "" {
		return "0"
	}
	return value
}
