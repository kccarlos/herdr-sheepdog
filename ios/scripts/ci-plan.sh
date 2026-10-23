#!/usr/bin/env bash
# Plans the iOS CI run (.github/workflows/ios.yml's plan job) from
# scripts/ci-shards.txt. docs/dev/development.md (CI/CD) has the tiers.
#
#   scripts/ci-plan.sh tier < changed-paths     # prints full, smoke or none
#   scripts/ci-plan.sh check [shards] [uitests dir]
#       fails unless every UI test class is in exactly one job and no job
#       mixes iPad and iPhone tests
#   scripts/ci-plan.sh matrix <full|smoke> [shards]
#       prints the jobs as a GitHub Actions matrix (JSON). Exactly one job
#       has "build": true and builds the app for the others: the one the
#       list's "build <job>" line names, or the first job when that one is
#       not in the tier.
#
# Tests: bash scripts/ci-plan_test.sh
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
default_shards="$here/ci-shards.txt"
default_tests="$here/../Tests/SheepdogUITests"

# The changed paths (one per line on stdin) decide what runs on a Mac:
#   full   the app itself, its CI (this script's inputs included) or ios.yml
#   smoke  the daemon's API the app talks to, and its wire goldens
#   none   anything else, including all Markdown
tier() {
  local path result=none
  while IFS= read -r path; do
    case "$path" in
      ''|*.md|ios/docs/*) continue ;;
      ios/*|.github/workflows/ios.yml) echo full; return ;;
      cmd/*|internal/web/*|internal/voice/wsproto/*|internal/termproto/*|testdata/protocol/*|go.mod|go.sum)
        result=smoke ;;
    esac
  done
  echo "$result"
}

# The entries, without comments and blank lines: "job id" per line.
entries() {
  sed -e 's/#.*//' "$1" | awk 'NF { print }'
}

check() {
  local shards="${1:-$default_shards}" tests="${2:-$default_tests}"
  [[ -f "$shards" ]] || { echo "ci-plan: no shard list $shards" >&2; return 2; }
  [[ -d "$tests" ]] || { echo "ci-plan: no UI tests in $tests" >&2; return 2; }
  # Every XCTestCase class in the UI test sources, and every test method.
  local classes methods
  classes="$(grep -rhoE '^[[:space:]]*(final[[:space:]]+)?class[[:space:]]+[A-Za-z0-9_]+[[:space:]]*:[[:space:]]*XCTestCase' "$tests" \
    | sed -E 's/.*class[[:space:]]+([A-Za-z0-9_]+).*/\1/' | sort -u)"
  methods="$(grep -rhoE 'func[[:space:]]+test[A-Za-z0-9_]*[[:space:]]*\(' "$tests" \
    | sed -E 's/func[[:space:]]+([A-Za-z0-9_]+).*/\1/' | sort -u)"
  entries "$shards" | CLASSES="$classes" METHODS="$methods" awk -v file="$shards" '
    BEGIN {
      n = split(ENVIRON["CLASSES"], c, "\n"); for (i = 1; i <= n; i++) if (c[i] != "") known[c[i]] = 1
      n = split(ENVIRON["METHODS"], m, "\n"); for (i = 1; i <= n; i++) if (m[i] != "") meth[m[i]] = 1
    }
    function err(msg) { printf "ci-plan: %s: %s\n", file, msg; bad = 1 }
    $1 == "build" { if (NF != 2 || builder != "") err("want one \"build <job>\" line, got: " $0); builder = $2; next }
    {
      if (NF != 2) { err("want \"<job> <test>\", got: " $0); next }
      job = $1; id = $2
      if (job !~ /^(smoke|shard-[0-9]+)$/) err("job " job " must be smoke or shard-<n>")
      k = split(id, p, "/")
      # One simulator per job: the iPad classes get jobs of their own.
      dev = (k >= 2 && p[2] ~ /^IPad/) ? "ipad" : "iphone"
      if ((job in jdev) && jdev[job] != dev) err("job " job " mixes iPad and iPhone tests (" id "); one simulator per job")
      jdev[job] = dev
      if (p[1] == "SheepdogKitTests") {
        if (k != 1 || job != "smoke") err("SheepdogKitTests runs whole, in smoke")
        kit = 1; next
      }
      if (p[1] != "SheepdogUITests" || k < 2 || k > 3) { err("unknown test " id); next }
      if (!(p[2] in known)) { err(id ": no class " p[2] " in the UI tests (renamed or removed?)"); next }
      if (k == 2) {
        whole[p[2]]++; wjob[p[2]] = wjob[p[2]] " " job; wone[p[2]] = job
      } else {
        if (!(p[3] in meth)) err(id ": no test method " p[3])
        nm++; mcl[nm] = p[2]; mjob[nm] = job; mname[nm] = id
      }
    }
    END {
      if (!kit) err("SheepdogKitTests is in no job; smoke runs the unit tests")
      if (builder != "" && !(builder in jdev)) err("build " builder ": no such job")
      for (cl in known) {
        if (!(cl in whole)) err(cl " is in no job: add \"shard-<n> SheepdogUITests/" cl "\"")
        else if (whole[cl] > 1) err(cl " is in more than one job:" wjob[cl])
      }
      for (i = 1; i <= nm; i++) if (wone[mcl[i]] == mjob[i]) err(mname[i] " is in the same job as its class")
      exit bad
    }
  ' || return 1
  echo "ci-plan: ok ($(echo "$classes" | grep -c .) UI test classes)"
}

matrix() {
  local which="${1:?full or smoke}" shards="${2:-$default_shards}"
  [[ "$which" == full || "$which" == smoke ]] || { echo "ci-plan: matrix full|smoke" >&2; return 2; }
  entries "$shards" | awk -v which="$which" '
    $1 == "build" { want = $2; next }
    which == "smoke" && $1 != "smoke" { next }
    {
      if (!($1 in seen)) { seen[$1] = 1; order[++jobs] = $1 }
      n++; ejob[n] = $1; eid[n] = $2
    }
    function add(dev, j, arg) {
      if (dev == "ipad") ipad[j] = ipad[j] (ipad[j] == "" ? "" : " ") arg
      else iphone[j] = iphone[j] (iphone[j] == "" ? "" : " ") arg
    }
    END {
      for (i = 1; i <= n; i++) if (split(eid[i], p, "/") == 2) whole[p[2]] = 1
      for (i = 1; i <= n; i++) {
        j = ejob[i]; k = split(eid[i], p, "/"); cl = p[2]
        # A method whose whole class runs in another job of this tier is
        # left to that job.
        if (k == 3 && (cl in whole)) continue
        if (k == 2 && cl == "HistoryApprovalUITests") { history[j] = 1; continue }
        if (k == 2 && cl == "ReconnectUITests") { reconnect[j] = 1; continue }
        dev = (cl ~ /^IPad/) ? "ipad" : "iphone"
        add(dev, j, "-only-testing:" eid[i])
      }
      builder = (want in seen) ? want : order[1]
      printf "{\"include\":["
      for (o = 1; o <= jobs; o++) {
        j = order[o]
        printf "%s{\"name\":\"%s\",\"iphone\":\"%s\",\"ipad\":\"%s\",\"history\":%s,\"reconnect\":%s,\"build\":%s}", \
          (o > 1 ? "," : ""), j, iphone[j], ipad[j], (history[j] ? "true" : "false"), (reconnect[j] ? "true" : "false"), (j == builder ? "true" : "false")
      }
      print "]}"
    }
  '
}

cmd="${1:-}"
shift || true
case "$cmd" in
  tier) tier ;;
  check) check "$@" ;;
  matrix) matrix "$@" ;;
  *) sed -n '2,11p' "${BASH_SOURCE[0]}" >&2; exit 2 ;;
esac
