package client

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestExecuteIdempotentRead_And_ExecuteMutating_And_DefaultSleep(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// 1. ExecuteIdempotentRead retries transient Unavailable and succeeds.
	var readAttempts atomic.Int32
	got, err := ExecuteIdempotentRead(ctx, func(callCtx context.Context) (string, error) {
		if _, ok := callCtx.Deadline(); !ok {
			t.Fatal("expected ExecuteIdempotentRead to set deadline on callCtx")
		}
		if readAttempts.Add(1) == 1 {
			return "", status.Error(codes.Unavailable, "transient")
		}
		return "ok-read", nil
	})
	if err != nil || got != "ok-read" || readAttempts.Load() != 2 {
		t.Fatalf("ExecuteIdempotentRead got (%q, %v) attempts=%d", got, err, readAttempts.Load())
	}

	// 2. ExecuteMutating does not retry Unavailable (fail-fast single attempt).
	var mutAttempts atomic.Int32
	_, err = ExecuteMutating(ctx, func(callCtx context.Context) (string, error) {
		if _, ok := callCtx.Deadline(); !ok {
			t.Fatal("expected ExecuteMutating to set deadline on callCtx")
		}
		mutAttempts.Add(1)
		return "", status.Error(codes.Unavailable, "fail-fast")
	})
	if status.Code(err) != codes.Unavailable || mutAttempts.Load() != 1 {
		t.Fatalf("ExecuteMutating expected 1 attempt Unavailable, got err=%v attempts=%d", err, mutAttempts.Load())
	}

	// 3. defaultSleep completes normally and aborts on context cancellation.
	if err := defaultSleep(ctx, time.Millisecond); err != nil {
		t.Fatalf("expected defaultSleep(1ms) to succeed, got %v", err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := defaultSleep(cancelCtx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled from defaultSleep, got %v", err)
	}
}
