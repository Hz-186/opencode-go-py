package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

// ErrMalformedFrame means that a provider returned a syntactically invalid
// stream frame. The raw response is intentionally never retained in the
// error, because provider responses can contain prompts, tool arguments, or
// other sensitive values.
var (
	ErrMalformedFrame = errors.New("malformed provider stream frame")
	ErrProviderHTTP   = errors.New("provider HTTP request failed")
	ErrBodyLimit      = errors.New("provider response body exceeds limit")
	ErrProviderReject = errors.New("provider rejected request")
)

// ProviderHTTPFailure carries only canonical control fields derived from a
// bounded non-2xx response. It deliberately retains no raw response bytes,
// provider message, prompt, tool input, URL, or credential.
type ProviderHTTPFailure struct {
	Classification *llm.ProviderFailureClassification
	Retryable      *bool
}

func (failure *ProviderHTTPFailure) Error() string { return ErrProviderReject.Error() }

func (failure *ProviderHTTPFailure) Unwrap() error { return ErrProviderReject }

// SSEFrame is one Server-Sent Events record after field parsing. Data lines
// are joined with a newline as required by the SSE specification.
type SSEFrame struct {
	Event string
	Data  string
}

// ReadSSE reads an SSE stream with bounded line sizes. It does not interpret
// provider-specific JSON; protocol adapters do that after framing.
func ReadSSE(ctx context.Context, reader io.Reader, maxLine int, visit func(SSEFrame) error) error {
	if ctx == nil || reader == nil || visit == nil {
		return fmt.Errorf("%w: nil argument", ErrMalformedFrame)
	}
	if maxLine <= 0 || maxLine > 16<<20 {
		return fmt.Errorf("%w: invalid line limit", ErrMalformedFrame)
	}
	scanner := bufio.NewScanner(reader)
	initial := 1024
	if maxLine < initial {
		initial = maxLine
	}
	scanner.Buffer(make([]byte, initial), maxLine)
	var eventName string
	var data bytes.Buffer
	flush := func() error {
		if eventName == "" && data.Len() == 0 {
			return nil
		}
		frame := SSEFrame{Event: eventName, Data: data.String()}
		eventName = ""
		data.Reset()
		return visit(frame)
	}
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "event":
			eventName = value
		case "data":
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
		case "id", "retry":
			// Adapters intentionally do not use server replay IDs or retry
			// directives; retry belongs to the canonical transport policy.
		default:
			// SSE requires clients to ignore extension fields. Provider-specific
			// semantics are carried in data JSON, not framing extensions.
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%w: read: %w", ErrMalformedFrame, err)
	}
	return flush()
}

// DecodeFrameJSON decodes a frame while retaining arbitrary JSON numbers and
// rejecting trailing values. It is the common boundary used by all adapters.
func DecodeFrameJSON(data string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(data))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, fmt.Errorf("%w: JSON object", ErrMalformedFrame)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing JSON", ErrMalformedFrame)
	}
	return object, nil
}

// RequestJSON converts a canonical value to bytes using encoding/json. The
// request projection functions sort their keys through canonical JSON tests;
// this helper only handles transport serialization.
func RequestJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: request JSON: %v", ErrMalformedFrame, err)
	}
	return encoded, nil
}

// DoHTTP executes one request and returns the response body to the caller.
// Bodies are bounded and never included in returned errors. A pre-stream HTTP
// failure is represented as AttemptError so RunWithRetry can make the sole
// retry decision.
func DoHTTP(ctx context.Context, client *http.Client, request *http.Request, maxBody int64) (*http.Response, error) {
	if ctx == nil || client == nil || request == nil {
		return nil, fmt.Errorf("%w: nil argument", ErrProviderHTTP)
	}
	if maxBody <= 0 || maxBody > 64<<20 {
		return nil, fmt.Errorf("%w: invalid body limit", ErrProviderHTTP)
	}
	response, err := client.Do(request.WithContext(ctx))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Do not retain the net/http error string: it can echo a URL query or
		// proxy credentials. The typed category is sufficient for retry.
		return nil, NewAttemptError(0, false, 0, ErrProviderHTTP)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		classification, retryable := classifyHTTPFailure(response.Body)
		_ = response.Body.Close()
		retryAfter := parseRetryAfter(response.Header)
		cause := error(fmt.Errorf("%w: status %d", ErrProviderHTTP, response.StatusCode))
		if classification != nil {
			cause = errors.Join(cause, &ProviderHTTPFailure{Classification: classification, Retryable: retryable})
		}
		return nil, NewAttemptError(response.StatusCode, false, retryAfter, cause)
	}
	response.Body = &boundedBody{source: response.Body, remaining: maxBody}
	return response, nil
}

func classifyHTTPFailure(reader io.Reader) (*llm.ProviderFailureClassification, *bool) {
	if reader == nil {
		return nil, nil
	}
	// Control fields are expected near the start of provider JSON errors. The
	// cap prevents an error response from becoming an allocation or log sink.
	data, err := io.ReadAll(io.LimitReader(reader, 64<<10))
	if err != nil {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, nil
	}
	object, _ := value.(map[string]any)
	if nested, _ := object["error"].(map[string]any); nested != nil {
		object = nested
	} else if response, _ := object["response"].(map[string]any); response != nil {
		if nested, _ := response["error"].(map[string]any); nested != nil {
			object = nested
		}
	}
	if object == nil {
		return nil, nil
	}
	return ClassifyProviderError(httpErrorString(object["code"]), httpErrorString(object["type"]), httpErrorString(object["message"]))
}

func httpErrorString(value any) string {
	result, _ := value.(string)
	return result
}

type boundedBody struct {
	source    io.ReadCloser
	remaining int64
}

func (body *boundedBody) Read(target []byte) (int, error) {
	if body.remaining == 0 {
		var probe [1]byte
		n, err := body.source.Read(probe[:])
		if n > 0 {
			return 0, ErrBodyLimit
		}
		return 0, err
	}
	if int64(len(target)) > body.remaining {
		target = target[:body.remaining]
	}
	n, err := body.source.Read(target)
	body.remaining -= int64(n)
	return n, err
}

func (body *boundedBody) Close() error { return body.source.Close() }

func ValidateSSEResponse(response *http.Response) error {
	if response == nil {
		return fmt.Errorf("%w: missing response", ErrProviderHTTP)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "text/event-stream") {
		return fmt.Errorf("%w: expected text/event-stream", ErrMalformedFrame)
	}
	return nil
}

func parseRetryAfter(headers http.Header) time.Duration {
	value := strings.TrimSpace(headers.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds >= 0 {
		return time.Duration(seconds * float64(time.Second))
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}
