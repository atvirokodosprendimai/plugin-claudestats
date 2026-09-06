# claudestats

A Claude Code plugin that measures what your sessions actually cost, from the
transcripts Claude Code already writes, and exports a self-contained HTML
report you can send to someone.

It was built to answer one question with data instead of recollection:

> I cut the auto-compact window from 1M to 600k. Did that make anything cheaper?

Answering that honestly turns out to need more care than a chart, so the tool
is built around the care rather than around the chart.

## Install

```bash
git clone https://github.com/atvirokodosprendimai/plugin-claudestats
cd plugin-claudestats
bash plugin/scripts/install.sh          # builds plugin/bin/claudestats (needs Go)
```

In Claude Code, add the marketplace and install the plugin:

```
/plugin marketplace add ./plugin-claudestats
/plugin install claudestats
```

The plugin adds a `/claudestats` command and a `SessionEnd` hook that collects
each session as it finishes. That hook is the point of installing it rather
than just running the binary: **Claude Code prunes transcripts after its
cleanup period**, so history that is not collected is gone.

The store lives at `~/.claudestats/claudestats.db` (`CLAUDESTATS_DB` to move
it).

## Use

```bash
claudestats collect                        # scan every config dir and store it
claudestats chart --out report.html        # the self-contained HTML report
claudestats trend --by week                # per-period table
claudestats split --at 2026-09-05          # before and after a setting change
claudestats regimes                        # cost by observed context ceiling
claudestats verify                         # our total vs Claude Code's own
claudestats prices                         # the price table and its provenance
```

Every reporting command takes `--since`, `--until` and `--config-dir`.

**`--config-dir` matters more than it looks.** Launch flags such as
`--autocompact` are set per configuration directory, so a comparison that mixes
`~/.claude` with `~/.sandboxes/foo` is comparing sessions that were never under
the same settings. Whenever the question is about a setting, scope it.

## What it measures

`collect` walks every Claude Code config directory it can find
(`$CLAUDE_CONFIG_DIR`, `~/.claude`, each `~/.sandboxes/*`), reads
`projects/<slug>/<session>.jsonl` plus the subagent transcripts beside each
session, and stores **one row per billed API attempt**.

Five behaviours of the transcript silently change a per-session sum, and each
is handled explicitly in `internal/claudecode`:

1. **One message is written once per content block**, every copy carrying the
   full usage. Messages are deduplicated on `message.id`.
2. **The usage block describes the last attempt only.** When the API refused an
   attempt and answered with a fallback model, `usage.iterations` lists both,
   both were billed under different models, and `message.model` names only the
   one that answered. Each iteration is stored separately.
3. **The transcript is not the bill.** Claude Code also makes side calls that
   leave no usage record, so every figure here is a **lower bound**. Where a
   `cost-state` record exists it carries Claude Code's own total, and `verify`
   measures the gap against it.
4. **A prompt typed while the model is working is not a user record.** It
   arrives as a `queued_command` attachment. Missing those under-counts human
   turns by about a third in busy sessions.
5. **User records are mostly not people**: tool results, injected context,
   compaction summaries, command echoes and interrupts all arrive as role
   `user`.

## Reading the output without fooling yourself

Everything is reported per **request** and per **human turn**, never as a bare
total, and the two are tied by one identity:

    cost per human turn  =  requests per turn  x  cost per request

This is the whole discipline of the tool. A change to the context window can:

- make each request cheaper and cost **more** per turn, if the smaller window
  compacts more often and the work takes more requests; or
- leave the price of a request **completely unchanged** and still halve the
  cost per turn, if it changes how many requests the work takes.

A report that prints only one factor cannot tell those apart, so none of these
commands does.

### The autocompact window cannot be read back

`--autocompact` is a launch flag and appears nowhere in the transcript. Two
different instruments recover it, and they are not equally good:

- **`split --at <when>`** is the honest one. It divides sessions by **when the
  session started**, not by when a request was sent, because a launch flag is
  fixed for the life of the session it launched. A session begun before a
  change keeps the old setting for as long as it runs, sometimes for days.
  Attributing its later requests to "after" is exactly what makes a working
  change look like it did nothing.
- **`regimes`** buckets sessions by the largest context they actually reached.
  It is useful for describing what work at a given context size costs, but a
  session that simply never grew large is bucketed identically to one that was
  capped, and the buckets overlap in time. Do not use it to judge a setting.

### `PeakContext` is a maximum, not a mean

The evidence that a ceiling moved is the largest **single request**. The
per-session mean is reported beside it and is easy to misread: a period holding
one long session and three short ones shows a low mean peak and a high
per-request context at the same time. That divergence already misled one
reading of this data during development.

## Throughput is an estimate twice over

Bytes are inferred from tokens at a fixed ratio (`usage.BytesPerToken`) rather
than measured, and **individual assistant records carry no duration at all**. A
rate computed from consecutive record timestamps would be measuring tool
execution and human thinking time, so it is not computed. The upload and
download figures use Claude Code's own `totalAPIDuration` from the `cost-state`
record, which only a minority of transcripts carry; where none does, the column
is blank rather than invented.

## Prices

`internal/pricing/prices.json` holds USD per million tokens for input, output,
cache read, and cache write at both TTLs, with a `verified` flag per row and
the source and date of the table. A model missing from the table is counted in
tokens and reported as unpriced, never guessed. When the transcript did not
record which cache TTL wrote a token, the **cheaper** rate is used on purpose,
so the total stays a lower bound.

## Development

```bash
bash scripts/check.sh          # build, vet, gofmt, then every test in parallel
RACE=1 bash scripts/check.sh   # the same with the race detector
JOBS=8 bash scripts/check.sh   # cap the parallelism
```

`check.sh` compiles one test binary per package and then runs **each test
function as its own process** across every core, rather than `go test ./...`,
which serialises a package's tests into one process. It also never reads a
result through a pipe: a pipeline reports the last command's status, which is
the most common way a red run reads as green.

Packages: `internal/usage` (domain and the regime model), `internal/pricing`
(the table and the cost formula), `internal/claudecode` (transcript discovery
and parsing), `internal/store` (SQLite via gorm, goose migrations),
`internal/report` (aggregation, split and regime comparisons),
`internal/chart` (SVG), `cmd/claudestats` (the CLI and the HTML export).

## Charts

The report is one HTML file with the charts embedded as SVG: no script
dependency, no network fetch, and it still works as an email attachment. It is
theme-aware in both directions, ships a legend on every multi-series chart, and
repeats every charted number in a table, because two of the light-mode palette
slots fall below 3:1 against the surface and identity must not rest on colour.

## Licence

MIT.
