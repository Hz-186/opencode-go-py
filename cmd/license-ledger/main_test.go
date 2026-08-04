package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRequiresOutput(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(nil, &stdout, &stderr)

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "-output is required") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunReportsMissingManifest(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run([]string{
		"-manifest", filepath.Join(t.TempDir(), "missing.json"),
		"-evidence", filepath.Join(t.TempDir(), "missing-evidence.json"),
		"-output", filepath.Join(t.TempDir(), "licenses.json"),
	}, &stdout, &stderr)

	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "read manifest") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunRequiresEvidence(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run([]string{
		"-output", filepath.Join(t.TempDir(), "licenses.json"),
	}, &stdout, &stderr)

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "-evidence is required") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
