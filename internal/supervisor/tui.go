package supervisor

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
)

// logWriter is an io.Writer that captures log output and forwards it to stdout.
// It buffers partial lines until a newline is received, ensuring complete lines
// are written atomically. Thread-safe: slog calls can be made from multiple goroutines.
type logWriter struct {
	buf strings.Builder
	mu  sync.Mutex
}

// Write implements io.Writer. Buffers input and writes complete lines to stdout.
// Partial lines (without a trailing newline) are held in the buffer until
// a subsequent write completes them.
func (lw *logWriter) Write(p []byte) (n int, err error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()

	lw.buf.Write(p)
	content := lw.buf.String()
	lines := strings.Split(content, "\n")

	// All but the last element are complete lines.
	for _, line := range lines[:len(lines)-1] {
		if line != "" {
			fmt.Fprintln(os.Stdout, line)
		}
	}
	// Keep any partial trailing line.
	lw.buf.Reset()
	lw.buf.WriteString(lines[len(lines)-1])

	return len(p), nil
}

// start is retained for compatibility but is a no-op in the simplified version.
// Log output is written directly to stdout in Write.
func (lw *logWriter) start(_ context.Context) {}
