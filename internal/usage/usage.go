// Package usage holds the domain of this tool: what one billed API attempt is,
// what a session is, and how a session's autocompact regime is derived.
//
// The central quantity is CONTEXT, not cost. Claude Code re-sends the whole
// conversation on every request, so a request's price is dominated by how much
// history it carried, and the autocompact window is the knob that caps that
// history. Everything here is arranged so that question can be answered from
// recorded data rather than from configuration.
package usage

import "time"

// BytesPerToken converts a token count to an approximate payload size.
//
// It is a documented approximation, not a measurement: the transcript records
// tokens, never bytes on the wire, and the real ratio varies with content and
// is further changed by transport compression. Every byte figure derived from
// it must be labelled an estimate wherever it is shown.
const BytesPerToken = 4

// Tokens is the token breakdown of one billed API attempt, named as the API
// names them.
type Tokens struct {
	Input         int64
	Output        int64
	Thinking      int64
	CacheRead     int64
	CacheCreation int64
	// CacheWrite5m and CacheWrite1h split CacheCreation by TTL when the
	// transcript records the split. They sum to CacheCreation when known and
	// are both zero when the transcript predates the breakdown.
	CacheWrite5m int64
	CacheWrite1h int64
}

// Context is everything the request carried up to the model: fresh input, the
// history replayed from cache, and the history written into cache on this
// attempt. This is the quantity an autocompact window bounds.
func (t Tokens) Context() int64 { return t.Input + t.CacheRead + t.CacheCreation }

// Uploaded is what went up the wire on this attempt. It is Context by another
// name, kept separate because the reports speak of upload and download while
// the domain speaks of context.
func (t Tokens) Uploaded() int64 { return t.Context() }

// Downloaded is what came back. Thinking tokens are part of Output and are not
// added again.
func (t Tokens) Downloaded() int64 { return t.Output }

// HasTTLSplit reports whether the transcript recorded which cache TTL wrote
// the cache-creation tokens. When it did not, a caller must price them at one
// rate and say that it assumed.
func (t Tokens) HasTTLSplit() bool { return t.CacheWrite5m+t.CacheWrite1h > 0 }

// Request is one billed API attempt: the unit the bill is made of.
//
// One assistant message can be several attempts. When the API refused an
// attempt and answered with a fallback model, both were billed under different
// models, and each is a Request of its own distinguished by Iteration.
type Request struct {
	ConfigDir   string
	Project     string
	SessionID   string
	MessageID   string
	Iteration   int
	RequestID   string
	Timestamp   time.Time
	Model       string
	Effort      string
	Version     string
	GitBranch   string
	IsSidechain bool
	Tokens      Tokens
}

// CostState is Claude Code's own accounting for a session, read from the
// transcript's cost-state record.
//
// It is present in a minority of transcripts (10.5% of 354 measured on
// 2026-09-06), so it is an oracle to check against and a source of API timing
// where it exists, never the collection path. A consumer must branch on
// Present rather than on a zero value, because a real session can legitimately
// have cost zero.
type CostState struct {
	Present bool
	// TotalCostUSD is Claude Code's own figure for the session.
	TotalCostUSD float64
	// APIDuration is time actually spent in the API. It is the only timing
	// this corpus offers: individual assistant records carry no duration, so
	// a rate computed from consecutive record timestamps would be measuring
	// tool execution and human thinking time too.
	APIDuration      time.Duration
	ToolDuration     time.Duration
	WallDuration     time.Duration
	UnknownModelCost bool
}

// Session aggregates the requests of one Claude Code session.
type Session struct {
	ConfigDir   string
	Project     string
	SessionID   string
	StartedAt   time.Time
	EndedAt     time.Time
	Requests    int
	HumanTurns  int
	Compactions int
	// PeakContext is the largest context any single request in the session
	// carried. It is how the autocompact window is observed: the window is a
	// launch flag that the transcript never records, but a session cannot
	// exceed it, so the peak is the ceiling made visible.
	PeakContext int64
	Tokens      Tokens
	CostState   CostState
}

// Regime is an autocompact window inferred from what a session actually did.
//
// The --autocompact flag is passed at launch and appears nowhere in the
// transcript, so it cannot be read. What can be read is the largest context a
// session reached, and a session cannot exceed its window. Bucketing sessions
// by that peak recovers the regime from data, which is the whole point: it
// makes the comparison arguable without trusting anyone's memory of when a
// setting was changed.
type Regime struct {
	// Name labels the bucket, e.g. "~1M" or "~600k".
	Name string
	// Ceiling is the window this bucket represents, in tokens.
	Ceiling int64
}

// Regimes are the buckets sessions are sorted into, largest ceiling first.
// A session falls in the first regime whose ceiling it does not exceed.
var Regimes = []Regime{
	{Name: "~1M", Ceiling: 1_000_000},
	{Name: "~600k", Ceiling: 600_000},
	{Name: "~400k", Ceiling: 400_000},
	{Name: "~200k", Ceiling: 200_000},
}

// RegimeOf buckets a session by its peak context.
//
// ⚠ A session that simply never grew large is indistinguishable from one that
// was capped low, so a single short session proves nothing about its window.
// The bucket is only meaningful in aggregate, and a report using it must say
// so beside the number.
func RegimeOf(peakContext int64) Regime {
	best := Regimes[0]
	for _, r := range Regimes {
		if peakContext <= r.Ceiling {
			best = r
		}
	}
	return best
}
