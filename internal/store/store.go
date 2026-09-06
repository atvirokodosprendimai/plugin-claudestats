// Package store persists billed attempts and sessions in SQLite.
//
// The store exists because Claude Code prunes transcripts after its cleanup
// period. Reading the transcripts fresh on every report would be simpler and
// would quietly lose history: the durable collection is the point, and that is
// why collect is idempotent rather than append-only.
package store

import (
	"context"
	"embed"
	"fmt"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/pressly/goose/v3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"github.com/atvirokodosprendimai/plugin-claudestats/internal/pricing"
	"github.com/atvirokodosprendimai/plugin-claudestats/internal/usage"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Request is the stored form of one billed API attempt.
type Request struct {
	SessionID   string    `gorm:"primaryKey;column:session_id"`
	MessageID   string    `gorm:"primaryKey;column:message_id"`
	Iteration   int       `gorm:"primaryKey;column:iteration"`
	ConfigDir   string    `gorm:"column:config_dir"`
	Project     string    `gorm:"column:project"`
	RequestID   string    `gorm:"column:request_id"`
	TS          time.Time `gorm:"column:ts"`
	Model       string    `gorm:"column:model"`
	Effort      string    `gorm:"column:effort"`
	Version     string    `gorm:"column:version"`
	GitBranch   string    `gorm:"column:git_branch"`
	IsSidechain bool      `gorm:"column:is_sidechain"`

	InputTokens         int64 `gorm:"column:input_tokens"`
	OutputTokens        int64 `gorm:"column:output_tokens"`
	ThinkingTokens      int64 `gorm:"column:thinking_tokens"`
	CacheReadTokens     int64 `gorm:"column:cache_read_tokens"`
	CacheCreationTokens int64 `gorm:"column:cache_creation_tokens"`
	CacheWrite5mTokens  int64 `gorm:"column:cache_write_5m_tokens"`
	CacheWrite1hTokens  int64 `gorm:"column:cache_write_1h_tokens"`
	ContextTokens       int64 `gorm:"column:context_tokens"`

	InputUSD         float64 `gorm:"column:input_usd"`
	OutputUSD        float64 `gorm:"column:output_usd"`
	CacheReadUSD     float64 `gorm:"column:cache_read_usd"`
	CacheWriteUSD    float64 `gorm:"column:cache_write_usd"`
	Priced           bool    `gorm:"column:priced"`
	PriceVerified    bool    `gorm:"column:price_verified"`
	AssumedTTLTokens int64   `gorm:"column:assumed_ttl_tokens"`
}

// TableName names the table, since gorm would otherwise pluralise differently.
func (Request) TableName() string { return "requests" }

// TotalUSD is the whole priced value of the attempt.
func (r Request) TotalUSD() float64 {
	return r.InputUSD + r.OutputUSD + r.CacheReadUSD + r.CacheWriteUSD
}

// Session is the stored form of one Claude Code session.
type Session struct {
	SessionID   string    `gorm:"primaryKey;column:session_id"`
	ConfigDir   string    `gorm:"column:config_dir"`
	Project     string    `gorm:"column:project"`
	StartedAt   time.Time `gorm:"column:started_at"`
	EndedAt     time.Time `gorm:"column:ended_at"`
	Requests    int       `gorm:"column:requests"`
	HumanTurns  int       `gorm:"column:human_turns"`
	Compactions int       `gorm:"column:compactions"`
	PeakContext int64     `gorm:"column:peak_context"`

	CostStatePresent bool    `gorm:"column:cost_state_present"`
	CCTotalCostUSD   float64 `gorm:"column:cc_total_cost_usd"`
	CCAPIDurationMS  int64   `gorm:"column:cc_api_duration_ms"`
	CCToolDurationMS int64   `gorm:"column:cc_tool_duration_ms"`
	CCWallDurationMS int64   `gorm:"column:cc_wall_duration_ms"`
}

// TableName names the table.
func (Session) TableName() string { return "sessions" }

// Regime is the autocompact bucket this session's peak context puts it in.
func (s Session) Regime() usage.Regime { return usage.RegimeOf(s.PeakContext) }

// DB is an open store.
type DB struct{ gorm *gorm.DB }

// Open opens the SQLite file at path and brings its schema up to date.
func Open(path string) (*DB, error) {
	g, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("store: opening %s: %w", path, err)
	}
	sqlDB, err := g.DB()
	if err != nil {
		return nil, fmt.Errorf("store: database handle: %w", err)
	}
	goose.SetBaseFS(migrationFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		return nil, fmt.Errorf("store: goose dialect: %w", err)
	}
	if err := goose.Up(sqlDB, "migrations"); err != nil {
		return nil, fmt.Errorf("store: migrating %s: %w", path, err)
	}
	return &DB{gorm: g}, nil
}

// Close releases the underlying handle.
func (d *DB) Close() error {
	sqlDB, err := d.gorm.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// SaveRequests writes attempts, replacing any row with the same identity.
//
// Upsert rather than insert is what makes re-collecting safe: a session that
// was still running when it was last collected will be seen again with more
// attempts, and the ones already stored must not duplicate.
func (d *DB) SaveRequests(ctx context.Context, rows []Request) error {
	if len(rows) == 0 {
		return nil
	}
	const batch = 500
	tx := d.gorm.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "session_id"}, {Name: "message_id"}, {Name: "iteration"}},
		UpdateAll: true,
	})
	if err := tx.CreateInBatches(rows, batch).Error; err != nil {
		return fmt.Errorf("store: saving requests: %w", err)
	}
	return nil
}

// SaveSession writes one session, replacing any row with the same id.
func (d *DB) SaveSession(ctx context.Context, s Session) error {
	err := d.gorm.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "session_id"}},
		UpdateAll: true,
	}).Create(&s).Error
	if err != nil {
		return fmt.Errorf("store: saving session %s: %w", s.SessionID, err)
	}
	return nil
}

// Filter narrows a query. A zero value returns everything.
//
// It is a struct rather than a parameter list because the two query methods
// take exactly the same narrowing, and every filter added as a positional
// argument has so far meant editing both signatures and all four call sites.
type Filter struct {
	// Since and Until bound the range, half-open. A zero time is unbounded.
	Since, Until time.Time

	// ConfigDir narrows to one Claude Code configuration directory, matched
	// as a substring so "aks" selects ~/.sandboxes/aks. This is not a
	// convenience: launch flags such as --autocompact are set per config
	// directory, so a comparison that mixes directories is comparing sessions
	// that were never under the same settings.
	ConfigDir string

	// Projects narrows to project slugs, matched as substrings and combined
	// with OR. It takes a LIST rather than one value for a specific reason:
	// Claude Code derives the slug from the working directory, so RENAMING A
	// DIRECTORY GIVES THE SAME PROJECT A NEW SLUG AND SPLITS ITS HISTORY IN
	// TWO. Measured 2026-09-06: agentmemories (141 sessions to 2026-09-01)
	// and agentsmemory (8 sessions after it) are one project whose directory
	// was renamed, and filtering on either slug alone hides most of its cost.
	Projects []string
}

func (f Filter) apply(q *gorm.DB, timeColumn string) *gorm.DB {
	if !f.Since.IsZero() {
		q = q.Where(timeColumn+" >= ?", f.Since)
	}
	if !f.Until.IsZero() {
		q = q.Where(timeColumn+" < ?", f.Until)
	}
	if f.ConfigDir != "" {
		q = q.Where("config_dir LIKE ?", "%"+f.ConfigDir+"%")
	}
	if len(f.Projects) > 0 {
		// OR across the slugs, AND-ed with everything else. The group is built
		// on a fresh session so gorm BRACKETS it rather than flattening the Or
		// into the surrounding AND, which would silently widen the query to
		// every project once any other narrowing was present.
		or := q.Session(&gorm.Session{NewDB: true}).Where("project LIKE ?", "%"+f.Projects[0]+"%")
		for _, p := range f.Projects[1:] {
			or = or.Or("project LIKE ?", "%"+p+"%")
		}
		q = q.Where(or)
	}
	return q
}

// Requests returns stored attempts matching the filter, oldest first.
func (d *DB) Requests(ctx context.Context, f Filter) ([]Request, error) {
	q := f.apply(d.gorm.WithContext(ctx).Order("ts"), "ts")
	var rows []Request
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("store: reading requests: %w", err)
	}
	return rows, nil
}

// Sessions returns stored sessions matching the filter, oldest first.
func (d *DB) Sessions(ctx context.Context, f Filter) ([]Session, error) {
	q := f.apply(d.gorm.WithContext(ctx).Order("started_at"), "started_at")
	var rows []Session
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("store: reading sessions: %w", err)
	}
	return rows, nil
}

// FromUsage converts a parsed attempt into its stored form, pricing it on the
// way. Pricing happens at collection so a report never has to re-price, and so
// the price table in force at collection time is what the stored figure means.
func FromUsage(r usage.Request, t *pricing.Table) Request {
	c := t.Cost(r.Model, r.Tokens)
	return Request{
		SessionID:   r.SessionID,
		MessageID:   r.MessageID,
		Iteration:   r.Iteration,
		ConfigDir:   r.ConfigDir,
		Project:     r.Project,
		RequestID:   r.RequestID,
		TS:          r.Timestamp,
		Model:       r.Model,
		Effort:      r.Effort,
		Version:     r.Version,
		GitBranch:   r.GitBranch,
		IsSidechain: r.IsSidechain,

		InputTokens:         r.Tokens.Input,
		OutputTokens:        r.Tokens.Output,
		ThinkingTokens:      r.Tokens.Thinking,
		CacheReadTokens:     r.Tokens.CacheRead,
		CacheCreationTokens: r.Tokens.CacheCreation,
		CacheWrite5mTokens:  r.Tokens.CacheWrite5m,
		CacheWrite1hTokens:  r.Tokens.CacheWrite1h,
		ContextTokens:       r.Tokens.Context(),

		InputUSD:         c.InputUSD,
		OutputUSD:        c.OutputUSD,
		CacheReadUSD:     c.CacheReadUSD,
		CacheWriteUSD:    c.CacheWriteUSD,
		Priced:           c.Priced,
		PriceVerified:    c.Verified,
		AssumedTTLTokens: c.AssumedTTLTokens,
	}
}

// SessionFromUsage converts a parsed session into its stored form.
func SessionFromUsage(s usage.Session) Session {
	return Session{
		SessionID:   s.SessionID,
		ConfigDir:   s.ConfigDir,
		Project:     s.Project,
		StartedAt:   s.StartedAt,
		EndedAt:     s.EndedAt,
		Requests:    s.Requests,
		HumanTurns:  s.HumanTurns,
		Compactions: s.Compactions,
		PeakContext: s.PeakContext,

		CostStatePresent: s.CostState.Present,
		CCTotalCostUSD:   s.CostState.TotalCostUSD,
		CCAPIDurationMS:  s.CostState.APIDuration.Milliseconds(),
		CCToolDurationMS: s.CostState.ToolDuration.Milliseconds(),
		CCWallDurationMS: s.CostState.WallDuration.Milliseconds(),
	}
}
