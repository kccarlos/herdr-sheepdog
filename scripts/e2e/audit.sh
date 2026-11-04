#!/usr/bin/env bash
# Daily audit: audit.sh <output dir> <commit> <date> <fixer 0|1>
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
OUT="$1" SHA="$2" DAY="$3" FIXER="$4"
SCOPE=$(python3 "$HERE/audit_data.py" scope "$DAY") || exit 3
REPORT="$OUT/audit.md"
TMP=$(mktemp -d "${TMPDIR:-/tmp}/sheepdog-audit.XXXXXX") || exit 3
WT="$TMP/fix"
cleanup() {
  if [ -d "$WT" ]; then git -C "$ROOT" worktree remove --force "$WT" >/dev/null 2>&1 || :; fi
  chmod -R u+w "$TMP" 2>/dev/null || :
  rm -rf "$TMP"
}
trap cleanup EXIT
skip() { echo "SKIP $*"; echo; echo "SKIP: $*" >>"$REPORT"; exit 3; }
note() { echo "$*"; printf '\n%s\n' "$*" >>"$REPORT"; }
printf '# Code audit: %s\n\nCommit: `%s` · scope: `%s`\n' "$DAY" "$SHA" "$SCOPE" >"$REPORT"
command -v agy >/dev/null 2>&1 || skip "agy is not installed"
# An archive contains only tracked files at the tested commit, never local edits.
mkdir "$TMP/read"
git -C "$ROOT" archive "$SHA" | tar -x -C "$TMP/read" || skip "could not create clean checkout"
chmod -R a-w "$TMP/read" || skip "could not make checkout read-only"
cat >"$TMP/audit-prompt" <<EOF2
Audit this repository at the checked-out commit. Scope: $SCOPE. Read only: do not modify files, run tests, invoke other agents, use network, or change any system state. Examine code and report only real, actionable problems. Return ONLY JSON lines, one object per finding with keys file (repository-relative path), line (positive integer), scenario, fix, severity (high, medium, or low). If none, return exactly []. Do not use markdown fences.
EOF2
if ! python3 "$HERE/agy_run.py" gemini-4-argon-high "$TMP/read" "$TMP/audit-prompt" "$OUT/audit-agent.log" 1800; then
  skip "audit agent could not complete; see audit-agent.log for its reason"
fi
cat >"$TMP/verify-prompt" <<EOF2
Independently try to disprove every finding below by reading the checked-out code. Read only: do not modify files, run tests, invoke other agents, use network, or change system state. Number findings from 1 in their given order. Return ONLY one JSON line per finding: {"id":1,"verdict":"confirmed" or "rejected","reason":"specific evidence"}. Return exactly [] for no findings. Do not use markdown fences.

Findings:
$(cat "$OUT/audit-agent.log.md")
EOF2
if ! python3 "$HERE/agy_run.py" gemini-4-argon-medium "$TMP/read" "$TMP/verify-prompt" "$OUT/audit-verify.log" 1200; then
  { printf '\n## Unverified findings\n\n```text\n'; sed 's/```/` ` `/g' "$OUT/audit-agent.log.md"; printf '\n```\n'; } >>"$REPORT"
  note 'Verification did not complete.'
  skip "verification agent could not complete; see audit-verify.log for its reason"
fi
if ! python3 "$HERE/audit_data.py" render "$OUT/audit-agent.log.md" "$OUT/audit-verify.log.md" "$REPORT" "$TMP/selected.json"; then
  skip "invalid findings or verification response; see audit agent logs"
fi
[ "$(cat "$TMP/selected.json")" != '[]' ] || { note 'No confirmed high-severity findings to fix.'; exit 0; }
[ "$FIXER" = 1 ] || { note 'Fix skipped (--no-fixer).'; exit 0; }
command -v gh >/dev/null 2>&1 || { note 'Fix skipped: gh is not installed.'; exit 0; }
# Fail closed if GitHub cannot tell us whether another PR exists.
if ! gh pr list --repo kccarlos/herdr-sheepdog --state open --limit 1000 --json headRefName,labels,url >"$TMP/prs.json"; then
  note 'Fix skipped: could not check open PRs.'; exit 0
fi
if ! python3 - "$TMP/prs.json" "$SCOPE" <<'PY'
import json, sys
prs = json.load(open(sys.argv[1]))
scope = sys.argv[2]
for pr in prs:
    branch = pr['headRefName']
    if branch.startswith('audit/') and branch.endswith('-' + scope):
        print('Existing audit PR:', pr['url'])
        sys.exit(1)
    if any(label['name'] == 'audit/' + scope for label in pr['labels']):
        print('Existing audit PR:', pr['url'])
        sys.exit(1)
PY
then
  note 'Fix skipped: an open PR already covers this scope, or PR data was invalid.'; exit 0
fi
LOCK="$(dirname "$OUT")/fixer.lock"
if ! mkdir "$LOCK" 2>/dev/null; then note 'Fix skipped: another fixer holds the lock.'; exit 0; fi
echo $$ >"$LOCK/pid"
# One fix attempt per date, including partial or failed attempts.
if ! mkdir "$(dirname "$OUT")/audit-fix-$DAY" 2>/dev/null; then
  rm -rf "$LOCK"
  note 'Fix skipped: an audit fix was already attempted today.'; exit 0
fi
# The lock is released even if the step is terminated.
trap 'rm -rf "$LOCK"; cleanup' EXIT
if ! git -C "$ROOT" worktree add -q --detach "$WT" "$SHA"; then note 'Fix skipped: could not create worktree.'; exit 0; fi
cat >"$TMP/fix-prompt" <<EOF2
Fix the confirmed findings below in this worktree. For each fix, first write and run a regression test that fails before the fix, then fix it and run relevant tests. Report the failing and passing test evidence in your final response. Follow AGENTS.md. Do not commit, push, open a PR, merge, invoke other agents, or change files outside this worktree. Never touch the live daemon or real sheepdog/herdr configuration, and never print secrets. Do not include real names, absolute home paths, session links, Claude-Session or Co-authored-by trailers in your response or changes. If a finding cannot be reproduced, leave it alone and explain.

$(cat "$TMP/selected.json")
EOF2
if ! python3 "$HERE/agy_run.py" claude-sonnet-5-5-medium "$WT" "$TMP/fix-prompt" "$OUT/audit-fix.log" 1500; then
  note 'Fix skipped: agent did not complete; see audit-fix.log.'; exit 0
fi
if [ "$(git -C "$WT" rev-parse HEAD)" != "$SHA" ]; then
  note 'Fix skipped: agent changed the worktree commit.'; exit 0
fi
if [ -z "$(git -C "$WT" status --porcelain)" ]; then note 'Fix agent made no changes.'; exit 0; fi
BRANCH="audit/$DAY-$SCOPE"
if ! git -C "$WT" switch -c "$BRANCH" || ! git -C "$WT" add -A || ! git -C "$WT" -c core.hooksPath=/dev/null commit -m "fix: audit $SCOPE"; then
  note 'Fix could not be committed.'; exit 0
fi
# Use a bounded, sanitized body; raw agent output stays in the local report.
python3 - "$TMP/selected.json" "$OUT/audit-fix.log.md" "$TMP/body.md" <<'PY'
import json, re, sys
findings = json.load(open(sys.argv[1]))
def clean(s):
    s = re.sub(r'https?://\S+', '[link removed]', str(s))
    s = re.sub(r'(?<!\w)/(?:Users|home)/[^\s"`]+', '[path removed]', s)
    return re.sub(r'(?im)^.*(?:co-authored-by|claude-session|claude\.ai).*(?:\n|$)', '', s)
with open(sys.argv[3], 'w') as out:
    out.write('## Confirmed findings and verification\n\n')
    for f in findings:
        out.write(clean(json.dumps(f, ensure_ascii=True)) + '\n\n')
    out.write('## Fix and test evidence\n\n' + clean(open(sys.argv[2]).read())[:12000] + '\n')
PY
if git -C "$WT" push -u origin "$BRANCH" && gh pr create --repo kccarlos/herdr-sheepdog --head "$BRANCH" --title "fix: audit $SCOPE" --body-file "$TMP/body.md" >"$TMP/pr-url"; then
  note "Fix PR: $(cat "$TMP/pr-url")"
else
  note "Fix PR could not be opened; branch $BRANCH may have been pushed."
fi
