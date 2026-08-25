package tfutil

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrPaginationCycleDetected is returned when the pagination loop receives a page token it has already processed.
	ErrPaginationCycleDetected = errors.New("pagination cycle detected")

	// ErrMaxPagesExceeded is returned when the pagination loop reaches the configured maximum number of pages.
	ErrMaxPagesExceeded = errors.New("maximum pagination pages exceeded")
)

const (
	// DefaultMaxPages is the default limit on the number of pages fetched during pagination.
	DefaultMaxPages = 100

	// DefaultPaginationTimeout is the default overall deadline across all pages.
	DefaultPaginationTimeout = 2 * time.Minute
)

// PageFetcher retrieves a single page of items using the provided pageToken.
// It returns the slice of items for the page, the nextPageToken for the subsequent page (empty string if this is the last page),
// and any error encountered during fetching.
type PageFetcher[T any] func(ctx context.Context, pageToken string) (items []T, nextPageToken string, err error)

// PaginationOptions holds configuration parameters for pagination.
type PaginationOptions struct {
	MaxPages     int
	InitialToken string
	Timeout      time.Duration
}

// PaginationOption is a functional option to configure pagination.
type PaginationOption func(*PaginationOptions)

// WithMaxPages sets the maximum number of pages to retrieve before returning ErrMaxPagesExceeded.
func WithMaxPages(maxPages int) PaginationOption {
	return func(o *PaginationOptions) {
		o.MaxPages = maxPages
	}
}

// WithInitialToken sets the starting page token for the pagination loop.
func WithInitialToken(token string) PaginationOption {
	return func(o *PaginationOptions) {
		o.InitialToken = token
	}
}

// WithTimeout sets the overall deadline duration across all pages.
func WithTimeout(timeout time.Duration) PaginationOption {
	return func(o *PaginationOptions) {
		o.Timeout = timeout
	}
}

// PaginateAll drains all pages from a paginated API using the provided PageFetcher.
// It returns all accumulated items or an error.
// It enforces cycle detection (preventing infinite loops when a page token repeats),
// a bounded maximum page limit, and a strict overall deadline.
func PaginateAll[T any](ctx context.Context, fetcher PageFetcher[T], opts ...PaginationOption) ([]T, error) {
	var allItems []T
	err := ForEachPage(ctx, fetcher, func(items []T, _ string) error {
		allItems = append(allItems, items...)
		return nil
	}, opts...)
	if err != nil {
		return nil, err
	}
	return allItems, nil
}

// ForEachPage invokes callback fn for each page of items returned by fetcher.
// If fn returns an error, pagination stops immediately and that error is returned.
// An overall 2-minute deadline (or configured Timeout) is strictly enforced across all pages.
func ForEachPage[T any](ctx context.Context, fetcher PageFetcher[T], fn func(items []T, pageToken string) error, opts ...PaginationOption) error {
	options := PaginationOptions{
		MaxPages: DefaultMaxPages,
		Timeout:  DefaultPaginationTimeout,
	}
	for _, opt := range opts {
		opt(&options)
	}
	if options.MaxPages <= 0 {
		options.MaxPages = DefaultMaxPages
	}
	if options.Timeout <= 0 {
		options.Timeout = DefaultPaginationTimeout
	}

	pageCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()

	visitedTokens := make(map[string]struct{})
	currentToken := options.InitialToken
	pageCount := 0

	for {
		select {
		case <-pageCtx.Done():
			return pageCtx.Err()
		default:
		}

		pageCount++
		if pageCount > options.MaxPages {
			return fmt.Errorf("%w: reached limit of %d pages", ErrMaxPagesExceeded, options.MaxPages)
		}

		items, nextPageToken, err := fetcher(pageCtx, currentToken)
		if err != nil {
			return err
		}

		visitedTokens[currentToken] = struct{}{}

		if fn != nil {
			if err := fn(items, currentToken); err != nil {
				return err
			}
		}

		if nextPageToken == "" {
			break
		}

		if _, seen := visitedTokens[nextPageToken]; seen {
			return fmt.Errorf("%w: token %q was returned multiple times", ErrPaginationCycleDetected, nextPageToken)
		}

		currentToken = nextPageToken
	}

	return nil
}
