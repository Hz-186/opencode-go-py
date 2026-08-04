package domain

import (
	"errors"
	"fmt"
	"math"
)

type SessionListDirection string

const (
	SessionListPrevious SessionListDirection = "previous"
	SessionListNext     SessionListDirection = "next"
)

type SessionListAnchor struct {
	ID        SessionID
	Time      float64
	Direction SessionListDirection
}

type SessionsCursor struct {
	WorkspaceID *WorkspaceID
	Order       *string
	Search      *string
	Directory   *string
	ProjectID   *ProjectID
	Subpath     *string
	Anchor      SessionListAnchor
}

func (cursor SessionsCursor) Validate() error {
	if cursor.WorkspaceID != nil {
		if _, err := ParseWorkspaceID(string(*cursor.WorkspaceID)); err != nil {
			return err
		}
	}
	if cursor.Order != nil && *cursor.Order != "asc" && *cursor.Order != "desc" {
		return fmt.Errorf("invalid session cursor order %q", *cursor.Order)
	}
	if cursor.Directory != nil && cursor.ProjectID != nil {
		return errors.New("session cursor directory and project are mutually exclusive")
	}
	if cursor.Subpath != nil && cursor.ProjectID == nil {
		return errors.New("session cursor subpath requires project")
	}
	if _, err := ParseSessionID(string(cursor.Anchor.ID)); err != nil {
		return err
	}
	if math.IsNaN(cursor.Anchor.Time) || math.IsInf(cursor.Anchor.Time, 0) {
		return errors.New("session cursor anchor time must be finite")
	}
	if cursor.Anchor.Direction != SessionListPrevious && cursor.Anchor.Direction != SessionListNext {
		return fmt.Errorf("invalid session cursor direction %q", cursor.Anchor.Direction)
	}
	return nil
}
