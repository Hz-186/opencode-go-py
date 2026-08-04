package provider

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

var (
	ErrInvalidRetryPolicy = errors.New("invalid provider retry policy")
	ErrTransportAttempt   = errors.New("provider transport attempt failed")
)

// AttemptError is the only error shape eligible for automatic transport
// retry. StreamStarted is mandatory semantic information: once a provider has
// emitted a meaningful stream, replaying the turn could duplicate billing or
// tool calls and is therefore forbidden.
type AttemptError struct {
	Status        int
	StreamStarted bool
	RetryAfter    time.Duration
	Cause         error
}

func (err *AttemptError) Error() string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%v: status=%d stream_started=%t: %v", ErrTransportAttempt,
		err.Status, err.StreamStarted, err.Cause)
}

func (err *AttemptError) Unwrap() error {
	if err == nil {
		return nil
	}
	return errors.Join(ErrTransportAttempt, err.Cause)
}

func NewAttemptError(status int, streamStarted bool, retryAfter time.Duration, cause error) error {
	if status < 0 || status > 599 || retryAfter < 0 {
		return fmt.Errorf("%w: status or retry-after is invalid", ErrInvalidRetryPolicy)
	}
	if cause == nil {
		cause = errors.New("provider transport failed")
	}
	return &AttemptError{Status: status, StreamStarted: streamStarted, RetryAfter: retryAfter, Cause: cause}
}

// RetryPolicy bounds transport retry and keeps delay deterministic for tests
// and cassette replay. No jitter is applied here; callers needing jitter must
// do so outside the canonical retry decision and record the resulting delay.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Sleep       func(context.Context, time.Duration) error
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 3, BaseDelay: 100 * time.Millisecond, MaxDelay: 5 * time.Second}
}

func (policy RetryPolicy) validate() error {
	if policy.MaxAttempts <= 0 || policy.MaxAttempts > 16 || policy.BaseDelay < 0 || policy.MaxDelay < policy.BaseDelay || policy.MaxDelay > 10*time.Minute {
		return fmt.Errorf("%w: attempts/delay outside bounded range", ErrInvalidRetryPolicy)
	}
	return nil
}

type RetryDiagnostic struct {
	Attempt     int
	NextAttempt int
	Status      int
	Delay       time.Duration
	Reason      string
}

type RetryObserver func(RetryDiagnostic)

// RunWithRetry executes one ProviderPort turn with bounded, transport-only
// retry. The sink is shared intentionally: adapters must never emit events on
// an attempt marked StreamStarted if they expect retry to be allowed.
func RunWithRetry(
	ctx context.Context,
	provider ProviderPort,
	request ProviderTurnRequest,
	sink LLMEventSink,
	policy RetryPolicy,
	observer RetryObserver,
) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", ErrInvalidRetryPolicy)
	}
	if provider == nil || sink == nil {
		return fmt.Errorf("%w: provider and sink are required", ErrInvalidRetryPolicy)
	}
	if err := request.Validate(); err != nil {
		return err
	}
	if err := policy.validate(); err != nil {
		return err
	}
	baseAttempt := request.Attempt
	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		attemptRequest := request
		attemptRequest.Attempt = baseAttempt + attempt
		err := provider.RunTurn(ctx, attemptRequest, sink)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var attemptErr *AttemptError
		if !errors.As(err, &attemptErr) || !retryableStatus(attemptErr.Status) || attemptErr.StreamStarted || attempt+1 >= policy.MaxAttempts {
			return err
		}
		delay := policy.delay(attempt, attemptErr.RetryAfter)
		if observer != nil {
			observer(RetryDiagnostic{
				Attempt: attempt + 1, NextAttempt: attempt + 2,
				Status: attemptErr.Status, Delay: delay, Reason: "retryable transport before stream start",
			})
		}
		if err := sleepContext(ctx, delay, policy.Sleep); err != nil {
			return err
		}
	}
	return ErrTransportAttempt
}

func (policy RetryPolicy) delay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > policy.MaxDelay {
			return policy.MaxDelay
		}
		return retryAfter
	}
	if policy.BaseDelay == 0 {
		return 0
	}
	multiplier := math.Pow(2, float64(attempt))
	delay := time.Duration(float64(policy.BaseDelay) * multiplier)
	if delay < 0 || delay > policy.MaxDelay {
		return policy.MaxDelay
	}
	return delay
}

func sleepContext(ctx context.Context, delay time.Duration, sleep func(context.Context, time.Duration) error) error {
	if sleep != nil {
		return sleep(ctx, delay)
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryableStatus(status int) bool {
	switch status {
	case 408, 409, 425, 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}
