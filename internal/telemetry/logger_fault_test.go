package telemetry

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestLoggerPreservesRedactedErrorMeaning(t *testing.T) {
	t.Parallel()

	const secret = "fixture-error-secret"
	var output bytes.Buffer
	logger := NewJSONLogger(&output, Options{Secrets: []string{secret}})
	logger.Error("operation failed", "err", errors.New("provider failed with "+secret))

	got := output.String()
	if strings.Contains(got, secret) {
		t.Fatalf("error log contains secret: %s", got)
	}
	if !strings.Contains(got, "provider failed with [REDACTED]") {
		t.Fatalf("error meaning was not preserved: %s", got)
	}
}

func TestLoggerRedactsCanonicalHTTPSecretKeysWithoutFixtureValues(t *testing.T) {
	t.Parallel()

	const secret = "unknown-at-configuration-time"
	var output bytes.Buffer
	logger := NewJSONLogger(&output, Options{})
	logger.Info("request", "X-Api-Key", secret, "Client-Secret", secret)

	got := output.String()
	if strings.Contains(got, secret) {
		t.Fatalf("HTTP secret key log contains value: %s", got)
	}
}
