package gok

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/sync/errgroup"
)

// TestStreamLogConcurrentCancel exercises two races that existed in streamLog:
//
//  1. CheckRedirect data race: eventsource.SubscribeWith mutates
//     http.Client.CheckRedirect. logsImplConfig.run calls streamLog
//     concurrently for stdout and stderr with the same *http.Client,
//     so both goroutines wrote to the same field. Fixed by cloning
//     the client in streamLog before passing it to the library.
//
//  2. send-on-closed-channel panic: the old code deferred stream.Close(),
//     which closes the Events/Errors channels. The library's background
//     goroutine could still be sending on those channels after Close
//     was called. Fixed by not calling stream.Close() at all — context
//     cancellation aborts the HTTP request and the goroutine exits.
//
// Run with -race to verify both fixes.
func TestStreamLogConcurrentCancel(t *testing.T) {
	// Mock SSE server: streams events as fast as possible until the
	// client disconnects (simulates a long-lived log stream).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		// Emit SSE events as fast as the client can read them.
		// The loop never ends on its own; it only stops when the write fails
		// after streamLog's context is canceled and the HTTP connection drops.
		for i := 0; ; i++ {
			_, err := fmt.Fprintf(w, "data: line %d\n\n", i)
			if err != nil {
				return
			}
			// Push each event immediately so both streamLog goroutines stay
			// busy reading while we cancel the context below.
			f.Flush()
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())

	cfg := &logsImplConfig{}
	client := srv.Client()

	// Start two concurrent streamLog calls sharing the same
	// *http.Client, mirroring how logsImplConfig.run starts
	// stdout and stderr streams via errgroup.
	// Under -race this detects the CheckRedirect data race (fix 1).
	var eg errgroup.Group
	for range 2 {
		eg.Go(func() error {
			return cfg.streamLog(ctx, io.Discard, srv.URL, client)
		})
	}

	// Cancel the context while both streams are active.
	// Before the fix, this triggered a panic in the eventsource
	// library's background goroutine (fix 2).
	cancel()

	err := eg.Wait()
	if err != nil && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("unexpected error: %v", err)
	}
}
