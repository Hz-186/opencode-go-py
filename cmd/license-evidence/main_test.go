package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRequiresOutput(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), nil, &stdout, &stderr, http.DefaultClient)

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

	exitCode := run(context.Background(), []string{
		"-manifest", filepath.Join(t.TempDir(), "missing.json"),
		"-output", filepath.Join(t.TempDir(), "evidence.json"),
	}, &stdout, &stderr, http.DefaultClient)

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

func TestRegistryFetcherUsesExactEscapedVersionEndpoint(t *testing.T) {
	var requestURI string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestURI = request.RequestURI
		if request.Header.Get("Accept") != "application/json" || request.Header.Get("User-Agent") == "" {
			t.Errorf("request headers = %#v", request.Header)
		}
		_, _ = io.WriteString(response, `{"name":"@fixture/pkg","version":"1.2.3"}`)
	}))
	defer server.Close()

	content, err := registryFetcher(server.Client(), server.URL)(context.Background(), "@fixture/pkg", "1.2.3")
	if err != nil {
		t.Fatalf("fetch registry metadata: %v", err)
	}
	if requestURI != "/@fixture%2Fpkg/1.2.3" {
		t.Fatalf("request URI = %q", requestURI)
	}
	if string(content) != `{"name":"@fixture/pkg","version":"1.2.3"}` {
		t.Fatalf("response = %q", content)
	}
}

func TestRegistryFetcherRejectsNonSuccessStatus(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "missing", http.StatusNotFound)
	}))
	defer server.Close()

	_, err := registryFetcher(server.Client(), server.URL)(context.Background(), "missing", "1.0.0")
	if err == nil {
		t.Fatal("registry 404 unexpectedly succeeded")
	}
}
