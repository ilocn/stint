package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/user/stint/internal/goal"
	"github.com/user/stint/internal/logbuf"
	"github.com/user/stint/internal/task"
	"github.com/user/stint/internal/workspace"
)

// statusSortOrder defines the display order: failed first, cancelled last.
var statusSortOrder = map[string]int{
	task.StatusFailed:    0,
	task.StatusBlocked:   1,
	task.StatusRunning:   2,
	task.StatusPending:   3,
	task.StatusDone:      4,
	task.StatusCancelled: 5,
}

// taskJSON is the JSON representation of a task for the dashboard.
type taskJSON struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Agent  string `json:"agent"`
	Repo   string `json:"repo"`
	Reason string `json:"reason,omitempty"`
}

// goalJSON is the JSON representation of a goal for the dashboard.
type goalJSON struct {
	ID     string     `json:"id"`
	Text   string     `json:"text"`
	Status string     `json:"status"`
	Tasks  []taskJSON `json:"tasks"`
}

// statusJSON is the full status payload sent to the dashboard.
type statusJSON struct {
	Goals     []goalJSON     `json:"goals"`
	Summary   map[string]int `json:"summary"`
	Total     int            `json:"total"`
	UpdatedAt int64          `json:"updated_at"`
}

// sseClient represents a connected SSE client.
type sseClient struct {
	ch chan string
}

// Server holds the HTTP server state.
type Server struct {
	ws      *workspace.Workspace
	lb      *logbuf.LogBuf
	mu      sync.Mutex
	clients map[*sseClient]struct{}
}

// buildStatusJSON reads the workspace and builds the dashboard payload.
func buildStatusJSON(ws *workspace.Workspace) (*statusJSON, error) {
	goals, err := goal.List(ws)
	if err != nil {
		return nil, fmt.Errorf("listing goals: %w", err)
	}

	allTasks, err := task.ListAll(ws)
	if err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}

	// Index tasks by goal ID.
	tasksByGoal := make(map[string][]*task.TaskWithStatus)
	for _, ts := range allTasks {
		tasksByGoal[ts.Task.GoalID] = append(tasksByGoal[ts.Task.GoalID], ts)
	}

	summary := make(map[string]int)
	for _, ts := range allTasks {
		summary[ts.Status]++
	}

	var goalJSONs []goalJSON
	for _, g := range goals {
		tasks := tasksByGoal[g.ID]

		// Sort tasks within each goal: by Seq ascending first (seq 1 always on
		// top, seq 9 at bottom), then by status priority as a tie-breaker.
		sort.Slice(tasks, func(i, j int) bool {
			si := tasks[i].Task.Seq
			sj := tasks[j].Task.Seq
			if si != sj {
				return si < sj
			}
			oi := statusSortOrder[tasks[i].Status]
			oj := statusSortOrder[tasks[j].Status]
			return oi < oj
		})

		var taskJSONs []taskJSON
		for _, ts := range tasks {
			t := ts.Task
			var reason string
			switch ts.Status {
			case task.StatusDone:
				if t.Result != nil && t.Result.Summary != "" {
					reason = t.Result.Summary
				}
			case task.StatusCancelled:
				if t.ErrorMsg != "" {
					reason = t.ErrorMsg
				} else {
					reason = "Cancelled"
				}
			default:
				reason = t.ErrorMsg
			}
			taskJSONs = append(taskJSONs, taskJSON{
				ID:     t.ID,
				Title:  t.Title,
				Status: ts.Status,
				Agent:  t.Agent,
				Repo:   t.Repo,
				Reason: reason,
			})
		}

		goalJSONs = append(goalJSONs, goalJSON{
			ID:     g.ID,
			Text:   g.Text,
			Status: g.Status,
			Tasks:  taskJSONs,
		})
	}

	return &statusJSON{
		Goals:     goalJSONs,
		Summary:   summary,
		Total:     len(allTasks),
		UpdatedAt: time.Now().Unix(),
	}, nil
}

// Serve starts the HTTP dashboard on the given address.
// It shuts down gracefully when ctx is cancelled.
func Serve(ctx context.Context, ws *workspace.Workspace, addr string, lb *logbuf.LogBuf) error {
	srv := &Server{
		ws:      ws,
		lb:      lb,
		clients: make(map[*sseClient]struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handleIndex)
	mux.HandleFunc("/api/status", srv.handleAPIStatus)
	mux.HandleFunc("/api/goals", srv.handleAddGoal)
	mux.HandleFunc("/api/logs", srv.handleAPILogs)
	mux.HandleFunc("/events", srv.handleEvents)

	httpSrv := &http.Server{Addr: addr, Handler: mux}

	// pollLoop runs until ctx is cancelled.
	go srv.pollLoop(ctx)

	// If a log buffer is provided, subscribe and broadcast log lines as SSE events.
	if lb != nil {
		go func() {
			ch := lb.Subscribe()
			defer lb.Unsubscribe(ch)
			for {
				select {
				case <-ctx.Done():
					return
				case line := <-ch:
					srv.broadcastRaw(fmt.Sprintf("event: log\ndata: %s\n\n", line))
				}
			}
		}()
	}

	// Shut down the HTTP server when the context is cancelled.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			slog.Error("shutdown failed", slog.Any("error", err))
		}
	}()

	slog.Info("dashboard listening", slog.String("addr", addr))
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// handleIndex serves the embedded HTML dashboard.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(dashboardHTML))
}

// handleAPIStatus returns the current status as JSON.
func (s *Server) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	payload, err := buildStatusJSON(s.ws)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

// handleAddGoal handles POST /api/goals to create a new goal.
func (s *Server) handleAddGoal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	g, err := goal.Create(s.ws, text, nil, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
		"id":     g.ID,
		"text":   g.Text,
		"status": g.Status,
	})
}

// handleAPILogs returns the buffered log lines as a JSON array.
func (s *Server) handleAPILogs(w http.ResponseWriter, r *http.Request) {
	var lines []string
	if s.lb != nil {
		lines = s.lb.Lines()
	}
	if lines == nil {
		lines = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	json.NewEncoder(w).Encode(lines) //nolint:errcheck
}

// handleEvents serves Server-Sent Events for live updates.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Flush headers immediately so the client receives a 200 response even if
	// the initial snapshot fails.
	flusher.Flush()

	client := &sseClient{ch: make(chan string, 16)}

	s.mu.Lock()
	s.clients[client] = struct{}{}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.clients, client)
		s.mu.Unlock()
	}()

	// Send initial status snapshot with event type "status".
	if payload, err := buildStatusJSON(s.ws); err == nil {
		if data, err := json.Marshal(payload); err == nil {
			fmt.Fprintf(w, "event: status\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}

	// Send recent log lines with event type "log".
	if s.lb != nil {
		for _, line := range s.lb.Lines() {
			fmt.Fprintf(w, "event: log\ndata: %s\n\n", line)
		}
		flusher.Flush()
	}

	for {
		select {
		case msg := <-client.ch:
			fmt.Fprint(w, msg)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// broadcastRaw sends a pre-formatted SSE string to all connected SSE clients.
func (s *Server) broadcastRaw(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		select {
		case c.ch <- msg:
		default:
			// Drop if client channel is full (slow consumer).
		}
	}
}

// pollLoop periodically reads workspace state and pushes updates to SSE clients.
// It exits when ctx is cancelled.
func (s *Server) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			payload, err := buildStatusJSON(s.ws)
			if err != nil {
				slog.Error("error polling task stream", slog.Any("error", err))
				continue
			}
			data, err := json.Marshal(payload)
			if err != nil {
				continue
			}
			s.broadcastRaw(fmt.Sprintf("event: status\ndata: %s\n\n", string(data)))
		}
	}
}
