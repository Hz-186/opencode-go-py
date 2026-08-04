package llm

type FailureTag string

const (
	FailureInvalidRequest        FailureTag = "InvalidRequest"
	FailureNoRoute               FailureTag = "NoRoute"
	FailureAuthentication        FailureTag = "Authentication"
	FailureRateLimit             FailureTag = "RateLimit"
	FailureQuotaExceeded         FailureTag = "QuotaExceeded"
	FailureContentPolicy         FailureTag = "ContentPolicy"
	FailureProviderInternal      FailureTag = "ProviderInternal"
	FailureTransport             FailureTag = "Transport"
	FailureInvalidProviderOutput FailureTag = "InvalidProviderOutput"
	FailureUnknownProvider       FailureTag = "UnknownProvider"
)

type AuthenticationKind string

const (
	AuthenticationMissing                 AuthenticationKind = "missing"
	AuthenticationInvalid                 AuthenticationKind = "invalid"
	AuthenticationExpired                 AuthenticationKind = "expired"
	AuthenticationInsufficientPermissions AuthenticationKind = "insufficient-permissions"
	AuthenticationUnknown                 AuthenticationKind = "unknown"
)

type HTTPRequestDetails struct {
	Method  string
	URL     string
	Headers map[string]string
}

type HTTPResponseDetails struct {
	Status  float64
	Headers map[string]string
}

type HTTPRateLimitDetails struct {
	RetryAfterMS *float64
	Limit        map[string]string
	Remaining    map[string]string
	Reset        map[string]string
}

type HTTPContext struct {
	Request       HTTPRequestDetails
	Response      *HTTPResponseDetails
	Body          *string
	BodyTruncated *bool
	RequestID     *string
	RateLimit     *HTTPRateLimitDetails
}

type FailureReason interface {
	FailureTag() FailureTag
	Retryable() bool
}

type InvalidRequestFailure struct {
	Message          string
	Parameter        *string
	Classification   *ProviderFailureClassification
	ProviderMetadata ProviderMetadata
	HTTP             *HTTPContext
}

func (InvalidRequestFailure) FailureTag() FailureTag { return FailureInvalidRequest }
func (InvalidRequestFailure) Retryable() bool        { return false }

type NoRouteFailure struct {
	Route    string
	Provider string
	Model    string
}

func (NoRouteFailure) FailureTag() FailureTag { return FailureNoRoute }
func (NoRouteFailure) Retryable() bool        { return false }

type AuthenticationFailure struct {
	Message          string
	Kind             AuthenticationKind
	ProviderMetadata ProviderMetadata
	HTTP             *HTTPContext
}

func (AuthenticationFailure) FailureTag() FailureTag { return FailureAuthentication }
func (AuthenticationFailure) Retryable() bool        { return false }

type RateLimitFailure struct {
	Message          string
	RetryAfterMS     *float64
	RateLimit        *HTTPRateLimitDetails
	ProviderMetadata ProviderMetadata
	HTTP             *HTTPContext
}

func (RateLimitFailure) FailureTag() FailureTag { return FailureRateLimit }
func (RateLimitFailure) Retryable() bool        { return true }

type QuotaExceededFailure struct {
	Message          string
	ProviderMetadata ProviderMetadata
	HTTP             *HTTPContext
}

func (QuotaExceededFailure) FailureTag() FailureTag { return FailureQuotaExceeded }
func (QuotaExceededFailure) Retryable() bool        { return false }

type ContentPolicyFailure struct {
	Message          string
	ProviderMetadata ProviderMetadata
	HTTP             *HTTPContext
}

func (ContentPolicyFailure) FailureTag() FailureTag { return FailureContentPolicy }
func (ContentPolicyFailure) Retryable() bool        { return false }

type ProviderInternalFailure struct {
	Message          string
	Status           float64
	RetryAfterMS     *float64
	ProviderMetadata ProviderMetadata
	HTTP             *HTTPContext
}

func (ProviderInternalFailure) FailureTag() FailureTag { return FailureProviderInternal }
func (ProviderInternalFailure) Retryable() bool        { return true }

type TransportFailure struct {
	Message string
	Kind    *string
	URL     *string
	HTTP    *HTTPContext
}

func (TransportFailure) FailureTag() FailureTag { return FailureTransport }
func (TransportFailure) Retryable() bool        { return false }

type InvalidProviderOutputFailure struct {
	Message          string
	Route            *string
	Raw              *string
	ProviderMetadata ProviderMetadata
}

func (InvalidProviderOutputFailure) FailureTag() FailureTag { return FailureInvalidProviderOutput }
func (InvalidProviderOutputFailure) Retryable() bool        { return false }

type UnknownProviderFailure struct {
	Message          string
	Status           *float64
	ProviderMetadata ProviderMetadata
	HTTP             *HTTPContext
}

func (UnknownProviderFailure) FailureTag() FailureTag { return FailureUnknownProvider }
func (UnknownProviderFailure) Retryable() bool        { return false }

type Failure struct {
	Module string
	Method string
	Reason FailureReason
}

func (failure Failure) Retryable() bool {
	return failure.Reason != nil && failure.Reason.Retryable()
}
