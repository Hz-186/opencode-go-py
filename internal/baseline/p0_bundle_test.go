package baseline

import "testing"

func TestBuildP0BundleSortsAndValidatesArtifacts(t *testing.T) {
	firstJSON := []byte("first\n")
	secondJSON := []byte("second\n")
	artifacts := []BundleInput{
		{Path: "z.json", JSON: secondJSON, SHA256: digestBytes(secondJSON)},
		{Path: "a.json", JSON: firstJSON, SHA256: digestBytes(firstJSON)},
	}

	first, err := BuildP0Bundle("commit", artifacts)
	if err != nil {
		t.Fatalf("build P0 bundle: %v", err)
	}
	second, err := BuildP0Bundle("commit", artifacts)
	if err != nil {
		t.Fatalf("repeat P0 bundle: %v", err)
	}
	if string(first.JSON) != string(second.JSON) || first.SHA256 != second.SHA256 {
		t.Fatal("P0 bundle is not deterministic")
	}
	if len(first.Bundle.Artifacts) != 2 || first.Bundle.Artifacts[0].Path != "a.json" || first.Bundle.Artifacts[1].Path != "z.json" {
		t.Fatalf("bundle artifacts = %#v", first.Bundle.Artifacts)
	}
}

func TestBuildP0BundleRejectsDigestMismatch(t *testing.T) {
	_, err := BuildP0Bundle("commit", []BundleInput{{Path: "bad.json", JSON: []byte("bad"), SHA256: "wrong"}})
	if err == nil {
		t.Fatal("digest mismatch unexpectedly succeeded")
	}
}
