package client

import (
	"context"
	"math/rand"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RetryPolicy defines configuration for exponential backoff and retry behavior.
type RetryPolicy struct {
	MaxAttempts      int
	InitialInterval  time.Duration
	MaxInterval      time.Duration
	AttemptTimeout   time.Duration
	OperationTimeout time.Duration
	IsIdempotent     bool
	Sleep            func(ctx context.Context, d time.Duration) error
}

// DefaultReadPolicy returns the standard retry policy for idempotent read operations (30s operation, 15s per attempt, at most 4 attempts).
func DefaultReadPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:      4,
		InitialInterval:  250 * time.Millisecond,
		MaxInterval:      5 * time.Second,
		AttemptTimeout:   15 * time.Second,
		OperationTimeout: 30 * time.Second,
		IsIdempotent:     true,
		Sleep:            defaultSleep,
	}
}

// DefaultPaginatedReadPolicy returns the retry policy for paginated read operations (2m operation, 15s per attempt, at most 4 attempts).
func DefaultPaginatedReadPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:      4,
		InitialInterval:  250 * time.Millisecond,
		MaxInterval:      5 * time.Second,
		AttemptTimeout:   15 * time.Second,
		OperationTimeout: 2 * time.Minute,
		IsIdempotent:     true,
		Sleep:            defaultSleep,
	}
}

// DefaultMutatingPolicy returns the policy for mutating operations (fail-fast, 1 attempt, 60s timeout).
func DefaultMutatingPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:      1,
		AttemptTimeout:   60 * time.Second,
		OperationTimeout: 60 * time.Second,
		IsIdempotent:     false,
		Sleep:            defaultSleep,
	}
}

// TokenIssuancePolicy returns the policy for token issuance operations (fail-fast, 1 attempt, 30s timeout).
func TokenIssuancePolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:      1,
		AttemptTimeout:   30 * time.Second,
		OperationTimeout: 30 * time.Second,
		IsIdempotent:     false,
		Sleep:            defaultSleep,
	}
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// ExecuteIdempotentRead executes a read operation with DefaultReadPolicy.
func ExecuteIdempotentRead[T any](ctx context.Context, op func(callCtx context.Context) (T, error)) (T, error) {
	return ExecuteWithRetry(ctx, DefaultReadPolicy(), op)
}

// ExecutePaginatedRead executes a paginated read operation with DefaultPaginatedReadPolicy.
func ExecutePaginatedRead[T any](ctx context.Context, op func(callCtx context.Context) (T, error)) (T, error) {
	return ExecuteWithRetry(ctx, DefaultPaginatedReadPolicy(), op)
}

// ExecuteMutating executes a mutating operation with DefaultMutatingPolicy.
func ExecuteMutating[T any](ctx context.Context, op func(callCtx context.Context) (T, error)) (T, error) {
	return ExecuteWithRetry(ctx, DefaultMutatingPolicy(), op)
}

// ExecuteTokenIssuance executes a token issuance operation with TokenIssuancePolicy (30s deadline).
func ExecuteTokenIssuance[T any](ctx context.Context, op func(callCtx context.Context) (T, error)) (T, error) {
	return ExecuteWithRetry(ctx, TokenIssuancePolicy(), op)
}

// ExecuteWithRetry executes an operation according to the given RetryPolicy.
func ExecuteWithRetry[T any](ctx context.Context, policy RetryPolicy, op func(ctx context.Context) (T, error)) (T, error) {
	var zero T

	if policy.MaxAttempts <= 1 || !policy.IsIdempotent {
		opTimeout := policy.OperationTimeout
		if opTimeout <= 0 {
			opTimeout = 60 * time.Second
		}
		opCtx, cancel := context.WithTimeout(ctx, opTimeout)
		defer cancel()

		return op(opCtx)
	}

	opTimeout := policy.OperationTimeout
	if opTimeout <= 0 {
		opTimeout = 30 * time.Second
	}
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	sleepFn := policy.Sleep
	if sleepFn == nil {
		sleepFn = defaultSleep
	}

	currentCeiling := policy.InitialInterval
	if currentCeiling <= 0 {
		currentCeiling = 250 * time.Millisecond
	}
	maxInterval := policy.MaxInterval
	if maxInterval <= 0 {
		maxInterval = 5 * time.Second
	}

	var lastErr error

	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		attemptTimeout := policy.AttemptTimeout
		if attemptTimeout <= 0 {
			attemptTimeout = 15 * time.Second
		}

		attemptCtx, attemptCancel := context.WithTimeout(opCtx, attemptTimeout)
		res, err := op(attemptCtx)
		attemptExpired := (attemptCtx.Err() == context.DeadlineExceeded)
		attemptCancel()

		if err == nil {
			return res, nil
		}

		lastErr = err

		if attempt == policy.MaxAttempts {
			break
		}

		if opCtx.Err() != nil {
			break
		}

		// Check if error is retryable
		retryable, retryDelay := isRetryable(err)
		if !retryable {
			// Only retry DeadlineExceeded when local attempt timer expired while overall opCtx has remaining budget
			if attemptExpired && opCtx.Err() == nil {
				retryDelay = 0
			} else {
				break
			}
		}

		// Calculate backoff delay
		var delay time.Duration
		if retryDelay > 0 {
			if deadline, ok := opCtx.Deadline(); ok && time.Until(deadline) < retryDelay {
				break
			}
			delay = retryDelay
		} else {
			// Full jitter exponential backoff: sleep in [0, currentCeiling)
			delay = time.Duration(rand.Float64() * float64(currentCeiling))
			currentCeiling *= 2
			if currentCeiling > maxInterval {
				currentCeiling = maxInterval
			}
		}

		if sleepErr := sleepFn(opCtx, delay); sleepErr != nil {
			if opCtx.Err() != nil {
				return zero, opCtx.Err()
			}
			return zero, sleepErr
		}
	}

	return zero, lastErr
}

func isRetryable(err error) (bool, time.Duration) {
	st, ok := status.FromError(err)
	if !ok {
		return false, 0
	}

	switch st.Code() {
	case codes.Unavailable:
		return true, 0
	case codes.ResourceExhausted:
		// Only retry ResourceExhausted if RetryInfo is present
		for _, detail := range st.Details() {
			if ri, ok := detail.(*errdetails.RetryInfo); ok && ri.RetryDelay != nil {
				return true, ri.RetryDelay.AsDuration()
			}
		}
		return false, 0
	default:
		return false, 0
	}
}
