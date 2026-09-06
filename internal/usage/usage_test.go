package usage

import "testing"

// Context is what an autocompact window bounds, so getting its definition
// wrong makes every regime and split figure wrong at once.
func TestContextIsEverythingSentUpNotJustFreshInput(t *testing.T) {
	tok := Tokens{Input: 3, CacheRead: 500, CacheCreation: 97, Output: 1000}
	if got := tok.Context(); got != 600 {
		t.Errorf("Context() = %d, want 600 (input + cache read + cache creation)", got)
	}
	if got := tok.Uploaded(); got != 600 {
		t.Errorf("Uploaded() = %d, want 600", got)
	}
	if got := tok.Downloaded(); got != 1000 {
		t.Errorf("Downloaded() = %d, want 1000 (output only)", got)
	}
}

// Thinking tokens are part of output. Adding them again would inflate both the
// download figure and, if it reached pricing, the cost.
func TestThinkingIsNotCountedOnTopOfOutput(t *testing.T) {
	tok := Tokens{Output: 1000, Thinking: 400}
	if got := tok.Downloaded(); got != 1000 {
		t.Errorf("Downloaded() = %d, want 1000; thinking is already inside output", got)
	}
}

// The TTL split decides whether cache writes are priced exactly or assumed.
func TestHasTTLSplitReportsWhetherTheTranscriptRecordedIt(t *testing.T) {
	if (Tokens{CacheCreation: 100}).HasTTLSplit() {
		t.Error("a transcript with no split reported one")
	}
	if !(Tokens{CacheCreation: 100, CacheWrite5m: 100}).HasTTLSplit() {
		t.Error("a recorded 5-minute split was not detected")
	}
	if !(Tokens{CacheCreation: 100, CacheWrite1h: 100}).HasTTLSplit() {
		t.Error("a recorded 1-hour split was not detected")
	}
}

// A session is bucketed into the smallest regime it fits inside. Getting the
// boundary wrong would put a capped session in the uncapped bucket, which is
// the exact comparison the tool exists to make.
func TestRegimeOfPicksTheSmallestCeilingTheSessionFitsIn(t *testing.T) {
	cases := []struct {
		peak int64
		want string
	}{
		{100_000, "~200k"},
		{200_000, "~200k"},
		{200_001, "~400k"},
		{400_000, "~400k"},
		{566_740, "~600k"}, // the largest post-cap session observed on this machine
		{600_000, "~600k"},
		{600_001, "~1M"},
		{999_679, "~1M"},
		{2_000_000, "~1M"}, // above every ceiling, it still lands in the largest
	}
	for _, c := range cases {
		if got := RegimeOf(c.peak).Name; got != c.want {
			t.Errorf("RegimeOf(%d) = %q, want %q", c.peak, got, c.want)
		}
	}
}

// The ceilings must stay ordered largest first, because RegimeOf and every
// report that sorts by ceiling depend on it.
func TestRegimesAreOrderedLargestFirst(t *testing.T) {
	for i := 1; i < len(Regimes); i++ {
		if Regimes[i].Ceiling >= Regimes[i-1].Ceiling {
			t.Fatalf("Regimes[%d] (%d) is not smaller than Regimes[%d] (%d)",
				i, Regimes[i].Ceiling, i-1, Regimes[i-1].Ceiling)
		}
	}
}
