package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// dynamicWriter forwards writes to os.Stderr and optionally to a secondary
// writer (e.g. a logbuf.LogBuf). It is safe for concurrent use.
type dynamicWriter struct {
	mu     sync.RWMutex
	second io.Writer
}

func (dw *dynamicWriter) Write(p []byte) (int, error) {
	n, err := os.Stderr.Write(p)
	dw.mu.RLock()
	second := dw.second
	dw.mu.RUnlock()
	if second != nil {
		second.Write(p) //nolint:errcheck
	}
	return n, err
}

var gw = &dynamicWriter{}

// Init initializes the global slog logger with a text handler that writes to
// stderr. Reads LOG_LEVEL from the environment (debug/info/warn/error; default
// is info). Call this once early in main before any logging occurs.
func Init() {
	level := parseLevel(os.Getenv("LOG_LEVEL"))
	opts := &slog.HandlerOptions{Level: level}
	h := slog.NewTextHandler(gw, opts)
	slog.SetDefault(slog.New(h))
}

// SetLogBuf adds a secondary write target so that log output is also sent to w
// (typically a *logbuf.LogBuf). Pass nil to clear the secondary target.
func SetLogBuf(w io.Writer) {
	gw.mu.Lock()
	gw.second = w
	gw.mu.Unlock()
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
