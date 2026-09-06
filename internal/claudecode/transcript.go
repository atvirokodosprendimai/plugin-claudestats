// Package claudecode finds and parses the transcripts Claude Code already
// writes, turning them into billed API attempts.
//
// The transcript is a generous but treacherous source. Five of its behaviours
// silently change a per-session sum, and every one of them is handled here
// explicitly rather than incidentally, because each was originally found the
// expensive way (measured 2026-09-03 over 670 transcripts while building the
// statsv1 project, recorded in the team's memory palace):
//
//  1. One assistant message is written once per content block, and every copy
//     carries the full usage. Summing records over-counts by the block count,
//     so messages are deduplicated on message.id.
//  2. The usage block describes the last attempt only. When the API refused an
//     attempt and answered with a fallback model, usage.iterations lists both,
//     both were billed, and message.model names only the one that answered.
//     Each iteration is emitted as its own Request under its own model.
//  3. The transcript is not the bill. Claude Code also makes side calls that
//     leave a system record and no usage, so a per-request sum is a lower
//     bound. Where a cost-state record exists it carries Claude Code's own
//     figure, which is what CostState is for.
//  4. A prompt typed while the model is working is not a user record. It
//     arrives as an attachment of type queued_command, and ignoring those
//     under-counted human turns by a third in busy sessions.
//  5. User records are mostly not people: tool results, meta context,
//     compaction summaries, local-command echoes and built-in slash commands
//     all arrive with role user.
package claudecode

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atvirokodosprendimai/plugin-claudestats/internal/usage"
)

// syntheticModel marks an assistant record Claude Code generated locally. It
// carries no usage and was never billed.
const syntheticModel = "<synthetic>"

// ConfigDirs returns every Claude Code configuration directory on this machine:
// $CLAUDE_CONFIG_DIR if set, ~/.claude, and each ~/.sandboxes/* that has a
// projects directory. Sandboxes matter because a sandbox is a separate config
// directory with its own transcripts and its own launch flags.
func ConfigDirs() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("claudecode: home directory: %w", err)
	}
	seen := map[string]bool{}
	var dirs []string
	add := func(d string) {
		if d == "" || seen[d] {
			return
		}
		if _, err := os.Stat(filepath.Join(d, "projects")); err != nil {
			return
		}
		seen[d] = true
		dirs = append(dirs, d)
	}
	add(os.Getenv("CLAUDE_CONFIG_DIR"))
	add(filepath.Join(home, ".claude"))
	sandboxes, _ := filepath.Glob(filepath.Join(home, ".sandboxes", "*"))
	for _, s := range sandboxes {
		add(s)
	}
	return dirs, nil
}

// TranscriptFiles lists every session transcript under a config directory,
// including the subagent transcripts stored beside each session, which are
// billed to the same session and are easy to miss.
func TranscriptFiles(configDir string) ([]string, error) {
	main, err := filepath.Glob(filepath.Join(configDir, "projects", "*", "*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("claudecode: globbing transcripts: %w", err)
	}
	sub, err := filepath.Glob(filepath.Join(configDir, "projects", "*", "*", "subagents", "agent-*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("claudecode: globbing subagent transcripts: %w", err)
	}
	return append(main, sub...), nil
}

// record is the union of the transcript line shapes this package reads. The
// transcript carries many more fields; only what is used is declared, so an
// unrelated schema change cannot break parsing.
type record struct {
	Type       string          `json:"type"`
	Subtype    string          `json:"subtype"`
	SessionID  string          `json:"sessionId"`
	Timestamp  string          `json:"timestamp"`
	Version    string          `json:"version"`
	GitBranch  string          `json:"gitBranch"`
	CWD        string          `json:"cwd"`
	Effort     string          `json:"effort"`
	RequestID  string          `json:"requestId"`
	Sidechain  bool            `json:"isSidechain"`
	IsMeta     bool            `json:"isMeta"`
	IsCompact  bool            `json:"isCompactSummary"`
	Message    *message        `json:"message"`
	Attachment *attachment     `json:"attachment"`
	Content    json.RawMessage `json:"content"`

	// cost-state fields: Claude Code's own accounting for the session.
	TotalCostUSD      float64 `json:"totalCostUSD"`
	TotalAPIDuration  int64   `json:"totalAPIDuration"`
	TotalToolDuration int64   `json:"totalToolDuration"`
	TotalDuration     int64   `json:"totalDuration"`
	UnknownModelCost  bool    `json:"hasUnknownModelCost"`
}

type message struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   *usageBlock     `json:"usage"`
}

type attachment struct {
	Type        string `json:"type"`
	CommandMode string `json:"commandMode"`
}

type usageBlock struct {
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
	OutputDetails struct {
		Thinking int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
	CacheCreationSplit *cacheSplit `json:"cache_creation"`
	Iterations         []iteration `json:"iterations"`
}

type cacheSplit struct {
	Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
	Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
}

type iteration struct {
	Input         int64       `json:"input_tokens"`
	Output        int64       `json:"output_tokens"`
	CacheRead     int64       `json:"cache_read_input_tokens"`
	CacheCreation int64       `json:"cache_creation_input_tokens"`
	Cache         *cacheSplit `json:"cache_creation"`
	// Type distinguishes the attempt that answered ("message") from one the
	// API refused and fell back from ("fallback_message"); Model names the
	// model that attempt was billed under, which the top-level model does not.
	Type  string `json:"type"`
	Model string `json:"model"`
}

func (u *usageBlock) tokens() usage.Tokens {
	t := usage.Tokens{
		Input:         u.Input,
		Output:        u.Output,
		Thinking:      u.OutputDetails.Thinking,
		CacheRead:     u.CacheRead,
		CacheCreation: u.CacheCreation,
	}
	if u.CacheCreationSplit != nil {
		t.CacheWrite5m = u.CacheCreationSplit.Ephemeral5m
		t.CacheWrite1h = u.CacheCreationSplit.Ephemeral1h
	}
	return t
}

func (i iteration) tokens() usage.Tokens {
	t := usage.Tokens{
		Input:         i.Input,
		Output:        i.Output,
		CacheRead:     i.CacheRead,
		CacheCreation: i.CacheCreation,
	}
	if i.Cache != nil {
		t.CacheWrite5m = i.Cache.Ephemeral5m
		t.CacheWrite1h = i.Cache.Ephemeral1h
	}
	return t
}

// Parsed is one transcript file turned into billed attempts plus the session
// facts that only the whole file can establish.
type Parsed struct {
	Session  usage.Session
	Requests []usage.Request
}

// ParseFile reads one transcript. A malformed line is skipped rather than
// failing the file: transcripts are appended to by a live process, so a
// truncated final line is normal and must not lose the session.
func ParseFile(configDir, path string) (*Parsed, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("claudecode: opening %s: %w", path, err)
	}
	defer f.Close()

	p := &Parsed{}
	p.Session.ConfigDir = configDir
	p.Session.Project = projectOf(configDir, path)

	sc := bufio.NewScanner(f)
	// Transcript lines carry whole messages and routinely exceed the default
	// 64KB token limit; a short buffer would silently drop the largest ones,
	// which are exactly the expensive requests this tool exists to measure.
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)

	seenMessage := map[string]bool{}
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		p.apply(&r, seenMessage)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("claudecode: reading %s: %w", path, err)
	}
	if len(p.Requests) == 0 && !p.Session.CostState.Present {
		return nil, nil
	}
	return p, nil
}

func (p *Parsed) apply(r *record, seenMessage map[string]bool) {
	switch r.Type {
	case "cost-state":
		// Gotcha 3. Two of 37 transcripts carrying one carried more than one,
		// so the last wins rather than the first.
		p.Session.CostState = usage.CostState{
			Present:          true,
			TotalCostUSD:     r.TotalCostUSD,
			APIDuration:      time.Duration(r.TotalAPIDuration) * time.Millisecond,
			ToolDuration:     time.Duration(r.TotalToolDuration) * time.Millisecond,
			WallDuration:     time.Duration(r.TotalDuration) * time.Millisecond,
			UnknownModelCost: r.UnknownModelCost,
		}
	case "attachment":
		// Gotcha 4: a prompt typed while the model works arrives here.
		if r.Attachment != nil && r.Attachment.Type == "queued_command" &&
			r.Attachment.CommandMode == "prompt" {
			p.Session.HumanTurns++
		}
	case "user":
		if r.IsCompact {
			p.Session.Compactions++
			return
		}
		if isHumanTurn(r) {
			p.Session.HumanTurns++
		}
	case "assistant":
		p.addAssistant(r, seenMessage)
	}
}

func (p *Parsed) addAssistant(r *record, seenMessage map[string]bool) {
	m := r.Message
	if m == nil || m.Usage == nil || m.Model == syntheticModel {
		return
	}
	// Gotcha 1: one message, one bill, however many content blocks wrote it.
	if m.ID != "" {
		if seenMessage[m.ID] {
			return
		}
		seenMessage[m.ID] = true
	}

	ts, _ := time.Parse(time.RFC3339Nano, r.Timestamp)
	base := usage.Request{
		ConfigDir:   p.Session.ConfigDir,
		Project:     p.Session.Project,
		SessionID:   r.SessionID,
		MessageID:   m.ID,
		RequestID:   r.RequestID,
		Timestamp:   ts,
		Model:       m.Model,
		Effort:      r.Effort,
		Version:     r.Version,
		GitBranch:   r.GitBranch,
		IsSidechain: r.Sidechain,
	}
	if p.Session.SessionID == "" {
		p.Session.SessionID = r.SessionID
	}

	// Gotcha 2: every iteration was billed, each under its own model. Older
	// transcripts have no iterations field at all, and then the top-level
	// counters are the only attempt.
	if len(m.Usage.Iterations) == 0 {
		p.emit(base, 0, m.Model, m.Usage.tokens(), ts)
		return
	}
	for i, it := range m.Usage.Iterations {
		model := it.Model
		if model == "" {
			model = m.Model
		}
		p.emit(base, i, model, it.tokens(), ts)
	}
}

func (p *Parsed) emit(base usage.Request, iter int, model string, t usage.Tokens, ts time.Time) {
	req := base
	req.Iteration = iter
	req.Model = model
	req.Tokens = t
	p.Requests = append(p.Requests, req)

	s := &p.Session
	s.Requests++
	s.Tokens.Input += t.Input
	s.Tokens.Output += t.Output
	s.Tokens.Thinking += t.Thinking
	s.Tokens.CacheRead += t.CacheRead
	s.Tokens.CacheCreation += t.CacheCreation
	s.Tokens.CacheWrite5m += t.CacheWrite5m
	s.Tokens.CacheWrite1h += t.CacheWrite1h
	if c := t.Context(); c > s.PeakContext {
		s.PeakContext = c
	}
	if !ts.IsZero() {
		if s.StartedAt.IsZero() || ts.Before(s.StartedAt) {
			s.StartedAt = ts
		}
		if ts.After(s.EndedAt) {
			s.EndedAt = ts
		}
	}
}

// isHumanTurn decides whether a user record carries a prompt a person typed.
//
// Gotcha 5: role user is the transport for tool results, harness-injected
// context, compaction continuations, task notifications, interrupts and
// built-in slash commands as well as for people. A skill invocation opens with
// <command-message> and a built-in command with <command-name>; that order is
// the tell that separates a prompt the model received from a command the
// harness ran.
func isHumanTurn(r *record) bool {
	if r.IsMeta || r.Message == nil || r.Message.Role != "user" {
		return false
	}
	text, ok := userText(r.Message.Content)
	if !ok {
		// An array content block that holds no text is a tool result.
		return false
	}
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	// A built-in slash command echo is the harness talking, not the person.
	// A skill invocation reaches the model and counts.
	if strings.HasPrefix(t, "<command-name>") {
		return false
	}
	if strings.HasPrefix(t, "<local-command-stdout>") ||
		strings.HasPrefix(t, "[Request interrupted") {
		return false
	}
	return true
}

// userText extracts the text of a user message, which is either a bare string
// or an array of content blocks. It reports false when the content is an array
// carrying no text block, which is what a tool result looks like.
func userText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", false
	}
	var b strings.Builder
	found := false
	for _, blk := range blocks {
		if blk.Type == "text" && blk.Text != "" {
			b.WriteString(blk.Text)
			found = true
		}
	}
	return b.String(), found
}

// projectOf recovers the project a transcript belongs to from its path.
// Claude Code stores transcripts under projects/<slug>/, where the slug is the
// working directory with separators replaced by dashes.
func projectOf(configDir, path string) string {
	rel, err := filepath.Rel(filepath.Join(configDir, "projects"), path)
	if err != nil {
		return ""
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}
