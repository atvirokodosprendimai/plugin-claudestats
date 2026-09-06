package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: v%v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func day(s string) time.Time {
	ts, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return ts
}

// seed writes one request and one session per row, so a filter can be checked
// against both tables with the same expectations.
func seed(t *testing.T, db *DB, rows []Request) {
	t.Helper()
	ctx := context.Background()
	if err := db.SaveRequests(ctx, rows); err != nil {
		t.Fatalf("SaveRequests: %v", err)
	}
	for _, r := range rows {
		s := Session{
			SessionID: r.SessionID, ConfigDir: r.ConfigDir, Project: r.Project,
			StartedAt: r.TS, EndedAt: r.TS, Requests: 1,
		}
		if err := db.SaveSession(ctx, s); err != nil {
			t.Fatalf("SaveSession: %v", err)
		}
	}
}

func row(session, project, configDir, date string) Request {
	return Request{
		SessionID: session, MessageID: "m-" + session, Iteration: 0,
		ConfigDir: configDir, Project: project, TS: day(date),
		Model: "claude-opus-5", Priced: true,
	}
}

// THE RISK THIS TEST EXISTS FOR: the project filter builds an OR group and
// AND-s it with the other narrowings. If gorm flattens that Or into the
// surrounding AND instead of bracketing it, the query becomes
// (config_dir LIKE x AND project LIKE a) OR (project LIKE b), which silently
// returns rows from config directories the caller excluded. That is a widening
// failure: it returns MORE data and looks like it worked.
func TestAProjectOrGroupIsBracketedAndDoesNotWidenTheOtherFilters(t *testing.T) {
	db := open(t)
	seed(t, db, []Request{
		row("s1", "-x-agentmemories", "/home/u/.sandboxes/aks", "2026-08-20"),
		row("s2", "-x-agentsmemory", "/home/u/.sandboxes/aks", "2026-09-03"),
		row("s3", "-x-brolis-lizdai", "/home/u/.sandboxes/aks", "2026-09-03"),
		// Same project slugs, but a DIFFERENT config directory. A flattened OR
		// lets these through, which is the bug.
		row("s4", "-x-agentsmemory", "/home/u/.claude", "2026-09-03"),
		row("s5", "-x-agentmemories", "/home/u/.claude", "2026-08-20"),
	})

	f := Filter{
		ConfigDir: "sandboxes/aks",
		Projects:  []string{"agentmemories", "agentsmemory"},
	}
	reqs, err := db.Requests(context.Background(), f)
	if err != nil {
		t.Fatalf("Requests: %v", err)
	}
	got := map[string]bool{}
	for _, r := range reqs {
		got[r.SessionID] = true
	}
	if len(reqs) != 2 || !got["s1"] || !got["s2"] {
		t.Errorf("got sessions %v, want exactly s1 and s2; anything else means the OR "+
			"group was flattened and widened past the config-dir filter", got)
	}

	sessions, err := db.Sessions(context.Background(), f)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("Sessions returned %d rows, want 2; the two query paths must narrow identically",
			len(sessions))
	}
}

// A rename splits one project's history across two slugs, and reuniting them
// is the whole reason Projects is a list.
func TestOneProjectSlugAloneMissesTheHistoryBeforeARename(t *testing.T) {
	db := open(t)
	seed(t, db, []Request{
		row("old1", "-x-agentmemories", "/c/aks", "2026-08-20"),
		row("old2", "-x-agentmemories", "/c/aks", "2026-08-21"),
		row("new1", "-x-agentsmemory", "/c/aks", "2026-09-03"),
	})
	ctx := context.Background()

	one, err := db.Requests(ctx, Filter{Projects: []string{"agentsmemory"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 {
		t.Errorf("the post-rename slug alone returned %d rows, want 1", len(one))
	}

	both, err := db.Requests(ctx, Filter{Projects: []string{"agentmemories", "agentsmemory"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(both) != 3 {
		t.Errorf("both slugs returned %d rows, want 3; a rename must be reunitable", len(both))
	}
}

// A zero filter must return everything rather than nothing, because every
// command builds one and most runs pass no narrowing at all.
func TestAZeroFilterReturnsEverything(t *testing.T) {
	db := open(t)
	seed(t, db, []Request{
		row("a", "-p1", "/c/one", "2026-08-20"),
		row("b", "-p2", "/c/two", "2026-09-03"),
	})
	reqs, err := db.Requests(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 {
		t.Errorf("a zero Filter returned %d rows, want 2", len(reqs))
	}
}

// The date range is half-open: Until excludes its own day, so two adjacent
// ranges neither overlap nor drop a row between them.
func TestTheDateRangeIsHalfOpen(t *testing.T) {
	db := open(t)
	seed(t, db, []Request{
		row("before", "-p", "/c", "2026-09-04"),
		row("on", "-p", "/c", "2026-09-05"),
		row("after", "-p", "/c", "2026-09-06"),
	})
	ctx := context.Background()
	lower, err := db.Requests(ctx, Filter{Until: day("2026-09-05")})
	if err != nil {
		t.Fatal(err)
	}
	upper, err := db.Requests(ctx, Filter{Since: day("2026-09-05")})
	if err != nil {
		t.Fatal(err)
	}
	if len(lower) != 1 || lower[0].SessionID != "before" {
		t.Errorf("Until 09-05 returned %d rows, want only 'before'", len(lower))
	}
	if len(upper) != 2 {
		t.Errorf("Since 09-05 returned %d rows, want 2 ('on' and 'after')", len(upper))
	}
}

// Re-collecting is expected: a session still running when it was last
// collected is seen again with more attempts. Storing must update in place,
// never duplicate, or every total drifts upward with each run.
func TestReCollectingTheSameAttemptUpdatesRatherThanDuplicates(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	first := row("s", "-p", "/c", "2026-09-05")
	first.OutputTokens = 10
	if err := db.SaveRequests(ctx, []Request{first}); err != nil {
		t.Fatal(err)
	}
	second := first
	second.OutputTokens = 25
	if err := db.SaveRequests(ctx, []Request{second}); err != nil {
		t.Fatal(err)
	}
	reqs, err := db.Requests(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 {
		t.Fatalf("re-saving one attempt produced %d rows, want 1", len(reqs))
	}
	if reqs[0].OutputTokens != 25 {
		t.Errorf("output tokens = %d, want 25 (the row must be updated in place)", reqs[0].OutputTokens)
	}
}

// Two attempts of ONE message are separate bills and must both survive: they
// differ only by iteration, which is part of the primary key for that reason.
func TestTwoIterationsOfOneMessageAreStoredSeparately(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	a := row("s", "-p", "/c", "2026-09-05")
	b := a
	b.Iteration = 1
	if err := db.SaveRequests(ctx, []Request{a, b}); err != nil {
		t.Fatal(err)
	}
	reqs, err := db.Requests(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 {
		t.Errorf("got %d rows, want 2; a fallback attempt and the one that answered are both billed", len(reqs))
	}
}
