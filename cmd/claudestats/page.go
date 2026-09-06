package main

// pageHead is the document head and stylesheet of the exported report.
//
// The colour roles are declared once here and referenced by name everywhere
// else, so the light and dark values swap in one place and no chart carries a
// hex literal. The dark values are declared under BOTH the media query and the
// data-theme scope, because the media query covers the operating system
// setting while the attribute covers an explicit choice, and the explicit
// choice has to win in both directions.
//
// The palette is the validated categorical set: slots 1 to 4 pass the
// lightness band, chroma floor, adjacent CVD separation and normal-vision
// floor in both modes. Two light-mode slots fall under 3:1 against the light
// surface, which is why every multi-series chart ships a legend and the page
// ships the table view.
const pageHead = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Claude Code usage and cost</title>
<style>
:root {
  color-scheme: light;
  --page:           #f9f9f7;
  --surface-1:      #fcfcfb;
  --text-primary:   #0b0b0b;
  --text-secondary: #52514e;
  --muted:          #898781;
  --gridline:       #e1e0d9;
  --baseline:       #c3c2b7;
  --border:         rgba(11,11,11,0.10);
  --series-1:       #2a78d6;
  --series-2:       #eb6834;
  --series-3:       #1baf7a;
  --series-4:       #eda100;
  --warn:           #ec835a;
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    color-scheme: dark;
    --page:           #0d0d0d;
    --surface-1:      #1a1a19;
    --text-primary:   #ffffff;
    --text-secondary: #c3c2b7;
    --muted:          #898781;
    --gridline:       #2c2c2a;
    --baseline:       #383835;
    --border:         rgba(255,255,255,0.10);
    --series-1:       #3987e5;
    --series-2:       #d95926;
    --series-3:       #199e70;
    --series-4:       #c98500;
  }
}
:root[data-theme="dark"] {
  color-scheme: dark;
  --page:           #0d0d0d;
  --surface-1:      #1a1a19;
  --text-primary:   #ffffff;
  --text-secondary: #c3c2b7;
  --muted:          #898781;
  --gridline:       #2c2c2a;
  --baseline:       #383835;
  --border:         rgba(255,255,255,0.10);
  --series-1:       #3987e5;
  --series-2:       #d95926;
  --series-3:       #199e70;
  --series-4:       #c98500;
}

* { box-sizing: border-box; }
body {
  margin: 0;
  background: var(--page);
  color: var(--text-primary);
  font: 15px/1.55 system-ui, -apple-system, "Segoe UI", sans-serif;
}
main { max-width: 1080px; margin: 0 auto; padding: 40px 20px 72px; }
header { margin-bottom: 32px; }
h1 { font-size: 1.6rem; line-height: 1.2; margin: 0 0 8px; letter-spacing: -0.01em; }
h2 { font-size: 1.05rem; margin: 0 0 6px; letter-spacing: -0.005em; }
.lede { color: var(--text-secondary); margin: 0; max-width: 68ch; }

section {
  background: var(--surface-1);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 20px;
  margin-bottom: 20px;
}
.note { color: var(--text-secondary); margin: 0 0 14px; max-width: 78ch; font-size: 0.92rem; }
code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.9em; }

.tiles { display: grid; gap: 14px; grid-template-columns: repeat(auto-fit, minmax(230px, 1fr)); }
.tile { border: 1px solid var(--border); border-radius: 8px; padding: 14px 16px; }
.tile-k { color: var(--text-secondary); font-size: 0.82rem; text-transform: uppercase; letter-spacing: 0.04em; }
.tile-v { font-size: 2rem; line-height: 1.1; margin: 6px 0 2px; }
.tile-s { color: var(--muted); font-size: 0.82rem; margin-bottom: 10px; }
.tile dl { display: grid; grid-template-columns: 1fr auto; gap: 3px 12px; margin: 0; font-size: 0.87rem; }
.tile dt { color: var(--text-secondary); }
.tile dd { margin: 0; text-align: right; font-variant-numeric: tabular-nums; }

.identity { margin: 16px 0 0; color: var(--text-secondary); font-size: 0.92rem; max-width: 78ch; }
.identity strong { color: var(--text-primary); }
.warn {
  margin: 14px 0 0; padding: 10px 12px; font-size: 0.9rem;
  border-left: 3px solid var(--warn); color: var(--text-secondary);
}
.warn strong { color: var(--text-primary); }

.legend { display: flex; flex-wrap: wrap; gap: 6px 18px; list-style: none; padding: 0; margin: 0 0 12px; font-size: 0.87rem; color: var(--text-secondary); }
.legend li { display: flex; align-items: center; gap: 7px; }
.swatch { width: 11px; height: 11px; border-radius: 3px; flex: none; }

.plot { width: 100%; }
.chart { width: 100%; height: auto; display: block; overflow: visible; }
.chart .tick { fill: var(--muted); font-size: 12px; font-family: inherit; font-variant-numeric: tabular-nums; }
.chart .marker { fill: var(--text-secondary); font-size: 12px; font-family: inherit; }
/* A mark lifts slightly on hover so the native tooltip has an obvious target. */
.chart .mk { transition: opacity .12s ease; }
.chart:hover .mk { opacity: .55; }
.chart .mk:hover { opacity: 1; }
.empty { color: var(--muted); font-style: italic; }

.scroll { overflow-x: auto; }
table { border-collapse: collapse; width: 100%; font-size: 0.87rem; }
th, td { padding: 7px 10px; text-align: right; white-space: nowrap; }
thead th { color: var(--text-secondary); font-weight: 600; border-bottom: 1px solid var(--baseline); }
tbody th[scope="row"] { text-align: left; font-weight: 500; }
tbody td { font-variant-numeric: tabular-nums; }
tbody tr + tr th, tbody tr + tr td { border-top: 1px solid var(--gridline); }

.caveats { margin: 0; padding-left: 20px; color: var(--text-secondary); font-size: 0.92rem; max-width: 80ch; }
.caveats li { margin-bottom: 8px; }
.caveats strong { color: var(--text-primary); }

@media (max-width: 620px) {
  main { padding: 24px 14px 48px; }
  section { padding: 16px 14px; }
  .tile-v { font-size: 1.6rem; }
}
</style>
</head>
<body>
`
