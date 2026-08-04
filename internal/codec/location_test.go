package codec

import (
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

func TestLocationRefJSONMatchesFrozenOptionalWorkspaceSemantics(t *testing.T) {
	withoutWorkspace, err := DecodeLocationRefJSON([]byte(`{"directory":"/tmp/工作区"}`))
	if err != nil {
		t.Fatalf("decode location: %v", err)
	}
	if withoutWorkspace.Directory != "/tmp/工作区" || withoutWorkspace.WorkspaceID != nil {
		t.Fatalf("location = %#v", withoutWorkspace)
	}
	encoded, err := EncodeLocationRefJSON(withoutWorkspace)
	if err != nil {
		t.Fatalf("encode location: %v", err)
	}
	if string(encoded) != `{"directory":"/tmp/工作区"}`+"\n" {
		t.Fatalf("encoded location = %s", encoded)
	}

	workspaceID := domain.WorkspaceID("wrk_test")
	withWorkspace := domain.LocationRef{Directory: "/tmp/project", WorkspaceID: &workspaceID}
	encoded, err = EncodeLocationRefJSON(withWorkspace)
	if err != nil {
		t.Fatalf("encode workspace location: %v", err)
	}
	if string(encoded) != `{"directory":"/tmp/project","workspaceID":"wrk_test"}`+"\n" {
		t.Fatalf("encoded workspace location = %s", encoded)
	}
}

func TestLocationRefJSONRejectsNullMissingInvalidAndUnknownFields(t *testing.T) {
	invalid := [][]byte{
		[]byte(`{}`),
		[]byte(`{"directory":null}`),
		[]byte(`{"directory":"/tmp","workspaceID":null}`),
		[]byte(`{"directory":"/tmp","workspaceID":"bad"}`),
		[]byte(`{"directory":"/tmp","extra":true}`),
		{'{', '"', 'd', 'i', 'r', 'e', 'c', 't', 'o', 'r', 'y', '"', ':', '"', 0xff, '"', '}'},
	}
	for _, input := range invalid {
		if _, err := DecodeLocationRefJSON(input); err == nil {
			t.Fatalf("invalid location %q unexpectedly succeeded", input)
		}
	}
}
