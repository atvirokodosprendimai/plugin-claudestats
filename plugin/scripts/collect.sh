#!/usr/bin/env bash
# Collect Claude Code usage into the claudestats store.
#
# This runs as a SessionEnd hook, which means it runs on a person's machine at
# the moment they finish working. Two consequences shape everything below:
# it must never fail loudly, and it must never be slow enough to notice.
#
# The reason it exists at all is that Claude Code prunes transcripts after its
# cleanup period. A report generated on demand from whatever is still on disk
# silently loses history; collecting at session end keeps it.
set -uo pipefail

# Deliberately NOT set -e. A hook that exits non-zero surfaces an error to
# someone who did not ask for a statistics tool, so every failure below is
# swallowed after being written to the log.

db="${CLAUDESTATS_DB:-$HOME/.claudestats/claudestats.db}"
log="${CLAUDESTATS_LOG:-$HOME/.claudestats/collect.log}"
mkdir -p "$(dirname "$db")" "$(dirname "$log")" 2>/dev/null || exit 0

# Prefer the binary built beside the plugin, then one on PATH. If neither is
# present the plugin is installed but not built, which is a normal state right
# after installation and not an error worth reporting at session end.
bin=""
if [ -x "${CLAUDE_PLUGIN_ROOT:-}/bin/claudestats" ]; then
  bin="${CLAUDE_PLUGIN_ROOT}/bin/claudestats"
elif command -v claudestats >/dev/null 2>&1; then
  bin="$(command -v claudestats)"
else
  echo "$(date -u +%FT%TZ) claudestats binary not found; run scripts/install.sh" >>"$log"
  exit 0
fi

# Timestamp the run so a stalled or failing collector is visible in the log
# rather than merely absent.
{
  echo "$(date -u +%FT%TZ) collect start"
  "$bin" collect --db "$db"
  echo "$(date -u +%FT%TZ) collect exit=$?"
} >>"$log" 2>&1

exit 0
