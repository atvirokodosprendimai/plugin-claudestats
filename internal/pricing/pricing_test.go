package pricing

import (
	"math"
	"testing"

	"github.com/atvirokodosprendimai/plugin-claudestats/internal/usage"
)

func load(t *testing.T) *Table {
	t.Helper()
	tab, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return tab
}

// The embedded table is data with provenance; an empty or unparseable one
// would silently price every request at zero.
func TestTheEmbeddedTableLoadsWithItsProvenance(t *testing.T) {
	tab := load(t)
	if tab.Source == "" || tab.AsOf == "" {
		t.Error("the table lost its source or date, which is what makes its numbers checkable")
	}
	if len(tab.Models) == 0 {
		t.Fatal("no models in the table")
	}
}

// A transcript model id carries a context marker and sometimes a snapshot
// date; neither appears in the price table, so a parser that does not strip
// them prices the request at zero and reports it as unpriced.
func TestNormalizeStripsTheMarkersThePriceTableDoesNotCarry(t *testing.T) {
	cases := map[string]string{
		"claude-opus-5[1m]":          "claude-opus-5",
		"claude-sonnet-4-5-20250929": "claude-sonnet-4-5",
		"  Claude-Opus-5  ":          "claude-opus-5",
		"claude-haiku-4-5":           "claude-haiku-4-5",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every id the normaliser produces from a real transcript must resolve, or the
// request is counted in tokens and dropped from the cost.
func TestTheModelThisMachineActuallyUsesIsPriced(t *testing.T) {
	tab := load(t)
	for _, id := range []string{"claude-opus-5[1m]", "claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5"} {
		if c := tab.Cost(id, usage.Tokens{Input: 1}); !c.Priced {
			t.Errorf("%s is not in the price table, so its requests would be reported as unpriced", id)
		}
	}
}

// The four components are priced separately because the report splits them,
// and a component summed into the wrong bucket would still total correctly and
// still be wrong on every chart.
func TestEachComponentIsPricedIntoItsOwnBucket(t *testing.T) {
	tab := load(t)
	// claude-opus-5: input 5, output 25, cache read 0.5, cache write 1h 10,
	// all per million tokens.
	got := tab.Cost("claude-opus-5", usage.Tokens{
		Input: 1_000_000, Output: 1_000_000,
		CacheRead: 1_000_000, CacheCreation: 1_000_000, CacheWrite1h: 1_000_000,
	})
	for _, c := range []struct {
		name string
		got  float64
		want float64
	}{
		{"input", got.InputUSD, 5},
		{"output", got.OutputUSD, 25},
		{"cache read", got.CacheReadUSD, 0.5},
		{"cache write 1h", got.CacheWriteUSD, 10},
	} {
		if math.Abs(c.got-c.want) > 1e-9 {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if math.Abs(got.Total()-40.5) > 1e-9 {
		t.Errorf("total = %v, want 40.5", got.Total())
	}
}

// When the transcript did not record which cache TTL wrote the tokens, the
// cheaper rate is used ON PURPOSE, so the figure stays a lower bound. Pricing
// the assumption high would let the tool overstate a saving.
func TestAnUnrecordedCacheTTLIsPricedLowAndDeclared(t *testing.T) {
	tab := load(t)
	got := tab.Cost("claude-opus-5", usage.Tokens{CacheCreation: 1_000_000})
	if math.Abs(got.CacheWriteUSD-6.25) > 1e-9 {
		t.Errorf("cache write = %v, want 6.25 (the cheaper 5-minute rate)", got.CacheWriteUSD)
	}
	if got.AssumedTTLTokens != 1_000_000 {
		t.Errorf("AssumedTTLTokens = %d, want 1000000 so the report can disclose the assumption",
			got.AssumedTTLTokens)
	}
	// With the split recorded, nothing is assumed.
	split := tab.Cost("claude-opus-5", usage.Tokens{
		CacheCreation: 1_000_000, CacheWrite1h: 1_000_000,
	})
	if split.AssumedTTLTokens != 0 {
		t.Errorf("AssumedTTLTokens = %d with a recorded split, want 0", split.AssumedTTLTokens)
	}
}

// An unknown model must be reported as unpriced rather than guessed, and its
// cost must be zero so it can never quietly enter a total.
func TestAnUnknownModelIsUnpricedRatherThanGuessed(t *testing.T) {
	tab := load(t)
	got := tab.Cost("some-model-that-does-not-exist", usage.Tokens{Input: 1_000_000, Output: 1_000_000})
	if got.Priced {
		t.Error("an unknown model reported Priced true")
	}
	if got.Total() != 0 {
		t.Errorf("total = %v, want 0 for an unknown model", got.Total())
	}
}

// A row copied from memory rather than a documented table must say so, because
// the report separates cost computed from unverified rows.
func TestAnUnverifiedRowIsFlagged(t *testing.T) {
	tab := load(t)
	got := tab.Cost("claude-opus-4-1", usage.Tokens{Input: 1})
	if !got.Priced {
		t.Fatal("claude-opus-4-1 is missing from the table")
	}
	if got.Verified {
		t.Error("claude-opus-4-1 is marked verified, but prices.json records it as from memory")
	}
}
