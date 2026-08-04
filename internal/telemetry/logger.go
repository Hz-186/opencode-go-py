// Package telemetry provides structured logging with mandatory redaction.
package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"unicode"
)

const defaultReplacement = "[REDACTED]"

// Options configures JSON logging and exact-value secret removal.
type Options struct {
	Level       slog.Leveler
	AddSource   bool
	Secrets     []string
	Replacement string
}

// Correlation is the canonical execution identity attached to runtime logs.
type Correlation struct {
	TraceID   string
	SessionID string
	TurnID    string
	Attempt   int
}

type redactor struct {
	secrets     []string
	replacement string
}

type redactingHandler struct {
	next     slog.Handler
	redactor redactor
}

// NewJSONLogger creates a structured logger that redacts sensitive keys and
// configured secret values before the underlying JSON handler sees them.
func NewJSONLogger(output io.Writer, options Options) *slog.Logger {
	replacement := options.Replacement
	if replacement == "" {
		replacement = defaultReplacement
	}
	secrets := make([]string, 0, len(options.Secrets))
	for _, secret := range options.Secrets {
		if secret != "" {
			secrets = append(secrets, secret)
		}
	}
	base := slog.NewJSONHandler(output, &slog.HandlerOptions{
		AddSource: options.AddSource,
		Level:     options.Level,
	})
	return slog.New(&redactingHandler{
		next: base,
		redactor: redactor{
			secrets:     secrets,
			replacement: replacement,
		},
	})
}

// WithCorrelation attaches all canonical correlation dimensions.
func WithCorrelation(logger *slog.Logger, correlation Correlation) *slog.Logger {
	return logger.With(
		"trace", correlation.TraceID,
		"session", correlation.SessionID,
		"turn", correlation.TurnID,
		"attempt", correlation.Attempt,
	)
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, h.redactor.text(record.Message), record.PC)
	record.Attrs(func(attribute slog.Attr) bool {
		clean.AddAttrs(h.redactor.attribute(attribute))
		return true
	})
	return h.next.Handle(ctx, clean)
}

func (h *redactingHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, 0, len(attributes))
	for _, attribute := range attributes {
		clean = append(clean, h.redactor.attribute(attribute))
	}
	return &redactingHandler{
		next:     h.next.WithAttrs(clean),
		redactor: h.redactor,
	}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{
		next:     h.next.WithGroup(name),
		redactor: h.redactor,
	}
}

func (r redactor) attribute(attribute slog.Attr) slog.Attr {
	attribute.Value = attribute.Value.Resolve()
	if sensitiveKey(attribute.Key) {
		return slog.String(attribute.Key, r.replacement)
	}

	switch attribute.Value.Kind() {
	case slog.KindString:
		return slog.String(attribute.Key, r.text(attribute.Value.String()))
	case slog.KindGroup:
		group := attribute.Value.Group()
		clean := make([]slog.Attr, 0, len(group))
		for _, nested := range group {
			clean = append(clean, r.attribute(nested))
		}
		return slog.Group(attribute.Key, attrsToAny(clean)...)
	case slog.KindAny:
		return slog.Any(attribute.Key, r.any(attribute.Value.Any()))
	default:
		return attribute
	}
}

func attrsToAny(attributes []slog.Attr) []any {
	values := make([]any, len(attributes))
	for i := range attributes {
		values[i] = attributes[i]
	}
	return values
}

func (r redactor) any(value any) any {
	if err, ok := value.(error); ok {
		return r.text(err.Error())
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return r.replacement
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return r.replacement
	}
	return r.decoded(decoded)
}

func (r redactor) decoded(value any) any {
	switch typed := value.(type) {
	case string:
		return r.text(typed)
	case []any:
		clean := make([]any, len(typed))
		for i := range typed {
			clean[i] = r.decoded(typed[i])
		}
		return clean
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, nested := range typed {
			if sensitiveKey(key) {
				clean[key] = r.replacement
			} else {
				clean[key] = r.decoded(nested)
			}
		}
		return clean
	default:
		return value
	}
}

func (r redactor) text(value string) string {
	for _, secret := range r.secrets {
		value = strings.ReplaceAll(value, secret, r.replacement)
	}
	return value
}

func sensitiveKey(key string) bool {
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, key)

	switch normalized {
	case "prompt", "prompts", "apikey", "authorization", "proxyauthorization",
		"headers", "rawheader", "rawheaders", "token", "accesstoken",
		"refreshtoken", "secret", "clientsecret", "password", "cookie", "setcookie":
		return true
	default:
		return strings.HasSuffix(normalized, "apikey") || strings.HasSuffix(normalized, "clientsecret")
	}
}
