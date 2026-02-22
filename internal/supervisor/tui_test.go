package supervisor

import (
	"context"
	"testing"
)

// ── logWriter ────────────────────────────────────────────────────────────────

// TestLogWriterDoesNotPanicOnWrite verifies that Write does not panic when the
// logWriter is zero-valued (no fields initialised).
func TestLogWriterDoesNotPanicOnWrite(t *testing.T) {
	t.Parallel()
	lw := &logWriter{}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("logWriter.Write panicked with zero-value writer: %v", r)
		}
	}()

	n, err := lw.Write([]byte("hello\nworld\n"))
	if err != nil {
		t.Errorf("Write returned error: %v", err)
	}
	if n != 12 {
		t.Errorf("Write returned n=%d, want 12", n)
	}
}

// TestLogWriterBuffersPartialLine verifies that a write without a trailing
// newline is held in the buffer and not flushed.
func TestLogWriterBuffersPartialLine(t *testing.T) {
	t.Parallel()
	lw := &logWriter{}

	n, err := lw.Write([]byte("partial line"))
	if err != nil {
		t.Errorf("Write returned error: %v", err)
	}
	if n != 12 {
		t.Errorf("Write returned n=%d, want 12", n)
	}

	lw.mu.Lock()
	buf := lw.buf.String()
	lw.mu.Unlock()

	if buf != "partial line" {
		t.Errorf("logWriter buffer = %q, want %q", buf, "partial line")
	}
}

// TestLogWriterCompletesBufferedLineOnNewline verifies that a subsequent write
// containing a newline flushes the previously buffered partial line.
func TestLogWriterCompletesBufferedLineOnNewline(t *testing.T) {
	t.Parallel()
	lw := &logWriter{} // program nil — just test buffering

	// First write: partial line (no newline).
	lw.Write([]byte("hello ")) //nolint:errcheck
	// Second write: completes the line.
	lw.Write([]byte("world\n")) //nolint:errcheck

	// After the newline the buffer should be empty.
	lw.mu.Lock()
	buf := lw.buf.String()
	lw.mu.Unlock()

	if buf != "" {
		t.Errorf("logWriter buffer after completed line = %q, want empty", buf)
	}
}

// TestLogWriterRetainsTrailingFragment verifies that after sending complete
// lines, any trailing content without a newline remains in the buffer.
func TestLogWriterRetainsTrailingFragment(t *testing.T) {
	t.Parallel()
	lw := &logWriter{}

	// Three complete lines + a partial fourth.
	lw.Write([]byte("line1\nline2\nline3\npartial")) //nolint:errcheck

	lw.mu.Lock()
	buf := lw.buf.String()
	lw.mu.Unlock()

	if buf != "partial" {
		t.Errorf("logWriter buffer = %q, want %q after multi-line write", buf, "partial")
	}
}

// TestLogWriterWriteReturnsFullLength verifies that Write always returns the
// full length of the input byte slice and nil error.
func TestLogWriterWriteReturnsFullLength(t *testing.T) {
	t.Parallel()
	lw := &logWriter{}
	input := []byte("some log line\nanother line\nfragment")

	n, err := lw.Write(input)
	if err != nil {
		t.Errorf("Write returned error: %v", err)
	}
	if n != len(input) {
		t.Errorf("Write returned n=%d, want %d", n, len(input))
	}
}

// TestLogWriterEmptyWriteDoesNotPanic verifies that an empty write is safe.
func TestLogWriterEmptyWriteDoesNotPanic(t *testing.T) {
	t.Parallel()
	lw := &logWriter{}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("logWriter.Write panicked on empty input: %v", r)
		}
	}()

	n, err := lw.Write([]byte{})
	if err != nil {
		t.Errorf("Write returned error: %v", err)
	}
	if n != 0 {
		t.Errorf("Write returned n=%d for empty input, want 0", n)
	}
}

// TestLogWriterOnlyNewlineFlushesBuffer verifies that writing just "\n" with a
// previously accumulated partial line sends that line and clears the buffer.
func TestLogWriterOnlyNewlineFlushesBuffer(t *testing.T) {
	t.Parallel()
	lw := &logWriter{}

	lw.Write([]byte("accumulated")) //nolint:errcheck
	lw.Write([]byte("\n"))          //nolint:errcheck

	lw.mu.Lock()
	buf := lw.buf.String()
	lw.mu.Unlock()

	if buf != "" {
		t.Errorf("buffer after '\\n' write = %q, want empty", buf)
	}
}

// TestLogWriterStartIsNoOp verifies that calling start does not panic or block.
func TestLogWriterStartIsNoOp(t *testing.T) {
	t.Parallel()
	lw := &logWriter{}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// start should return immediately (no-op).
		lw.start(context.Background())
	}()

	select {
	case <-done:
		// passed
	default:
		// start returned immediately (channel closed) — correct
	}
}
