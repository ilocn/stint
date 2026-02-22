package idgen

import (
	"strings"
	"testing"
)

// TestNegativeMsClamp verifies that when the system clock returns a timestamp
// before the custom epoch (ms < 0), the clamp sets ms = 0, producing a
// well-formed ID with the zero time prefix rather than a "-" prefix character
// that would violate the base36 alphabet and lexicographic sort invariants.
func TestNegativeMsClamp(t *testing.T) {
	// Override nowMs to return a negative value (clock behind epoch).
	orig := nowMs
	nowMs = func() int64 { return -100 }
	defer func() { nowMs = orig }()

	// Force a new-ms reset by pointing lastMs away from -100.
	mu.Lock()
	lastMs = -999 // ensure the new-ms branch runs (lastMs != -100)
	seqCounter = 0
	mu.Unlock()

	id := NewTimeSortableID("g")

	// The ID must start with "g-".
	if !strings.HasPrefix(id, "g-") {
		t.Errorf("ID %q does not start with 'g-'", id)
	}
	// Suffix (after "g-") must be exactly 11 base36 chars with no "-".
	suffix := id[2:]
	if len(suffix) != 11 {
		t.Errorf("suffix %q has len=%d, want 11", suffix, len(suffix))
	}
	// Time component (first 8 chars) must be all '0' because ms was clamped to 0.
	timeComp := suffix[:8]
	for _, c := range timeComp {
		if c != '0' {
			t.Errorf("time component %q should be all zeros when ms < 0, got %q", timeComp, string(c))
		}
	}
	// All chars must be valid base36 (no '-').
	for pos, c := range suffix {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')) {
			t.Errorf("suffix %q has non-base36 char %q at pos %d", suffix, string(c), pos)
		}
	}
}

// TestRandStrPadding forces seqCounter to a value that produces a 1-char or
// 2-char base36 rand string, ensuring the "0"-padding loop in NewTimeSortableID
// is exercised.
//
// The code does seqCounter++ when ms == lastMs, so we set seqCounter = target-1
// and lastMs = nowMs() to trigger the same-ms branch (increment).
func TestRandStrPadding(t *testing.T) {
	const sentinelMs int64 = 999999
	tests := []struct {
		name     string
		target   int64 // desired randVal (seqCounter % 46656)
		wantRand string
	}{
		{"zero randVal pads to 000", 0, "000"},
		{"single char z pads to 00z", 35, "00z"},
		{"two chars 10 pads to 010", 36, "010"},
		{"two chars zz pads to 0zz", 1295, "0zz"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Override nowMs to return our sentinel value.
			orig := nowMs
			nowMs = func() int64 { return sentinelMs }
			defer func() { nowMs = orig }()

			// Set lastMs = sentinelMs and seqCounter = target - 1 so that
			// the same-ms branch (seqCounter++) produces seqCounter = target,
			// giving randVal = target % 46656 = target (since target < 46656).
			mu.Lock()
			lastMs = sentinelMs
			seqCounter = tc.target - 1
			mu.Unlock()

			id := NewTimeSortableID("t")

			// Total length: 1 (prefix) + 1 (dash) + 11 (suffix) = 13.
			if len(id) != 13 {
				t.Fatalf("ID %q has len=%d, want 13", id, len(id))
			}
			// Last 3 chars of the 11-char suffix are the rand component.
			suffix := id[2:]      // strip "t-" → 11 chars
			gotRand := suffix[8:] // last 3 chars
			if gotRand != tc.wantRand {
				t.Errorf("rand component = %q, want %q (target=%d)", gotRand, tc.wantRand, tc.target)
			}
		})
	}
}
