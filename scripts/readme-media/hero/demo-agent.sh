#!/usr/bin/env bash
# A scripted demo agent for the hero video's desk shots: prints the iOS
# demo world's story for one agent (auth, billing, migrate or docs) and
# reports its state to herdr (`herdr pane report-agent`), so herdr's own
# sidebar shows it working, then idle. No model and no real agent runs.
#
#   demo-agent.sh NAME working|done
set -u
name=$1 mode=$2
pane=${HERDR_PANE_ID:-$(herdr pane current 2>/dev/null | sed -n 's/.*"pane_id": *"\([^"]*\)".*/\1/p' | head -1)}
case $name in auth | migrate) kind=claude ;; *) kind=codex ;; esac
report() { [ -n "$pane" ] && herdr pane report-agent --source hero-demo --agent "$kind" --state "$1" "$pane" >/dev/null 2>&1; }
dim=$'\e[2m' bold=$'\e[1m' green=$'\e[32m' red=$'\e[31m' cyan=$'\e[36m' off=$'\e[0m'
say() { printf '%s\n' "$1"; sleep "${2:-0}"; }

clear
printf '\e[?25l' # no cursor
printf '%s%s%s  %s(%s, scripted demo agent)%s\n\n' "$bold" "$name" "$off" "$dim" "$kind" "$off"
case $name in
auth) task="Fix the login redirect loop" ;;
billing) task="Add retries to the payment webhook" ;;
migrate) task="Run the database migration on the staging copy" ;;
docs) task="Update the install section of the README" ;;
esac
say "${cyan}>${off} $task"
say ""
report working
if [ "$mode" = working ]; then
	case $name in
	auth) for l in "● Read app/session.ts" "● Read test/login.test.ts" "● The session cookie is set on /login, not /." "● Edit app/session.ts" "● Run npm test -- auth"; do say "$l" 2.2; done ;;
	billing) for l in "● Read src/webhook.ts" "● Read test/webhook.test.ts" "● Webhook calls fail on a flaky provider." "● I'll add p-retry for the retries." "● Planning the change…"; do say "$l" 2.6; done ;;
	migrate) for l in "● Read db/migrations/0042_add_user_plan.sql" "● Snapshot the staging copy" "\$ make migrate-staging" "Applying 0042_add_user_plan…"; do say "$l" 3; done ;;
	docs) for l in "● Read README.md" "● Check the Homebrew formula" "● Edit README.md: Install with Homebrew" "● Edit README.md: Build from source"; do say "$l" 3; done ;;
	esac
	sleep 3600
fi
# done: the story's end, all at once (the closing shot).
case $name in
auth) for l in "● Read app/session.ts" "● Edit app/session.ts: cookie path /" "● Edit test/login.test.ts: regression test" "\$ npm test -- auth" "${green}✓ 42 passed${off}" "" "Done: the login redirect loop is fixed."; do say "$l"; done ;;
billing) for l in "● I'll add p-retry for the retries." "\$ npm install p-retry   ${dim}(approved from the phone)${off}" "added 1 package" "● Edit src/webhook.ts: 3 retries, backoff" "\$ npm test -- webhook" "${green}✓ 2 passed${off}" "" "Done: the webhook retries three times."; do say "$l"; done ;;
migrate) for l in "\$ make migrate-staging" "Applying 0042_add_user_plan…" "${red}ERROR: column users.plan already exists${off}" "Rolled back. No changes were made." "" "Needs a decision: drop the column first," "or mark 0042 as applied."; do say "$l"; done ;;
docs) for l in "● Edit README.md: Install with Homebrew" "● Edit README.md: Build from source" "● Checked every command in the section" "" "Done: the install section covers both paths."; do say "$l"; done ;;
esac
report idle
sleep 3600
