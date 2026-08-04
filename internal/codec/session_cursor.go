package codec

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

type sessionAnchorWire struct {
	ID        domain.SessionID            `json:"id"`
	Time      float64                     `json:"time"`
	Direction domain.SessionListDirection `json:"direction"`
}

func EncodeSessionsCursor(cursor domain.SessionsCursor) (string, error) {
	if err := cursor.Validate(); err != nil {
		return "", err
	}
	anchor := sessionAnchorWire{ID: cursor.Anchor.ID, Time: normalizeNumber(cursor.Anchor.Time), Direction: cursor.Anchor.Direction}
	var encoded []byte
	var err error
	switch {
	case cursor.Directory != nil:
		wire := struct {
			WorkspaceID *domain.WorkspaceID `json:"workspace,omitempty"`
			Order       *string             `json:"order,omitempty"`
			Search      *string             `json:"search,omitempty"`
			Directory   string              `json:"directory"`
			Anchor      sessionAnchorWire   `json:"anchor"`
		}{cursor.WorkspaceID, cursor.Order, cursor.Search, *cursor.Directory, anchor}
		encoded, err = json.Marshal(wire)
	case cursor.ProjectID != nil:
		wire := struct {
			WorkspaceID *domain.WorkspaceID `json:"workspace,omitempty"`
			Order       *string             `json:"order,omitempty"`
			Search      *string             `json:"search,omitempty"`
			ProjectID   domain.ProjectID    `json:"project"`
			Subpath     *string             `json:"subpath,omitempty"`
			Anchor      sessionAnchorWire   `json:"anchor"`
		}{cursor.WorkspaceID, cursor.Order, cursor.Search, *cursor.ProjectID, cursor.Subpath, anchor}
		encoded, err = json.Marshal(wire)
	default:
		wire := struct {
			WorkspaceID *domain.WorkspaceID `json:"workspace,omitempty"`
			Order       *string             `json:"order,omitempty"`
			Search      *string             `json:"search,omitempty"`
			Anchor      sessionAnchorWire   `json:"anchor"`
		}{cursor.WorkspaceID, cursor.Order, cursor.Search, anchor}
		encoded, err = json.Marshal(wire)
	}
	if err != nil {
		return "", fmt.Errorf("encode session cursor JSON: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func DecodeSessionsCursor(value string) (domain.SessionsCursor, error) {
	content, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil {
		return domain.SessionsCursor{}, errors.New("invalid session cursor encoding")
	}
	if !utf8.Valid(content) {
		return domain.SessionsCursor{}, errors.New("session cursor JSON is not valid UTF-8")
	}
	var wire struct {
		WorkspaceID json.RawMessage `json:"workspace"`
		Order       json.RawMessage `json:"order"`
		Search      json.RawMessage `json:"search"`
		Directory   json.RawMessage `json:"directory"`
		ProjectID   json.RawMessage `json:"project"`
		Subpath     json.RawMessage `json:"subpath"`
		Anchor      json.RawMessage `json:"anchor"`
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return domain.SessionsCursor{}, fmt.Errorf("decode session cursor JSON: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return domain.SessionsCursor{}, err
	}
	if len(wire.Anchor) == 0 || isJSONNull(wire.Anchor) {
		return domain.SessionsCursor{}, errors.New("session cursor anchor is required")
	}
	result := domain.SessionsCursor{}
	if result.Order, err = decodeOptionalString(wire.Order, "session cursor order"); err != nil {
		return domain.SessionsCursor{}, err
	}
	if result.Search, err = decodeOptionalString(wire.Search, "session cursor search"); err != nil {
		return domain.SessionsCursor{}, err
	}
	if result.Directory, err = decodeOptionalString(wire.Directory, "session cursor directory"); err != nil {
		return domain.SessionsCursor{}, err
	}
	if result.Subpath, err = decodeOptionalString(wire.Subpath, "session cursor subpath"); err != nil {
		return domain.SessionsCursor{}, err
	}
	if len(wire.ProjectID) != 0 {
		project, err := decodeRequiredString(wire.ProjectID, "session cursor project")
		if err != nil {
			return domain.SessionsCursor{}, err
		}
		projectID := domain.ProjectID(project)
		result.ProjectID = &projectID
	}
	if len(wire.WorkspaceID) != 0 {
		workspace, err := decodeRequiredString(wire.WorkspaceID, "session cursor workspace")
		if err != nil {
			return domain.SessionsCursor{}, err
		}
		workspaceID, err := domain.ParseWorkspaceID(workspace)
		if err != nil {
			return domain.SessionsCursor{}, err
		}
		result.WorkspaceID = &workspaceID
	}
	anchor, err := decodeSessionAnchor(wire.Anchor)
	if err != nil {
		return domain.SessionsCursor{}, err
	}
	result.Anchor = anchor
	if err := result.Validate(); err != nil {
		return domain.SessionsCursor{}, err
	}
	return result, nil
}

func decodeSessionAnchor(content []byte) (domain.SessionListAnchor, error) {
	var wire struct {
		ID        json.RawMessage `json:"id"`
		Time      json.RawMessage `json:"time"`
		Direction json.RawMessage `json:"direction"`
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return domain.SessionListAnchor{}, fmt.Errorf("decode session cursor anchor: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return domain.SessionListAnchor{}, err
	}
	idValue, err := decodeRequiredString(wire.ID, "session cursor anchor id")
	if err != nil {
		return domain.SessionListAnchor{}, err
	}
	id, err := domain.ParseSessionID(idValue)
	if err != nil {
		return domain.SessionListAnchor{}, err
	}
	directionValue, err := decodeRequiredString(wire.Direction, "session cursor anchor direction")
	if err != nil {
		return domain.SessionListAnchor{}, err
	}
	if len(wire.Time) == 0 || isJSONNull(wire.Time) {
		return domain.SessionListAnchor{}, errors.New("session cursor anchor time is required")
	}
	var timeValue float64
	if err := json.Unmarshal(wire.Time, &timeValue); err != nil || math.IsNaN(timeValue) || math.IsInf(timeValue, 0) {
		return domain.SessionListAnchor{}, errors.New("session cursor anchor time must be finite")
	}
	return domain.SessionListAnchor{
		ID: id, Time: normalizeNumber(timeValue), Direction: domain.SessionListDirection(directionValue),
	}, nil
}

func decodeOptionalString(content []byte, label string) (*string, error) {
	if len(content) == 0 {
		return nil, nil
	}
	value, err := decodeRequiredString(content, label)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func decodeRequiredString(content []byte, label string) (string, error) {
	if len(content) == 0 || isJSONNull(content) {
		return "", fmt.Errorf("%s must be a string when present", label)
	}
	var value string
	if err := json.Unmarshal(content, &value); err != nil {
		return "", fmt.Errorf("%s must be a string", label)
	}
	return value, nil
}

func isJSONNull(content []byte) bool {
	return bytes.Equal(bytes.TrimSpace(content), []byte("null"))
}

func normalizeNumber(value float64) float64 {
	if value == 0 {
		return 0
	}
	return value
}
