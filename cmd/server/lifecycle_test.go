package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func receive[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for lifecycle event")
		var zero T
		return zero
	}
}

func TestServeJoinsPollingAfterHTTPStartupFailure(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	var cleaned atomic.Bool
	startupErr := errors.New("listener unavailable")
	done := make(chan error, 1)
	go func() {
		done <- serve(context.Background(), &http.Server{}, func() error {
			<-started
			return startupErr
		}, func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			cleaned.Store(true)
			return ctx.Err()
		}, time.Second)
	}()
	receive(t, canceled)
	select {
	case err := <-done:
		t.Fatalf("returned before polling cleanup: %v", err)
	default:
	}
	close(release)
	if err := receive(t, done); !errors.Is(err, startupErr) || !cleaned.Load() {
		t.Fatalf("startup failure lost or worker not joined: %v", err)
	}
}

func TestServeDrainsHTTPAndJoinsPolling(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requestStarted := make(chan struct{})
	release := make(chan struct{})
	pollStopped := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-release
		w.WriteHeader(http.StatusOK)
	})}
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, srv, func() error { return srv.Serve(listener) }, func(ctx context.Context) error {
			<-ctx.Done()
			close(pollStopped)
			return ctx.Err()
		}, time.Second)
	}()
	response := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 2 * time.Second}
		r, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
			if r.StatusCode != http.StatusOK {
				err = errors.New("request was not drained successfully")
			}
		}
		response <- err
	}()
	receive(t, requestStarted)
	cancel()
	receive(t, pollStopped)
	select {
	case err := <-done:
		t.Fatalf("returned before the in-flight request completed: %v", err)
	default:
	}
	close(release)
	if err := receive(t, response); err != nil {
		t.Fatal(err)
	}
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestServeForceClosesRequestAfterGraceDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	requestCanceled := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(requestCanceled)
	})}
	done := make(chan error, 1)
	go func() { done <- serve(ctx, srv, func() error { return srv.Serve(listener) }, nil, 30*time.Millisecond) }()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			resp.Body.Close()
		}
	}()
	receive(t, started)
	cancel()
	if err := receive(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected a bounded shutdown timeout, got %v", err)
	}
	receive(t, requestCanceled)
	receive(t, clientDone)
}

func TestServeStopsWhenPollingExitsUnexpectedly(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	srv := &http.Server{}
	pollErr := errors.New("polling worker failed")
	err = serve(context.Background(), srv, func() error { return srv.Serve(listener) }, func(context.Context) error {
		return pollErr
	}, time.Second)
	if !errors.Is(err, pollErr) {
		t.Fatalf("polling failure was lost: %v", err)
	}
}
