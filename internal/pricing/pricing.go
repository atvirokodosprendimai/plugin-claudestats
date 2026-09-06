// Package pricing prices token usage from an embedded, versioned list-price
// table.
//
// The table is data, not code: its provenance and date travel with it, and a
// model missing from it is reported as unpriced rather than guessed. The price
// data is carried over verbatim from the statsv1 project, whose opus-5 row was
// checked against Claude Code's own lastCost figure; the provenance string in
// prices.json is that check's only durable record, so do not edit it away.
package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/atvirokodosprendimai/plugin-claudestats/internal/usage"
)

//go:embed prices.json
var pricesJSON []byte

// Price is the list price of one model in USD per million tokens.
type Price struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheRead    float64 `json:"cache_read"`
	CacheWrite5m float64 `json:"cache_write_5m"`
	CacheWrite1h float64 `json:"cache_write_1h"`
	// Verified is true when the row was copied from a documented price table
	// rather than recalled from memory.
	Verified bool   `json:"verified"`
	Note     string `json:"note,omitempty"`
}

// Table is the price list with its provenance.
type Table struct {
	Source string           `json:"source"`
	AsOf   string           `json:"as_of"`
	Models map[string]Price `json:"models"`
}

// Load returns the embedded table.
func Load() (*Table, error) {
	var t Table
	if err := json.Unmarshal(pricesJSON, &t); err != nil {
		return nil, fmt.Errorf("pricing: embedded prices.json: %w", err)
	}
	return &t, nil
}

// ModelIDs lists the priced models in a stable order.
func (t *Table) ModelIDs() []string {
	ids := make([]string, 0, len(t.Models))
	for id := range t.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

var dateSuffix = regexp.MustCompile(`-\d{8}$`)

// Normalize maps a transcript model id onto a price-table key: lower case,
// with the "[1m]" context marker and a trailing -YYYYMMDD snapshot date
// removed, so claude-sonnet-4-5-20250929 prices as claude-sonnet-4-5.
func Normalize(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimSuffix(m, "[1m]")
	return dateSuffix.ReplaceAllString(m, "")
}

// Cost is the priced value of one request, split the way the question is
// usually asked: what did the fresh input cost, what did the answer cost, and
// what did replaying the history cost.
type Cost struct {
	InputUSD      float64
	OutputUSD     float64
	CacheReadUSD  float64
	CacheWriteUSD float64
	// Priced is false when the model is not in the table; every USD field is
	// then zero and the request must be counted as unpriced, never as free.
	Priced bool
	// Verified is false when the price used came from memory rather than a
	// documented table.
	Verified bool
	// AssumedTTLTokens counts cache-write tokens priced at the 5-minute rate
	// because the transcript did not record which TTL wrote them, so a report
	// can say how much of its total rests on that assumption.
	AssumedTTLTokens int64
}

// Total is the whole priced value of the request.
func (c Cost) Total() float64 {
	return c.InputUSD + c.OutputUSD + c.CacheReadUSD + c.CacheWriteUSD
}

const perMillion = 1_000_000

// Cost prices one request.
//
// Cache writes are priced per TTL when the transcript carries the breakdown.
// When it does not, every cache-write token is priced at the cheaper 5-minute
// rate and counted in AssumedTTLTokens: choosing the cheaper rate keeps the
// result a lower bound, which is the safe direction for a number used to argue
// that something got cheaper.
func (t *Table) Cost(model string, u usage.Tokens) Cost {
	p, ok := t.Models[Normalize(model)]
	if !ok {
		return Cost{}
	}
	c := Cost{Priced: true, Verified: p.Verified}
	c.InputUSD = float64(u.Input) * p.Input / perMillion
	c.OutputUSD = float64(u.Output) * p.Output / perMillion
	c.CacheReadUSD = float64(u.CacheRead) * p.CacheRead / perMillion

	if u.HasTTLSplit() {
		c.CacheWriteUSD = float64(u.CacheWrite5m)*p.CacheWrite5m/perMillion +
			float64(u.CacheWrite1h)*p.CacheWrite1h/perMillion
		return c
	}
	c.CacheWriteUSD = float64(u.CacheCreation) * p.CacheWrite5m / perMillion
	c.AssumedTTLTokens = u.CacheCreation
	return c
}
