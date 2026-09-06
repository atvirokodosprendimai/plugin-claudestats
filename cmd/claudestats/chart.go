package main

import (
	"fmt"
	"html"
	"io"
	"strings"
	"time"

	"github.com/atvirokodosprendimai/plugin-claudestats/internal/chart"
	"github.com/atvirokodosprendimai/plugin-claudestats/internal/report"
	"github.com/atvirokodosprendimai/plugin-claudestats/internal/store"
)

// chartPage is the exported HTML report: one self-contained file with the
// charts embedded as SVG, so it can be attached to a message and still work
// with no server, no script dependency and no network fetch.
type chartPage struct {
	Buckets   []report.Bucket
	Regimes   []report.RegimeSplit
	Sessions  []store.Session
	Period    string
	Generated time.Time
}

func (p chartPage) render(w io.Writer) error {
	var b strings.Builder
	b.WriteString(pageHead)
	p.writeHeader(&b)
	p.writeRegimes(&b)
	p.writeCharts(&b)
	p.writeTable(&b)
	p.writeCaveats(&b)
	b.WriteString("</main></body></html>\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func (p chartPage) writeHeader(b *strings.Builder) {
	fmt.Fprintf(b, `<main class="viz-root"><header>
<h1>Claude Code usage and cost</h1>
<p class="lede">Every figure below is a sum over billed API attempts recorded on this machine, priced from a
dated list-price table. Generated %s from %d %s buckets across %d sessions.</p>
</header>`, p.Generated.UTC().Format("2006-01-02 15:04 MST"), len(p.Buckets), html.EscapeString(p.Period), len(p.Sessions))
}

// writeRegimes leads with the question the tool was built to answer, because a
// reader who stops after the first screen should still have the answer.
func (p chartPage) writeRegimes(b *strings.Builder) {
	if len(p.Regimes) == 0 {
		return
	}
	b.WriteString(`<section><h2>Autocompact regimes</h2>
<p class="note">The <code>--autocompact</code> window is a launch flag and is never written to the transcript,
so it cannot be read back. What can be read is the largest context each session actually reached, and a session
cannot exceed its window. Sessions are bucketed by that observed peak, which recovers the regime from data
rather than from anyone's memory of when the setting changed.</p>`)

	b.WriteString(`<div class="tiles">`)
	for _, r := range p.Regimes {
		bk := r.Bucket
		fmt.Fprintf(b, `<div class="tile">
<div class="tile-k">%s window</div>
<div class="tile-v">%s</div>
<div class="tile-s">USD per request</div>
<dl>
<dt>USD per human turn</dt><dd>%.3f</dd>
<dt>Requests per turn</dt><dd>%.1f</dd>
<dt>Upload per request</dt><dd>%s tokens</dd>
<dt>Mean peak context</dt><dd>%s tokens</dd>
<dt>Sessions</dt><dd>%d</dd>
<dt>Requests</dt><dd>%d</dd>
</dl></div>`,
			html.EscapeString(r.Regime.Name), fmt.Sprintf("%.4f", bk.USDPerRequest()),
			bk.USDPerTurn(), bk.RequestsPerTurn(),
			compact(int64(bk.UploadedPerRequest())), compact(int64(bk.MeanPeakContext)),
			bk.Sessions, bk.Requests)
	}
	b.WriteString(`</div>`)

	if len(p.Regimes) >= 2 {
		a, c := p.Regimes[0].Bucket, p.Regimes[1].Bucket
		fmt.Fprintf(b, `<p class="identity"><strong>Read it through the identity:</strong>
cost per human turn = requests per turn &times; cost per request.
A cheaper request is only a saving if requests per turn did not rise to meet it, because a smaller
window compacts more often and every compaction costs requests.
<br>%s against %s: USD per request &times;%.2f, USD per turn &times;%.2f, requests per turn &times;%.2f.</p>`,
			html.EscapeString(p.Regimes[1].Regime.Name), html.EscapeString(p.Regimes[0].Regime.Name),
			ratio(c.USDPerRequest(), a.USDPerRequest()),
			ratio(c.USDPerTurn(), a.USDPerTurn()),
			ratio(c.RequestsPerTurn(), a.RequestsPerTurn()))

		if a.Sessions < 10 || c.Sessions < 10 {
			fmt.Fprintf(b, `<p class="warn"><strong>Small sample.</strong> %d and %d sessions. These are different
sessions doing different work, not a controlled experiment, and a short session that never grew large is
indistinguishable from one that was capped low. Treat the direction as suggestive and the magnitude as unproven
until both regimes hold comparable, varied work.</p>`, a.Sessions, c.Sessions)
		}
	}
	b.WriteString(`</section>`)
}

func (p chartPage) writeCharts(b *strings.Builder) {
	labels := make([]string, len(p.Buckets))
	for i, bk := range p.Buckets {
		labels[i] = bk.Key
	}

	// The day the context ceiling visibly moved, marked on every time chart so
	// the eye lands on the same boundary in each.
	markerAt, markerLabel := p.regimeBoundary()

	tokens := func(v float64) string { return compact(int64(v)) }
	usd := func(v float64) string { return fmt.Sprintf("%.3f", v) }

	// 1. The ceiling itself. Dots, not a line: each session is its own
	// observation and joining them would assert a trajectory nobody measured.
	peak := chart.Series{Name: "Peak context", Color: chart.Series1}
	for _, s := range p.Sessions {
		if s.PeakContext > 0 {
			peak.Points = append(peak.Points, chart.Point{
				Label: s.StartedAt.UTC().Format("2006-01-02"), Y: float64(s.PeakContext),
			})
		}
	}
	p.section(b, "Peak context per session",
		"The largest context any single request in a session carried. This is the ceiling made visible: the flat top is the autocompact window, and a drop in that top is the window being lowered.",
		chart.Dots(peak, chart.Options{Title: "Peak context per session", Height: 280, ValueFormat: tokens, MarkerAt: markerAt, MarkerLabel: markerLabel}),
		[]chart.Series{peak})

	// 2. Payload composition per request. Three slots, which is the set that
	// validates on the all-pairs list as well as the adjacent one.
	up := []chart.Series{
		{Name: "Cache read (history replayed)", Color: chart.Series1},
		{Name: "Cache write (history stored)", Color: chart.Series2},
		{Name: "Fresh input", Color: chart.Series3},
	}
	for i, bk := range p.Buckets {
		r := float64(max64(int64(bk.Requests), 1))
		up[0].Points = append(up[0].Points, chart.Point{Label: labels[i], Y: float64(bk.CacheReadTokens) / r})
		up[1].Points = append(up[1].Points, chart.Point{Label: labels[i], Y: float64(bk.CacheCreationTokens) / r})
		up[2].Points = append(up[2].Points, chart.Point{Label: labels[i], Y: float64(bk.InputTokens) / r})
	}
	p.section(b, "Upload payload per request",
		"What one request sends up, split into its parts. Cache read is the conversation so far being replayed on every single request, and it is normally the bulk of both the payload and the price. This is the quantity the autocompact window bounds.",
		chart.StackedBars(up, chart.Options{Title: "Upload payload per request", Height: 280, ValueFormat: tokens, MarkerAt: markerAt, MarkerLabel: markerLabel}),
		up)

	// 3. Unit price. One axis, one series.
	price := chart.Series{Name: "USD per request", Color: chart.Series1}
	for i, bk := range p.Buckets {
		price.Points = append(price.Points, chart.Point{Label: labels[i], Y: bk.USDPerRequest()})
	}
	p.section(b, "Price per request",
		"The headline unit price. It tracks the payload above, because replaying the history is what a request mostly pays for.",
		chart.Lines([]chart.Series{price}, chart.Options{Title: "USD per request", Height: 240, ValueFormat: usd, MarkerAt: markerAt, MarkerLabel: markerLabel}),
		nil)

	// 4 and 5 are deliberately two charts rather than one with two y scales.
	turn := chart.Series{Name: "USD per human turn", Color: chart.Series1}
	rpt := chart.Series{Name: "Requests per human turn", Color: chart.Series2}
	for i, bk := range p.Buckets {
		turn.Points = append(turn.Points, chart.Point{Label: labels[i], Y: bk.USDPerTurn()})
		rpt.Points = append(rpt.Points, chart.Point{Label: labels[i], Y: bk.RequestsPerTurn()})
	}
	p.section(b, "Price per human turn",
		"What one thing a person asked for cost. This is the figure that decides whether a change was a saving, because it absorbs any rise in the number of requests needed to do the same work.",
		chart.Lines([]chart.Series{turn}, chart.Options{Title: "USD per human turn", Height: 240, ValueFormat: usd, MarkerAt: markerAt, MarkerLabel: markerLabel}),
		nil)
	p.section(b, "Requests per human turn",
		"The other half of the identity. A smaller context window compacts more often, and compaction costs requests, so this is where a saving on unit price can quietly be given back.",
		chart.Lines([]chart.Series{rpt}, chart.Options{Title: "Requests per human turn", Height: 220, ValueFormat: func(v float64) string { return fmt.Sprintf("%.0f", v) }, MarkerAt: markerAt, MarkerLabel: markerLabel}),
		nil)

	// 6. Where the money actually goes.
	cost := []chart.Series{
		{Name: "Cache read", Color: chart.Series1},
		{Name: "Cache write", Color: chart.Series2},
		{Name: "Output", Color: chart.Series3},
		{Name: "Input", Color: chart.Series4},
	}
	for i, bk := range p.Buckets {
		cost[0].Points = append(cost[0].Points, chart.Point{Label: labels[i], Y: bk.CacheReadUSD})
		cost[1].Points = append(cost[1].Points, chart.Point{Label: labels[i], Y: bk.CacheWriteUSD})
		cost[2].Points = append(cost[2].Points, chart.Point{Label: labels[i], Y: bk.OutputUSD})
		cost[3].Points = append(cost[3].Points, chart.Point{Label: labels[i], Y: bk.InputUSD})
	}
	p.section(b, "Where the money goes",
		"Spend split by what was billed. If cache read dominates, the lever is context size rather than output length.",
		chart.StackedBars(cost, chart.Options{Title: "Cost composition", Height: 280, ValueFormat: usd, MarkerAt: markerAt, MarkerLabel: markerLabel}),
		cost)
}

// regimeBoundary finds the first bucket whose mean peak context drops below
// the largest regime ceiling, which is where a window change shows itself.
// It returns empty strings when no such drop exists, and the charts then carry
// no marker rather than an invented one.
func (p chartPage) regimeBoundary() (at, label string) {
	if len(p.Buckets) < 2 {
		return "", ""
	}
	const drop = 0.7
	for i := 1; i < len(p.Buckets); i++ {
		prev, cur := p.Buckets[i-1].MeanPeakContext, p.Buckets[i].MeanPeakContext
		if prev > 0 && cur > 0 && cur < prev*drop {
			return p.Buckets[i].Key, "context ceiling drops"
		}
	}
	return "", ""
}

// section renders a titled chart with its explanation and, for a multi-series
// chart, a legend. The legend is never optional: two of the light-mode slots
// sit below 3:1 against the surface, so identity must not rest on colour.
func (p chartPage) section(b *strings.Builder, title, note, svg string, legend []chart.Series) {
	fmt.Fprintf(b, `<section><h2>%s</h2><p class="note">%s</p>`,
		html.EscapeString(title), html.EscapeString(note))
	if len(legend) > 1 {
		b.WriteString(`<ul class="legend">`)
		for _, s := range legend {
			fmt.Fprintf(b, `<li><span class="swatch" style="background:%s"></span>%s</li>`,
				s.Color, html.EscapeString(s.Name))
		}
		b.WriteString(`</ul>`)
	}
	fmt.Fprintf(b, `<div class="plot">%s</div></section>`, svg)
}

// writeTable is the table view the relief rule obliges, and it doubles as the
// accessible form of every chart above: the same numbers, readable without
// colour and without a pointing device.
func (p chartPage) writeTable(b *strings.Builder) {
	b.WriteString(`<section><h2>The same numbers</h2>
<p class="note">Every chart above is drawn from this table.</p>
<div class="scroll"><table><thead><tr>
<th scope="col">Period</th><th scope="col">Sessions</th><th scope="col">Requests</th><th scope="col">Turns</th>
<th scope="col">Mean peak ctx</th><th scope="col">Up / req</th><th scope="col">Down / req</th>
<th scope="col">USD / req</th><th scope="col">USD / turn</th><th scope="col">Total USD</th>
<th scope="col">Upload kbps</th></tr></thead><tbody>`)
	for _, bk := range p.Buckets {
		kbps := "&mdash;"
		if v := bk.UploadKbps(); v > 0 {
			kbps = fmt.Sprintf("%.0f", v)
		}
		fmt.Fprintf(b, `<tr><th scope="row">%s</th><td>%d</td><td>%d</td><td>%d</td><td>%s</td><td>%s</td><td>%s</td><td>%.4f</td><td>%.3f</td><td>%.2f</td><td>%s</td></tr>`,
			html.EscapeString(bk.Key), bk.Sessions, bk.Requests, bk.Turns,
			compact(int64(bk.MeanPeakContext)), compact(int64(bk.UploadedPerRequest())),
			compact(int64(bk.DownloadedPerRequest())),
			bk.USDPerRequest(), bk.USDPerTurn(), bk.TotalUSD(), kbps)
	}
	b.WriteString(`</tbody></table></div></section>`)
}

func (p chartPage) writeCaveats(b *strings.Builder) {
	var api, total int
	for _, s := range p.Sessions {
		total++
		if s.CostStatePresent && s.CCAPIDurationMS > 0 {
			api++
		}
	}
	fmt.Fprintf(b, `<section><h2>What these numbers do not say</h2><ul class="caveats">
<li><strong>They are a lower bound.</strong> The transcript records the requests the model answered.
Claude Code also bills side calls of its own that leave no usage record, so a per-request sum runs a few
per cent under what was actually billed. The shortfall is roughly uniform across periods, so ratios between
periods compare even though absolute totals run low.</li>
<li><strong>Throughput is an estimate twice over.</strong> Bytes are inferred from tokens at a fixed ratio
rather than measured, and per-request timing does not exist in the transcript at all. The upload figure uses
Claude Code's own recorded API time, which %d of %d sessions here carry. Where that column is blank, no
session in the period recorded any.</li>
<li><strong>Periods are not an experiment.</strong> Different periods contain different work. Read
USD per turn beside requests per turn before concluding that anything got cheaper.</li>
<li><strong>Cache-write pricing is assumed where the transcript did not record the TTL split.</strong>
Those tokens are priced at the cheaper five-minute rate, which keeps the total a lower bound.</li>
</ul></section>`, api, total)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
