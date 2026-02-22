package idgen

import (
	"strconv"
	"sync"
	"time"
)

// epochMs is the custom epoch (2024-01-01T00:00:00Z) in milliseconds.
const epochMs int64 = 1704067200000

// nowMs returns the current time as milliseconds since epochMs.
// It is a variable so tests can override it.
var nowMs = func() int64 {
	return time.Now().UnixMilli() - epochMs
}

// mu, lastMs, and seqCounter provide monotonic, sortable IDs within the same
// millisecond.  seqCounter increments when two calls fall in the same ms;
// it resets to 0 on a new ms.
var (
	mu         sync.Mutex
	lastMs     int64 = -1
	seqCounter int64
)

// NewTimeSortableID returns a time-sortable ID with the given prefix letter.
// The format is: prefix + "-" + 8-char-time + 3-char-random (11 chars total).
//
// Time component: milliseconds since 2024-01-01 encoded in base36, left-padded
// to 8 chars with '0'. Rand component: a per-ms sequential counter mod 46656
// (36^3) encoded in base36, left-padded to 3 chars with '0'.
//
// Lexicographic sort of the 11-char suffix equals temporal sort because base36
// uses digits 0-9 then a-z, so '0' < '1' < ... < '9' < 'a' < ... < 'z'.
//
// The 8-char time component overflows (becomes 9 chars) at ms = 36^8, which
// corresponds to approximately 2113-05-25. Until then the suffix is always
// exactly 11 chars.
func NewTimeSortableID(prefix string) string {
	mu.Lock()
	ms := nowMs()
	// Clamp to 0 if the system clock is behind the epoch (extreme NTP skew):
	// FormatInt of a negative value produces a "-" prefix which is not a
	// base36 digit and would break both the alphabet invariant and lexicographic
	// sort ordering.
	if ms < 0 {
		ms = 0
	}
	if ms == lastMs {
		seqCounter++
	} else {
		lastMs = ms
		seqCounter = 0
	}
	randVal := seqCounter % 46656 // 46656 = 36^3
	mu.Unlock()

	// Time component: ms since custom epoch, base36, left-padded to 8 chars.
	timeStr := strconv.FormatInt(ms, 36)
	for len(timeStr) < 8 {
		timeStr = "0" + timeStr
	}

	// Random component: sequential counter mod 36^3, base36, left-padded to 3 chars.
	randStr := strconv.FormatInt(randVal, 36)
	for len(randStr) < 3 {
		randStr = "0" + randStr
	}

	return prefix + "-" + timeStr + randStr
}
