#!/usr/bin/env bash
# hflock offline demo — lockfile → Hugging Face download (local fixture) →
# SHA256 → weights.lock.manifest.json. Fully reproducible: no network beyond a
# localhost fixture server. Drives both docs/demo.tape (gif) and the asciinema
# cast, so the two stay in sync.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

run() { printf '\n$ %s\n' "$*"; eval "$@"; }

run go build -o hflock ./cmd/hflock
python3 -m http.server 8053 --directory examples/fixture >/tmp/hflock_demo_httpd.log 2>&1 &
SRV=$!
disown "$SRV" 2>/dev/null || true
trap 'kill "$SRV" 2>/dev/null || true; rm -f hflock weights.lock.manifest.json' EXIT
sleep 1

run 'HFLOCK_HF_BASE=http://localhost:8053 ./hflock verify examples/weights.lock.yaml'
run cat weights.lock.manifest.json
run shasum -a 256 examples/fixture/deepseek-ai/DeepSeek-V3/resolve/v3.0/config.json
echo
echo '# the sha256 above matches the manifest entry for config.json — provenance verified.'
