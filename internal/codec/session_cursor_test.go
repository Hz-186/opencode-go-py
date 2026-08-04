package codec

import (
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

const frozenSessionsCursor = "eyJvcmRlciI6ImRlc2MiLCJzZWFyY2giOiJwcm90b2NvbCIsImFuY2hvciI6eyJpZCI6InNlc190ZXN0IiwidGltZSI6MSwiZGlyZWN0aW9uIjoibmV4dCJ9fQ"

func TestSessionsCursorMatchesFrozenBase64URLJSONVector(t *testing.T) {
	input := domain.SessionsCursor{
		Order:  stringPointer("desc"),
		Search: stringPointer("protocol"),
		Anchor: domain.SessionListAnchor{ID: domain.SessionID("ses_test"), Time: 1, Direction: domain.SessionListNext},
	}

	encoded, err := EncodeSessionsCursor(input)
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}
	if encoded != frozenSessionsCursor {
		t.Fatalf("cursor = %q", encoded)
	}
	decoded, err := DecodeSessionsCursor(encoded)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if decoded.Order == nil || *decoded.Order != "desc" || decoded.Search == nil || *decoded.Search != "protocol" {
		t.Fatalf("decoded query = %#v", decoded)
	}
	if decoded.Anchor != input.Anchor {
		t.Fatalf("decoded anchor = %#v, want %#v", decoded.Anchor, input.Anchor)
	}
}

func TestSessionsCursorRejectsInvalidEncodingAndQueryShapes(t *testing.T) {
	invalid := []string{
		"%%not-base64%%",
		"bnVsbA",
		"e30",
		"eyJhbmNob3IiOnsiaWQiOiJiYWQiLCJ0aW1lIjoxLCJkaXJlY3Rpb24iOiJuZXh0In19",
	}
	for _, cursor := range invalid {
		if _, err := DecodeSessionsCursor(cursor); err == nil {
			t.Fatalf("invalid cursor %q unexpectedly succeeded", cursor)
		}
	}

	directory := "/tmp/project"
	project := domain.ProjectID("project")
	_, err := EncodeSessionsCursor(domain.SessionsCursor{
		Directory: &directory, ProjectID: &project,
		Anchor: domain.SessionListAnchor{ID: domain.SessionID("ses_test"), Time: 1, Direction: domain.SessionListNext},
	})
	if err == nil {
		t.Fatal("cursor with directory and project unexpectedly succeeded")
	}
}

func stringPointer(value string) *string {
	return &value
}
