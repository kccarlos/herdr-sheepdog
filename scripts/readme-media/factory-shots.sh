#!/usr/bin/env bash
# factory-shots.sh: reproducibly captures README/guide screenshots of /factory
# into assets/readme/factory/ using a private demo daemon and fixture data.
#
#   scripts/readme-media/factory-shots.sh
#
# Builds sheepdog from this checkout, creates tiny demo git repos under
# /tmp/sheepdog-demo/{notes,recipes} with GitHub-looking remotes, places the
# fixtures under /tmp/sheepdog-demo/data (notes with started:true against
# --herdr=mock where the factory engine has nil hc client so no agents or
# git/gh commands can ever run; recipes finished/stopped), starts a private
# daemon on port 17820 with HOME, XDG_* and data-dir under /tmp/sheepdog-demo,
# verifies project state and every task state via /api/factory/projects and
# /api/factory/projects/notes/graph, drives headless Chromium via
# scripts/readme-media/factory-shots.go (which also re-verifies no drift before
# each shot), and stops everything it started.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
out=$repo/assets/readme/factory
port=17820

if [ -z "${BROWSER:-}" ]; then
	for b in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser" \
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"; do
		[ -x "$b" ] && BROWSER=$b && break
	done
fi
[ -n "${BROWSER:-}" ] || { echo "no Chromium found: set BROWSER" >&2; exit 1; }

pid=""
cleanup() {
	if [ -n "$pid" ]; then
		kill "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
	fi
	rm -rf /tmp/sheepdog-demo
}
trap cleanup EXIT

rm -rf /tmp/sheepdog-demo
mkdir -p /tmp/sheepdog-demo/{bin,home,xdg/config,xdg/data,xdg/state,xdg/cache,data/factory/projects/notes,data/factory/projects/recipes}
mkdir -p "$out"

(cd "$repo" && go build -o /tmp/sheepdog-demo/bin/sheepdog ./cmd/sheepdog)

make_demo_repo() {
	local dir=$1 id=$2 remote=$3 title=$4
	mkdir -p "$dir/.github/workflows"
	cat >"$dir/README.md" <<EOF
# $title
EOF
	cat >"$dir/.github/workflows/ci.yml" <<'EOF'
name: CI
on:
  pull_request:
    branches: [main]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: echo ok
EOF
	git -C "$dir" init -q -b main
	git -C "$dir" config --local user.name "Demo User"
	git -C "$dir" config --local user.email "demo@example.com"
	git -C "$dir" config --local sheepdog.projectId "$id"
	git -C "$dir" remote add origin "$remote"
	git -C "$dir" add README.md .github/workflows/ci.yml
	git -C "$dir" commit -q -m "initial demo commit"
	local sha
	sha=$(git -C "$dir" rev-parse HEAD)
	git -C "$dir" update-ref refs/remotes/origin/main "$sha"
	git -C "$dir" symbolic-ref refs/remotes/origin/HEAD refs/remotes/origin/main
}

make_demo_repo /tmp/sheepdog-demo/notes notes https://github.com/example/notes "Notes"
make_demo_repo /tmp/sheepdog-demo/recipes recipes https://github.com/example/recipes "Recipes"

cp "$here/factory/notes.state.json" /tmp/sheepdog-demo/data/factory/projects/notes/state.json
cp "$here/factory/notes.log.jsonl" /tmp/sheepdog-demo/data/factory/projects/notes/log.jsonl
cp "$here/factory/recipes.state.json" /tmp/sheepdog-demo/data/factory/projects/recipes/state.json
cp "$here/factory/recipes.log.jsonl" /tmp/sheepdog-demo/data/factory/projects/recipes/log.jsonl

# Scrubbed environment: no keys, no real HOME/XDG folders, no real CLIs/gh.
env -i \
	HOME=/tmp/sheepdog-demo/home \
	XDG_CONFIG_HOME=/tmp/sheepdog-demo/xdg/config \
	XDG_DATA_HOME=/tmp/sheepdog-demo/xdg/data \
	XDG_STATE_HOME=/tmp/sheepdog-demo/xdg/state \
	XDG_CACHE_HOME=/tmp/sheepdog-demo/xdg/cache \
	PATH=/usr/bin:/bin:/usr/sbin:/sbin \
	/tmp/sheepdog-demo/bin/sheepdog serve \
	--herdr=mock --voice=off --brain=off --cli=off \
	--port "$port" --data-dir /tmp/sheepdog-demo/data \
	</dev/null >/tmp/sheepdog-demo/daemon.log 2>&1 &
pid=$!

for _ in $(seq 1 40); do
	if curl -fs --max-time 2 "http://127.0.0.1:$port/api/factory/projects" >/dev/null 2>&1; then
		break
	fi
	sleep 0.5
done
curl -fs --max-time 2 "http://127.0.0.1:$port/api/factory/projects" >/dev/null

# Verify projects status and GET /api/factory/projects/notes/graph cover every task state without drift.
curl -fs --max-time 5 "http://127.0.0.1:$port/api/factory/projects" >/tmp/sheepdog-demo/projects.json
curl -fs --max-time 5 "http://127.0.0.1:$port/api/factory/projects/notes/graph" >/tmp/sheepdog-demo/notes-graph.json
python3 - /tmp/sheepdog-demo/projects.json /tmp/sheepdog-demo/notes-graph.json <<'PY'
import json, sys

projs = {p["id"]: p for p in json.load(open(sys.argv[1]))["projects"]}
assert projs["notes"]["running"] is True and projs["notes"]["state"] == "running", f"notes project not running: {projs['notes']}"
assert projs["recipes"]["running"] is False and projs["recipes"]["state"] == "finished", f"recipes project not finished: {projs['recipes']}"

g = json.load(open(sys.argv[2]))
tasks = g["tasks"]
assert len(tasks) == 20, f"expected 20 tasks, got {len(tasks)}"

stopped_why = [t["ref"] for t in tasks if t.get("why_waiting") == "Project is stopped"]
assert not stopped_why, f"tasks still claim Project is stopped: {stopped_why}"

agents = {t["ref"]: (t.get("model", ""), t.get("terminal", ""), t.get("why_waiting", "")) for t in tasks if t.get("terminal")}
want_agents = {
    "T5": ("opus", "sd-notes-T5-r2"),
    "T6": ("sonnet", "sd-notes-T6-1"),
    "T10": ("sonnet", "sd-notes-T10-1"),
}
assert set(agents) == set(want_agents), f"unexpected running agents: {agents}"
for ref, (m, term) in want_agents.items():
    got_m, got_t, got_w = agents[ref]
    assert got_m == m and got_t == term and got_w, f"agent {ref} mismatch: {agents[ref]}"
print("running agents:", ", ".join(f"{r}({agents[r][0]}: {agents[r][2]})" for r in sorted(agents)))

checks = {
    "done/merged": lambda t: t["stage"] == "done" and any(s["name"] == "merged" and s["state"] == "done" for s in t["steps"]),
    "implementing": lambda t: t["stage"] == "working" and any(s["name"] == "implementing" and s["state"] == "now" and not s.get("text", "").startswith("sent back") for s in t["steps"]),
    "in review (live reviewer)": lambda t: t["stage"] == "working" and any(s["name"] == "review" and s["state"] == "now" and s.get("text", "").startswith("review round ") for s in t["steps"]),
    "sent back from review": lambda t: t["stage"] == "working" and any(s["name"] == "implementing" and s["state"] == "now" and s.get("text", "").startswith("sent back from review round ") for s in t["steps"]),
    "waiting on CI": lambda t: t["stage"] == "ci" and any(s["name"] == "CI" and s["state"] == "now" for s in t["steps"]),
    "waiting on you (open owner ask)": lambda t: t["stage"] == "owner_answer",
    "queued for a slot": lambda t: t["stage"] == "queued",
    "waiting on others (dependency)": lambda t: t["stage"] == "waiting_on_dep",
    "dropped": lambda t: t["stage"] == "done" and any(s["name"] == "dropped" and s["state"] == "ended" for s in t["steps"]),
    "closed": lambda t: t["stage"] == "done" and any(s["name"] == "closed" and s["state"] == "ended" for s in t["steps"]),
    "parked": lambda t: t["stage"] == "blocked_or_failed" and any(s.get("text") == "parked here, waiting on the coordinator" for s in t["steps"]),
}
missing = []
for label, pred in checks.items():
    refs = [t["ref"] for t in tasks if pred(t)]
    print(f"state {label:32s}: {', '.join(refs) or 'MISSING'}")
    if not refs:
        missing.append(label)
if missing:
    raise SystemExit(f"missing states: {missing}")
PY

(cd "$repo" && go run scripts/readme-media/factory-shots.go -browser "$BROWSER" -url "http://127.0.0.1:$port/factory" -out "$out")
