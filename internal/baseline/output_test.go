package baseline

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSnapshotWritesManifestAndDigest(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "baseline.json")
	content := []byte("{\n  \"schema_version\": 1\n}\n")
	digest := sha256.Sum256(content)
	result := Result{JSON: content, SHA256: hex.EncodeToString(digest[:])}

	if err := WriteSnapshot(output, result); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	written, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if string(written) != string(content) {
		t.Fatalf("manifest = %q, want %q", written, content)
	}
	writtenDigest, err := os.ReadFile(output + ".sha256")
	if err != nil {
		t.Fatalf("read digest: %v", err)
	}
	if string(writtenDigest) != result.SHA256+"  baseline.json\n" {
		t.Fatalf("digest file = %q", writtenDigest)
	}
}

func TestWriteSnapshotRejectsDigestMismatchWithoutOverwriting(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "baseline.json")
	if err := os.WriteFile(output, []byte("previous\n"), 0o644); err != nil {
		t.Fatalf("write previous manifest: %v", err)
	}
	if err := os.WriteFile(output+".sha256", []byte("previous digest\n"), 0o644); err != nil {
		t.Fatalf("write previous digest: %v", err)
	}

	err := WriteSnapshot(output, Result{JSON: []byte("next\n"), SHA256: "incorrect"})
	if err == nil {
		t.Fatal("digest mismatch unexpectedly succeeded")
	}
	manifest, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatalf("read previous manifest: %v", readErr)
	}
	digest, readErr := os.ReadFile(output + ".sha256")
	if readErr != nil {
		t.Fatalf("read previous digest: %v", readErr)
	}
	if string(manifest) != "previous\n" || string(digest) != "previous digest\n" {
		t.Fatalf("previous snapshot changed: manifest=%q digest=%q", manifest, digest)
	}
}
