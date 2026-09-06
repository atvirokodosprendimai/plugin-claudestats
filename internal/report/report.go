// Package report aggregates stored attempts into the buckets the charts and
// tables are drawn from.
//
// Every rate here is per REQUEST or per HUMAN TURN, never a bare total,
// because the question this tool exists to answer is whether a request got
// cheaper, and a bare total moves with how much work was done. The identity
// that ties the two together is:
//
//	cost per human turn = requests per turn x cost per request
//
// That identity is what stops a false conclusion in either direction. Halving
// the context can halve the cost of a request and still cost more per turn, if
// the smaller window forces more compactions and therefore more requests. It
// can also leave the unit price untouched and still halve the cost per turn,
// if the smaller window changes how many requests a piece of work takes. Only
// the pair of figures says which happened, so no report here prints one
// without the other.
package report

import (
	"fmt"
	"sort"
	"time"

	"github.com/atvirokodosprendimai/plugin-claudestats/internal/store"
	"github.com/atvirokodosprendimai/plugin-claudestats/internal/usage"
)

// Bucket is one period of aggregated usage.
type Bucket struct {
	// Key is the bucket label, e.g. "2026-09-05" or "2026-W36".
	Key   string
	Start time.Time

	Sessions int
	Requests int
	Turns    int

	InputTokens         int64
	OutputTokens        int64
	ThinkingTokens      int64
	CacheReadTokens     int64
	CacheCreationTokens int64

	InputUSD      float64
	OutputUSD     float64
	CacheReadUSD  float64
	CacheWriteUSD float64

	UnpricedRequests int
	AssumedTTLTokens int64
	Compactions      int

	// PeakContext is the largest context any single REQUEST in this period
	// carried, and MeanPeakContext averages the per-SESSION peaks.
	//
	// ⚠ Read PeakContext, not the mean, when asking whether a context ceiling
	// moved. The mean is taken over sessions while everything else here is
	// taken over requests, so a period holding one long session and three
	// short ones has a low mean peak and a high per-request context at the
	// same time. That divergence is not a bug and it has already misled one
	// reading of this data.
	PeakContext     int64
	MeanPeakContext float64

	// APIDuration totals Claude Code's own recorded API time for sessions in
	// this bucket that carried a cost-state record, and APISessions counts
	// them. Throughput is only meaningful when APISessions is a decent share
	// of Sessions, so the two travel together and are never separated.
	APIDuration time.Duration
	APISessions int
}

// TotalUSD is everything this bucket cost.
func (b Bucket) TotalUSD() float64 {
	return b.InputUSD + b.OutputUSD + b.CacheReadUSD + b.CacheWriteUSD
}

// UploadedTokens is what went up the wire: fresh input plus the history, both
// the part replayed from cache and the part written to it.
func (b Bucket) UploadedTokens() int64 {
	return b.InputTokens + b.CacheReadTokens + b.CacheCreationTokens
}

// DownloadedTokens is what came back. Thinking is part of output, not extra.
func (b Bucket) DownloadedTokens() int64 { return b.OutputTokens }

// USDPerRequest is the headline unit price.
func (b Bucket) USDPerRequest() float64 { return div(b.TotalUSD(), float64(b.Requests)) }

// USDPerTurn is what one thing a person asked for cost.
func (b Bucket) USDPerTurn() float64 { return div(b.TotalUSD(), float64(b.Turns)) }

// RequestsPerTurn is the other half of the identity in the package comment.
func (b Bucket) RequestsPerTurn() float64 { return div(float64(b.Requests), float64(b.Turns)) }

// UploadedPerRequest is the average payload sent up per request, in tokens.
// This is the quantity an autocompact window bounds, so it is the one that
// should move when the window moves.
func (b Bucket) UploadedPerRequest() float64 {
	return div(float64(b.UploadedTokens()), float64(b.Requests))
}

// DownloadedPerRequest is the average answer size, in tokens.
func (b Bucket) DownloadedPerRequest() float64 {
	return div(float64(b.DownloadedTokens()), float64(b.Requests))
}

// CacheReadPerRequest is the history replayed on an average request. It
// usually dominates both the payload and the price.
func (b Bucket) CacheReadPerRequest() float64 {
	return div(float64(b.CacheReadTokens), float64(b.Requests))
}

// CompactionsPerTurn is how often the window filled up per thing asked for.
// A lower context ceiling should raise it, and that is the cost side of the
// trade the ceiling buys.
func (b Bucket) CompactionsPerTurn() float64 {
	return div(float64(b.Compactions), float64(b.Turns))
}

// UploadKbps estimates upload throughput in kilobits per second, from the
// bytes implied by the token count and Claude Code's own recorded API time.
//
// ⚠ It is an estimate twice over and must be labelled as one wherever shown:
// bytes are inferred from tokens through usage.BytesPerToken rather than
// measured, and the API duration covers only the sessions that carried a
// cost-state record. It returns 0 when no session in the bucket did, because a
// rate computed from consecutive record timestamps would be measuring tool
// execution and human thinking time, which is not a transfer rate.
func (b Bucket) UploadKbps() float64 { return b.kbps(b.UploadedTokens()) }

// DownloadKbps estimates download throughput. The same two caveats apply.
func (b Bucket) DownloadKbps() float64 { return b.kbps(b.DownloadedTokens()) }

func (b Bucket) kbps(tokens int64) float64 {
	if b.APISessions == 0 || b.APIDuration <= 0 || b.Sessions == 0 {
		return 0
	}
	// Scale the payload to the share of sessions the timing actually covers,
	// so a bucket where one session in ten reported time is not read as if
	// the whole bucket's bytes moved in that one session's seconds.
	share := float64(b.APISessions) / float64(b.Sessions)
	bits := float64(tokens) * share * usage.BytesPerToken * 8
	return bits / b.APIDuration.Seconds() / 1000
}

// addRequest folds one billed attempt into a bucket. Build, ByRegime and
// SplitAt all aggregate the same way and share this so the three can never
// drift into computing subtly different totals.
func (b *Bucket) addRequest(r store.Request) {
	b.Requests++
	b.InputTokens += r.InputTokens
	b.OutputTokens += r.OutputTokens
	b.ThinkingTokens += r.ThinkingTokens
	b.CacheReadTokens += r.CacheReadTokens
	b.CacheCreationTokens += r.CacheCreationTokens
	b.InputUSD += r.InputUSD
	b.OutputUSD += r.OutputUSD
	b.CacheReadUSD += r.CacheReadUSD
	b.CacheWriteUSD += r.CacheWriteUSD
	b.AssumedTTLTokens += r.AssumedTTLTokens
	if !r.Priced {
		b.UnpricedRequests++
	}
	if r.ContextTokens > b.PeakContext {
		b.PeakContext = r.ContextTokens
	}
}

// addSession folds one session's facts in, exactly once per bucket.
func (b *Bucket) addSession(s store.Session) {
	b.Sessions++
	b.Turns += s.HumanTurns
	b.Compactions += s.Compactions
	b.MeanPeakContext += float64(s.PeakContext)
	if s.CostStatePresent && s.CCAPIDurationMS > 0 {
		b.APIDuration += time.Duration(s.CCAPIDurationMS) * time.Millisecond
		b.APISessions++
	}
}

// finish converts the accumulated per-session peak sum into a mean.
func (b *Bucket) finish() {
	if b.Sessions > 0 {
		b.MeanPeakContext /= float64(b.Sessions)
	}
}

// Period selects how requests are bucketed.
type Period string

// The supported bucket widths.
const (
	Daily   Period = "day"
	Weekly  Period = "week"
	Monthly Period = "month"
)

func index(sessions []store.Session) map[string]store.Session {
	m := make(map[string]store.Session, len(sessions))
	for _, s := range sessions {
		m[s.SessionID] = s
	}
	return m
}

// Build aggregates requests into buckets of the given period, folding in the
// session facts (turns, peaks, API time) that only the sessions table holds.
func Build(reqs []store.Request, sessions []store.Session, p Period) []Bucket {
	byID := index(sessions)
	acc := map[string]*Bucket{}
	// A session is counted once per bucket, so a session spanning midnight is
	// not counted twice in one day's figures.
	seen := map[string]bool{}

	for _, r := range reqs {
		key, start := bucketKey(r.TS, p)
		b := acc[key]
		if b == nil {
			b = &Bucket{Key: key, Start: start}
			acc[key] = b
		}
		b.addRequest(r)
		if k := key + "\x00" + r.SessionID; !seen[k] {
			seen[k] = true
			if s, ok := byID[r.SessionID]; ok {
				b.addSession(s)
			}
		}
	}

	out := make([]Bucket, 0, len(acc))
	for _, b := range acc {
		b.finish()
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

func bucketKey(ts time.Time, p Period) (string, time.Time) {
	ts = ts.UTC()
	switch p {
	case Weekly:
		y, w := ts.ISOWeek()
		// Monday of the ISO week, so buckets sort chronologically by Start.
		start := ts.AddDate(0, 0, -(int(ts.Weekday())+6)%7)
		start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
		return isoWeekLabel(y, w), start
	case Monthly:
		start := time.Date(ts.Year(), ts.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start.Format("2006-01"), start
	default:
		start := time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC)
		return start.Format("2006-01-02"), start
	}
}

// isoWeekLabel formats an ISO week as 2026-W36. The ISO year is used, not the
// calendar year of any particular day, because the two disagree in the days
// either side of New Year and a mismatched label would sort a week wrongly.
func isoWeekLabel(year, week int) string {
	return fmt.Sprintf("%04d-W%02d", year, week)
}

// Split is a before-and-after comparison around one instant.
//
// This is the honest instrument for "I changed a setting on this date, what
// happened", and it is deliberately keyed on when a session STARTED rather
// than on when a request was sent. A launch flag is fixed for the life of the
// session it launched, so a session begun before the change keeps the old
// setting for as long as it runs, sometimes for days. Bucketing its later
// requests as "after" is precisely the mistake that makes a working change
// look like it did nothing.
type Split struct {
	At     time.Time
	Before Bucket
	After  Bucket
	// BeforeCarried counts requests sent after the instant by sessions that
	// began before it. They are attributed to Before, and reporting the count
	// is what lets a reader see how much of the later calendar time was still
	// running under the old setting.
	BeforeCarried int
}

// SplitAt divides requests by whether their session started before or after an
// instant.
func SplitAt(reqs []store.Request, sessions []store.Session, at time.Time) Split {
	byID := index(sessions)
	sp := Split{At: at}
	sp.Before.Key = "before " + at.UTC().Format("2006-01-02 15:04")
	sp.After.Key = "after " + at.UTC().Format("2006-01-02 15:04")
	seen := map[string]bool{}

	for _, r := range reqs {
		s, ok := byID[r.SessionID]
		if !ok {
			continue
		}
		before := s.StartedAt.Before(at)
		b := &sp.After
		if before {
			b = &sp.Before
			if r.TS.After(at) {
				sp.BeforeCarried++
			}
		}
		b.addRequest(r)
		if k := r.SessionID; !seen[k] {
			seen[k] = true
			b.addSession(s)
		}
	}
	sp.Before.finish()
	sp.After.finish()
	return sp
}

// RegimeSplit is one autocompact regime with everything measured under it.
type RegimeSplit struct {
	Regime usage.Regime
	Bucket Bucket
	// SessionIDs is what the split rests on, so a reader can check the sample
	// rather than take the aggregate on trust. A regime backed by a handful of
	// sessions is not evidence, and only the count reveals that.
	SessionIDs []string
	FirstSeen  time.Time
	LastSeen   time.Time
}

// ByRegime groups every request by the autocompact regime of its session.
//
// ⚠ THIS IS A WEAKER INSTRUMENT THAN SplitAt AND IS EASY TO MISREAD. A session
// that simply never grew large is bucketed exactly like one that was capped
// low, so the regimes correlate with how long a session ran as much as with
// any setting, and their date ranges overlap. Use it to describe what work at
// a given context size costs; use SplitAt to judge whether changing a setting
// changed anything.
func ByRegime(reqs []store.Request, sessions []store.Session) []RegimeSplit {
	byID := index(sessions)
	acc := map[string]*RegimeSplit{}
	seen := map[string]bool{}

	for _, r := range reqs {
		s, ok := byID[r.SessionID]
		if !ok {
			continue
		}
		reg := s.Regime()
		sp := acc[reg.Name]
		if sp == nil {
			sp = &RegimeSplit{Regime: reg, Bucket: Bucket{Key: reg.Name}}
			acc[reg.Name] = sp
		}
		sp.Bucket.addRequest(r)

		if k := reg.Name + "\x00" + s.SessionID; !seen[k] {
			seen[k] = true
			sp.Bucket.addSession(s)
			sp.SessionIDs = append(sp.SessionIDs, s.SessionID)
			if sp.FirstSeen.IsZero() || s.StartedAt.Before(sp.FirstSeen) {
				sp.FirstSeen = s.StartedAt
			}
			if s.StartedAt.After(sp.LastSeen) {
				sp.LastSeen = s.StartedAt
			}
		}
	}

	out := make([]RegimeSplit, 0, len(acc))
	for _, sp := range acc {
		sp.Bucket.finish()
		out = append(out, *sp)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Regime.Ceiling > out[j].Regime.Ceiling
	})
	return out
}

func div(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}
