#!/bin/bash
# usage: rejudge.sh ARM   merge ARM's scenario chunks, then the Sonnet run-level and segment rejudge
# (Claude CLI on the Claude plan). The branch binary judges both arms, so the rubric is the same.
S=/private/tmp/claude-501/-Users-kc-Documents-herdr-sheepdog/dda19d26-9a6f-43a3-b88b-66f64e63940c/scratchpad
R=$(cd "$(dirname "$0")" && pwd)
cd "$R" || exit 1
python3 merge.py "$1/merged" "$1"/workday-*
cd "$S/sp/spawn-src" || exit 1
"$S/sp/sd-spawn" bench rejudge --from "$R/$1/merged" --judge claude --judge-model sonnet > "$R/rejudge-$1.log" 2>&1
echo "exit $? $1"
