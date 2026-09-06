package claudecode

import (
	"os"
	"path/filepath"
	"testing"
)

// write puts a transcript on disk and returns the config dir and path.
func write(t *testing.T, lines ...string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	proj := filepath.Join(dir, "projects", "-some-project")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(proj, "s1.jsonl")
	var body string
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, p
}

func parse(t *testing.T, lines ...string) *Parsed {
	t.Helper()
	dir, p := write(t, lines...)
	got, err := ParseFile(dir, p)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if got == nil {
		t.Fatal("ParseFile returned nil for a transcript with content")
	}
	return got
}

// A transcript is JSON LINES: one whole record per line. These fixtures are
// deliberately single-line, because a fixture wrapped across lines parses as
// nothing and every assertion then fails for a reason that has nothing to do
// with the behaviour under test.
const assistantBlockA = `{"type":"assistant","sessionId":"s1","timestamp":"2026-09-06T10:00:00Z","message":{"id":"msg_1","role":"assistant","model":"claude-opus-5","usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":300,"cache_creation_input_tokens":40}}}`

// Gotcha 1. Claude Code writes one assistant message once per content block,
// each copy carrying the FULL usage. A parser that sums records bills the same
// request several times.
func TestOneMessageIsBilledOnceHoweverManyBlocksWroteIt(t *testing.T) {
	got := parse(t, assistantBlockA, assistantBlockA, assistantBlockA)
	if len(got.Requests) != 1 {
		t.Fatalf("three copies of one message produced %d requests, want 1", len(got.Requests))
	}
	if got.Session.Tokens.Output != 20 {
		t.Errorf("output tokens = %d, want 20 (usage counted once)", got.Session.Tokens.Output)
	}
}

// Gotcha 2. A refused attempt and the fallback that answered are BOTH billed,
// under different models, and message.model names only the one that answered.
func TestEveryUsageIterationIsBilledUnderItsOwnModel(t *testing.T) {
	line := `{"type":"assistant","sessionId":"s1","timestamp":"2026-09-06T10:00:00Z","message":{"id":"msg_2","role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":5,"output_tokens":7,"iterations":[{"type":"message","model":"claude-fable-5","input_tokens":1,"output_tokens":2},{"type":"fallback_message","model":"claude-opus-4-8","input_tokens":5,"output_tokens":7}]}}}`
	got := parse(t, line)
	if len(got.Requests) != 2 {
		t.Fatalf("got %d requests, want 2 (both attempts were billed)", len(got.Requests))
	}
	if got.Requests[0].Model != "claude-fable-5" {
		t.Errorf("iteration 0 model = %q, want claude-fable-5 (not the answering model)", got.Requests[0].Model)
	}
	if got.Requests[1].Model != "claude-opus-4-8" {
		t.Errorf("iteration 1 model = %q, want claude-opus-4-8", got.Requests[1].Model)
	}
	if got.Requests[0].Iteration == got.Requests[1].Iteration {
		t.Error("both attempts share an iteration index, so the store would collapse them")
	}
}

// A transcript predating the iterations field must still be billed once, from
// the top-level counters.
func TestATranscriptWithoutIterationsIsStillBilled(t *testing.T) {
	got := parse(t, assistantBlockA)
	if len(got.Requests) != 1 {
		t.Fatalf("got %d requests, want 1", len(got.Requests))
	}
	if got.Requests[0].Tokens.CacheRead != 300 {
		t.Errorf("cache read = %d, want 300", got.Requests[0].Tokens.CacheRead)
	}
}

// Gotcha 3. cost-state is Claude Code's own accounting, and two of the
// transcripts carrying one carried more than one, so the LAST must win.
func TestTheLastCostStateWins(t *testing.T) {
	got := parse(t, assistantBlockA,
		`{"type":"cost-state","sessionId":"s1","totalCostUSD":1.5,"totalAPIDuration":1000}`,
		`{"type":"cost-state","sessionId":"s1","totalCostUSD":9.25,"totalAPIDuration":4000}`)
	if !got.Session.CostState.Present {
		t.Fatal("cost-state not detected")
	}
	if got.Session.CostState.TotalCostUSD != 9.25 {
		t.Errorf("totalCostUSD = %v, want 9.25 (the last record)", got.Session.CostState.TotalCostUSD)
	}
	if got.Session.CostState.APIDuration.Milliseconds() != 4000 {
		t.Errorf("APIDuration = %v, want 4s", got.Session.CostState.APIDuration)
	}
}

// A session with no cost-state must report Present false rather than a zero
// cost that reads as "this session was free".
func TestAbsentCostStateIsNotAZeroCost(t *testing.T) {
	got := parse(t, assistantBlockA)
	if got.Session.CostState.Present {
		t.Error("CostState.Present is true for a transcript carrying no cost-state record")
	}
}

// Gotcha 4. A prompt typed while the model is working arrives as an
// attachment, not as a user record. Missing it under-counted human turns by a
// third in busy sessions, which inflates every per-turn figure.
func TestAQueuedPromptCountsAsAHumanTurn(t *testing.T) {
	got := parse(t, assistantBlockA,
		`{"type":"attachment","sessionId":"s1","attachment":{"type":"queued_command","commandMode":"prompt"}}`)
	if got.Session.HumanTurns != 1 {
		t.Errorf("human turns = %d, want 1 (a queued prompt is a person typing)", got.Session.HumanTurns)
	}
}

// Gotcha 5. Role user is the transport for tool results and harness chatter as
// well as for people. Counting them all makes every per-turn cost too low.
func TestUserRecordsThatAreNotPeopleAreNotTurns(t *testing.T) {
	cases := []struct {
		name string
		line string
		want int
	}{
		{"a typed prompt", `{"type":"user","sessionId":"s1","message":{"role":"user","content":"do the thing"}}`, 1},
		{"a tool result", `{"type":"user","sessionId":"s1","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`, 0},
		{"harness context", `{"type":"user","sessionId":"s1","isMeta":true,"message":{"role":"user","content":"context"}}`, 0},
		{"a built-in command", `{"type":"user","sessionId":"s1","message":{"role":"user","content":"<command-name>/clear</command-name>"}}`, 0},
		{"a local command echo", `{"type":"user","sessionId":"s1","message":{"role":"user","content":"<local-command-stdout>x</local-command-stdout>"}}`, 0},
		{"an interrupt", `{"type":"user","sessionId":"s1","message":{"role":"user","content":"[Request interrupted by user]"}}`, 0},
		{"an empty message", `{"type":"user","sessionId":"s1","message":{"role":"user","content":"   "}}`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parse(t, assistantBlockA, c.line)
			if got.Session.HumanTurns != c.want {
				t.Errorf("human turns = %d, want %d", got.Session.HumanTurns, c.want)
			}
		})
	}
}

// A compaction continuation is not a turn either, and it is the signal that
// says how often the context window filled up.
func TestACompactionIsCountedAndIsNotATurn(t *testing.T) {
	got := parse(t, assistantBlockA,
		`{"type":"user","sessionId":"s1","isCompactSummary":true,"message":{"role":"user","content":"summary"}}`)
	if got.Session.Compactions != 1 {
		t.Errorf("compactions = %d, want 1", got.Session.Compactions)
	}
	if got.Session.HumanTurns != 0 {
		t.Errorf("human turns = %d, want 0", got.Session.HumanTurns)
	}
}

// A synthetic assistant record is generated locally and was never billed.
func TestSyntheticRecordsAreNotBilled(t *testing.T) {
	got := parse(t, assistantBlockA,
		`{"type":"assistant","sessionId":"s1","message":{"id":"m2","role":"assistant","model":"<synthetic>","usage":{"input_tokens":99}}}`)
	if len(got.Requests) != 1 {
		t.Errorf("got %d requests, want 1 (the synthetic record is not a bill)", len(got.Requests))
	}
	if got.Session.Tokens.Input != 10 {
		t.Errorf("input tokens = %d, want 10 (the synthetic 99 must not be added)", got.Session.Tokens.Input)
	}
}

// PeakContext is what the whole autocompact comparison rests on, so it must be
// the largest context of any single request rather than a sum or an average.
func TestPeakContextIsTheLargestSingleRequest(t *testing.T) {
	small := `{"type":"assistant","sessionId":"s1","timestamp":"2026-09-06T10:00:00Z","message":{"id":"a","role":"assistant","model":"claude-opus-5","usage":{"input_tokens":1,"cache_read_input_tokens":100}}}`
	big := `{"type":"assistant","sessionId":"s1","timestamp":"2026-09-06T10:01:00Z","message":{"id":"b","role":"assistant","model":"claude-opus-5","usage":{"input_tokens":2,"cache_read_input_tokens":900,"cache_creation_input_tokens":98}}}`
	got := parse(t, small, big, small)
	if got.Session.PeakContext != 1000 {
		t.Errorf("PeakContext = %d, want 1000 (2 + 900 + 98)", got.Session.PeakContext)
	}
}

// A transcript is appended to by a live process, so its last line is routinely
// half-written. Losing the whole session to that would lose the session that
// is running right now, which is the one a person most wants to see.
func TestATruncatedFinalLineDoesNotLoseTheSession(t *testing.T) {
	got := parse(t, assistantBlockA, `{"type":"assistant","sessionId":"s1","mess`)
	if len(got.Requests) != 1 {
		t.Errorf("got %d requests, want 1 despite the truncated line", len(got.Requests))
	}
}
