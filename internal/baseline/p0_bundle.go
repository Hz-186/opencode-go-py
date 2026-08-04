package baseline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

type BundleInput struct {
	Path   string
	JSON   []byte
	SHA256 string
}

type P0BundleResult struct {
	Bundle P0Bundle
	JSON   []byte
	SHA256 string
}

type P0Bundle struct {
	SchemaVersion  int                `json:"schema_version"`
	BaselineCommit string             `json:"baseline_commit"`
	Artifacts      []P0BundleArtifact `json:"artifacts"`
}

type P0BundleArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

func BuildP0Bundle(commit string, inputs []BundleInput) (P0BundleResult, error) {
	if commit == "" {
		return P0BundleResult{}, errors.New("P0 bundle baseline commit is required")
	}
	if len(inputs) == 0 {
		return P0BundleResult{}, errors.New("P0 bundle requires at least one artifact")
	}
	artifacts := make([]P0BundleArtifact, 0, len(inputs))
	paths := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		name := path.Clean(strings.ReplaceAll(input.Path, "\\", "/"))
		if name == "." || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return P0BundleResult{}, fmt.Errorf("invalid P0 bundle artifact path %q", input.Path)
		}
		if _, duplicate := paths[name]; duplicate {
			return P0BundleResult{}, fmt.Errorf("duplicate P0 bundle artifact path %q", name)
		}
		paths[name] = struct{}{}
		actual := digestBytes(input.JSON)
		if !strings.EqualFold(actual, input.SHA256) {
			return P0BundleResult{}, fmt.Errorf("P0 bundle artifact %s digest mismatch: computed=%s provided=%s", name, actual, input.SHA256)
		}
		artifacts = append(artifacts, P0BundleArtifact{
			Path: name, SHA256: actual, Bytes: int64(len(input.JSON)),
		})
	}
	slices.SortFunc(artifacts, func(a, b P0BundleArtifact) int { return strings.Compare(a.Path, b.Path) })
	bundle := P0Bundle{SchemaVersion: 1, BaselineCommit: commit, Artifacts: artifacts}
	encoded, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return P0BundleResult{}, fmt.Errorf("encode P0 bundle: %w", err)
	}
	encoded = append(encoded, '\n')
	return P0BundleResult{Bundle: bundle, JSON: encoded, SHA256: digestBytes(encoded)}, nil
}

func digestBytes(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
