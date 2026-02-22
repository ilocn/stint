package web

// server_boost_test.go — targeted tests to cover previously uncovered code
// paths and push the web package to ≥95% coverage.
//
// Key areas addressed:
//   - handleEvents: the `case msg := <-client.ch` broadcast-receive path,
//     which fires when Server.broadcast is called after a client connects.

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newServerAndTestServer creates a Server instance and a corresponding
// httptest.Server. Unlike newTestServer, it returns the Server so the test
// can call srv.broadcast() directly.
func newServerAndTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	ws := newTestWS(t)
	srv := &Server{ws: ws, clients: make(map[*sseClient]struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handleIndex)
	mux.HandleFunc("/api/status", srv.handleAPIStatus)
	mux.HandleFunc("/events", srv.handleEvents)
	ts := httptest.NewServer(mux)
	return srv, ts
}

// TestHandleEventsClientReceivesBroadcast verifies that a connected SSE client
// receives a message when Server.broadcast is called after the connection is
// established. This exercises the `case msg := <-client.ch:` path in
// handleEvents, plus the associated Fprintf and Flush calls.
func TestHandleEventsClientReceivesBroadcast(t *testing.T) {
	t.Parallel()

	srv, ts := newServerAndTestServer(t)
	defer ts.Close()

	// Open an SSE connection with a cancellable context so we can close it.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil && ctx.Err() == nil {
		t.Fatalf("GET /events: %v", err)
	}
	if resp == nil {
		return
	}
	defer resp.Body.Close()

	// Give time for the SSE handler to register the client and send the
	// initial snapshot before we broadcast.
	time.Sleep(150 * time.Millisecond)

	// Broadcast a message — it should be pushed to the connected SSE client
	// via client.ch, covering the `case msg := <-client.ch:` branch.
	const want = "broadcast-coverage-test"
	srv.broadcastRaw(fmt.Sprintf("data: %s\n\n", want))

	// Read SSE lines with a timeout. The scanner reads the streaming response body.
	lineCh := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				lineCh <- line
			}
		}
		close(lineCh)
	}()

	deadline := time.After(4 * time.Second)
	for {
		select {
		case line, ok := <-lineCh:
			if !ok {
				t.Error("SSE stream closed before receiving broadcast message")
				return
			}
			// The broadcast sends the raw string; the initial snapshot sends JSON.
			// Either way, receiving any data line confirms the path is executed.
			if strings.Contains(line, want) {
				// Exactly the broadcast message — coverage confirmed.
				cancel()
				return
			}
			// This could be the initial snapshot — continue reading.
		case <-deadline:
			t.Error("no SSE broadcast message received within 4 seconds")
			return
		}
	}
}

// TestHandleEventsInitialSnapshotSent verifies that handleEvents sends the
// initial status snapshot immediately on connection, even without a broadcast.
// This complements the existing tests and ensures the initial data path runs.
func TestHandleEventsInitialSnapshotSent(t *testing.T) {
	t.Parallel()

	_, ts := newServerAndTestServer(t)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil && ctx.Err() == nil {
		t.Fatalf("GET /events: %v", err)
	}
	if resp == nil {
		return
	}
	defer resp.Body.Close()

	// Read the first SSE data line (initial snapshot).
	lineCh := make(chan string, 4)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				lineCh <- line
				return
			}
		}
		close(lineCh)
	}()

	select {
	case line := <-lineCh:
		if !strings.Contains(line, "goals") {
			t.Errorf("initial SSE snapshot missing 'goals' key: %s", line)
		}
	case <-time.After(3 * time.Second):
		t.Error("no initial SSE snapshot received within 3 seconds")
	}
	cancel()
}
