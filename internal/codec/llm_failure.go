package codec

import (
	"errors"
	"fmt"

	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/domain/llm"
)

func DecodeLLMFailureJSON(content []byte) (llm.Failure, error) {
	object, err := decodeContractObject(content, "LLM failure")
	if err != nil {
		return llm.Failure{}, err
	}
	if err := rejectUnknownContractFields(object, "LLM failure", "module", "method", "reason"); err != nil {
		return llm.Failure{}, err
	}
	module, err := requiredContractString(object, "module", "LLM failure")
	if err != nil {
		return llm.Failure{}, err
	}
	method, err := requiredContractString(object, "method", "LLM failure")
	if err != nil {
		return llm.Failure{}, err
	}
	reasonValue, err := requireContractValue(object, "reason", "LLM failure")
	if err != nil {
		return llm.Failure{}, err
	}
	if reasonValue.Kind != domain.JSONKindObject {
		return llm.Failure{}, errors.New("LLM failure reason must be an object")
	}
	reason, err := decodeLLMFailureReason(reasonValue.Object)
	if err != nil {
		return llm.Failure{}, err
	}
	return llm.Failure{Module: module, Method: method, Reason: reason}, nil
}

func EncodeLLMFailureJSON(failure llm.Failure) ([]byte, error) {
	if failure.Reason == nil {
		return nil, errors.New("LLM failure reason is nil")
	}
	reason, err := encodeLLMFailureReason(failure.Reason)
	if err != nil {
		return nil, err
	}
	encoded, err := EncodeJSONValue(domain.JSONObject(map[string]domain.JSONValue{
		"module": domain.JSONString(failure.Module), "method": domain.JSONString(failure.Method), "reason": reason,
	}))
	if err != nil {
		return nil, err
	}
	if _, err := DecodeLLMFailureJSON(encoded); err != nil {
		return nil, fmt.Errorf("validate encoded LLM failure: %w", err)
	}
	return encoded, nil
}

func decodeLLMFailureReason(object map[string]domain.JSONValue) (llm.FailureReason, error) {
	tagValue, err := requiredContractString(object, "_tag", "LLM failure reason")
	if err != nil {
		return nil, err
	}
	switch llm.FailureTag(tagValue) {
	case llm.FailureInvalidRequest:
		return decodeInvalidRequestFailure(object)
	case llm.FailureNoRoute:
		return decodeNoRouteFailure(object)
	case llm.FailureAuthentication:
		return decodeAuthenticationFailure(object)
	case llm.FailureRateLimit:
		return decodeRateLimitFailure(object)
	case llm.FailureQuotaExceeded:
		return decodeQuotaExceededFailure(object)
	case llm.FailureContentPolicy:
		return decodeContentPolicyFailure(object)
	case llm.FailureProviderInternal:
		return decodeProviderInternalFailure(object)
	case llm.FailureTransport:
		return decodeTransportFailure(object)
	case llm.FailureInvalidProviderOutput:
		return decodeInvalidProviderOutputFailure(object)
	case llm.FailureUnknownProvider:
		return decodeUnknownProviderFailure(object)
	default:
		return nil, fmt.Errorf("unknown LLM failure reason tag %q", tagValue)
	}
}

func decodeInvalidRequestFailure(object map[string]domain.JSONValue) (llm.FailureReason, error) {
	if err := rejectUnknownContractFields(object, "InvalidRequest reason", "_tag", "message", "parameter", "classification", "providerMetadata", "http"); err != nil {
		return nil, err
	}
	message, err := requiredContractString(object, "message", "InvalidRequest reason")
	if err != nil {
		return nil, err
	}
	parameter, err := optionalContractString(object, "parameter", "InvalidRequest reason")
	if err != nil {
		return nil, err
	}
	classification, err := decodeFailureClassification(object, "InvalidRequest reason")
	if err != nil {
		return nil, err
	}
	metadata, err := optionalEventMetadata(object)
	if err != nil {
		return nil, err
	}
	http, err := decodeOptionalHTTPContext(object)
	if err != nil {
		return nil, err
	}
	return llm.InvalidRequestFailure{Message: message, Parameter: parameter, Classification: classification, ProviderMetadata: metadata, HTTP: http}, nil
}

func decodeNoRouteFailure(object map[string]domain.JSONValue) (llm.FailureReason, error) {
	if err := rejectUnknownContractFields(object, "NoRoute reason", "_tag", "route", "provider", "model"); err != nil {
		return nil, err
	}
	route, err := requiredContractString(object, "route", "NoRoute reason")
	if err != nil {
		return nil, err
	}
	provider, err := requiredContractString(object, "provider", "NoRoute reason")
	if err != nil {
		return nil, err
	}
	model, err := requiredContractString(object, "model", "NoRoute reason")
	if err != nil {
		return nil, err
	}
	return llm.NoRouteFailure{Route: route, Provider: provider, Model: model}, nil
}

func decodeAuthenticationFailure(object map[string]domain.JSONValue) (llm.FailureReason, error) {
	if err := rejectUnknownContractFields(object, "Authentication reason", "_tag", "message", "kind", "providerMetadata", "http"); err != nil {
		return nil, err
	}
	message, err := requiredContractString(object, "message", "Authentication reason")
	if err != nil {
		return nil, err
	}
	kindValue, err := requiredContractString(object, "kind", "Authentication reason")
	if err != nil {
		return nil, err
	}
	kind := llm.AuthenticationKind(kindValue)
	if err := validateAuthenticationKind(kind); err != nil {
		return nil, err
	}
	metadata, err := optionalEventMetadata(object)
	if err != nil {
		return nil, err
	}
	http, err := decodeOptionalHTTPContext(object)
	if err != nil {
		return nil, err
	}
	return llm.AuthenticationFailure{Message: message, Kind: kind, ProviderMetadata: metadata, HTTP: http}, nil
}

func decodeRateLimitFailure(object map[string]domain.JSONValue) (llm.FailureReason, error) {
	if err := rejectUnknownContractFields(object, "RateLimit reason", "_tag", "message", "retryAfterMs", "rateLimit", "providerMetadata", "http"); err != nil {
		return nil, err
	}
	message, err := requiredContractString(object, "message", "RateLimit reason")
	if err != nil {
		return nil, err
	}
	retryAfter, err := optionalContractNumber(object, "retryAfterMs", "RateLimit reason")
	if err != nil {
		return nil, err
	}
	rateLimit, err := decodeOptionalRateLimit(object, "rateLimit", "RateLimit reason")
	if err != nil {
		return nil, err
	}
	metadata, err := optionalEventMetadata(object)
	if err != nil {
		return nil, err
	}
	http, err := decodeOptionalHTTPContext(object)
	if err != nil {
		return nil, err
	}
	return llm.RateLimitFailure{Message: message, RetryAfterMS: retryAfter, RateLimit: rateLimit, ProviderMetadata: metadata, HTTP: http}, nil
}

func decodeQuotaExceededFailure(object map[string]domain.JSONValue) (llm.FailureReason, error) {
	message, metadata, http, err := decodeMessageMetadataHTTP(object, "QuotaExceeded reason", llm.FailureQuotaExceeded)
	if err != nil {
		return nil, err
	}
	return llm.QuotaExceededFailure{Message: message, ProviderMetadata: metadata, HTTP: http}, nil
}

func decodeContentPolicyFailure(object map[string]domain.JSONValue) (llm.FailureReason, error) {
	message, metadata, http, err := decodeMessageMetadataHTTP(object, "ContentPolicy reason", llm.FailureContentPolicy)
	if err != nil {
		return nil, err
	}
	return llm.ContentPolicyFailure{Message: message, ProviderMetadata: metadata, HTTP: http}, nil
}

func decodeMessageMetadataHTTP(object map[string]domain.JSONValue, label string, _ llm.FailureTag) (string, llm.ProviderMetadata, *llm.HTTPContext, error) {
	if err := rejectUnknownContractFields(object, label, "_tag", "message", "providerMetadata", "http"); err != nil {
		return "", nil, nil, err
	}
	message, err := requiredContractString(object, "message", label)
	if err != nil {
		return "", nil, nil, err
	}
	metadata, err := optionalEventMetadata(object)
	if err != nil {
		return "", nil, nil, err
	}
	http, err := decodeOptionalHTTPContext(object)
	return message, metadata, http, err
}

func decodeProviderInternalFailure(object map[string]domain.JSONValue) (llm.FailureReason, error) {
	if err := rejectUnknownContractFields(object, "ProviderInternal reason", "_tag", "message", "status", "retryAfterMs", "providerMetadata", "http"); err != nil {
		return nil, err
	}
	message, err := requiredContractString(object, "message", "ProviderInternal reason")
	if err != nil {
		return nil, err
	}
	status, err := requiredContractNumber(object, "status", "ProviderInternal reason")
	if err != nil {
		return nil, err
	}
	retryAfter, err := optionalContractNumber(object, "retryAfterMs", "ProviderInternal reason")
	if err != nil {
		return nil, err
	}
	metadata, err := optionalEventMetadata(object)
	if err != nil {
		return nil, err
	}
	http, err := decodeOptionalHTTPContext(object)
	if err != nil {
		return nil, err
	}
	return llm.ProviderInternalFailure{Message: message, Status: status, RetryAfterMS: retryAfter, ProviderMetadata: metadata, HTTP: http}, nil
}

func decodeTransportFailure(object map[string]domain.JSONValue) (llm.FailureReason, error) {
	if err := rejectUnknownContractFields(object, "Transport reason", "_tag", "message", "kind", "url", "http"); err != nil {
		return nil, err
	}
	message, err := requiredContractString(object, "message", "Transport reason")
	if err != nil {
		return nil, err
	}
	kind, err := optionalContractString(object, "kind", "Transport reason")
	if err != nil {
		return nil, err
	}
	url, err := optionalContractString(object, "url", "Transport reason")
	if err != nil {
		return nil, err
	}
	http, err := decodeOptionalHTTPContext(object)
	if err != nil {
		return nil, err
	}
	return llm.TransportFailure{Message: message, Kind: kind, URL: url, HTTP: http}, nil
}

func decodeInvalidProviderOutputFailure(object map[string]domain.JSONValue) (llm.FailureReason, error) {
	if err := rejectUnknownContractFields(object, "InvalidProviderOutput reason", "_tag", "message", "route", "raw", "providerMetadata"); err != nil {
		return nil, err
	}
	message, err := requiredContractString(object, "message", "InvalidProviderOutput reason")
	if err != nil {
		return nil, err
	}
	route, err := optionalContractString(object, "route", "InvalidProviderOutput reason")
	if err != nil {
		return nil, err
	}
	raw, err := optionalContractString(object, "raw", "InvalidProviderOutput reason")
	if err != nil {
		return nil, err
	}
	metadata, err := optionalEventMetadata(object)
	if err != nil {
		return nil, err
	}
	return llm.InvalidProviderOutputFailure{Message: message, Route: route, Raw: raw, ProviderMetadata: metadata}, nil
}

func decodeUnknownProviderFailure(object map[string]domain.JSONValue) (llm.FailureReason, error) {
	if err := rejectUnknownContractFields(object, "UnknownProvider reason", "_tag", "message", "status", "providerMetadata", "http"); err != nil {
		return nil, err
	}
	message, err := requiredContractString(object, "message", "UnknownProvider reason")
	if err != nil {
		return nil, err
	}
	status, err := optionalContractNumber(object, "status", "UnknownProvider reason")
	if err != nil {
		return nil, err
	}
	metadata, err := optionalEventMetadata(object)
	if err != nil {
		return nil, err
	}
	http, err := decodeOptionalHTTPContext(object)
	if err != nil {
		return nil, err
	}
	return llm.UnknownProviderFailure{Message: message, Status: status, ProviderMetadata: metadata, HTTP: http}, nil
}

func encodeLLMFailureReason(reason llm.FailureReason) (domain.JSONValue, error) {
	object := map[string]domain.JSONValue{"_tag": domain.JSONString(string(reason.FailureTag()))}
	var err error
	switch reason := reason.(type) {
	case llm.InvalidRequestFailure:
		object["message"] = domain.JSONString(reason.Message)
		addOptionalContractString(object, "parameter", reason.Parameter)
		if reason.Classification != nil {
			object["classification"] = domain.JSONString(string(*reason.Classification))
		}
		err = addMetadataAndHTTP(object, reason.ProviderMetadata, reason.HTTP)
	case llm.NoRouteFailure:
		object["route"] = domain.JSONString(reason.Route)
		object["provider"] = domain.JSONString(reason.Provider)
		object["model"] = domain.JSONString(reason.Model)
	case llm.AuthenticationFailure:
		if err := validateAuthenticationKind(reason.Kind); err != nil {
			return domain.JSONValue{}, err
		}
		object["message"] = domain.JSONString(reason.Message)
		object["kind"] = domain.JSONString(string(reason.Kind))
		err = addMetadataAndHTTP(object, reason.ProviderMetadata, reason.HTTP)
	case llm.RateLimitFailure:
		object["message"] = domain.JSONString(reason.Message)
		addOptionalContractNumber(object, "retryAfterMs", reason.RetryAfterMS)
		if reason.RateLimit != nil {
			object["rateLimit"], err = encodeRateLimit(*reason.RateLimit)
		}
		if err == nil {
			err = addMetadataAndHTTP(object, reason.ProviderMetadata, reason.HTTP)
		}
	case llm.QuotaExceededFailure:
		object["message"] = domain.JSONString(reason.Message)
		err = addMetadataAndHTTP(object, reason.ProviderMetadata, reason.HTTP)
	case llm.ContentPolicyFailure:
		object["message"] = domain.JSONString(reason.Message)
		err = addMetadataAndHTTP(object, reason.ProviderMetadata, reason.HTTP)
	case llm.ProviderInternalFailure:
		object["message"] = domain.JSONString(reason.Message)
		object["status"] = jsonNumber(reason.Status)
		addOptionalContractNumber(object, "retryAfterMs", reason.RetryAfterMS)
		err = addMetadataAndHTTP(object, reason.ProviderMetadata, reason.HTTP)
	case llm.TransportFailure:
		object["message"] = domain.JSONString(reason.Message)
		addOptionalContractString(object, "kind", reason.Kind)
		addOptionalContractString(object, "url", reason.URL)
		if reason.HTTP != nil {
			object["http"], err = encodeHTTPContext(*reason.HTTP)
		}
	case llm.InvalidProviderOutputFailure:
		object["message"] = domain.JSONString(reason.Message)
		addOptionalContractString(object, "route", reason.Route)
		addOptionalContractString(object, "raw", reason.Raw)
		err = addProviderMetadata(object, reason.ProviderMetadata)
	case llm.UnknownProviderFailure:
		object["message"] = domain.JSONString(reason.Message)
		addOptionalContractNumber(object, "status", reason.Status)
		err = addMetadataAndHTTP(object, reason.ProviderMetadata, reason.HTTP)
	default:
		return domain.JSONValue{}, fmt.Errorf("unsupported LLM failure reason %T", reason)
	}
	if err != nil {
		return domain.JSONValue{}, err
	}
	return domain.JSONObject(object), nil
}

func decodeFailureClassification(object map[string]domain.JSONValue, label string) (*llm.ProviderFailureClassification, error) {
	value, present := object["classification"]
	if !present {
		return nil, nil
	}
	if value.Kind != domain.JSONKindString || value.String != string(llm.ProviderFailureContextOverflow) {
		return nil, fmt.Errorf("%s classification is invalid", label)
	}
	classification := llm.ProviderFailureClassification(value.String)
	return &classification, nil
}

func validateAuthenticationKind(kind llm.AuthenticationKind) error {
	return validateContractEnum(string(kind), "authentication kind",
		string(llm.AuthenticationMissing), string(llm.AuthenticationInvalid), string(llm.AuthenticationExpired),
		string(llm.AuthenticationInsufficientPermissions), string(llm.AuthenticationUnknown))
}

func decodeOptionalHTTPContext(object map[string]domain.JSONValue) (*llm.HTTPContext, error) {
	httpObject, present, err := optionalContractObject(object, "http", "LLM failure reason")
	if err != nil || !present {
		return nil, err
	}
	if err := rejectUnknownContractFields(httpObject, "HTTP context", "request", "response", "body", "bodyTruncated", "requestId", "rateLimit"); err != nil {
		return nil, err
	}
	requestValue, err := requireContractValue(httpObject, "request", "HTTP context")
	if err != nil || requestValue.Kind != domain.JSONKindObject {
		if err == nil {
			err = errors.New("HTTP context request must be an object")
		}
		return nil, err
	}
	request, err := decodeHTTPRequest(requestValue.Object)
	if err != nil {
		return nil, err
	}
	var response *llm.HTTPResponseDetails
	if responseObject, present, err := optionalContractObject(httpObject, "response", "HTTP context"); err != nil {
		return nil, err
	} else if present {
		value, err := decodeHTTPResponse(responseObject)
		if err != nil {
			return nil, err
		}
		response = &value
	}
	body, err := optionalContractString(httpObject, "body", "HTTP context")
	if err != nil {
		return nil, err
	}
	bodyTruncated, err := optionalContractBool(httpObject, "bodyTruncated", "HTTP context")
	if err != nil {
		return nil, err
	}
	requestID, err := optionalContractString(httpObject, "requestId", "HTTP context")
	if err != nil {
		return nil, err
	}
	rateLimit, err := decodeOptionalRateLimit(httpObject, "rateLimit", "HTTP context")
	if err != nil {
		return nil, err
	}
	return &llm.HTTPContext{Request: request, Response: response, Body: body, BodyTruncated: bodyTruncated, RequestID: requestID, RateLimit: rateLimit}, nil
}

func decodeHTTPRequest(object map[string]domain.JSONValue) (llm.HTTPRequestDetails, error) {
	if err := rejectUnknownContractFields(object, "HTTP request", "method", "url", "headers"); err != nil {
		return llm.HTTPRequestDetails{}, err
	}
	method, err := requiredContractString(object, "method", "HTTP request")
	if err != nil {
		return llm.HTTPRequestDetails{}, err
	}
	url, err := requiredContractString(object, "url", "HTTP request")
	if err != nil {
		return llm.HTTPRequestDetails{}, err
	}
	headersValue, err := requireContractValue(object, "headers", "HTTP request")
	if err != nil {
		return llm.HTTPRequestDetails{}, err
	}
	headers, err := decodeContractStringMap(headersValue, "HTTP request headers")
	if err != nil {
		return llm.HTTPRequestDetails{}, err
	}
	return llm.HTTPRequestDetails{Method: method, URL: url, Headers: headers}, nil
}

func decodeHTTPResponse(object map[string]domain.JSONValue) (llm.HTTPResponseDetails, error) {
	if err := rejectUnknownContractFields(object, "HTTP response", "status", "headers"); err != nil {
		return llm.HTTPResponseDetails{}, err
	}
	status, err := requiredContractNumber(object, "status", "HTTP response")
	if err != nil {
		return llm.HTTPResponseDetails{}, err
	}
	headersValue, err := requireContractValue(object, "headers", "HTTP response")
	if err != nil {
		return llm.HTTPResponseDetails{}, err
	}
	headers, err := decodeContractStringMap(headersValue, "HTTP response headers")
	if err != nil {
		return llm.HTTPResponseDetails{}, err
	}
	return llm.HTTPResponseDetails{Status: status, Headers: headers}, nil
}

func decodeOptionalRateLimit(object map[string]domain.JSONValue, field string, label string) (*llm.HTTPRateLimitDetails, error) {
	rateLimitObject, present, err := optionalContractObject(object, field, label)
	if err != nil || !present {
		return nil, err
	}
	if err := rejectUnknownContractFields(rateLimitObject, "HTTP rate limit", "retryAfterMs", "limit", "remaining", "reset"); err != nil {
		return nil, err
	}
	retryAfter, err := optionalContractNumber(rateLimitObject, "retryAfterMs", "HTTP rate limit")
	if err != nil {
		return nil, err
	}
	limit, err := decodeOptionalStringMap(rateLimitObject, "limit")
	if err != nil {
		return nil, err
	}
	remaining, err := decodeOptionalStringMap(rateLimitObject, "remaining")
	if err != nil {
		return nil, err
	}
	reset, err := decodeOptionalStringMap(rateLimitObject, "reset")
	if err != nil {
		return nil, err
	}
	return &llm.HTTPRateLimitDetails{RetryAfterMS: retryAfter, Limit: limit, Remaining: remaining, Reset: reset}, nil
}

func decodeOptionalStringMap(object map[string]domain.JSONValue, field string) (map[string]string, error) {
	value, present := object[field]
	if !present {
		return nil, nil
	}
	return decodeContractStringMap(value, "HTTP rate limit "+field)
}

func encodeHTTPContext(http llm.HTTPContext) (domain.JSONValue, error) {
	object := map[string]domain.JSONValue{
		"request": domain.JSONObject(map[string]domain.JSONValue{
			"method": domain.JSONString(http.Request.Method), "url": domain.JSONString(http.Request.URL),
			"headers": contractStringMap(http.Request.Headers),
		}),
	}
	if http.Response != nil {
		object["response"] = domain.JSONObject(map[string]domain.JSONValue{
			"status": jsonNumber(http.Response.Status), "headers": contractStringMap(http.Response.Headers),
		})
	}
	addOptionalContractString(object, "body", http.Body)
	contractOptionalBool(object, "bodyTruncated", http.BodyTruncated)
	addOptionalContractString(object, "requestId", http.RequestID)
	if http.RateLimit != nil {
		value, err := encodeRateLimit(*http.RateLimit)
		if err != nil {
			return domain.JSONValue{}, err
		}
		object["rateLimit"] = value
	}
	return domain.JSONObject(object), nil
}

func encodeRateLimit(rateLimit llm.HTTPRateLimitDetails) (domain.JSONValue, error) {
	object := make(map[string]domain.JSONValue)
	addOptionalContractNumber(object, "retryAfterMs", rateLimit.RetryAfterMS)
	if rateLimit.Limit != nil {
		object["limit"] = contractStringMap(rateLimit.Limit)
	}
	if rateLimit.Remaining != nil {
		object["remaining"] = contractStringMap(rateLimit.Remaining)
	}
	if rateLimit.Reset != nil {
		object["reset"] = contractStringMap(rateLimit.Reset)
	}
	value := domain.JSONObject(object)
	if err := value.Validate(); err != nil {
		return domain.JSONValue{}, err
	}
	return value, nil
}

func addMetadataAndHTTP(object map[string]domain.JSONValue, metadata llm.ProviderMetadata, http *llm.HTTPContext) error {
	if err := addProviderMetadata(object, metadata); err != nil {
		return err
	}
	if http != nil {
		value, err := encodeHTTPContext(*http)
		if err != nil {
			return err
		}
		object["http"] = value
	}
	return nil
}

func addOptionalContractString(object map[string]domain.JSONValue, field string, value *string) {
	if value != nil {
		object[field] = domain.JSONString(*value)
	}
}

func addOptionalContractNumber(object map[string]domain.JSONValue, field string, value *float64) {
	if value != nil {
		object[field] = jsonNumber(*value)
	}
}
