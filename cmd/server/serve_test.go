package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/i-got-this-faa/fbs/internal/config"
)

type shutdownLogHandler struct {
	slog.Handler
	shutdown chan struct{}
}

func (h shutdownLogHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "shutting down server" {
		close(h.shutdown)
	}
	return h.Handler.Handle(ctx, record)
}

func TestServeShutdownWaitsForInflightRequest(t *testing.T) {
	testServeInflightShutdown(t, false)
}

func TestServeShutdownUsesConfiguredDeadline(t *testing.T) {
	testServeInflightShutdown(t, true)
}

func testServeInflightShutdown(t *testing.T, exceedDeadline bool) {
	t.Helper()
	cfg := config.Default()
	cfg.HTTPAddr = findFreePort(t)
	cfg.ShutdownTimeout = time.Second
	if exceedDeadline {
		cfg.ShutdownTimeout = 75 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		close(entered)
		select {
		case <-release:
			_, _ = io.WriteString(w, "request completed")
		case <-r.Context().Done():
		}
	})
	shutdownStarted := make(chan struct{})
	logger := slog.New(shutdownLogHandler{
		Handler: slog.NewTextHandler(io.Discard, nil), shutdown: shutdownStarted,
	})
	served := make(chan error, 1)
	serveDone := make(chan struct{})
	go func() {
		served <- serve(ctx, cfg, handler, logger)
		close(serveDone)
	}()
	t.Cleanup(func() {
		cancel()
		unblock()
		select {
		case <-serveDone:
		case <-time.After(3 * time.Second):
			t.Error("serve did not exit during cleanup")
		}
	})
	client := &http.Client{Timeout: 3 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	// Reuse the same bounded HTTP readiness check as the compiled-server tests.
	probe := &testServerProcess{baseURL: "http://" + cfg.HTTPAddr, client: client, done: make(chan struct{})}
	if err := probe.waitReady(); err != nil {
		t.Fatal(err)
	}
	response := make(chan error, 1)
	go func() {
		resp, err := client.Get(probe.baseURL + "/inflight")
		if err == nil {
			defer resp.Body.Close()
			var body []byte
			body, err = io.ReadAll(resp.Body)
			if err == nil && (resp.StatusCode != http.StatusOK || string(body) != "request completed") {
				err = errors.New("in-flight request did not complete with its full response")
			}
		}
		response <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not enter handler")
	}
	cancel()
	select {
	case <-shutdownStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not begin shutdown")
	}
	if exceedDeadline {
		select {
		case err := <-served:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("shutdown error = %v, want deadline exceeded", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("serve ignored the configured 75ms shutdown deadline")
		}
	} else {
		// Observe that cancellation leaves an active request running. Close would
		// return immediately and terminate its connection instead of draining it.
		select {
		case err := <-served:
			t.Fatalf("serve returned with an in-flight request: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}
	unblock()
	select {
	case err := <-response:
		if err != nil {
			t.Fatalf("in-flight response: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight response did not finish")
	}
	if !exceedDeadline {
		select {
		case err := <-served:
			if err != nil {
				t.Fatalf("serve: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("serve did not finish after the request drained")
		}
	}
}
