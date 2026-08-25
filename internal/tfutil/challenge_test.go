package tfutil_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

// =============================================================================
// CHALLENGE 3: PAGINATION LOOP ADVERSARIAL STRESS TESTS
// =============================================================================

func TestChallenge_Pagination_CircularCursorLoops(t *testing.T) {
	ctx := context.Background()

	t.Run("Direct 1-step cycle (A -> A)", func(t *testing.T) {
		fetcher := func(_ context.Context, token string) ([]string, string, error) {
			return []string{"item_a"}, "token_a", nil
		}

		_, err := tfutil.PaginateAll(ctx, fetcher, tfutil.WithInitialToken("token_a"))
		if !errors.Is(err, tfutil.ErrPaginationCycleDetected) {
			t.Fatalf("expected ErrPaginationCycleDetected, got: %v", err)
		}
	})

	t.Run("2-step cycle ('' -> A -> B -> A)", func(t *testing.T) {
		fetcher := func(_ context.Context, token string) ([]string, string, error) {
			switch token {
			case "":
				return []string{"item1"}, "token_a", nil
			case "token_a":
				return []string{"item2"}, "token_b", nil
			case "token_b":
				return []string{"item3"}, "token_a", nil // Cycle back to token_a!
			default:
				return nil, "", errors.New("unexpected token")
			}
		}

		_, err := tfutil.PaginateAll(ctx, fetcher)
		if !errors.Is(err, tfutil.ErrPaginationCycleDetected) {
			t.Fatalf("expected ErrPaginationCycleDetected, got: %v", err)
		}
	})

	t.Run("5-step cycle with arbitrary tokens ('' -> p1 -> p2 -> p3 -> p4 -> p5 -> p3)", func(t *testing.T) {
		tokens := []string{"p1", "p2", "p3", "p4", "p5"}
		fetcher := func(_ context.Context, token string) ([]string, string, error) {
			switch token {
			case "":
				return []string{"item0"}, tokens[0], nil
			case "p1":
				return []string{"item1"}, tokens[1], nil
			case "p2":
				return []string{"item2"}, tokens[2], nil
			case "p3":
				return []string{"item3"}, tokens[3], nil
			case "p4":
				return []string{"item4"}, tokens[4], nil
			case "p5":
				return []string{"item5"}, "p3", nil // Cycle back to p3
			default:
				return nil, "", errors.New("unexpected token")
			}
		}

		_, err := tfutil.PaginateAll(ctx, fetcher)
		if !errors.Is(err, tfutil.ErrPaginationCycleDetected) {
			t.Fatalf("expected ErrPaginationCycleDetected, got: %v", err)
		}
	})
}

func TestChallenge_Pagination_ZeroItemsWithNonEmptyCursor(t *testing.T) {
	ctx := context.Background()

	t.Run("Empty pages interspersed with non-empty pages", func(t *testing.T) {
		// Page 0: [] (empty), nextToken: "p1"
		// Page 1: [] (empty), nextToken: "p2"
		// Page 2: ["val1", "val2"], nextToken: "p3"
		// Page 3: [] (empty), nextToken: "p4"
		// Page 4: ["val3"], nextToken: ""
		fetcher := func(_ context.Context, token string) ([]string, string, error) {
			switch token {
			case "":
				return nil, "p1", nil
			case "p1":
				return []string{}, "p2", nil
			case "p2":
				return []string{"val1", "val2"}, "p3", nil
			case "p3":
				return []string{}, "p4", nil
			case "p4":
				return []string{"val3"}, "", nil
			default:
				return nil, "", errors.New("unknown token")
			}
		}

		items, err := tfutil.PaginateAll(ctx, fetcher)
		if err != nil {
			t.Fatalf("unexpected error during empty-page pagination: %v", err)
		}
		if len(items) != 3 {
			t.Fatalf("expected 3 items, got %d: %v", len(items), items)
		}
		if items[0] != "val1" || items[1] != "val2" || items[2] != "val3" {
			t.Errorf("unexpected items content: %v", items)
		}
	})

	t.Run("All empty pages until terminal empty token", func(t *testing.T) {
		fetcher := func(_ context.Context, token string) ([]string, string, error) {
			switch token {
			case "":
				return nil, "p1", nil
			case "p1":
				return []string{}, "p2", nil
			case "p2":
				return nil, "", nil
			default:
				return nil, "", errors.New("unknown token")
			}
		}

		items, err := tfutil.PaginateAll(ctx, fetcher)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(items) != 0 {
			t.Fatalf("expected 0 items, got %d", len(items))
		}
	})
}

func TestChallenge_Pagination_MaxPageBounds(t *testing.T) {
	ctx := context.Background()

	t.Run("Strict MaxPages limit enforced exactly", func(t *testing.T) {
		var pagesFetched int32
		fetcher := func(_ context.Context, token string) ([]int, string, error) {
			pg := atomic.AddInt32(&pagesFetched, 1)
			return []int{int(pg)}, fmt.Sprintf("page_%d", pg), nil
		}

		_, err := tfutil.PaginateAll(ctx, fetcher, tfutil.WithMaxPages(7))
		if !errors.Is(err, tfutil.ErrMaxPagesExceeded) {
			t.Fatalf("expected ErrMaxPagesExceeded, got: %v", err)
		}
		if pagesFetched != 7 {
			t.Fatalf("expected exactly 7 pages fetched before limit triggered, got %d", pagesFetched)
		}
	})

	t.Run("DefaultMaxPages fallback when MaxPages <= 0", func(t *testing.T) {
		var pagesFetched int32
		fetcher := func(_ context.Context, token string) ([]int, string, error) {
			pg := atomic.AddInt32(&pagesFetched, 1)
			return []int{int(pg)}, fmt.Sprintf("page_%d", pg), nil
		}

		_, err := tfutil.PaginateAll(ctx, fetcher, tfutil.WithMaxPages(0))
		if !errors.Is(err, tfutil.ErrMaxPagesExceeded) {
			t.Fatalf("expected ErrMaxPagesExceeded for default max pages, got: %v", err)
		}
		if pagesFetched != tfutil.DefaultMaxPages {
			t.Fatalf("expected exactly DefaultMaxPages (%d) fetched, got %d", tfutil.DefaultMaxPages, pagesFetched)
		}
	})

	t.Run("Negative MaxPages resets to DefaultMaxPages", func(t *testing.T) {
		var pagesFetched int32
		fetcher := func(_ context.Context, token string) ([]int, string, error) {
			pg := atomic.AddInt32(&pagesFetched, 1)
			return []int{int(pg)}, fmt.Sprintf("page_%d", pg), nil
		}

		_, err := tfutil.PaginateAll(ctx, fetcher, tfutil.WithMaxPages(-10))
		if !errors.Is(err, tfutil.ErrMaxPagesExceeded) {
			t.Fatalf("expected ErrMaxPagesExceeded for negative max pages, got: %v", err)
		}
		if pagesFetched != tfutil.DefaultMaxPages {
			t.Fatalf("expected DefaultMaxPages (%d) fetched, got %d", tfutil.DefaultMaxPages, pagesFetched)
		}
	})
}

func TestChallenge_Pagination_ContextAndErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("Pre-canceled context returns context.Canceled immediately without calling fetcher", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()

		var fetcherCalled bool
		fetcher := func(_ context.Context, token string) ([]string, string, error) {
			fetcherCalled = true
			return []string{"item"}, "", nil
		}

		_, err := tfutil.PaginateAll(cancelCtx, fetcher)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
		if fetcherCalled {
			t.Fatal("fetcher should not have been called on pre-canceled context")
		}
	})

	t.Run("Context canceled during page iteration stops pagination", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(ctx)

		var fetchCount int32
		fetcher := func(fCtx context.Context, token string) ([]int, string, error) {
			cnt := atomic.AddInt32(&fetchCount, 1)
			if cnt == 2 {
				cancel()
			}
			return []int{int(cnt)}, fmt.Sprintf("token_%d", cnt), nil
		}

		_, err := tfutil.PaginateAll(cancelCtx, fetcher)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
		if fetchCount != 2 {
			t.Fatalf("expected exactly 2 fetches, got %d", fetchCount)
		}
	})

	t.Run("Nil callback in ForEachPage behaves gracefully", func(t *testing.T) {
		fetcher := func(_ context.Context, token string) ([]string, string, error) {
			if token == "" {
				return []string{"a"}, "p1", nil
			}
			return []string{"b"}, "", nil
		}

		err := tfutil.ForEachPage[string](ctx, fetcher, nil)
		if err != nil {
			t.Fatalf("unexpected error with nil callback: %v", err)
		}
	})

	t.Run("Overall pagination timeout stops iteration across multiple pages", func(t *testing.T) {
		var fetchCount int32
		fetcher := func(fCtx context.Context, token string) ([]int, string, error) {
			cnt := atomic.AddInt32(&fetchCount, 1)
			time.Sleep(30 * time.Millisecond)
			return []int{int(cnt)}, fmt.Sprintf("token_%d", cnt), nil
		}

		_, err := tfutil.PaginateAll(ctx, fetcher, tfutil.WithTimeout(50*time.Millisecond))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected context.DeadlineExceeded, got: %v", err)
		}
	})
}
