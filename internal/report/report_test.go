package report

import (
	"math"
	"testing"
	"time"

	"github.com/atvirokodosprendimai/plugin-claudestats/internal/store"
)

func ts(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func req(session, msg string, at time.Time, ctx int64, usd float64) store.Request {
	return store.Request{
		SessionID: session, MessageID: msg, TS: at,
		ContextTokens: ctx, CacheReadTokens: ctx, InputTokens: 0,
		CacheReadUSD: usd, Priced: true,
	}
}

func sess(id string, started time.Time, turns int, peak int64) store.Session {
	return store.Session{SessionID: id, StartedAt: started, HumanTurns: turns, PeakContext: peak}
}

// THE CENTRAL BEHAVIOUR. A launch flag is fixed for the life of the session it
// launched, so a session begun before a change keeps the old setting for as
// long as it runs, sometimes for days. Attributing its later requests to
// "after" is exactly what makes a working change look like it did nothing, and
// it is the mistake this whole type exists to prevent.
func TestSplitAttributesByWhenTheSessionStartedNotWhenTheRequestWasSent(t *testing.T) {
	at := ts("2026-09-05 17:05")
	sessions := []store.Session{
		sess("old", ts("2026-09-04 10:00"), 2, 999_000),
		sess("new", ts("2026-09-05 18:00"), 2, 560_000),
	}
	reqs := []store.Request{
		req("old", "a", ts("2026-09-04 11:00"), 999_000, 1),
		// Sent well after the change, but by a session launched before it.
		req("old", "b", ts("2026-09-06 09:00"), 999_000, 1),
		req("new", "c", ts("2026-09-05 19:00"), 560_000, 1),
	}
	sp := SplitAt(reqs, sessions, at)

	if sp.Before.Requests != 2 {
		t.Errorf("before requests = %d, want 2 (the carried-over request belongs to the old setting)",
			sp.Before.Requests)
	}
	if sp.After.Requests != 1 {
		t.Errorf("after requests = %d, want 1", sp.After.Requests)
	}
	if sp.BeforeCarried != 1 {
		t.Errorf("BeforeCarried = %d, want 1 so the reader can see the overlap", sp.BeforeCarried)
	}
	if sp.Before.Sessions != 1 || sp.After.Sessions != 1 {
		t.Errorf("sessions before/after = %d/%d, want 1/1", sp.Before.Sessions, sp.After.Sessions)
	}
}

// PeakContext must be the largest single request, because it is the evidence
// that a ceiling moved. A mean would be dragged down by short sessions and
// would report a drop that never happened.
func TestPeakContextIsAMaximumOverRequestsNotAMean(t *testing.T) {
	sessions := []store.Session{sess("s", ts("2026-09-06 10:00"), 1, 900_000)}
	reqs := []store.Request{
		req("s", "a", ts("2026-09-06 10:01"), 10_000, 1),
		req("s", "b", ts("2026-09-06 10:02"), 900_000, 1),
		req("s", "c", ts("2026-09-06 10:03"), 10_000, 1),
	}
	b := Build(reqs, sessions, Daily)
	if len(b) != 1 {
		t.Fatalf("got %d buckets, want 1", len(b))
	}
	if b[0].PeakContext != 900_000 {
		t.Errorf("PeakContext = %d, want 900000", b[0].PeakContext)
	}
}

// The identity is what stops a false conclusion in either direction, so it has
// to hold exactly on the computed figures rather than approximately.
func TestTheCostIdentityHolds(t *testing.T) {
	sessions := []store.Session{sess("s", ts("2026-09-06 10:00"), 4, 100)}
	var reqs []store.Request
	for i := range 10 {
		reqs = append(reqs, req("s", string(rune('a'+i)), ts("2026-09-06 10:00"), 100, 0.25))
	}
	b := Build(reqs, sessions, Daily)[0]
	got := b.USDPerTurn()
	want := b.RequestsPerTurn() * b.USDPerRequest()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("USD/turn = %v but requests/turn x USD/request = %v; the identity the reports "+
			"are read through does not hold", got, want)
	}
}

// A session spanning midnight must not be counted in two days' session totals,
// which would deflate every per-session figure.
func TestASessionSpanningMidnightIsCountedOncePerBucket(t *testing.T) {
	sessions := []store.Session{sess("s", ts("2026-09-05 23:00"), 2, 100)}
	reqs := []store.Request{
		req("s", "a", ts("2026-09-05 23:30"), 100, 1),
		req("s", "b", ts("2026-09-05 23:45"), 100, 1),
		req("s", "c", ts("2026-09-06 00:15"), 100, 1),
	}
	b := Build(reqs, sessions, Daily)
	if len(b) != 2 {
		t.Fatalf("got %d buckets, want 2", len(b))
	}
	for _, x := range b {
		if x.Sessions != 1 {
			t.Errorf("bucket %s counted %d sessions, want 1", x.Key, x.Sessions)
		}
	}
}

// Division by zero must yield zero rather than NaN, because a NaN reaches the
// SVG as an unparseable coordinate and silently blanks a chart.
func TestRatesAreZeroRatherThanNaNWhenTheDenominatorIsZero(t *testing.T) {
	var b Bucket
	for name, v := range map[string]float64{
		"USDPerRequest":      b.USDPerRequest(),
		"USDPerTurn":         b.USDPerTurn(),
		"RequestsPerTurn":    b.RequestsPerTurn(),
		"UploadedPerRequest": b.UploadedPerRequest(),
		"CompactionsPerTurn": b.CompactionsPerTurn(),
		"UploadKbps":         b.UploadKbps(),
		"DownloadKbps":       b.DownloadKbps(),
	} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Errorf("%s on an empty bucket = %v, want 0", name, v)
		}
	}
}

// Throughput is only reported when Claude Code actually recorded API time. A
// figure invented from record timestamps would be measuring tool execution and
// human thinking time and calling it a transfer rate.
func TestThroughputIsZeroWithoutRecordedAPITime(t *testing.T) {
	b := Bucket{Sessions: 3, Requests: 10, InputTokens: 1_000_000, OutputTokens: 500}
	if got := b.UploadKbps(); got != 0 {
		t.Errorf("UploadKbps = %v with no recorded API time, want 0", got)
	}
	b.APISessions, b.APIDuration = 3, time.Second
	if got := b.UploadKbps(); got <= 0 {
		t.Errorf("UploadKbps = %v with recorded API time, want a positive rate", got)
	}
}

func TestWeekBucketsAreLabelledByISOWeekAndSortChronologically(t *testing.T) {
	sessions := []store.Session{
		sess("a", ts("2026-08-31 10:00"), 1, 100),
		sess("b", ts("2026-09-07 10:00"), 1, 100),
	}
	reqs := []store.Request{
		req("b", "y", ts("2026-09-07 10:00"), 100, 1),
		req("a", "x", ts("2026-08-31 10:00"), 100, 1),
	}
	b := Build(reqs, sessions, Weekly)
	if len(b) != 2 {
		t.Fatalf("got %d buckets, want 2", len(b))
	}
	if !b[0].Start.Before(b[1].Start) {
		t.Errorf("buckets not in chronological order: %s then %s", b[0].Key, b[1].Key)
	}
	if b[0].Key != "2026-W36" {
		t.Errorf("first bucket key = %q, want 2026-W36", b[0].Key)
	}
}
