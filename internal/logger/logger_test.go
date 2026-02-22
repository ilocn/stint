package logger

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestInit_SetsGlobalDefault(t *testing.T) {
	Init()
	// After Init, slog.Default() should be a non-nil logger backed by our handler.
	l := slog.Default()
	if l == nil {
		t.Fatal("slog.Default() is nil after Init()")
	}
}

func TestSetLogBuf_WritesToSecondary(t *testing.T) {
	Init()

	var buf bytes.Buffer
	SetLogBuf(&buf)
	defer SetLogBuf(nil)

	slog.Info("test message", slog.String("key", "value"))

	got := buf.String()
	if !strings.Contains(got, "test message") {
		t.Errorf("secondary writer did not receive log output; got: %q", got)
	}
	if !strings.Contains(got, "key=value") {
		t.Errorf("secondary writer missing structured field; got: %q", got)
	}
}

func TestSetLogBuf_NilClearsSecondary(t *testing.T) {
	Init()

	var buf bytes.Buffer
	SetLogBuf(&buf)
	SetLogBuf(nil)

	slog.Info("after clear")

	// buf should be empty since we cleared the secondary writer.
	if buf.Len() != 0 {
		t.Errorf("expected empty secondary buffer after SetLogBuf(nil), got: %q", buf.String())
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"ERROR", slog.LevelError},
		{"unknown", slog.LevelInfo},
	}
	for _, tt := range tests {
		got := parseLevel(tt.input)
		if got != tt.want {
			t.Errorf("parseLevel(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
