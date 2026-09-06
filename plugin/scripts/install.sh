#!/usr/bin/env bash
# Build the claudestats binary into the plugin's bin directory.
#
# The plugin ships source rather than a binary because the collector reads the
# user's own transcripts: a person installing a tool that reads their whole
# Claude Code history should be able to build it from the source they can see.
set -euo pipefail

root="${CLAUDE_PLUGIN_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
# The Go module lives one level above the plugin directory in this repository.
src="$(cd "$root/.." && pwd)"

if ! command -v go >/dev/null 2>&1; then
  echo "claudestats: Go is required to build the collector." >&2
  echo "Install Go (https://go.dev/dl/) and run this script again." >&2
  exit 1
fi

if [ ! -f "$src/go.mod" ]; then
  echo "claudestats: no go.mod found at $src" >&2
  echo "Run this from a checkout of the plugin-claudestats repository." >&2
  exit 1
fi

mkdir -p "$root/bin"
( cd "$src" && go build -trimpath -o "$root/bin/claudestats" ./cmd/claudestats )

echo "claudestats: built $root/bin/claudestats"
"$root/bin/claudestats" --version 2>/dev/null || true
echo
echo "Next: run a first collection, then open a report."
echo "  $root/bin/claudestats collect"
echo "  $root/bin/claudestats chart --out claudestats.html"
