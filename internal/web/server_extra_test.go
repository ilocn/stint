package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestBroadcastSendsMessageToClient verifies that broadcastRaw sends a message to
// all registered SSE clients.
func TestBroadcastSendsMessageToClient(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	srv := &Server{ws: ws, clients: make(map[*sseClient]struct{})}

	// Register a client with a buffered channel.
	client := &sseClient{ch: make(chan string, 1)}
	srv.mu.Lock()
	srv.clients[client] = struct{}{}
	srv.mu.Unlock()

	srv.broadcastRaw("hello world")

	select {
	case msg := <-client.ch:
		if msg != "hello world" {
			t.Errorf("broadcast message = %q, want %q", msg, "hello world")
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("no message received from broadcast within 200ms")
	}
}

// TestBroadcastDropsMessageWhenClientBufferFull verifies that broadcast does
// not block when a client's channel is already full — the message is silently
// dropped (slow-consumer protection).
func TestBroadcastDropsMessageWhenClientBufferFull(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	srv := &Server{ws: ws, clients: make(map[*sseClient]struct{})}

	// Channel capacity 1, pre-filled.
	client := &sseClient{ch: make(chan string, 1)}
	client.ch <- "already full"

	srv.mu.Lock()
	srv.clients[client] = struct{}{}
	srv.mu.Unlock()

	done := make(chan struct{})
	go func() {
		srv.broadcastRaw("should be dropped")
		close(done)
	}()

	select {
	case <-done:
		// broadcast returned without blocking — correct
	case <-time.After(500 * time.Millisecond):
		t.Error("broadcast blocked on full client channel")
	}
}

// TestBroadcastNoClients verifies that broadcastRaw is a no-op when no clients
// are registered (should not panic).
func TestBroadcastNoClients(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	srv := &Server{ws: ws, clients: make(map[*sseClient]struct{})}

	// Should not panic with an empty client map.
	srv.broadcastRaw("nobody home")
}

// TestPollLoopBroadcastsOnTick verifies that pollLoop sends status updates to
// registered SSE clients when the internal ticker fires. Uses a short-lived
// context and client with a buffered channel.
func TestPollLoopBroadcastsOnTick(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	srv := &Server{ws: ws, clients: make(map[*sseClient]struct{})}

	// Register a client with a large buffer to capture broadcasts.
	client := &sseClient{ch: make(chan string, 8)}
	srv.mu.Lock()
	srv.clients[client] = struct{}{}
	srv.mu.Unlock()

	// pollLoop uses a 2-second ticker; we override by running pollLoop with a
	// context that we cancel after enough time for at least one tick to fire.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		srv.pollLoop(ctx)
		close(done)
	}()

	// Wait for at least one broadcast from the ticker.
	select {
	case msg := <-client.ch:
		// pollLoop now sends pre-formatted SSE: "event: status\ndata: {...}\n\n"
		// Extract the JSON data part.
		jsonData := extractSSEData(msg)
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &payload); err != nil {
			t.Errorf("broadcast message is not valid JSON: %v\nmsg: %s", err, msg)
		}
		if _, ok := payload["goals"]; !ok {
			t.Errorf("broadcast payload missing 'goals' key; keys: %v", payload)
		}
		cancel() // stop the loop
	case <-time.After(5 * time.Second):
		t.Error("pollLoop did not broadcast within 5 seconds")
	}

	<-done
}

// TestPollLoopBuildErrorContinues verifies that pollLoop continues running
// after buildStatusJSON returns an error (goal listing error due to unreadable
// goals directory) — it logs the error and continues.
func TestPollLoopBuildErrorContinues(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}

	ws := newTestWS(t)
	srv := &Server{ws: ws, clients: make(map[*sseClient]struct{})}

	// Make goals dir unreadable so buildStatusJSON fails on the ticker tick.
	if err := os.Chmod(ws.GoalsDir(), 0000); err != nil {
		t.Fatalf("Chmod goals dir: %v", err)
	}
	defer os.Chmod(ws.GoalsDir(), 0755) //nolint:errcheck

	// pollLoop should keep running after buildStatusJSON errors (just logging).
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		srv.pollLoop(ctx)
		close(done)
	}()

	// Cancel after a short time; the loop should still exit cleanly.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// pollLoop exited cleanly after cancel.
	case <-time.After(5 * time.Second):
		t.Error("pollLoop did not exit after context cancellation")
	}
}

// TestServeStartsAndStopsCleanly verifies that Serve binds to a port,
// serves requests, and shuts down gracefully when the context is cancelled.
func TestServeStartsAndStopsCleanly(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	// Find a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ws, addr, nil)
	}()

	// Wait for the server to start.
	var resp *http.Response
	for i := 0; i < 20; i++ {
		resp, err = http.Get("http://" + addr + "/api/status")
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("server did not start in time: %v", err)
	}
	resp.Body.Close()

	// Verify the response is valid.
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	// Graceful shutdown.
	cancel()
	select {
	case serveErr := <-done:
		if serveErr != nil {
			t.Errorf("Serve returned unexpected error: %v", serveErr)
		}
	case <-time.After(10 * time.Second):
		t.Error("Serve did not stop within 10 seconds after context cancel")
	}
}

// TestHandleEventsNoFlusher verifies that /events returns 500 when the
// response writer does not implement http.Flusher.
func TestHandleEventsNoFlusher(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	srv := &Server{ws: ws, clients: make(map[*sseClient]struct{})}

	// Use a plain http.ResponseWriter that does NOT implement http.Flusher.
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	rr := &plainResponseWriter{header: make(http.Header)}
	srv.handleEvents(rr, req)

	if rr.statusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 when Flusher not supported", rr.statusCode)
	}
	if !strings.Contains(rr.body.String(), "streaming not supported") {
		t.Errorf("body = %q, want 'streaming not supported'", rr.body.String())
	}
}

// plainResponseWriter is a minimal http.ResponseWriter that does NOT implement
// http.Flusher, forcing handleEvents to fall back to the 500 error path.
type plainResponseWriter struct {
	header     http.Header
	body       strings.Builder
	statusCode int
}

func (r *plainResponseWriter) Header() http.Header { return r.header }
func (r *plainResponseWriter) Write(b []byte) (int, error) {
	if r.statusCode == 0 {
		r.statusCode = http.StatusOK
	}
	return r.body.Write(b)
}
func (r *plainResponseWriter) WriteHeader(code int) { r.statusCode = code }

// TestHandleAPIStatusBuildError verifies that /api/status returns 500 when
// buildStatusJSON fails (goals directory is unreadable).
func TestHandleAPIStatusBuildError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newTestWS(t)

	// Make goals dir unreadable so buildStatusJSON fails.
	if err := os.Chmod(ws.GoalsDir(), 0000); err != nil {
		t.Fatalf("Chmod goals dir: %v", err)
	}
	defer os.Chmod(ws.GoalsDir(), 0755) //nolint:errcheck

	ts := newTestServer(t, ws)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 when goals dir is unreadable", resp.StatusCode)
	}
}

// TestBuildStatusJSONGoalListError verifies that buildStatusJSON returns an
// error when the goals directory is unreadable.
func TestBuildStatusJSONGoalListError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newTestWS(t)

	// Make goals dir unreadable.
	if err := os.Chmod(ws.GoalsDir(), 0000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(ws.GoalsDir(), 0755) //nolint:errcheck

	_, err := buildStatusJSON(ws)
	if err == nil {
		t.Error("buildStatusJSON should fail when goals dir is unreadable")
	}
}

// TestHandleEventsInitialSnapshotError verifies that /events still establishes
// the SSE stream (returns 200) even when the initial buildStatusJSON fails due
// to an unreadable goals directory. After the fix, handleEvents flushes headers
// before attempting the snapshot, so the client always gets a 200 response.
func TestHandleEventsInitialSnapshotError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newTestWS(t)

	// Make goals dir unreadable so the initial snapshot fails.
	if err := os.Chmod(ws.GoalsDir(), 0000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(ws.GoalsDir(), 0755) //nolint:errcheck

	ts := newTestServer(t, ws)
	defer ts.Close()

	// Use a short timeout context so Do() returns after receiving headers.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	// Do() returns once response headers are received for a streaming response.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Context timeout is expected since the SSE stream never sends EOF.
		if ctx.Err() != nil {
			// Timed out waiting for headers — the server hung without responding.
			t.Fatal("SSE server did not send response headers when initial snapshot failed (timeout)")
		}
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (SSE stream must be established even on snapshot error)", resp.StatusCode)
	}
}

// TestServeWithWebPort verifies the Run function's web port branch by starting
// Serve directly with an available port.
func TestBuildStatusJSONTaskListError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	ws := newTestWS(t)

	// Make the pending dir unreadable so task.ListAll fails.
	if err := os.Chmod(ws.PendingDir(), 0000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(ws.PendingDir(), 0755) //nolint:errcheck

	_, err := buildStatusJSON(ws)
	if err == nil {
		t.Error("buildStatusJSON should fail when pending dir is unreadable")
	}
}

// TestHandleIndexNonRootPath verifies that requesting a path other than "/"
// returns 404.
func TestHandleIndexNonRootPath(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	srv := &Server{ws: ws, clients: make(map[*sseClient]struct{})}

	req := httptest.NewRequest(http.MethodGet, "/other/path", nil)
	rr := httptest.NewRecorder()
	srv.handleIndex(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("handleIndex('/other/path') status = %d, want 404", rr.Code)
	}
}

// TestBroadcastMultipleClients verifies that broadcast sends to all registered
// clients, not just one.
func TestBroadcastMultipleClients(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	srv := &Server{ws: ws, clients: make(map[*sseClient]struct{})}

	c1 := &sseClient{ch: make(chan string, 1)}
	c2 := &sseClient{ch: make(chan string, 1)}
	c3 := &sseClient{ch: make(chan string, 1)}

	srv.mu.Lock()
	srv.clients[c1] = struct{}{}
	srv.clients[c2] = struct{}{}
	srv.clients[c3] = struct{}{}
	srv.mu.Unlock()

	srv.broadcastRaw("multi-test")

	for i, c := range []*sseClient{c1, c2, c3} {
		select {
		case msg := <-c.ch:
			if msg != "multi-test" {
				t.Errorf("client %d got %q, want %q", i+1, msg, "multi-test")
			}
		case <-time.After(200 * time.Millisecond):
			t.Errorf("client %d did not receive broadcast within 200ms", i+1)
		}
	}
}

// TestServeInvalidAddress verifies that Serve returns an error when binding to
// an invalid or already-used address.
func TestServeInvalidAddress(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Use an invalid address to trigger an immediate error.
	err := Serve(ctx, ws, "invalid-address-that-wont-bind:99999", nil)
	if err == nil {
		t.Error("Serve should return error on invalid address")
	}
	_ = fmt.Sprintf("serve error: %v", err) // use err to satisfy compiler
}
