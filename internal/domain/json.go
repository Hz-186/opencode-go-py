package domain

import (
	"errors"
	"fmt"
	"regexp"
)

type JSONKind string

const (
	JSONKindNull   JSONKind = "null"
	JSONKindBool   JSONKind = "bool"
	JSONKindNumber JSONKind = "number"
	JSONKindString JSONKind = "string"
	JSONKindArray  JSONKind = "array"
	JSONKindObject JSONKind = "object"
)

var jsonNumberPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

type JSONValue struct {
	Kind   JSONKind
	Bool   bool
	Number string
	String string
	Array  []JSONValue
	Object map[string]JSONValue
}

func JSONNull() JSONValue {
	return JSONValue{Kind: JSONKindNull}
}

func JSONBool(value bool) JSONValue {
	return JSONValue{Kind: JSONKindBool, Bool: value}
}

func JSONNumber(value string) JSONValue {
	return JSONValue{Kind: JSONKindNumber, Number: value}
}

func JSONString(value string) JSONValue {
	return JSONValue{Kind: JSONKindString, String: value}
}

func JSONArray(value []JSONValue) JSONValue {
	return JSONValue{Kind: JSONKindArray, Array: value}
}

func JSONObject(value map[string]JSONValue) JSONValue {
	return JSONValue{Kind: JSONKindObject, Object: value}
}

func (value JSONValue) Validate() error {
	switch value.Kind {
	case JSONKindNull, JSONKindBool, JSONKindString:
		return nil
	case JSONKindNumber:
		if !jsonNumberPattern.MatchString(value.Number) {
			return fmt.Errorf("invalid JSON number %q", value.Number)
		}
		return nil
	case JSONKindArray:
		for index, item := range value.Array {
			if err := item.Validate(); err != nil {
				return fmt.Errorf("invalid JSON array item %d: %w", index, err)
			}
		}
		return nil
	case JSONKindObject:
		for key, item := range value.Object {
			if err := item.Validate(); err != nil {
				return fmt.Errorf("invalid JSON object field %q: %w", key, err)
			}
		}
		return nil
	default:
		return errors.New("JSON value kind is invalid")
	}
}
