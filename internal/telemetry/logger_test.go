package telemetry

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestJSONLoggerRedactsSecretsAndSensitiveFields(t *testing.T) {
	t.Parallel()

	const (
		apiKey = "sk-fixture-do-not-log"
		prompt = "fixture private prompt"
	)
	var output bytes.Buffer
	logger := NewJSONLogger(&output, Options{
		Level:       slog.LevelDebug,
		Secrets:     []string{apiKey, prompt},
		Replacement: "[REDACTED]",
	})
	logger.InfoContext(
		context.Background(),
		"provider failed with "+apiKey,
		"safe", "visible",
		"prompt", prompt,
		"api_key", apiKey,
		"Authorization", "Bearer "+apiKey,
		"raw_headers", map[string][]string{"X-Api-Key": {apiKey}},
		"nested", slog.GroupValue(
			slog.String("token", apiKey),
			slog.String("result", "kept"),
		),
	)

	got := output.String()
	for _, forbidden := range []string{apiKey, prompt, "Bearer " + apiKey} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("log output contains secret %q: %s", forbidden, got)
		}
	}
	for _, required := range []string{"[REDACTED]", "visible", "kept"} {
		if !strings.Contains(got, required) {
			t.Fatalf("log output does not contain %q: %s", required, got)
		}
	}
}

func TestLoggerCarriesCanonicalCorrelationFields(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := NewJSONLogger(&output, Options{})
	logger = WithCorrelation(logger, Correlation{
		TraceID:   "trace-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
		Attempt:   3,
	})
	logger.Info("settled")

	got := output.String()
	for _, required := range []string{
		`"trace":"trace-1"`,
		`"session":"session-1"`,
		`"turn":"turn-1"`,
		`"attempt":3`,
	} {
		if !strings.Contains(got, required) {
			t.Fatalf("correlated log does not contain %s: %s", required, got)
		}
	}
}

func TestRedactionTraversesAnyMapsAndSlices(t *testing.T) {
	t.Parallel()

	const secret = "fixture-bearer-secret"
	var output bytes.Buffer
	logger := NewJSONLogger(&output, Options{Secrets: []string{secret}})
	logger.Info("nested payload",
		"metadata", map[string]any{
			"safe": "kept",
			"items": []any{
				map[string]any{"password": secret},
				"prefix-" + secret,
			},
		},
	)

	got := output.String()
	if strings.Contains(got, secret) {
		t.Fatalf("nested log output contains secret: %s", got)
	}
	if !strings.Contains(got, "kept") {
		t.Fatalf("nested log output lost safe value: %s", got)
	}
}
