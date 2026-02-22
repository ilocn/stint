package logbuf

import (
	"strings"
	"sync"
)

// LogBuf is a ring buffer of log lines with pub/sub support.
// It implements io.Writer so it can be used with log.SetOutput.
type LogBuf struct {
	mu    sync.Mutex
	lines []string
	max   int
	subs  []chan string
}

// New creates a LogBuf with the given maximum line capacity.
func New(max int) *LogBuf {
	return &LogBuf{max: max}
}

// Write implements io.Writer. It splits p on newlines, appends each complete
// line to the ring buffer, and notifies subscribers.
func (lb *LogBuf) Write(p []byte) (int, error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	s := string(p)
	parts := strings.Split(s, "\n")
	// All elements except the last are complete lines (the last may be empty or a partial line).
	for _, line := range parts[:len(parts)-1] {
		if line == "" {
			continue
		}
		lb.lines = append(lb.lines, line)
		if len(lb.lines) > lb.max {
			lb.lines = lb.lines[len(lb.lines)-lb.max:]
		}
		for _, ch := range lb.subs {
			select {
			case ch <- line:
			default:
				// drop if subscriber channel is full
			}
		}
	}
	return len(p), nil
}

// Subscribe returns a buffered channel that receives new log lines as they
// arrive.
func (lb *LogBuf) Subscribe() chan string {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	ch := make(chan string, 256)
	lb.subs = append(lb.subs, ch)
	return ch
}

// Unsubscribe removes a previously subscribed channel.
func (lb *LogBuf) Unsubscribe(ch chan string) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	for i, s := range lb.subs {
		if s == ch {
			lb.subs = append(lb.subs[:i], lb.subs[i+1:]...)
			return
		}
	}
}

// Lines returns a snapshot of all currently buffered lines.
func (lb *LogBuf) Lines() []string {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	result := make([]string, len(lb.lines))
	copy(result, lb.lines)
	return result
}
