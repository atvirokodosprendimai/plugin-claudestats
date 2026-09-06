// Command claudestats collects Claude Code token usage and cost from the
// transcripts Claude Code already writes, stores them in SQLite, and exports a
// self-contained HTML report.
//
// It exists to answer one question with data rather than recollection: does
// capping the autocompact window make a request cheaper, and does that survive
// being measured per human turn rather than per request.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/atvirokodosprendimai/plugin-claudestats/internal/claudecode"
	"github.com/atvirokodosprendimai/plugin-claudestats/internal/pricing"
	"github.com/atvirokodosprendimai/plugin-claudestats/internal/report"
	"github.com/atvirokodosprendimai/plugin-claudestats/internal/store"
)

func main() {
	if err := newApp().Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "claudestats:", err)
		os.Exit(1)
	}
}

// Flags are built per command: urfave/cli flags carry parse state, so one
// instance must not be shared between commands.
func dbFlag() cli.Flag {
	return &cli.StringFlag{
		Name:    "db",
		Value:   "claudestats.db",
		Usage:   "SQLite file the collector writes and the reports read",
		Sources: cli.EnvVars("CLAUDESTATS_DB"),
	}
}

func rangeFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "since", Usage: "only requests on or after this date (YYYY-MM-DD)"},
		&cli.StringFlag{Name: "until", Usage: "only requests before this date (YYYY-MM-DD)"},
		&cli.StringFlag{Name: "config-dir", Usage: "only this Claude Code config directory, matched as a substring (e.g. aks); launch flags are set per directory, so a comparison across directories compares different settings"},
		&cli.StringSliceFlag{Name: "project", Usage: "only these project slugs, matched as substrings (repeatable). Repeat it when a directory was renamed: Claude Code derives the slug from the working directory, so a rename splits one project's history across two slugs"},
	}
}

func newApp() *cli.Command {
	return &cli.Command{
		Name:  "claudestats",
		Usage: "Claude Code token usage, cost and context size, from the transcripts on this machine",
		Commands: []*cli.Command{
			{
				Name:  "collect",
				Usage: "scan Claude Code config directories and store every billed attempt",
				Flags: []cli.Flag{dbFlag(), &cli.StringSliceFlag{
					Name:  "config-dir",
					Usage: "config directory to scan (repeatable); default: $CLAUDE_CONFIG_DIR, ~/.claude and every ~/.sandboxes/*",
				}},
				Action: runCollect,
			},
			{
				Name:  "split",
				Usage: "before and after a setting change: what actually moved, and why",
				Flags: append([]cli.Flag{dbFlag(), &cli.StringFlag{
					Name:     "at",
					Required: true,
					Usage:    "the instant the setting changed: YYYY-MM-DD or 'YYYY-MM-DD HH:MM' (UTC)",
				}}, rangeFlags()...),
				Action: runSplit,
			},
			{
				Name:   "regimes",
				Usage:  "compare autocompact regimes: what a request cost under each context ceiling",
				Flags:  append([]cli.Flag{dbFlag()}, rangeFlags()...),
				Action: runRegimes,
			},
			{
				Name:  "trend",
				Usage: "per-period table: requests, payload up and down, and unit price",
				Flags: append([]cli.Flag{dbFlag(), &cli.StringFlag{
					Name:  "by",
					Value: "week",
					Usage: "bucket width: day, week or month",
				}}, rangeFlags()...),
				Action: runTrend,
			},
			{
				Name:  "chart",
				Usage: "write a self-contained HTML report with the charts embedded",
				Flags: append([]cli.Flag{dbFlag(),
					&cli.StringFlag{Name: "out", Value: "claudestats.html", Usage: "file to write"},
					&cli.StringFlag{Name: "by", Value: "day", Usage: "bucket width: day, week or month"},
				}, rangeFlags()...),
				Action: runChart,
			},
			{
				Name:   "verify",
				Usage:  "our priced total beside Claude Code's own, for sessions that recorded one",
				Flags:  append([]cli.Flag{dbFlag()}, rangeFlags()...),
				Action: runVerify,
			},
			{
				Name:   "prices",
				Usage:  "the price table and where it came from",
				Action: runPrices,
			},
		},
	}
}

func openDB(c *cli.Command) (*store.DB, error) { return store.Open(c.String("db")) }

// buildFilter turns the shared narrowing flags into a store.Filter.
func buildFilter(c *cli.Command) (store.Filter, error) {
	var f store.Filter
	var err error
	if s := c.String("since"); s != "" {
		if f.Since, err = time.Parse("2006-01-02", s); err != nil {
			return f, fmt.Errorf("--since: %w", err)
		}
	}
	if s := c.String("until"); s != "" {
		if f.Until, err = time.Parse("2006-01-02", s); err != nil {
			return f, fmt.Errorf("--until: %w", err)
		}
	}
	f.ConfigDir = c.String("config-dir")
	f.Projects = c.StringSlice("project")
	return f, nil
}

func runCollect(ctx context.Context, c *cli.Command) error {
	dirs := c.StringSlice("config-dir")
	if len(dirs) == 0 {
		found, err := claudecode.ConfigDirs()
		if err != nil {
			return err
		}
		dirs = found
	}
	if len(dirs) == 0 {
		return fmt.Errorf("no Claude Code config directory found; pass --config-dir")
	}

	table, err := pricing.Load()
	if err != nil {
		return err
	}
	db, err := openDB(c)
	if err != nil {
		return err
	}
	defer db.Close()

	var files, sessions, requests int
	for _, dir := range dirs {
		paths, err := claudecode.TranscriptFiles(dir)
		if err != nil {
			return err
		}
		for _, p := range paths {
			files++
			parsed, err := claudecode.ParseFile(dir, p)
			if err != nil {
				// One unreadable transcript must not abandon the collection;
				// say which and carry on, so the run is still usable and the
				// gap is visible rather than silent.
				fmt.Fprintf(os.Stderr, "claudestats: skipping %s: %v\n", p, err)
				continue
			}
			if parsed == nil || parsed.Session.SessionID == "" {
				continue
			}
			rows := make([]store.Request, 0, len(parsed.Requests))
			for _, r := range parsed.Requests {
				rows = append(rows, store.FromUsage(r, table))
			}
			if err := db.SaveRequests(ctx, rows); err != nil {
				return err
			}
			if err := db.SaveSession(ctx, store.SessionFromUsage(parsed.Session)); err != nil {
				return err
			}
			sessions++
			requests += len(rows)
		}
	}
	fmt.Printf("collected %d billed attempts from %d sessions in %d transcripts across %d config directories\n",
		requests, sessions, files, len(dirs))
	fmt.Printf("database: %s\n", c.String("db"))
	return nil
}

func loadRange(ctx context.Context, c *cli.Command) (*store.DB, []store.Request, []store.Session, error) {
	f, err := buildFilter(c)
	if err != nil {
		return nil, nil, nil, err
	}
	db, err := openDB(c)
	if err != nil {
		return nil, nil, nil, err
	}
	reqs, err := db.Requests(ctx, f)
	if err != nil {
		db.Close()
		return nil, nil, nil, err
	}
	sessions, err := db.Sessions(ctx, f)
	if err != nil {
		db.Close()
		return nil, nil, nil, err
	}
	return db, reqs, sessions, nil
}

// runSplit answers "I changed a setting on this date; what moved?".
//
// It reports both halves of the identity rather than a single headline,
// because a change to the context ceiling can move the cost per turn without
// touching the price of a request, and a report that printed only the unit
// price would call that no change at all.
func runSplit(ctx context.Context, c *cli.Command) error {
	at, err := parseInstant(c.String("at"))
	if err != nil {
		return err
	}
	db, reqs, sessions, err := loadRange(ctx, c)
	if err != nil {
		return err
	}
	defer db.Close()

	sp := report.SplitAt(reqs, sessions, at)
	if sp.Before.Requests == 0 || sp.After.Requests == 0 {
		return fmt.Errorf("one side of the split is empty (before %d requests, after %d); pick a date inside the data",
			sp.Before.Requests, sp.After.Requests)
	}

	fmt.Printf("SPLIT AT %s (UTC), by when each session STARTED.\n", at.Format("2006-01-02 15:04"))
	fmt.Println("A launch flag is fixed for the life of the session it launched, so a session begun before")
	fmt.Println("the change keeps the old setting for as long as it runs. Sessions are attributed by start,")
	fmt.Println("never by the timestamp of an individual request.")
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "measure\tbefore\tafter\tratio")
	row := func(name string, b, a float64, f string) {
		fmt.Fprintf(w, "%s\t"+f+"\t"+f+"\t x%.2f\n", name, b, a, ratio(a, b))
	}
	fmt.Fprintf(w, "sessions\t%d\t%d\t\n", sp.Before.Sessions, sp.After.Sessions)
	fmt.Fprintf(w, "requests\t%d\t%d\t\n", sp.Before.Requests, sp.After.Requests)
	fmt.Fprintf(w, "human turns\t%d\t%d\t\n", sp.Before.Turns, sp.After.Turns)
	fmt.Fprintf(w, "max request context\t%s\t%s\t x%.2f\n",
		compact(sp.Before.PeakContext), compact(sp.After.PeakContext),
		ratio(float64(sp.After.PeakContext), float64(sp.Before.PeakContext)))
	row("upload per request", sp.Before.UploadedPerRequest(), sp.After.UploadedPerRequest(), "%.0f")
	row("cache read per request", sp.Before.CacheReadPerRequest(), sp.After.CacheReadPerRequest(), "%.0f")
	row("USD per request", sp.Before.USDPerRequest(), sp.After.USDPerRequest(), "%.4f")
	row("requests per turn", sp.Before.RequestsPerTurn(), sp.After.RequestsPerTurn(), "%.1f")
	row("USD per turn", sp.Before.USDPerTurn(), sp.After.USDPerTurn(), "%.3f")
	row("compactions per turn", sp.Before.CompactionsPerTurn(), sp.After.CompactionsPerTurn(), "%.2f")
	w.Flush()

	fmt.Println()
	fmt.Printf("THE IDENTITY: USD/turn x%.2f  =  requests/turn x%.2f  x  USD/request x%.2f\n",
		ratio(sp.After.USDPerTurn(), sp.Before.USDPerTurn()),
		ratio(sp.After.RequestsPerTurn(), sp.Before.RequestsPerTurn()),
		ratio(sp.After.USDPerRequest(), sp.Before.USDPerRequest()))
	fmt.Println("Read which factor moved. A saving that comes from requests per turn is a change in how the")
	fmt.Println("work is done; a saving in USD per request is a change in what each request carries.")

	if sp.BeforeCarried > 0 {
		fmt.Printf("\n%d requests after the split instant belong to sessions that started before it,\n", sp.BeforeCarried)
		fmt.Println("and are counted as before. That is the point of splitting on session start.")
	}
	if sp.After.Sessions < 20 || sp.After.Turns < 100 {
		fmt.Printf("\n⚠ SMALL SAMPLE AFTER: %d sessions, %d human turns. These are different sessions doing\n",
			sp.After.Sessions, sp.After.Turns)
		fmt.Println("  different work, not a controlled experiment. A ratio from this few sessions shows a")
		fmt.Println("  direction at best; collect more days before quoting the magnitude to anyone.")
	}
	return nil
}

// parseInstant accepts a bare date or a date and time, both read as UTC.
func parseInstant(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("--at %q: want YYYY-MM-DD or 'YYYY-MM-DD HH:MM'", s)
}

func runRegimes(ctx context.Context, c *cli.Command) error {
	db, reqs, sessions, err := loadRange(ctx, c)
	if err != nil {
		return err
	}
	defer db.Close()

	splits := report.ByRegime(reqs, sessions)
	if len(splits) == 0 {
		return fmt.Errorf("no data; run collect first")
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "AUTOCOMPACT REGIMES, inferred from the largest context each session actually reached.")
	fmt.Fprintln(w, "The --autocompact flag is never written to the transcript, so the ceiling is observed, not read.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "regime\tsessions\trequests\tturns\tmean peak ctx\tup/req\tcache read/req\tUSD/req\treq/turn\tUSD/turn\ttotal USD\tfirst seen\tlast seen")
	for _, s := range splits {
		b := s.Bucket
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%s\t%s\t%s\t%.4f\t%.1f\t%.3f\t%.2f\t%s\t%s\n",
			s.Regime.Name, b.Sessions, b.Requests, b.Turns,
			compact(int64(b.MeanPeakContext)), compact(int64(b.UploadedPerRequest())),
			compact(int64(b.CacheReadPerRequest())),
			b.USDPerRequest(), b.RequestsPerTurn(), b.USDPerTurn(), b.TotalUSD(),
			day(s.FirstSeen), day(s.LastSeen))
	}
	w.Flush()

	fmt.Println()
	fmt.Println("HOW TO READ IT: cost per turn = requests per turn x cost per request.")
	fmt.Println("A cheaper request is only a saving if requests per turn did not rise to match,")
	fmt.Println("because a smaller window compacts more often and compaction costs requests.")
	if len(splits) >= 2 {
		a, b := splits[0].Bucket, splits[1].Bucket
		fmt.Println()
		fmt.Printf("%s vs %s: USD/request x%.2f, USD/turn x%.2f, requests/turn x%.2f, upload/request x%.2f\n",
			splits[1].Regime.Name, splits[0].Regime.Name,
			ratio(b.USDPerRequest(), a.USDPerRequest()),
			ratio(b.USDPerTurn(), a.USDPerTurn()),
			ratio(b.RequestsPerTurn(), a.RequestsPerTurn()),
			ratio(b.UploadedPerRequest(), a.UploadedPerRequest()))
		if b.Sessions < 10 || a.Sessions < 10 {
			fmt.Println()
			fmt.Printf("⚠ SMALL SAMPLE: %d and %d sessions. These are different sessions doing different work,\n",
				a.Sessions, b.Sessions)
			fmt.Println("  not a controlled experiment. Read the per-period trend before drawing a conclusion.")
		}
	}
	return nil
}

func runTrend(ctx context.Context, c *cli.Command) error {
	db, reqs, sessions, err := loadRange(ctx, c)
	if err != nil {
		return err
	}
	defer db.Close()

	buckets := report.Build(reqs, sessions, report.Period(c.String("by")))
	if len(buckets) == 0 {
		return fmt.Errorf("no data; run collect first")
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "period\tsess\treq\tturns\tmean peak ctx\tup/req\tdown/req\tcache rd/req\tUSD/req\tUSD/turn\ttotal USD")
	for _, b := range buckets {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%.4f\t%.3f\t%.2f\n",
			b.Key, b.Sessions, b.Requests, b.Turns,
			compact(int64(b.MeanPeakContext)), compact(int64(b.UploadedPerRequest())),
			compact(int64(b.DownloadedPerRequest())), compact(int64(b.CacheReadPerRequest())),
			b.USDPerRequest(), b.USDPerTurn(), b.TotalUSD())
	}
	return w.Flush()
}

func runVerify(ctx context.Context, c *cli.Command) error {
	db, err := openDB(c)
	if err != nil {
		return err
	}
	defer db.Close()

	f, err := buildFilter(c)
	if err != nil {
		return err
	}
	sessions, err := db.Sessions(ctx, f)
	if err != nil {
		return err
	}
	reqs, err := db.Requests(ctx, f)
	if err != nil {
		return err
	}
	ours := map[string]float64{}
	for _, r := range reqs {
		ours[r.SessionID] += r.TotalUSD()
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "OUR PRICED TOTAL vs CLAUDE CODE'S OWN, for sessions carrying a cost-state record.")
	fmt.Fprintln(w, "A figure under 100% is expected: Claude Code also bills side calls that leave no usage record.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "session\tstarted\tour USD\tclaude USD\tcoverage\tAPI time")
	var sumOurs, sumTheirs float64
	var n int
	for _, s := range sessions {
		if !s.CostStatePresent || s.CCTotalCostUSD <= 0 {
			continue
		}
		o := ours[s.SessionID]
		sumOurs += o
		sumTheirs += s.CCTotalCostUSD
		n++
		fmt.Fprintf(w, "%s\t%s\t%.4f\t%.4f\t%.1f%%\t%s\n",
			short(s.SessionID), day(s.StartedAt), o, s.CCTotalCostUSD,
			100*ratio(o, s.CCTotalCostUSD),
			(time.Duration(s.CCAPIDurationMS) * time.Millisecond).Round(time.Second))
	}
	w.Flush()
	if n == 0 {
		fmt.Println("\nNo session in the store carried a cost-state record, so there is nothing to check against.")
		fmt.Println("That is a property of the corpus, not a failure: only a minority of transcripts carry one.")
		return nil
	}
	fmt.Printf("\n%d sessions: ours %.2f USD, Claude Code %.2f USD, coverage %.2f%%\n",
		n, sumOurs, sumTheirs, 100*ratio(sumOurs, sumTheirs))
	fmt.Printf("%d of %d stored sessions carried a cost-state record (%.1f%%).\n",
		n, len(sessions), 100*ratio(float64(n), float64(len(sessions))))
	return nil
}

func runPrices(_ context.Context, _ *cli.Command) error {
	t, err := pricing.Load()
	if err != nil {
		return err
	}
	fmt.Printf("as of %s\n%s\n\n", t.AsOf, t.Source)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "model\tinput\toutput\tcache read\tcache write 5m\tcache write 1h\tverified\tnote")
	for _, id := range t.ModelIDs() {
		p := t.Models[id]
		fmt.Fprintf(w, "%s\t%g\t%g\t%g\t%g\t%g\t%t\t%s\n",
			id, p.Input, p.Output, p.CacheRead, p.CacheWrite5m, p.CacheWrite1h, p.Verified, p.Note)
	}
	fmt.Fprintln(w, "\nUSD per million tokens.")
	return w.Flush()
}

func runChart(ctx context.Context, c *cli.Command) error {
	db, reqs, sessions, err := loadRange(ctx, c)
	if err != nil {
		return err
	}
	defer db.Close()

	buckets := report.Build(reqs, sessions, report.Period(c.String("by")))
	if len(buckets) == 0 {
		return fmt.Errorf("no data; run collect first")
	}
	out := c.String("out")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil && filepath.Dir(out) != "." {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("creating %s: %w", out, err)
	}
	defer f.Close()

	page := chartPage{
		Buckets:   buckets,
		Regimes:   report.ByRegime(reqs, sessions),
		Sessions:  sessions,
		Period:    c.String("by"),
		Generated: time.Now(),
	}
	if err := page.render(f); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d %s buckets, %d sessions)\n", out, len(buckets), c.String("by"), len(sessions))
	return nil
}

// compact renders a token count as 1.2M / 340k / 900 so a wide table stays
// readable. Precision is deliberately low: these are averages over thousands
// of requests and a trailing digit would suggest an accuracy they lack.
func compact(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func day(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02")
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func ratio(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}
