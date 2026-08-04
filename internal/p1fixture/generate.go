package p1fixture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/Hz-186/opencode-go-py/internal/codec"
	"github.com/Hz-186/opencode-go-py/internal/domain"
)

const (
	SchemaVersion = 1
	ParserPolicy  = "p1-canonical-json-v1"
	SourceCommit  = "89130db6b0060a345548d870c51132ee71d6a828"
	ManifestName  = "p1-canonical-fixtures.json"
	ChecksumName  = ManifestName + ".sha256"
)

type Definition struct {
	Contract   string
	Name       string
	SourcePath string
	Input      string
}

func Generate() ([]byte, error) {
	definitions := Catalog()
	sort.Slice(definitions, func(left, right int) bool {
		if definitions[left].Contract != definitions[right].Contract {
			return definitions[left].Contract < definitions[right].Contract
		}
		return definitions[left].Name < definitions[right].Name
	})
	items := make([]domain.JSONValue, len(definitions))
	seen := make(map[string]struct{}, len(definitions))
	for index, definition := range definitions {
		key := definition.Contract + "\x00" + definition.Name
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("duplicate P1 fixture %s/%s", definition.Contract, definition.Name)
		}
		seen[key] = struct{}{}
		canonical, err := canonicalize(definition)
		if err != nil {
			return nil, fmt.Errorf("canonicalize P1 fixture %s/%s: %w", definition.Contract, definition.Name, err)
		}
		value, err := codec.DecodeJSONValue(canonical)
		if err != nil {
			return nil, fmt.Errorf("decode canonical P1 fixture %s/%s: %w", definition.Contract, definition.Name, err)
		}
		if definition.Contract != "sessions-cursor" {
			inputValue, err := codec.DecodeJSONValue([]byte(definition.Input))
			if err != nil {
				return nil, fmt.Errorf("decode source P1 fixture %s/%s: %w", definition.Contract, definition.Name, err)
			}
			if !reflect.DeepEqual(inputValue, value) {
				return nil, fmt.Errorf("P1 fixture %s/%s has field-level round-trip drift", definition.Contract, definition.Name)
			}
		}
		items[index] = domain.JSONObject(map[string]domain.JSONValue{
			"contract": domain.JSONString(definition.Contract), "name": domain.JSONString(definition.Name),
			"sourcePath": domain.JSONString(definition.SourcePath), "value": value,
		})
	}
	manifest := domain.JSONObject(map[string]domain.JSONValue{
		"schemaVersion": domain.JSONNumber("1"), "parserPolicy": domain.JSONString(ParserPolicy),
		"source": domain.JSONObject(map[string]domain.JSONValue{
			"repository": domain.JSONString("opencode"), "commit": domain.JSONString(SourceCommit),
		}),
		"count": domain.JSONNumber(fmt.Sprintf("%d", len(items))), "fixtures": domain.JSONArray(items),
	})
	return codec.EncodeJSONValue(manifest)
}

func Write(outputDirectory string) ([sha256.Size]byte, error) {
	var empty [sha256.Size]byte
	content, err := Generate()
	if err != nil {
		return empty, err
	}
	if err := os.MkdirAll(outputDirectory, 0o755); err != nil {
		return empty, fmt.Errorf("create P1 fixture directory: %w", err)
	}
	digest := sha256.Sum256(content)
	checksum := []byte(hex.EncodeToString(digest[:]) + "\n")
	manifestPath := filepath.Join(outputDirectory, ManifestName)
	checksumPath := filepath.Join(outputDirectory, ChecksumName)
	manifestTemporary, err := writeTemporary(outputDirectory, ManifestName, content)
	if err != nil {
		return empty, err
	}
	defer os.Remove(manifestTemporary)
	checksumTemporary, err := writeTemporary(outputDirectory, ChecksumName, checksum)
	if err != nil {
		return empty, err
	}
	defer os.Remove(checksumTemporary)
	if err := os.Rename(manifestTemporary, manifestPath); err != nil {
		return empty, fmt.Errorf("replace P1 fixture manifest: %w", err)
	}
	if err := os.Rename(checksumTemporary, checksumPath); err != nil {
		return empty, fmt.Errorf("replace P1 fixture checksum: %w", err)
	}
	return digest, nil
}

func Verify(outputDirectory string) error {
	expected, err := Generate()
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(outputDirectory, ManifestName)
	actual, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read P1 fixture manifest: %w", err)
	}
	if !bytes.Equal(actual, expected) {
		return errors.New("P1 fixture manifest differs from deterministic generation")
	}
	digest := sha256.Sum256(actual)
	expectedChecksum := hex.EncodeToString(digest[:]) + "\n"
	checksum, err := os.ReadFile(filepath.Join(outputDirectory, ChecksumName))
	if err != nil {
		return fmt.Errorf("read P1 fixture checksum: %w", err)
	}
	if string(checksum) != expectedChecksum {
		return errors.New("P1 fixture checksum does not match manifest")
	}
	return nil
}

func writeTemporary(directory string, name string, content []byte) (string, error) {
	temporary, err := os.CreateTemp(directory, "."+name+".*")
	if err != nil {
		return "", fmt.Errorf("create temporary P1 fixture: %w", err)
	}
	path := temporary.Name()
	remove := true
	defer func() {
		if remove {
			os.Remove(path)
		}
	}()
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return "", fmt.Errorf("set temporary P1 fixture mode: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return "", fmt.Errorf("write temporary P1 fixture: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", fmt.Errorf("sync temporary P1 fixture: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close temporary P1 fixture: %w", err)
	}
	remove = false
	return path, nil
}

func canonicalize(definition Definition) ([]byte, error) {
	input := []byte(definition.Input)
	switch definition.Contract {
	case "json-value":
		value, err := codec.DecodeJSONValue(input)
		if err != nil {
			return nil, err
		}
		return codec.EncodeJSONValue(value)
	case "location-ref":
		value, err := codec.DecodeLocationRefJSON(input)
		if err != nil {
			return nil, err
		}
		return codec.EncodeLocationRefJSON(value)
	case "sessions-cursor":
		value, err := codec.DecodeSessionsCursor(definition.Input)
		if err != nil {
			return nil, err
		}
		encoded, err := codec.EncodeSessionsCursor(value)
		if err != nil {
			return nil, err
		}
		if encoded != definition.Input {
			return nil, errors.New("session cursor does not round-trip byte-for-byte")
		}
		return codec.EncodeJSONValue(domain.JSONString(encoded))
	case "llm-usage":
		value, err := codec.DecodeUsageJSON(input)
		if err != nil {
			return nil, err
		}
		return codec.EncodeUsageJSON(value)
	case "llm-event":
		value, err := codec.DecodeLLMEventJSON(input)
		if err != nil {
			return nil, err
		}
		return codec.EncodeLLMEventJSON(value)
	case "llm-message":
		value, err := codec.DecodeLLMMessageJSON(input)
		if err != nil {
			return nil, err
		}
		return codec.EncodeLLMMessageJSON(value)
	case "llm-failure":
		value, err := codec.DecodeLLMFailureJSON(input)
		if err != nil {
			return nil, err
		}
		return codec.EncodeLLMFailureJSON(value)
	case "permission-request":
		value, err := codec.DecodePermissionRequestJSON(input)
		if err != nil {
			return nil, err
		}
		return codec.EncodePermissionRequestJSON(value)
	case "permission-reply":
		value, err := codec.DecodePermissionReplyJSON(input)
		if err != nil {
			return nil, err
		}
		return codec.EncodePermissionReplyJSON(value)
	case "permission-ruleset":
		value, err := codec.DecodePermissionRulesetJSON(input)
		if err != nil {
			return nil, err
		}
		return codec.EncodePermissionRulesetJSON(value)
	case "question-request":
		value, err := codec.DecodeQuestionRequestJSON(input)
		if err != nil {
			return nil, err
		}
		return codec.EncodeQuestionRequestJSON(value)
	case "question-reply":
		value, err := codec.DecodeQuestionReplyJSON(input)
		if err != nil {
			return nil, err
		}
		return codec.EncodeQuestionReplyJSON(value)
	case "event-envelope":
		value, err := codec.DecodeEventEnvelopeJSON(input)
		if err != nil {
			return nil, err
		}
		return codec.EncodeEventEnvelopeJSON(value)
	case "session-message":
		value, err := codec.DecodeSessionMessageJSON(input)
		if err != nil {
			return nil, err
		}
		return codec.EncodeSessionMessageJSON(value)
	case "session-event":
		value, err := codec.DecodeSessionEventJSON(input)
		if err != nil {
			return nil, err
		}
		return codec.EncodeSessionEventJSON(value)
	default:
		return nil, fmt.Errorf("unknown P1 fixture contract %q", definition.Contract)
	}
}

func DefinitionKey(definition Definition) string {
	return strings.Join([]string{definition.Contract, definition.Name}, "/")
}
