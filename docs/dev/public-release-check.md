# Public release check (2026-10-14)

A full-history scan of `herdr-sheepdog` and `herdr-sheepdog-ios` before the repos go public (step 1 of [plan-onboarding-and-presentation.md](plan-onboarding-and-presentation.md)). This is a report only: no history was rewritten and nothing was rotated. No secret value is copied here; findings give the file, the commit and at most a short masked prefix.

## Method

- **gitleaks 8 (`gitleaks git --log-opts=--all --redact`)** over every commit on every branch and remote ref: 496 commits in the daemon repo and 112 in the iOS repo.
- **A pattern scan of `git log -p --all`** (added lines) for:
  - Google API keys (`AIza…`), `sk-…` keys (OpenAI, DeepSeek, OpenRouter), GitHub tokens, Slack tokens, AWS keys, private-key blocks and bearer tokens;
  - `token|api_key|secret|password = …` assignments and `?token=` in URLs;
  - IPv4 addresses, `*.ts.net` hosts, e-mail addresses and the GCP project id.
- **Filenames ever added:** `.env`, `*.p8`, `*.pem`, `*.p12`, `*.key`, `config.toml`, audio and log files.
- **The binary `sheepdog`, once committed (see below):** its strings were scanned with the same patterns.
- **Commit metadata:** author and committer identities, and links in commit messages.

## Summary

**No live credential was found in either repo's history.** The CEO does not have to rotate anything because of the history. The privacy items below are the CEO's call before the repos go public. The three to decide first are:
- the session links in commit messages;
- the personal e-mail in `docs/dev/signing.md`;
- the GCP project id.

## Secrets

| # | Finding | Where | Risk | Action |
|---|---|---|---|---|
| S1 | gitleaks `generic-api-key`, 44 hits | `testdata/bench/openings.json` (`cache_key` fields, 25 hits, e.g. f1565294), `docs/dev/bench/results/2026-10-03/calibration.json` (`key` fields, 19 hits) | None: these are 24-character content hashes used as cache keys, not credentials | None. Optionally add a `.gitleaksignore` so a future CI scan stays quiet |
| S2 | Test fixtures shaped like keys: `AIzaEnvF…` (`cmd/sheepdog/serve_test.go`), `sk-t…` (`internal/app/voice_test.go`), `sk-abc…` and `ghp_ab…` (`internal/eval/extract_test.go`, a redaction test), `s3cr…` (`internal/web/auth_test.go`) | tests | None: made-up values that test redaction and auth | None |
| S3 | `token = "pick…"` | `README.md` | None: the placeholder `pick-something-long-and-random` | None (the README rewrite can point to `sheepdog init` instead) |
| S4 | `.env.example` | root | None: every value is empty | None. `.env` itself is gitignored and was never committed |
| S5 | The daemon web token | not in history | The live token is set outside the repo (config file / env); no `token = …` with a real value and no `?token=` URL with a real value was ever committed | The plan still asks for a longer token before public use: replace it when the phone is next re-paired (the onboarding `init` keeps an existing token on purpose) |
| S6 | The iOS CI secrets (`APP_STORE_CONNECT_API_KEY_P8`, key id, issuer id) | `.github/workflows/*` in the iOS repo | None: referenced only as `${{ secrets.… }}`; no `.p8` file and no key id value is committed | None |
| S7 | `ci-runners.sh` reads `github-webhook-secret` | `infra/ci-runners/ci-runners.sh` | None: the name of a Secret Manager secret, fetched at run time | None |

## Privacy and hygiene

| # | Finding | Where | Risk | Action |
|---|---|---|---|---|
| P1 | Session links (`claude.ai/code/session_…`) in commit messages: 520 matches in the daemon repo and 119 in the iOS repo; one private artifact link in a tracked file | commit trailers in most commits since the start; `docs/dev/handover-2026-10-04.md` (labelling page link) | Low to medium: the links need the owner's login, but they reveal the account's session ids and invite questions; the CEO said the public repos should carry none | Decide: (a) accept them (history is kept per the CEO, 2026-10-13), or (b) rewrite only the messages with `git filter-repo --message-callback` before publishing. Rewriting changes every hash and breaks open PR branches. Remove the artifact link from the handover in a normal commit either way |
| P2 | A personal e-mail address `this…@gmail.com` (it contains part of a personal name) | Daemon `docs/dev/signing.md` (added in 69b61ab, removed at HEAD by #86); iOS `docs/xcode-cloud.md` and `project.yml` with the legal name (removed at HEAD by iOS #50) | Medium: personal contact, spam | Done at HEAD (2026-10-14); it stays in history unless rewritten |
| P3 | A second personal e-mail as commit author: `akc…@gmail.com` on 5 daemon commits and 1 iOS commit; all others use the GitHub noreply address | commit metadata | Low: visible in public history | Accept, or rewrite with a `.mailmap` (display only) or `filter-repo --mailmap` (rewrite) |
| P4 | The GCP project id `project-f6d4…` | 7 commits from 5d8ec20 on: `docs/dev/handover-2026-10-04.md`, `docs/dev/eval/fast-brain-facts-2026-10-04/reports/facts-old-r1.md`, and error strings in `docs/dev/bench/results/2026-10-11-t1-gemini/**` and `docs/dev/bench/results/2026-10-11-t2/rl/**` (events, runs, reports) | Low: a project id is not a credential, but it names the project that had a Trust & Safety flag and invites probing | Accept, or replace it with `<gcp-project>` in the handover in a normal commit (the bench logs are generated data; leave them) |
| P5 | LAN addresses: `192.168.0.33` (the Mac, in DeepSeek connection-error strings in bench event logs, from d45476d) and `192.168.8.141` (an earlier LAN address, in docs, tests and defaults of both repos, from 100e533 / iOS 12671e8); also `192.168.1.20`, `100.64.x` / `100.101.102.103` (examples), `3.173.21.63` (a DeepSeek server) | docs, tests, `docs/dev/bench/results/**/events/*.jsonl` | Low: private addresses are not reachable from the internet; together with P2 they hint at a home network | Optional: replace `192.168.8.141` in docs and app defaults with a documentation example (`192.168.1.20`) in a normal commit |
| P6 | A 30 MB `sheepdog` binary in the repo root | added in 0bc12a5 (#166), removed in 44d30d7 (#204); still reachable from several eval branches | Low for privacy (no keys in its strings; it holds `/Users/kc/…` build paths). Hygiene: 30 MB of history in every clone | Accept, or drop it with `filter-repo --path sheepdog --invert-paths` together with P1 if history is rewritten. Delete the stale eval branches that still contain it |
| P7 | `/Users/kc/...` paths and the Apple team id `GV776BZVA8` | tests, docs, the iOS `project.yml` | None: a team id is public in every signed app; the paths show only the username | None |
| P8 | Voice transcripts | `docs/dev/bench/results/**/events/*.jsonl`, `docs/dev/research/live-resume-context-results/**`, `testdata/**` | Low: every committed event log found comes from the bench (simulated user, `say` voices, synthetic fixtures). No raw log of the live daemon (`~/.local/share/sheepdog`) was ever committed; the live data appears only as counts (e.g. `docs/dev/bench/results/2026-10-13-iter7/live-counts.txt`) | The CEO skims for paraphrased quotes of their own sessions in `docs/dev/handover-2026-10-04.md`, `docs/dev/history/*.md` and `docs/dev/eval/*.md` (the plan asks for this skim). Nothing found by the scan needs removal |

## Before making the repos public (checklist for the CEO)

1. Decide P1 (session links): keep, or one coordinated `filter-repo` rewrite (messages + P6), done once, after all open PRs are merged or closed.
2. Normal commits on main: remove the e-mail in `docs/dev/signing.md` (P2, both repos) and the artifact link in the handover (P1). Optionally also the project id in the handover (P4) and `192.168.8.141` (P5).
3. Delete stale branches that will not be merged (they carry P6 and old results).
4. Replace the daemon web token with a long one and re-pair the phone (S5).
5. Optional: run gitleaks in CI with a `.gitleaksignore` for S1.
