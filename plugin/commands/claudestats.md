---
description: Collect Claude Code token usage and open an HTML report of tokens, cost and context size
argument-hint: "[report | split <YYYY-MM-DD> | trend | verify]"
---

# claudestats

Run the claudestats collector and report on this machine's Claude Code usage.

The binary lives at `${CLAUDE_PLUGIN_ROOT}/bin/claudestats`. If it is missing,
build it first with `bash ${CLAUDE_PLUGIN_ROOT}/scripts/install.sh` (needs Go).
The store defaults to `~/.claudestats/claudestats.db`.

Interpret `$ARGUMENTS` as follows, defaulting to `report` when empty.

## report

Collect, then write and report on a self-contained HTML file.

```bash
BIN="${CLAUDE_PLUGIN_ROOT}/bin/claudestats"
DB="${CLAUDESTATS_DB:-$HOME/.claudestats/claudestats.db}"
"$BIN" collect --db "$DB"
"$BIN" chart --db "$DB" --by day --out ./claudestats.html
```

Then tell the user the path, and summarise from `"$BIN" trend --db "$DB" --by week`:
the direction of USD per request, USD per human turn, and upload tokens per
request over the last few buckets.

## split <YYYY-MM-DD>

Answer "I changed a setting on this date; what moved?".

```bash
"$BIN" split --db "$DB" --at "<the date>"
```

⚠ When you report the result, **report both halves of the identity**, never the
headline alone:

    USD per turn = requests per turn x USD per request

A change to the context window often leaves the price of a request untouched
and still moves the cost per turn, because it changes how many requests the
work takes. Saying only "cost per turn halved" hides which of the two moved,
and the two have completely different consequences.

Also pass on the sample warning if the command prints one. A ratio from a
handful of sessions is a direction, not a magnitude.

## trend

```bash
"$BIN" trend --db "$DB" --by week
```

## verify

Check the tool's own priced total against Claude Code's own figure, for the
sessions that recorded one.

```bash
"$BIN" verify --db "$DB"
```

Coverage under 100% is expected and is not an error: Claude Code also bills
side calls that leave no usage record in the transcript, so every figure this
tool produces is a lower bound.

## Useful flags

- `--config-dir aks` restricts to one Claude Code config directory. Launch
  flags such as `--autocompact` are set per directory, so a comparison that
  mixes directories is comparing sessions that were never under the same
  settings. Use it whenever the question is about a setting.
- `--since` / `--until` bound the date range.
- `--by day|week|month` sets the bucket width.
