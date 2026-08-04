package codec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

func DecodeLocationRefJSON(content []byte) (domain.LocationRef, error) {
	if !utf8.Valid(content) {
		return domain.LocationRef{}, errors.New("location JSON is not valid UTF-8")
	}
	var wire struct {
		Directory   json.RawMessage `json:"directory"`
		WorkspaceID json.RawMessage `json:"workspaceID"`
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return domain.LocationRef{}, fmt.Errorf("decode location JSON: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return domain.LocationRef{}, err
	}
	if len(wire.Directory) == 0 {
		return domain.LocationRef{}, errors.New("location directory is required")
	}
	if bytes.Equal(bytes.TrimSpace(wire.Directory), []byte("null")) {
		return domain.LocationRef{}, errors.New("location directory must not be null")
	}
	var directory string
	if err := json.Unmarshal(wire.Directory, &directory); err != nil {
		return domain.LocationRef{}, errors.New("location directory must be a string")
	}
	result := domain.LocationRef{Directory: directory}
	if len(wire.WorkspaceID) == 0 {
		return result, nil
	}
	if bytes.Equal(bytes.TrimSpace(wire.WorkspaceID), []byte("null")) {
		return domain.LocationRef{}, errors.New("location workspaceID must not be null")
	}
	var workspaceValue string
	if err := json.Unmarshal(wire.WorkspaceID, &workspaceValue); err != nil {
		return domain.LocationRef{}, errors.New("location workspaceID must be a string when present")
	}
	workspaceID, err := domain.ParseWorkspaceID(workspaceValue)
	if err != nil {
		return domain.LocationRef{}, err
	}
	result.WorkspaceID = &workspaceID
	return result, nil
}

func EncodeLocationRefJSON(location domain.LocationRef) ([]byte, error) {
	if location.WorkspaceID != nil {
		if _, err := domain.ParseWorkspaceID(string(*location.WorkspaceID)); err != nil {
			return nil, err
		}
	}
	wire := struct {
		Directory   string              `json:"directory"`
		WorkspaceID *domain.WorkspaceID `json:"workspaceID,omitempty"`
	}{Directory: location.Directory, WorkspaceID: location.WorkspaceID}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encode location JSON: %w", err)
	}
	return append(encoded, '\n'), nil
}
