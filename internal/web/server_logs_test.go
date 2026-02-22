package web

// server_logs_test.go — tests for the /api/logs endpoint and the 30-second
// log polling fallback guarantee added in t-0v5ledza001.
//
// What is tested:
//   - handleAPILogs returns a valid JSON array in all cases (nil buf, empty buf,
//     buf with lines, ring-buffer overflow)
//   - Content-Type and Cache-Control headers are correct
//   - The embedded dashboardHTML contains the setInterval(/api/logs, 30000) code
//     that guarantees logs refresh at least every 30 seconds even when SSE is
//     not delivering events.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/user/stint/internal/logbuf"
)

// newTestServerWithAPILogs creates an httptest.Server wired to a Server
// instance that includes the /api/logs route (absent from newTestServer).
func newTestServerWithAPILogs(t *testing.T, lb *logbuf.LogBuf) (*Server, *httptest.Server) {
	t.Helper()
	ws := newTestWS(t)
	srv := &Server{
		ws:      ws,
		lb:      lb,
		clients: make(map[*sseClient]struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handleIndex)
	mux.HandleFunc("/api/status", srv.handleAPIStatus)
	mux.HandleFunc("/api/logs", srv.handleAPILogs)
	mux.HandleFunc("/events", srv.handleEvents)
	return srv, httptest.NewServer(mux)
}

// ── handleAPILogs unit tests (via httptest.NewRecorder) ──────────────────────

// TestHandleAPILogsNilLogBuf verifies that handleAPILogs returns a non-null
// empty JSON array (not "null") when no log buffer is attached to the server.
// The frontend JSON.parse step requires an array; "null" would break dedup logic.
func TestHandleAPILogsNilLogBuf(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	srv := &Server{ws: ws, lb: nil, clients: make(map[*sseClient]struct{})}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	srv.handleAPILogs(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	body := strings.TrimSpace(w.Body.String())
	// Must decode as a valid JSON array.
	var lines []string
	if err := json.Unmarshal([]byte(body), &lines); err != nil {
		t.Fatalf("response is not valid JSON array: %v; body: %s", err, body)
	}
	// Must not be null — frontend checks Array.isArray(lines).
	if lines == nil {
		t.Errorf("response decoded to nil slice (JSON null), want [] empty array; body: %s", body)
	}
	if len(lines) != 0 {
		t.Errorf("expected empty array, got %v", lines)
	}
}

// TestHandleAPILogsEmptyLogBuf verifies handleAPILogs returns an empty array
// when the log buffer exists but has no lines written yet.
func TestHandleAPILogsEmptyLogBuf(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	lb := logbuf.New(100) // empty buffer
	srv := &Server{ws: ws, lb: lb, clients: make(map[*sseClient]struct{})}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	srv.handleAPILogs(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var lines []string
	if err := json.NewDecoder(w.Body).Decode(&lines); err != nil {
		t.Fatalf("JSON decode error: %v; body: %s", err, w.Body.String())
	}
	if len(lines) != 0 {
		t.Errorf("expected empty array for empty log buffer, got %v", lines)
	}
}

// TestHandleAPILogsWithLines verifies that /api/logs returns buffered log lines
// in the correct order as a JSON array.
func TestHandleAPILogsWithLines(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	lb := logbuf.New(100)
	if _, err := lb.Write([]byte("alpha\nbeta\ngamma\n")); err != nil {
		t.Fatalf("lb.Write: %v", err)
	}
	srv := &Server{ws: ws, lb: lb, clients: make(map[*sseClient]struct{})}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	srv.handleAPILogs(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var got []string
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("JSON decode error: %v; body: %s", err, w.Body.String())
	}

	want := []string{"alpha", "beta", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d; got: %v", len(got), len(want), got)
	}
	for i, wantLine := range want {
		if got[i] != wantLine {
			t.Errorf("lines[%d] = %q, want %q", i, got[i], wantLine)
		}
	}
}

// TestHandleAPILogsContentTypeHeader verifies handleAPILogs sets the correct
// Content-Type so the frontend can parse the response as JSON.
func TestHandleAPILogsContentTypeHeader(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	srv := &Server{ws: ws, lb: nil, clients: make(map[*sseClient]struct{})}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	srv.handleAPILogs(w, r)

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// TestHandleAPILogsCacheControlHeader verifies handleAPILogs sets
// Cache-Control: no-cache so browsers do not serve stale log data between
// the 30-second polling calls.
func TestHandleAPILogsCacheControlHeader(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	srv := &Server{ws: ws, lb: nil, clients: make(map[*sseClient]struct{})}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	srv.handleAPILogs(w, r)

	cc := w.Header().Get("Cache-Control")
	if cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-cache")
	}
}

// TestHandleAPILogsRingBufferOverflow verifies that when the log buffer has
// overflowed, /api/logs returns only the most-recent lines (not dropped ones).
func TestHandleAPILogsRingBufferOverflow(t *testing.T) {
	t.Parallel()
	ws := newTestWS(t)
	lb := logbuf.New(3) // capacity 3
	// Write 5 lines — oldest two should be evicted.
	if _, err := lb.Write([]byte("old1\nold2\nold3\nnew1\nnew2\n")); err != nil {
		t.Fatalf("lb.Write: %v", err)
	}
	srv := &Server{ws: ws, lb: lb, clients: make(map[*sseClient]struct{})}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	srv.handleAPILogs(w, r)

	var got []string
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("JSON decode error: %v; body: %s", err, w.Body.String())
	}

	// Ring buffer of size 3 keeps the last 3 lines.
	want := []string{"old3", "new1", "new2"}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d; got: %v", len(got), len(want), got)
	}
	for i, wantLine := range want {
		if got[i] != wantLine {
			t.Errorf("lines[%d] = %q, want %q", i, got[i], wantLine)
		}
	}
}

// ── Table-driven coverage for all handleAPILogs scenarios ────────────────────

// TestHandleAPILogsTableDriven exercises handleAPILogs across multiple
// scenarios using a table-driven approach to verify all edge cases.
func TestHandleAPILogsTableDriven(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		setupLB   func() *logbuf.LogBuf
		wantLines []string
	}{
		{
			name:      "nil log buffer",
			setupLB:   func() *logbuf.LogBuf { return nil },
			wantLines: []string{},
		},
		{
			name:      "empty log buffer",
			setupLB:   func() *logbuf.LogBuf { return logbuf.New(100) },
			wantLines: []string{},
		},
		{
			name: "single line",
			setupLB: func() *logbuf.LogBuf {
				lb := logbuf.New(100)
				lb.Write([]byte("hello\n")) //nolint:errcheck
				return lb
			},
			wantLines: []string{"hello"},
		},
		{
			name: "multiple lines",
			setupLB: func() *logbuf.LogBuf {
				lb := logbuf.New(100)
				lb.Write([]byte("line1\nline2\nline3\n")) //nolint:errcheck
				return lb
			},
			wantLines: []string{"line1", "line2", "line3"},
		},
		{
			name: "ring buffer overflow keeps most recent",
			setupLB: func() *logbuf.LogBuf {
				lb := logbuf.New(3)
				lb.Write([]byte("drop1\ndrop2\nkeep1\nkeep2\nkeep3\n")) //nolint:errcheck
				return lb
			},
			wantLines: []string{"keep1", "keep2", "keep3"},
		},
		{
			name: "lines with special characters",
			setupLB: func() *logbuf.LogBuf {
				lb := logbuf.New(100)
				lb.Write([]byte("[ERROR] something went wrong: \"file not found\"\n[INFO] retry #1\n")) //nolint:errcheck
				return lb
			},
			wantLines: []string{
				`[ERROR] something went wrong: "file not found"`,
				"[INFO] retry #1",
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ws := newTestWS(t)
			lb := tc.setupLB()
			srv := &Server{ws: ws, lb: lb, clients: make(map[*sseClient]struct{})}

			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
			srv.handleAPILogs(w, r)

			if w.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", w.Code)
			}
			ct := w.Header().Get("Content-Type")
			if !strings.Contains(ct, "application/json") {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			var got []string
			if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
				t.Fatalf("JSON decode error: %v; body: %s", err, w.Body.String())
			}
			// Nil buffer and empty buffer both produce a non-nil slice after
			// the nil-guard in handleAPILogs, so got should never be nil.
			if got == nil {
				t.Fatalf("decoded nil slice — response body was: %s", w.Body.String())
			}
			if len(got) != len(tc.wantLines) {
				t.Fatalf("len(got) = %d, want %d; got: %v", len(got), len(tc.wantLines), got)
			}
			for i, wantLine := range tc.wantLines {
				if got[i] != wantLine {
					t.Errorf("[%d] got %q, want %q", i, got[i], wantLine)
				}
			}
		})
	}
}

// ── HTTP integration tests (real httptest.Server) ────────────────────────────

// TestGetAPILogsHTTPEmptyReturns200 verifies /api/logs returns 200 with a
// valid JSON array over a real HTTP connection when no log buffer is set.
func TestGetAPILogsHTTPEmptyReturns200(t *testing.T) {
	t.Parallel()
	_, ts := newTestServerWithAPILogs(t, nil)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/logs")
	if err != nil {
		t.Fatalf("GET /api/logs: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	var lines []string
	if err := json.Unmarshal(body, &lines); err != nil {
		t.Fatalf("JSON parse error: %v\nbody: %s", err, body)
	}
	if len(lines) != 0 {
		t.Errorf("expected empty array, got %v", lines)
	}
}

// TestGetAPILogsHTTPWithLines verifies /api/logs returns the correct log lines
// over a real HTTP connection.
func TestGetAPILogsHTTPWithLines(t *testing.T) {
	t.Parallel()
	lb := logbuf.New(100)
	if _, err := lb.Write([]byte("first line\nsecond line\nthird line\n")); err != nil {
		t.Fatalf("lb.Write: %v", err)
	}

	_, ts := newTestServerWithAPILogs(t, lb)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/logs")
	if err != nil {
		t.Fatalf("GET /api/logs: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	var got []string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("JSON parse error: %v\nbody: %s", err, body)
	}

	want := []string{"first line", "second line", "third line"}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d; got: %v", len(got), len(want), got)
	}
	for i, wantLine := range want {
		if got[i] != wantLine {
			t.Errorf("lines[%d] = %q, want %q", i, got[i], wantLine)
		}
	}
}

// TestGetAPILogsHTTPCacheControlNoCache verifies the Cache-Control: no-cache
// header is present on the HTTP response from /api/logs, ensuring browsers
// don't serve stale responses between the 30-second polling intervals.
func TestGetAPILogsHTTPCacheControlNoCache(t *testing.T) {
	t.Parallel()
	_, ts := newTestServerWithAPILogs(t, nil)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/logs")
	if err != nil {
		t.Fatalf("GET /api/logs: %v", err)
	}
	defer resp.Body.Close()

	cc := resp.Header.Get("Cache-Control")
	if cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-cache")
	}
}

// ── Dashboard HTML static analysis for 30s polling guarantee ─────────────────

// TestDashboardHTMLContainsLogPollingCode verifies that the embedded
// dashboardHTML constant includes the setInterval block that polls /api/logs
// every 30 seconds. This is the primary guarantee that the logs tab refreshes
// at least once every 30 seconds, even when SSE events are not flowing.
func TestDashboardHTMLContainsLogPollingCode(t *testing.T) {
	t.Parallel()
	for _, want := range []string{
		"/api/logs",   // endpoint polled by the fallback
		"setInterval", // interval timer used for polling
		"30000",       // 30-second interval in milliseconds
	} {
		if !strings.Contains(dashboardHTML, want) {
			t.Errorf("dashboardHTML missing %q — required for 30s log polling fallback", want)
		}
	}
}

// TestDashboardHTMLSetIntervalReferences30sAndAPILogs verifies that the
// setInterval call in dashboardHTML specifically references both /api/logs and
// the 30000 ms interval — ensuring the polling block is for logs and is at the
// guaranteed 30-second cadence, not a different interval or a different fetch.
func TestDashboardHTMLSetIntervalReferences30sAndAPILogs(t *testing.T) {
	t.Parallel()

	idx := strings.Index(dashboardHTML, "setInterval")
	if idx < 0 {
		t.Fatal("dashboardHTML does not contain setInterval — log polling fallback is missing")
	}

	// Inspect the setInterval block (the remainder of the HTML from there).
	snippet := dashboardHTML[idx:]

	if !strings.Contains(snippet, "/api/logs") {
		t.Error("setInterval block does not call fetch('/api/logs') — polling calls wrong endpoint")
	}
	if !strings.Contains(snippet, "30000") {
		t.Error("setInterval block does not use 30000 ms — 30-second refresh guarantee is not met")
	}
}

// TestDashboardHTMLAPILogsAppearsBeforeSetInterval verifies that the /api/logs
// string appears in the context of the setInterval polling block (not only in
// some other part of the HTML), confirming the polling logic is wired correctly.
func TestDashboardHTMLAPILogsAppearsBeforeSetInterval(t *testing.T) {
	t.Parallel()

	// Find the setInterval block.
	siIdx := strings.Index(dashboardHTML, "setInterval")
	if siIdx < 0 {
		t.Fatal("dashboardHTML missing setInterval")
	}

	// Find closing brace of the setInterval call — look for }, 30000 as the
	// canonical end marker of the polling setInterval pattern.
	pollingBlock := dashboardHTML[siIdx:]
	if !strings.Contains(pollingBlock, "}, 30000") {
		t.Errorf("setInterval does not close with }, 30000 — interval may not be 30s; snippet: %.200s", pollingBlock)
	}

	// /api/logs must appear inside the polling block.
	endIdx := strings.Index(pollingBlock, "}, 30000")
	if endIdx < 0 {
		t.Fatal("could not locate end of setInterval block")
	}
	blockBody := pollingBlock[:endIdx]
	if !strings.Contains(blockBody, "/api/logs") {
		t.Errorf("setInterval body does not contain /api/logs; body: %.300s", blockBody)
	}
}
