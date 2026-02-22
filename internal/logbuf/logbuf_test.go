package logbuf

import (
	"fmt"
	"strings"
	"testing"
)

// TestWriteSingleLine verifies that Write stores a single complete line.
func TestWriteSingleLine(t *testing.T) {
	t.Parallel()
	lb := New(100)
	n, err := lb.Write([]byte("hello world\n"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 12 {
		t.Errorf("n = %d, want 12", n)
	}
	lines := lb.Lines()
	if len(lines) != 1 || lines[0] != "hello world" {
		t.Errorf("Lines() = %v, want [hello world]", lines)
	}
}

// TestWriteMultipleLines verifies that Write stores multiple lines in one call.
func TestWriteMultipleLines(t *testing.T) {
	t.Parallel()
	lb := New(100)
	_, err := lb.Write([]byte("line1\nline2\nline3\n"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	lines := lb.Lines()
	if len(lines) != 3 {
		t.Fatalf("len(Lines) = %d, want 3", len(lines))
	}
	for i, want := range []string{"line1", "line2", "line3"} {
		if lines[i] != want {
			t.Errorf("lines[%d] = %q, want %q", i, lines[i], want)
		}
	}
}

// TestRingBufferOverflow verifies that when max is exceeded, oldest lines are dropped.
func TestRingBufferOverflow(t *testing.T) {
	t.Parallel()
	max := 5
	lb := New(max)
	for i := 0; i < max+3; i++ {
		lb.Write([]byte(fmt.Sprintf("line %d\n", i))) //nolint:errcheck
	}
	lines := lb.Lines()
	if len(lines) != max {
		t.Fatalf("len(Lines) = %d, want %d after overflow", len(lines), max)
	}
	// First remaining line should be line 3 (lines 0, 1, 2 were dropped).
	if lines[0] != "line 3" {
		t.Errorf("lines[0] = %q, want %q", lines[0], "line 3")
	}
}

// TestSubscribeReceivesNewLines verifies that a subscriber gets new lines.
func TestSubscribeReceivesNewLines(t *testing.T) {
	t.Parallel()
	lb := New(100)
	ch := lb.Subscribe()
	defer lb.Unsubscribe(ch)

	lb.Write([]byte("test line\n")) //nolint:errcheck

	select {
	case line := <-ch:
		if line != "test line" {
			t.Errorf("subscriber received %q, want %q", line, "test line")
		}
	default:
		t.Error("subscriber did not receive the line")
	}
}

// TestUnsubscribeStopsDelivery verifies that after Unsubscribe, no more lines are sent.
func TestUnsubscribeStopsDelivery(t *testing.T) {
	t.Parallel()
	lb := New(100)
	ch := lb.Subscribe()
	lb.Unsubscribe(ch)

	lb.Write([]byte("post-unsub line\n")) //nolint:errcheck

	select {
	case line := <-ch:
		t.Errorf("received line %q after unsubscribe", line)
	default:
		// correct: no delivery after unsubscribe
	}
}

// TestLinesSnapshot verifies Lines returns an independent snapshot.
func TestLinesSnapshot(t *testing.T) {
	t.Parallel()
	lb := New(100)
	lb.Write([]byte("a\nb\nc\n")) //nolint:errcheck

	snap := lb.Lines()
	lb.Write([]byte("d\n")) //nolint:errcheck

	if len(snap) != 3 {
		t.Errorf("snapshot len = %d, want 3 (snapshot must not be affected by later writes)", len(snap))
	}
}

// TestWritePartialLineNotStored verifies that a line without a trailing newline
// is not stored (treated as a partial line).
func TestWritePartialLineNotStored(t *testing.T) {
	t.Parallel()
	lb := New(100)
	lb.Write([]byte("no newline")) //nolint:errcheck

	lines := lb.Lines()
	if len(lines) != 0 {
		t.Errorf("Lines() = %v, want empty (partial line without newline)", lines)
	}
}

// TestWriteEmptyInput verifies that an empty write does not panic.
func TestWriteEmptyInput(t *testing.T) {
	t.Parallel()
	lb := New(100)
	n, err := lb.Write([]byte{})
	if err != nil {
		t.Errorf("Write: %v", err)
	}
	if n != 0 {
		t.Errorf("n = %d, want 0", n)
	}
}

// TestMultipleSubscribers verifies all subscribers receive the same lines.
func TestMultipleSubscribers(t *testing.T) {
	t.Parallel()
	lb := New(100)
	ch1 := lb.Subscribe()
	ch2 := lb.Subscribe()
	defer lb.Unsubscribe(ch1)
	defer lb.Unsubscribe(ch2)

	lb.Write([]byte("broadcast\n")) //nolint:errcheck

	for i, ch := range []chan string{ch1, ch2} {
		select {
		case line := <-ch:
			if line != "broadcast" {
				t.Errorf("subscriber %d received %q, want %q", i+1, line, "broadcast")
			}
		default:
			t.Errorf("subscriber %d did not receive the line", i+1)
		}
	}
}

// TestWriteReturnsFullLength verifies Write always returns len(p).
func TestWriteReturnsFullLength(t *testing.T) {
	t.Parallel()
	lb := New(100)
	input := []byte("foo\nbar\nbaz\n")
	n, err := lb.Write(input)
	if err != nil {
		t.Errorf("Write: %v", err)
	}
	if n != len(input) {
		t.Errorf("n = %d, want %d", n, len(input))
	}
}

// TestLinesExactlyAtMax verifies that at max capacity, all lines are kept.
func TestLinesExactlyAtMax(t *testing.T) {
	t.Parallel()
	max := 3
	lb := New(max)
	for i := 0; i < max; i++ {
		lb.Write([]byte(fmt.Sprintf("line%d\n", i))) //nolint:errcheck
	}
	lines := lb.Lines()
	if len(lines) != max {
		t.Errorf("len(Lines) = %d, want %d", len(lines), max)
	}
}

// TestNewReturnsNonNil verifies that New always returns a non-nil LogBuf.
func TestNewReturnsNonNil(t *testing.T) {
	t.Parallel()
	lb := New(10)
	if lb == nil {
		t.Error("New returned nil")
	}
}

// TestEmptyLinesOnNew verifies that a new LogBuf has no lines.
func TestEmptyLinesOnNew(t *testing.T) {
	t.Parallel()
	lb := New(100)
	lines := lb.Lines()
	if len(lines) != 0 {
		t.Errorf("new LogBuf has %d lines, want 0", len(lines))
	}
}

// TestSkipsEmptyLinesFromBlankLines verifies that blank lines (just "\n") don't add empty strings.
func TestSkipsEmptyLinesFromBlankLines(t *testing.T) {
	t.Parallel()
	lb := New(100)
	lb.Write([]byte("line1\n\nline2\n")) //nolint:errcheck
	lines := lb.Lines()
	if len(lines) != 2 {
		t.Errorf("Lines() = %v (len=%d), want 2 non-empty lines", lines, len(lines))
	}
	joined := strings.Join(lines, " ")
	if !strings.Contains(joined, "line1") || !strings.Contains(joined, "line2") {
		t.Errorf("Lines() = %v, want [line1, line2]", lines)
	}
}
