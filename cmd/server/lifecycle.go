package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

type workerResult struct {
	name string
	err  error
}

// serve owns both workers. Its caller may close the database only after it returns.
// HTTP requests drain during the grace period; an expired drain closes connections
// to cancel their request contexts. Polling I/O is canceled immediately.
func serve(ctx context.Context, srv *http.Server, serveHTTP func() error, poll func(context.Context) error, grace time.Duration) error {
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()
	results := make(chan workerResult, 2)
	remaining := 1
	go func() { results <- workerResult{"HTTP server", serveHTTP()} }()
	if poll != nil {
		remaining++
		go func() { results <- workerResult{"Telegram polling", poll(workerCtx)} }()
	}

	var cause error
	select {
	case <-ctx.Done():
	case result := <-results:
		remaining--
		if ctx.Err() == nil && !errors.Is(result.err, http.ErrServerClosed) {
			if result.err == nil {
				cause = fmt.Errorf("%s stopped unexpectedly", result.name)
			} else {
				cause = fmt.Errorf("%s stopped: %w", result.name, result.err)
			}
		}
	}
	cancelWorkers()
	shutdown, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		cause = errors.Join(cause, fmt.Errorf("drain HTTP requests: %w", err))
		if closeErr := srv.Close(); closeErr != nil {
			cause = errors.Join(cause, fmt.Errorf("close HTTP connections: %w", closeErr))
		}
	}
	for remaining > 0 {
		// Prefer results already queued even when the grace deadline has expired.
		select {
		case <-results:
			remaining--
			continue
		default:
		}
		select {
		case <-results:
			remaining--
		case <-shutdown.Done():
			return errors.Join(cause, fmt.Errorf("workers did not stop within the shutdown grace period: %w", shutdown.Err()))
		}
	}
	return cause
}
