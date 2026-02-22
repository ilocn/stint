package logger

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
)

// PrettyHandler is a slog.Handler that produces clean, human-readable log lines.
//
// Format:
//
//	15:04:05.000  INFO   message text          key=val key2="val with spaces"
//
// When color is true, ANSI escape codes are applied to timestamp, level, and message.
type PrettyHandler struct {
	opts  slog.HandlerOptions
	w     io.Writer
	color bool
	mu    sync.Mutex
	attrs []slog.Attr
	group string
}

const (
	ansiReset  = "\033[0m"
	ansiDim    = "\033[2m"
	ansiBold   = "\033[1m"
	ansiCyan   = "\033[36m"
	ansiYellow = "\033[33m"
	ansiRed    = "\033[31m"
	ansiGray   = "\033[90m"
)

func levelColor(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return ansiRed
	case level >= slog.LevelWarn:
		return ansiYellow
	case level >= slog.LevelInfo:
		return ansiCyan
	default:
		return ansiGray
	}
}

func (h *PrettyHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.opts.Level.Level()
}

func (h *PrettyHandler) Handle(_ context.Context, r slog.Record) error {
	var buf bytes.Buffer

	// Timestamp
	ts := r.Time.Format("15:04:05.000")
	if h.color {
		buf.WriteString(ansiDim)
		buf.WriteString(ts)
		buf.WriteString(ansiReset)
	} else {
		buf.WriteString(ts)
	}
	buf.WriteString("  ")

	// Level (left-justified in 5 chars)
	levelStr := fmt.Sprintf("%-5s", r.Level.String())
	if h.color {
		buf.WriteString(levelColor(r.Level))
		buf.WriteString(levelStr)
		buf.WriteString(ansiReset)
	} else {
		buf.WriteString(levelStr)
	}
	buf.WriteString("  ")

	// Message
	if h.color {
		buf.WriteString(ansiBold)
		buf.WriteString(r.Message)
		buf.WriteString(ansiReset)
	} else {
		buf.WriteString(r.Message)
	}

	// Collect all attrs: pre-set handler attrs then per-record attrs
	var allAttrs []slog.Attr
	allAttrs = append(allAttrs, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		allAttrs = append(allAttrs, a)
		return true
	})

	// Write attrs
	for _, a := range allAttrs {
		key := a.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		buf.WriteByte(' ')
		buf.WriteString(key)
		buf.WriteByte('=')
		buf.WriteString(formatValue(a.Value))
	}

	buf.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(buf.Bytes())
	return err
}

func (h *PrettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)
	return &PrettyHandler{
		opts:  h.opts,
		w:     h.w,
		color: h.color,
		attrs: newAttrs,
		group: h.group,
	}
}

func (h *PrettyHandler) WithGroup(name string) slog.Handler {
	g := name
	if h.group != "" {
		g = h.group + "." + name
	}
	return &PrettyHandler{
		opts:  h.opts,
		w:     h.w,
		color: h.color,
		attrs: h.attrs,
		group: g,
	}
}

// formatValue converts a slog.Value to a string, quoting string values that
// contain spaces, quotes, or are empty.
func formatValue(v slog.Value) string {
	v = v.Resolve()
	switch v.Kind() {
	case slog.KindString:
		s := v.String()
		if needsQuoting(s) {
			return strconv.Quote(s)
		}
		return s
	case slog.KindInt64:
		return strconv.FormatInt(v.Int64(), 10)
	case slog.KindUint64:
		return strconv.FormatUint(v.Uint64(), 10)
	case slog.KindFloat64:
		return strconv.FormatFloat(v.Float64(), 'g', -1, 64)
	case slog.KindBool:
		return strconv.FormatBool(v.Bool())
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().Format("15:04:05.000")
	case slog.KindGroup:
		// Inline group as key=val pairs joined by space (rare, keep simple)
		var parts []string
		for _, a := range v.Group() {
			parts = append(parts, a.Key+"="+formatValue(a.Value))
		}
		return strings.Join(parts, " ")
	default:
		s := fmt.Sprintf("%v", v.Any())
		if needsQuoting(s) {
			return strconv.Quote(s)
		}
		return s
	}
}

func needsQuoting(s string) bool {
	if s == "" {
		return true
	}
	for _, c := range s {
		if c == ' ' || c == '"' || c == '=' || c == '\n' || c == '\t' {
			return true
		}
	}
	return false
}
