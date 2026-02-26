package idgen_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ilocn/stint/internal/idgen"
)

// TestNewTimeSortableID_Format verifies that every generated ID has the form:
//
//	<prefix>-<11 base36 chars>
//
// where all 11 chars are in the alphabet [0-9a-z].
func TestNewTimeSortableID_Format(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"g", "t", "w"} {
		id := idgen.NewTimeSortableID(prefix)
		want := prefix + "-"
		if !strings.HasPrefix(id, want) {
			t.Errorf("prefix=%q: ID %q does not start with %q", prefix, id, want)
		}
		suffix := id[len(prefix)+1:] // strip "prefix-"
		if len(suffix) != 11 {
			t.Errorf("prefix=%q: suffix of %q has len=%d, want 11", prefix, id, len(suffix))
		}
		for _, c := range suffix {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')) {
				t.Errorf("prefix=%q: ID %q contains non-base36 char %q", prefix, id, c)
			}
		}
	}
}

// TestNewTimeSortableID_TimeSortable verifies that lexicographic order of IDs
// produced at different points in time matches temporal order.
// The 8-char time component is left-padded base36, so lex < lex ⟺ earlier < later.
func TestNewTimeSortableID_TimeSortable(t *testing.T) {
	t.Parallel()
	id1 := idgen.NewTimeSortableID("t")
	time.Sleep(2 * time.Millisecond)
	id2 := idgen.NewTimeSortableID("t")

	suffix1 := id1[2:] // strip "t-"
	suffix2 := id2[2:]
	if suffix1 >= suffix2 {
		t.Errorf("temporal ordering violated: earlier ID %q >= later ID %q", id1, id2)
	}
}

// TestNewTimeSortableID_Unique generates 100 IDs rapidly and verifies there are
// no collisions. The random 3-char suffix gives 36^3=46 656 values per ms,
// making collisions negligible in practice and impossible in this test.
func TestNewTimeSortableID_Unique(t *testing.T) {
	t.Parallel()
	const n = 100
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		id := idgen.NewTimeSortableID("t")
		if seen[id] {
			t.Fatalf("duplicate ID after %d generations: %q", i, id)
		}
		seen[id] = true
	}
}

// TestNewTimeSortableID_Prefixes verifies that each canonical prefix (g, t, w)
// produces an ID that begins with that prefix followed by a dash, and that
// the suffixes are identical in length regardless of prefix length.
func TestNewTimeSortableID_Prefixes(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"g", "t", "w"} {
		t.Run(prefix, func(t *testing.T) {
			t.Parallel()
			id := idgen.NewTimeSortableID(prefix)
			if !strings.HasPrefix(id, prefix+"-") {
				t.Errorf("ID %q does not start with %q", id, prefix+"-")
			}
			// Total length: len(prefix) + 1 (dash) + 11 = 13 for single-char prefix.
			wantLen := len(prefix) + 1 + 11
			if len(id) != wantLen {
				t.Errorf("ID %q has len=%d, want %d", id, len(id), wantLen)
			}
			suffix := id[len(prefix)+1:]
			if len(suffix) != 11 {
				t.Errorf("suffix of %q has len=%d, want 11", id, len(suffix))
			}
		})
	}
}

// TestNewTimeSortableID_LengthConstraint verifies that the short part (after
// the prefix dash) is exactly 11 characters, which satisfies the design
// requirement that it is strictly less than 12.
func TestNewTimeSortableID_LengthConstraint(t *testing.T) {
	t.Parallel()
	// Run many times to hit multiple millisecond boundaries and random values.
	for i := 0; i < 200; i++ {
		id := idgen.NewTimeSortableID("g")
		suffix := id[2:] // strip "g-"
		if len(suffix) != 11 {
			t.Fatalf("iteration %d: suffix %q has len=%d, want exactly 11 (< 12)", i, suffix, len(suffix))
		}
	}
}

// TestNewTimeSortableID_UniquenessAcrossMilliseconds verifies uniqueness of 100
// IDs spread across multiple millisecond boundaries.
func TestNewTimeSortableID_UniquenessAcrossMilliseconds(t *testing.T) {
	t.Parallel()
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := idgen.NewTimeSortableID("t")
		if seen[id] {
			t.Errorf("duplicate ID generated: %q", id)
		}
		seen[id] = true
		if i%10 == 9 {
			time.Sleep(2 * time.Millisecond)
		}
	}
}

// TestNewTimeSortableID_NegativeMsClamp verifies that the function does not
// produce invalid IDs when the system clock is behind the custom epoch.
// Negative ms would cause FormatInt to emit a "-" prefix (not a base36 digit),
// breaking both the alphabet invariant and lexicographic sort ordering.
// The clamp ensures the output is always a valid, well-formed base36 suffix.
//
// We can't directly set the system clock, so this test validates the invariant
// property: the suffix always contains only [0-9a-z] across many rapid calls
// that exercise the padding path (time component currently 7 chars, padded to 8).
func TestNewTimeSortableID_NegativeMsClamp(t *testing.T) {
	t.Parallel()
	// Generate many IDs and assert none contain non-base36 characters.
	// This catches any regression where a "-" leaks into the suffix.
	for i := 0; i < 500; i++ {
		id := idgen.NewTimeSortableID("g")
		suffix := id[2:] // strip "g-"
		if len(suffix) != 11 {
			t.Fatalf("iteration %d: suffix %q has len=%d, want 11", i, suffix, len(suffix))
		}
		for pos, c := range suffix {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')) {
				t.Fatalf("iteration %d: suffix %q has invalid char %q at position %d (only [0-9a-z] allowed)", i, suffix, c, pos)
			}
		}
	}
}

// FuzzNewTimeSortableID_FormatInvariant is a fuzz test that verifies the
// format invariants (prefix, dash, 11-char base36 suffix) hold for arbitrary
// prefix strings.  It seeds the corpus with the three canonical prefixes.
//
// Run with: go test -fuzz=FuzzNewTimeSortableID_FormatInvariant -fuzztime=30s ./internal/idgen/
func FuzzNewTimeSortableID_FormatInvariant(f *testing.F) {
	f.Add("g")
	f.Add("t")
	f.Add("w")
	f.Add("x")
	f.Add("ab")

	f.Fuzz(func(t *testing.T, prefix string) {
		if prefix == "" {
			t.Skip("empty prefix produces no dash separator — skipping")
		}
		id := idgen.NewTimeSortableID(prefix)

		// ID must start with the prefix followed by a dash.
		wantPrefix := prefix + "-"
		if !strings.HasPrefix(id, wantPrefix) {
			t.Errorf("ID %q does not start with %q", id, wantPrefix)
		}

		// Suffix (after "prefix-") must be exactly 11 chars.
		suffix := id[len(wantPrefix):]
		if len(suffix) != 11 {
			t.Errorf("suffix of %q has len=%d, want 11", id, len(suffix))
		}

		// All suffix chars must be in [0-9a-z].
		for pos, c := range suffix {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')) {
				t.Errorf("suffix %q has invalid char %q at pos %d (only [0-9a-z] allowed)", suffix, c, pos)
			}
		}
	})
}
