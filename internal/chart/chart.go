// Package chart renders small, self-contained SVG charts.
//
// The charts are generated server-side as SVG and embedded directly in the
// exported page, so the report is one file with no script dependency and no
// network fetch: it can be attached to a message and still work.
//
// The palette, mark sizes and rules here come from the team's data
// visualisation guidance and were validated with its checker on 2026-09-06:
// the four categorical slots used for stacks pass the lightness band, chroma
// floor, adjacent CVD separation and normal-vision floor in both light and
// dark mode. Two light-mode slots sit under 3:1 against the light surface,
// which obliges the relief rule, and that is why every chart here ships a
// legend and the page ships a table of the same numbers. Do not add a fifth
// stacked series without re-running that checker.
package chart

import (
	"fmt"
	"html"
	"math"
	"strings"
)

// Colours are the validated categorical slots, as CSS custom-property names.
// A chart never writes a hex literal: the page defines the light and dark
// value of each role once, so a theme change is a single swap.
const (
	Series1 = "var(--series-1)" // blue
	Series2 = "var(--series-2)" // orange
	Series3 = "var(--series-3)" // aqua
	Series4 = "var(--series-4)" // yellow
)

// Point is one observation.
type Point struct {
	// Label is the x-axis category, e.g. a date. Charts here are categorical
	// on x rather than continuous: the buckets are already discrete periods,
	// and spacing them evenly keeps a sparse week from implying a gap in time
	// that the data does not claim.
	Label string
	Y     float64
}

// Series is one named, coloured sequence.
type Series struct {
	Name  string
	Color string
	// Unit is appended to hover values, e.g. " tokens" or " USD".
	Points []Point
}

// Options configure one chart.
type Options struct {
	// Title names the chart. It is rendered by the page, not the SVG, so it
	// participates in the page's heading structure and is reachable by a
	// screen reader as a heading rather than as an image label.
	Title string
	// Height is the plot height in CSS pixels; width is fluid.
	Height int
	// ValueFormat renders a y value for ticks and tooltips.
	ValueFormat func(float64) string
	// Marker draws attention to a boundary, e.g. the day a setting changed.
	// It is drawn as a labelled vertical rule, never as a coloured region,
	// because a shaded band reads as a data series.
	MarkerLabel string
	MarkerAt    string
}

const (
	// The plot is drawn in a fixed user-space viewBox and scaled by CSS, so
	// stroke widths stay visually constant while the chart fills its column.
	vbWidth = 900
	padL    = 72
	padR    = 24
	padT    = 12
	padB    = 44
	// gapPx is the surface-coloured separation between adjacent stacked
	// segments and adjacent bars. Two pixels is enough to read as a boundary
	// without eating a small value.
	gapPx = 2
)

func fmtDefault(v float64) string { return fmt.Sprintf("%.0f", v) }

// StackedBars renders series stacked per category: the composition of a total
// over time. Use it when the parts sum to something meaningful, which for this
// tool means a payload or a price broken into its components.
func StackedBars(series []Series, o Options) string {
	if len(series) == 0 || len(series[0].Points) == 0 {
		return empty(o)
	}
	f := o.ValueFormat
	if f == nil {
		f = fmtDefault
	}
	h := o.Height
	if h == 0 {
		h = 260
	}
	n := len(series[0].Points)

	totals := make([]float64, n)
	for _, s := range series {
		for i, p := range s.Points {
			if i < n {
				totals[i] += p.Y
			}
		}
	}
	maxY := niceMax(maxOf(totals))

	plotW := float64(vbWidth - padL - padR)
	plotH := float64(h - padT - padB)
	step := plotW / float64(n)
	// Bars stay thin: the guidance calls for recessive marks, and a bar that
	// fills its slot turns a time series into a wall.
	barW := math.Min(step*0.62, 46)

	var b strings.Builder
	openSVG(&b, h, o)
	grid(&b, plotH, maxY, f)

	for i := 0; i < n; i++ {
		x := padL + step*(float64(i)+0.5) - barW/2
		var acc float64
		for si := len(series) - 1; si >= 0; si-- {
			s := series[si]
			if i >= len(s.Points) {
				continue
			}
			v := s.Points[i].Y
			if v <= 0 {
				continue
			}
			y0 := yPos(acc, maxY, plotH)
			acc += v
			y1 := yPos(acc, maxY, plotH)
			segH := y0 - y1
			// Reserve the 2px surface gap out of the segment, but never let a
			// small segment vanish into it.
			drawH := math.Max(segH-gapPx, 1)
			// Only the segment sitting on the baseline gets rounded ends, so
			// the rounding reads as the end of the bar rather than as a gap
			// between segments.
			r := 0.0
			if si == len(series)-1 {
				r = 4
			}
			fmt.Fprintf(&b, `<rect class="mk" x="%.2f" y="%.2f" width="%.2f" height="%.2f" rx="%.0f" fill="%s"><title>%s — %s: %s</title></rect>`,
				x, y1, barW, drawH, r, s.Color,
				html.EscapeString(s.Points[i].Label), html.EscapeString(s.Name), html.EscapeString(f(v)))
		}
	}
	xAxis(&b, series[0].Points, step, plotH)
	marker(&b, series[0].Points, step, plotH, o)
	b.WriteString("</svg>")
	return b.String()
}

// Lines renders one or more series as lines. Every series shares one y axis:
// a second scale is never introduced, so two measures of different magnitude
// belong in two charts rather than in one with two axes.
func Lines(series []Series, o Options) string {
	if len(series) == 0 || len(series[0].Points) == 0 {
		return empty(o)
	}
	f := o.ValueFormat
	if f == nil {
		f = fmtDefault
	}
	h := o.Height
	if h == 0 {
		h = 240
	}
	n := len(series[0].Points)

	maxY := 0.0
	for _, s := range series {
		for _, p := range s.Points {
			if p.Y > maxY {
				maxY = p.Y
			}
		}
	}
	maxY = niceMax(maxY)

	plotW := float64(vbWidth - padL - padR)
	plotH := float64(h - padT - padB)
	step := plotW / float64(n)

	var b strings.Builder
	openSVG(&b, h, o)
	grid(&b, plotH, maxY, f)

	for _, s := range series {
		var d strings.Builder
		for i, p := range s.Points {
			x := padL + step*(float64(i)+0.5)
			y := yPos(p.Y, maxY, plotH)
			if i == 0 {
				fmt.Fprintf(&d, "M%.2f %.2f", x, y)
			} else {
				fmt.Fprintf(&d, " L%.2f %.2f", x, y)
			}
		}
		fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>`,
			d.String(), s.Color)
		// Markers carry the hover target and are ringed in the surface colour
		// so an overlap stays legible.
		for i, p := range s.Points {
			x := padL + step*(float64(i)+0.5)
			y := yPos(p.Y, maxY, plotH)
			fmt.Fprintf(&b, `<circle class="mk" cx="%.2f" cy="%.2f" r="4.5" fill="%s" stroke="var(--surface-1)" stroke-width="2"><title>%s — %s: %s</title></circle>`,
				x, y, s.Color, html.EscapeString(p.Label), html.EscapeString(s.Name), html.EscapeString(f(p.Y)))
		}
	}
	xAxis(&b, series[0].Points, step, plotH)
	marker(&b, series[0].Points, step, plotH, o)
	b.WriteString("</svg>")
	return b.String()
}

// Dots renders one value per category without joining them, for a quantity
// where the connecting line would assert a continuity that does not exist.
// Session peak context is exactly that: each session is its own observation,
// and a line between two sessions would imply a trajectory nobody measured.
func Dots(s Series, o Options) string {
	if len(s.Points) == 0 {
		return empty(o)
	}
	f := o.ValueFormat
	if f == nil {
		f = fmtDefault
	}
	h := o.Height
	if h == 0 {
		h = 260
	}
	maxY := 0.0
	for _, p := range s.Points {
		if p.Y > maxY {
			maxY = p.Y
		}
	}
	maxY = niceMax(maxY)

	plotW := float64(vbWidth - padL - padR)
	plotH := float64(h - padT - padB)
	n := len(s.Points)
	step := plotW / float64(n)

	var b strings.Builder
	openSVG(&b, h, o)
	grid(&b, plotH, maxY, f)
	for i, p := range s.Points {
		x := padL + step*(float64(i)+0.5)
		y := yPos(p.Y, maxY, plotH)
		fmt.Fprintf(&b, `<circle class="mk" cx="%.2f" cy="%.2f" r="4" fill="%s" fill-opacity="0.75" stroke="var(--surface-1)" stroke-width="1.5"><title>%s: %s</title></circle>`,
			x, y, s.Color, html.EscapeString(p.Label), html.EscapeString(f(p.Y)))
	}
	xAxis(&b, s.Points, step, plotH)
	marker(&b, s.Points, step, plotH, o)
	b.WriteString("</svg>")
	return b.String()
}

func openSVG(b *strings.Builder, h int, o Options) {
	fmt.Fprintf(b, `<svg class="chart" viewBox="0 0 %d %d" preserveAspectRatio="none" role="img" aria-label="%s">`,
		vbWidth, h, html.EscapeString(o.Title))
}

func empty(o Options) string {
	return `<p class="empty">No data in range for ` + html.EscapeString(o.Title) + `.</p>`
}

func yPos(v, maxY, plotH float64) float64 {
	if maxY == 0 {
		return padT + plotH
	}
	return padT + plotH - (v/maxY)*plotH
}

// grid draws four recessive horizontal rules with their values. Gridlines are
// hairlines in a muted token so they sit behind the data rather than compete
// with it.
func grid(b *strings.Builder, plotH, maxY float64, f func(float64) string) {
	const ticks = 4
	for i := 0; i <= ticks; i++ {
		v := maxY * float64(i) / ticks
		y := yPos(v, maxY, plotH)
		fmt.Fprintf(b, `<line x1="%d" y1="%.2f" x2="%d" y2="%.2f" stroke="var(--gridline)" stroke-width="1"/>`,
			padL, y, vbWidth-padR, y)
		fmt.Fprintf(b, `<text class="tick" x="%d" y="%.2f" text-anchor="end" dominant-baseline="middle">%s</text>`,
			padL-10, y, html.EscapeString(f(v)))
	}
}

// xAxis labels at most eight categories, evenly spaced, so a long series does
// not collapse into unreadable overlapping text.
func xAxis(b *strings.Builder, pts []Point, step, plotH float64) {
	fmt.Fprintf(b, `<line x1="%d" y1="%.2f" x2="%d" y2="%.2f" stroke="var(--baseline)" stroke-width="1"/>`,
		padL, padT+plotH, vbWidth-padR, padT+plotH)
	n := len(pts)
	every := 1
	if n > 8 {
		every = int(math.Ceil(float64(n) / 8))
	}
	for i, p := range pts {
		if i%every != 0 && i != n-1 {
			continue
		}
		x := padL + step*(float64(i)+0.5)
		fmt.Fprintf(b, `<text class="tick" x="%.2f" y="%.2f" text-anchor="middle">%s</text>`,
			x, padT+plotH+18, html.EscapeString(p.Label))
	}
}

// marker draws a labelled vertical rule at a named category. It is a rule and
// a label rather than a shaded region, because a filled band reads as data.
func marker(b *strings.Builder, pts []Point, step, plotH float64, o Options) {
	if o.MarkerAt == "" {
		return
	}
	for i, p := range pts {
		if p.Label != o.MarkerAt {
			continue
		}
		x := padL + step*(float64(i)+0.5)
		fmt.Fprintf(b, `<line x1="%.2f" y1="%d" x2="%.2f" y2="%.2f" stroke="var(--text-secondary)" stroke-width="1.5" stroke-dasharray="4 3"/>`,
			x, padT, x, padT+plotH)
		anchor, dx := "start", 6.0
		if float64(i) > float64(len(pts))*0.7 {
			anchor, dx = "end", -6.0
		}
		fmt.Fprintf(b, `<text class="marker" x="%.2f" y="%d" text-anchor="%s">%s</text>`,
			x+dx, padT+12, anchor, html.EscapeString(o.MarkerLabel))
		return
	}
}

func maxOf(v []float64) float64 {
	m := 0.0
	for _, x := range v {
		if x > m {
			m = x
		}
	}
	return m
}

// niceMax rounds an axis maximum up to 1, 2, 2.5 or 5 times a power of ten, so
// the four gridlines land on values a reader can hold in their head.
func niceMax(m float64) float64 {
	if m <= 0 {
		return 1
	}
	exp := math.Floor(math.Log10(m))
	pow := math.Pow(10, exp)
	switch f := m / pow; {
	case f <= 1:
		return pow
	case f <= 2:
		return 2 * pow
	case f <= 2.5:
		return 2.5 * pow
	case f <= 5:
		return 5 * pow
	default:
		return 10 * pow
	}
}
