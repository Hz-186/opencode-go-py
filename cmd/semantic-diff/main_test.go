package main

import (
	"bytes"
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
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "-output is required") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
