package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Hz-186/opencode-go-py/internal/codec"
	"github.com/Hz-186/opencode-go-py/internal/domain"
)

func parseJSONC(content []byte) (domain.JSONValue, error) {
	if !utf8.Valid(content) {
		return domain.JSONValue{}, errors.New("config is not valid UTF-8")
	}
	stripped, err := stripComments(content)
	if err != nil {
		return domain.JSONValue{}, err
	}
	stripped = stripTrailingCommas(stripped)
	value, err := codec.DecodeJSONValue(stripped)
	if err != nil {
		return domain.JSONValue{}, err
	}
	if value.Kind != domain.JSONKindObject {
		return domain.JSONValue{}, errors.New("config root must be an object")
	}
	return value, nil
}

func stripComments(content []byte) ([]byte, error) {
	result := append([]byte(nil), content...)
	const (
		normal = iota
		inString
		lineComment
		blockComment
	)
	state := normal
	escaped := false
	for i := 0; i < len(result); i++ {
		switch state {
		case normal:
			if result[i] == '"' {
				state = inString
				continue
			}
			if result[i] != '/' || i+1 >= len(result) {
				continue
			}
			if result[i+1] == '/' {
				result[i], result[i+1] = ' ', ' '
				i++
				state = lineComment
			} else if result[i+1] == '*' {
				result[i], result[i+1] = ' ', ' '
				i++
				state = blockComment
			}
		case inString:
			if escaped {
				escaped = false
			} else if result[i] == '\\' {
				escaped = true
			} else if result[i] == '"' {
				state = normal
			}
		case lineComment:
			if result[i] == '\n' {
				state = normal
			} else {
				result[i] = ' '
			}
		case blockComment:
			if result[i] == '*' && i+1 < len(result) && result[i+1] == '/' {
				result[i], result[i+1] = ' ', ' '
				i++
				state = normal
			} else if result[i] != '\n' {
				result[i] = ' '
			}
		}
	}
	if state == blockComment {
		return nil, errors.New("unterminated block comment")
	}
	return result, nil
}

func stripTrailingCommas(content []byte) []byte {
	result := append([]byte(nil), content...)
	inString := false
	escaped := false
	for i := 0; i < len(result); i++ {
		if inString {
			if escaped {
				escaped = false
			} else if result[i] == '\\' {
				escaped = true
			} else if result[i] == '"' {
				inString = false
			}
			continue
		}
		if result[i] == '"' {
			inString = true
			continue
		}
		if result[i] != ',' {
			continue
		}
		next := i + 1
		for next < len(result) &&
			(result[next] == ' ' || result[next] == '\t' || result[next] == '\r' || result[next] == '\n') {
			next++
		}
		if next < len(result) && (result[next] == '}' || result[next] == ']') {
			result[i] = ' '
		}
	}
	return result
}

type fieldError struct {
	field string
	cause error
}

func (e *fieldError) Error() string {
	return e.field + ": " + e.cause.Error()
}

var allowedTopLevel = map[string]struct{}{
	"$schema": {}, "shell": {}, "logLevel": {}, "server": {}, "command": {},
	"skills": {}, "references": {}, "reference": {}, "watcher": {}, "snapshot": {},
	"plugin": {}, "share": {}, "autoshare": {}, "autoupdate": {},
	"disabled_providers": {}, "enabled_providers": {}, "model": {}, "small_model": {},
	"default_agent": {}, "subagent_depth": {}, "username": {}, "mode": {}, "agent": {},
	"provider": {}, "mcp": {}, "formatter": {}, "lsp": {}, "instructions": {},
	"layout": {}, "permission": {}, "tools": {}, "attachment": {}, "enterprise": {},
	"tool_output": {}, "compaction": {}, "experimental": {},
	"theme": {}, "keybinds": {}, "tui": {},
}

func validateTopLevel(value domain.JSONValue) error {
	for key, item := range value.Object {
		if _, ok := allowedTopLevel[key]; !ok {
			return &fieldError{field: pointer("", key), cause: errors.New("unrecognized config key")}
		}
		switch key {
		case "$schema", "shell", "model", "small_model", "default_agent", "username":
			if item.Kind != domain.JSONKindString {
				return typeFieldError(key, "string")
			}
		case "logLevel":
			if item.Kind != domain.JSONKindString ||
				(item.String != "DEBUG" && item.String != "INFO" && item.String != "WARN" && item.String != "ERROR") {
				return &fieldError{field: pointer("", key), cause: errors.New("must be DEBUG, INFO, WARN, or ERROR")}
			}
		case "snapshot", "autoshare":
			if item.Kind != domain.JSONKindBool {
				return typeFieldError(key, "boolean")
			}
		case "share":
			if item.Kind != domain.JSONKindString ||
				(item.String != "manual" && item.String != "auto" && item.String != "disabled") {
				return &fieldError{field: pointer("", key), cause: errors.New("must be manual, auto, or disabled")}
			}
		case "layout":
			if item.Kind != domain.JSONKindString || (item.String != "auto" && item.String != "stretch") {
				return &fieldError{field: pointer("", key), cause: errors.New("must be auto or stretch")}
			}
		case "subagent_depth":
			if !nonNegativeInteger(item) {
				return &fieldError{field: pointer("", key), cause: errors.New("must be a non-negative integer")}
			}
		case "disabled_providers", "enabled_providers", "instructions":
			if !stringArray(item) {
				return typeFieldError(key, "array of strings")
			}
		case "plugin":
			if item.Kind != domain.JSONKindArray {
				return typeFieldError(key, "array")
			}
		case "server", "command", "skills", "references", "reference", "watcher", "mode", "agent",
			"provider", "mcp", "permission", "tools", "attachment", "enterprise", "tool_output",
			"compaction", "experimental", "theme", "keybinds", "tui":
			if item.Kind != domain.JSONKindObject {
				return typeFieldError(key, "object")
			}
		case "formatter", "lsp":
			if item.Kind != domain.JSONKindObject && item.Kind != domain.JSONKindBool {
				return typeFieldError(key, "object or boolean")
			}
		case "autoupdate":
			if item.Kind != domain.JSONKindBool &&
				!(item.Kind == domain.JSONKindString && item.String == "notify") {
				return &fieldError{field: pointer("", key), cause: errors.New("must be a boolean or notify")}
			}
		}
	}
	return nil
}

func typeFieldError(key, expected string) error {
	return &fieldError{field: pointer("", key), cause: fmt.Errorf("must be %s", expected)}
}

func nonNegativeInteger(value domain.JSONValue) bool {
	if value.Kind != domain.JSONKindNumber || strings.ContainsAny(value.Number, ".eE") ||
		strings.HasPrefix(value.Number, "-") {
		return false
	}
	_, err := strconv.ParseUint(value.Number, 10, 64)
	return err == nil
}

func stringArray(value domain.JSONValue) bool {
	if value.Kind != domain.JSONKindArray {
		return false
	}
	for _, item := range value.Array {
		if item.Kind != domain.JSONKindString {
			return false
		}
	}
	return true
}

func mergeValues(
	path string,
	target domain.JSONValue,
	source domain.JSONValue,
	ref SourceRef,
	origins map[string]SourceRef,
) domain.JSONValue {
	if target.Kind == domain.JSONKindObject && source.Kind == domain.JSONKindObject {
		result := cloneValue(target)
		if result.Object == nil {
			result.Object = make(map[string]domain.JSONValue)
		}
		for key, sourceValue := range source.Object {
			childPath := pointer(path, key)
			targetValue, exists := result.Object[key]
			if childPath == "/instructions" && exists &&
				targetValue.Kind == domain.JSONKindArray && sourceValue.Kind == domain.JSONKindArray {
				result.Object[key] = mergeInstructions(targetValue, sourceValue)
				clearOrigins(origins, childPath)
				origins[childPath] = ref
				continue
			}
			if exists && targetValue.Kind == domain.JSONKindObject && sourceValue.Kind == domain.JSONKindObject {
				result.Object[key] = mergeValues(childPath, targetValue, sourceValue, ref, origins)
				continue
			}
			result.Object[key] = cloneValue(sourceValue)
			clearOrigins(origins, childPath)
			setOrigins(origins, childPath, sourceValue, ref)
		}
		return result
	}
	clearOrigins(origins, path)
	setOrigins(origins, path, source, ref)
	return cloneValue(source)
}

func mergeInstructions(target, source domain.JSONValue) domain.JSONValue {
	result := make([]domain.JSONValue, 0, len(target.Array)+len(source.Array))
	seen := make(map[string]struct{}, len(target.Array)+len(source.Array))
	for _, list := range [][]domain.JSONValue{target.Array, source.Array} {
		for _, item := range list {
			if _, ok := seen[item.String]; ok {
				continue
			}
			seen[item.String] = struct{}{}
			result = append(result, cloneValue(item))
		}
	}
	return domain.JSONArray(result)
}

func setOrigins(origins map[string]SourceRef, path string, value domain.JSONValue, ref SourceRef) {
	if value.Kind != domain.JSONKindObject || len(value.Object) == 0 {
		origins[path] = ref
		return
	}
	for key, item := range value.Object {
		setOrigins(origins, pointer(path, key), item, ref)
	}
}

func clearOrigins(origins map[string]SourceRef, path string) {
	for key := range origins {
		if key == path || strings.HasPrefix(key, path+"/") {
			delete(origins, key)
		}
	}
}

func pointer(parent, key string) string {
	key = strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
	return parent + "/" + key
}

func cloneResolved(value ResolvedConfig) ResolvedConfig {
	result := ResolvedConfig{
		Value: cloneValue(value.Value), Sources: append([]SourceRef(nil), value.Sources...),
		Origins: make(map[string]SourceRef, len(value.Origins)), Generation: value.Generation,
	}
	for path, ref := range value.Origins {
		result.Origins[path] = ref
	}
	return result
}

func cloneValue(value domain.JSONValue) domain.JSONValue {
	result := value
	if value.Array != nil {
		result.Array = make([]domain.JSONValue, len(value.Array))
		for i := range value.Array {
			result.Array[i] = cloneValue(value.Array[i])
		}
	}
	if value.Object != nil {
		result.Object = make(map[string]domain.JSONValue, len(value.Object))
		for key, item := range value.Object {
			result.Object[key] = cloneValue(item)
		}
	}
	return result
}
