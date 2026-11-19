# Engine v2: independent review 2

2026-11-18. Same reviewer as [review-1.md](review-1.md); read-only. Read this round: design.md sections 0, 3, 4, 6, 7.1, 7.2, 9.3, 13 and 17.6 in full, the rest by grep; prompts.md in full; plan.md sections 2 to 4; the origin check in `internal/web` (`sniff.go`, `auth.go`, `pair.go`, `proxy.go`). No sub-agents this round. Rule ids are the renumbered ones.

**Verdict: CHANGES, narrowly.** Every round-1 blocker and major is fixed in the text except the size plan, which is partly fixed. The new parts add one blocker (the page-origin proof) and three majors. All four are text changes of a few lines. A third full review is not needed: a check of those four diffs is enough.

Not checked: sections 5, 8 to 12 and 14 to 16 line by line; the knowledge-base "Maps to" column after the renumbering; `notes/renumber.tsv` against every reference; no template was run.

## 1. Round-1 findings

| Finding | State | Evidence |
|---|---|---|
| B1 owner accept not bound to a commit, delegable | **Fixed** | 3.1: "the owner's yes to a gate and the owner's override all use it [covers]"; 7.1: "Never delegable: an override…"; 7.1: "An ask about a task stores the commit it was asked at"; N31 "no yes covers `H`"; rows N15–N19; 10.1 rail now reads "unless the owner overrode that very commit". Walked again in section 2 |
| B2 `factory set` trusted a terminal | **Fixed** in 7.2 ("A terminal is never taken for the owner") and plan.md (no shortcut in version 1). Reopened another way by NB1 below |
| M0 red main | **Fixed.** N38: after `main_red_minutes` the oldest held task's worker is told to merge the base, and the coordinator is woken with the exit |
| M1 report tag | **Fixed.** 9.3 and 7.2: the tag carries the run's one-time code; the templates carry `{nonce}` |
| M2 two reviews | **Fixed.** L8: "fewer than `review_rounds` passing rounds cover `H`" |
| M3 worker at a dialog | **Fixed.** R29 ends a `blocked` run after `blocked_minutes`; J10 for the coordinator |
| M4 retro that cannot run | **Fixed.** L10 has "the retro has not failed to run"; L11 follows |
| M5 two deciders | **Fixed.** N24 (owner first, no recap yet), then N25 |
| M6 dead ends at old N29/N31 (now N35/N37) | **Fixed.** N35 names three exits; doctor refuses a repo with no PR workflow or a required approving review (plan PR 12) |
| M7 import cycle | **Fixed.** `plan` is a leaf; no `rules` package |
| M8 size | **Partly.** The full figure is now 8,500 to 10,000. Version 1 at "about 4,000" does not agree with section 13's own budgets: see NM3 |
| M9 two homes, hand-kept lists | **Fixed in text.** Stage and wake are row fields; the lists are printed and checked in `go test` (4.11) |
| M10 missing version 1 work | **Fixed.** Report endpoint and run tokens PR 4; `AnswerProof` and the supervisor PR 11; doctor PR 12; replay evidence PR 13 |
| M11 no client work behind PR 14 | **Fixed.** Version 1 uses the existing phone cards; W1 to W3 are a separate track, web first (D14) |
| M12 hc gaps | **Fixed.** S0.3, S0.6, S0.8 |
| m1, m2 landing order | **Fixed.** N39 (worker working) now sits above N40 and N41 |
| m3 pool and sign-in loops | **Fixed.** 7.3: unusable "until the reset time it printed, or for `pool_minutes`"; 4.8: a disputed sign-out "counts as a restart" |
| m4 to m8 | **Fixed** (accept wording in 3; ids in table order; "told once per alert id, whoever is alive"; the exit test has red main, a usage limit and a silent reviewer) |
| Weak rows of section 1 | **Partly, as 17.6 says.** Fixed where seen: N13/N14 on a passed head, working time, plan amend voids rounds, slot check on restart, J4, `no_pool` scope, pool windows, KB-ASK-9. Left to builders: the rest of review-1 section 1 |

## 2. The new parts

### The override (4.3b, N15–N19, L1): safe, with three small holes

Walked again:
- Card made at c2; worker reports ready at c3; owner says yes. The override is "at the commit the override card named", c2; it does not cover c3; N15–N19 and L1 do not hold; the task goes on by the normal rules. Fixed.
- No round yet, owner says yes: merged by override, and 10.1 now says so. That is the owner's decision D10, not a hole.
- A delegation rule: `override` is never delegable; `not_delegable`. Fixed.
- Order: N3 (parked), N4 (PR closed), N5, N7 (diverged, or changes the engine did not push) all sit above N15, so an override never merges a rewritten or foreign head. A conflict is N16; the hand-resolved merge is `changes`, so it is not covered. Good.

Holes (minor each):
- m1. **The checks shown on the card are not bound.** D10: "red CI may be merged if the card names it". The card showed green; checks turn red before the tap; N19 has no checks condition and merges red. Fix: the override record keeps the checks state shown, and an answer is refused as stale when a listed task's state is worse than shown.
- m2. **"The commit it had" is not defined as `H`, and nothing says the engine fills it.** If the coordinator can supply commits, or the card lists a task with no ready report (`H` is the base commit), the card is wrong. Against the new ready proofs: N15–N19 sit above N20–N23, so a task whose tip is ahead of `H`, or whose last `ready` was refused for a missing proof, still merges at `H`, the last commit that did pass `ready`; the unreported work is left out. That is safe, but the card should say "has unreported work" beside such a task. One sentence: the engine fills each commit with `H`; a task with no ready report cannot be listed; the card marks unreported work.
- m3. A behind PR on a repo that requires up-to-date branches: N19's merge is refused, N18 retries, then wakes the coordinator. No override row updates the branch (N40 is below). And the owner's result line reports "not merged" only for a conflict, not for "a newer commit" or "GitHub refused".
- m4. A gate ask for c2 stays open after `H` moves to c3 with changes. N31's first clause then holds on the stale ask; the owner answers it, the yes does not cover c3, and N31 asks again. Cancel a gate ask when its commit stops covering `H`.

### NB1 (blocker). The page-origin proof is not proof against a local agent (7.1 "The owner's proof")

7.1: "…or from the daemon's own factory page on this computer under the same origin check the setup page uses today. A bare request from the machine itself is not proof, because agents run as the owner's user and can make one."

The check the setup page uses, read in the code:
- `internal/web/setup.go:137-142` (`localOnly`, the setup routes' own wrapper): a loopback client on a loopback host name, and for a change "Origin absent or equal", as JSON, "so another website open in the same browser cannot post to it". An absent `Origin` passes.
- `internal/web/auth.go:122`: a request from loopback with no ticket, not proxied and with Funnel off, is `localDevice`. No secret is involved.
- `internal/web/sniff.go:154-158` (`crossOrigin`): `if o == "" { return false }`. A request with **no** `Origin` header passes. The comment says why: "The app sends no Origin". `pair.go:150` says the same of `Sec-Fetch-Site`: "A client that sends no such header (the CLI, the panel, curl) is not a browser page and may ask."
- `guard` (`sniff.go:191`) also needs a loopback `Host`. That stops DNS rebinding.

So the check stops **another website in the owner's browser**. It does not tell the page from `curl`: a worker's shell can POST to `127.0.0.1` with no `Origin`, or with the daemon's own origin typed in, and gets `localDevice`. "From the factory page under the origin check" and "a bare request from the machine itself" are the same request. History: a worker told its PR waits on review runs one `curl` to the answer route for an open override card, or to a `rule` card setting `review_rounds` to 1. That is round 1's B1 and B2 again, one step less obvious than `factory set`.

**Today's code already has the stronger rail, and the new sentence removes it.** `internal/supervisor/factory.go:478-480` (`mintAnswerProofLocked`): a tap whose device is empty or `"local"` is refused with "ticketless local tap cannot authorize factory ask"; any input that is not a tap must be live voice (`in.kind != InputVoice` is refused), so a typed utterance cannot answer either. The setup routes add nothing a local process cannot meet (`localOnly`, above).

Fix (one sentence in 7.1): keep today's rule. Ticketless loopback is never the owner. Two options for the page, in order of size: (a) never-delegable kinds (override, rule change, paid key, quota reset) are answered only from a paired device or live voice, and the page shows them read-only on this computer; (b) the page may answer when the browser holds a session ticket (the `sheepdog_session` cookie, `auth.go:25`). I did not check that the web client can enrol as a device on loopback, so (b) needs a spike; (a) needs none.

State the threat model beside it: this stops an agent taking a shortcut, not a hostile process as the same user. Such a process can also read `GET /api/pair` on loopback and enrol its own device, or edit `state.json`. The cheap detective control is one the design already has: every override and rule change is in the log and the owner's digest with the device that answered.

Version 1 is not exposed **if PR 11 keeps the guard at `factory.go:478`** when the supervisor moves to the `Engine` interface (the old code refuses a local tap; no factory page exists until W2). Make "a ticketless local answer to a gated ask is refused" a done-when line of PR 11. One thing in the old path I did not trace: a voice answer with no paired ticket is recorded as `voice-session` (`factory.go:57`); whether a local process can feed the voice path was not checked.

### The prompts (prompts.md): the right shape, small, three mismatches

Good: the three layers; fields over prose; 40 lines per template; a source per rule sentence; the five prompt-only rails are named (section 9). Sizes as drafted: coordinator about 38 text lines, reviewer about 21, plan reviewer about 18, worker about 41 (already at the limit as a first draft).

- **NM1 (major). The reviewer's tag cannot satisfy 7.2.** 7.2 `verdict` is valid only when "every outcome is judged 'the check proves it' or not". The tag of 5.4 has `code, round, head, verdict, majors, findings, self_check_misses, class, deletable, not_run` and no per-outcome field. Read literally, every version 1 verdict is `invalid_report`, spends `reviewer_retries` and ends at N5. N42 marks the per-outcome clause "from the evidence increment on"; 7.2 does not. Fix one side.
- **NM2 (major). A worker that rejects every finding has no path.** The worker template: "fix it, or reject it with one line of why in your commit message". The reviewer template and its facts block: "the worker's rejections". But `ready` is valid only when "the commit is ahead of `H`". Reject all, no new commit: `ready` is refused `nothing_new`; L12 still says fix; N22 has told once; the turn ends; one nudge; then the coordinator. With two passing rounds required this will be common. Fix: `ready --reject "<why>"` is valid at `H` with no new commit and is a record after which the ladder says review; the next reviewer gets the rejections from that record.
- m5. Where outcomes and proofs land is told three ways. prompts.md section 8: outcomes with `proved_by` and proofs on `ready` are version 1. plan.md "left out": the same, version 1. plan.md increment 1: "Evidence: outcomes on tasks…". plan PR 1's checks list "shape, the four scope fields, size" with no outcomes. Name the PR.
- m6. The `ready` record of section 3 (`ready{head, outcome → check}`) has no output, `touched` or `skipped`; and `--proof <outcome>="<command>"` carries a command but 7.2 wants "the command it ran and the output". Say whether the client runs the command and captures the output, or the worker pastes it.
- m8. 7.1 says "Workers and reviewers open only `decision` asks", but the 7.2 setting-change row lets `sheepdog factory set` "from a terminal" raise a `rule` ask, and a worker has a terminal. The answer still needs proof, so this is noise for the owner, not a hole: refuse it from a run's token, or count it.
- m9. The plan reviewer's report and the coordinator's answer to each finding have no line in 7.2. Due with increment 1a.
- m7. The version 1 coordinator template tells it "A reviewer will ask what can be deleted"; plan review is increment 1a.

## 3. New blockers and majors

| # | Rank | What |
|---|---|---|
| NB1 | blocker | The page-origin proof equals ticketless loopback; any local agent passes it (above) |
| NM1 | major | 7.2 `verdict` and the reviewer tag disagree; every verdict would be invalid |
| NM2 | major | No acceptance line for a worker that rejects all findings |
| NM3 | major | Version 1's size and PR split disagree with section 13 (below) |

## 4. Is version 1 now the smallest that runs end to end?

**The scope is close to right; the numbers are not.**

- Right: the thin slice with a first real merge at PR 6; one worker CLI and one reviewer CLI; evidence, retro, rotation, settings changes, sweep, probe, merge queue and background jobs moved out; doctor refuses what would dead-end.
- Not needed to run end to end, kept by the owner's decision: the override (PR 9, about 250 lines and one exit-test step). It could be the first increment. So could the recap limits N13 and N14 (N12, the size cap, is the one that matters first).
- **NM3.** "About 4,000 lines" in 14 PRs of at most 300 does not follow from section 13, which the same revision wrote. By its own bases (my arithmetic on the design's estimates, not measurements): PR 3 is 36 run rows at about 10 lines, the alive table and `Resolve`, about 600. PR 5 is two adapters ("adapters and trust pre-setting 900" for three), start, send, stop ("400"), the observer, the watcher and the tag reader: about 900. PR 6 is git ("400"), gh ("800" full), the rollup reader and four verbs: about 700 even thin. PR 7 is about 26 rows and the recap ladder at 20 lines: over 500. Version 1 is nearer 6,000, in about 20 PRs. The plan's "stop at 1.5 times" rule would fire at PRs 3, 5, 6 and 7. Re-split now; do not let the first builder find out.
- The exit test asks for "one override of two stacked PRs", and version 1 has only the supervisor's existing cards to answer it with. Check in S0 that the supervisor's generic card (one line, 2 to 9 options) can show two tasks with their commits and check states.

## 5. What to change before code (all text)

1. 7.1: ticketless loopback is never the owner (today's rule, `supervisor/factory.go:478`); pick option (a) or (b) for the page; state the threat model; add the done-when line to PR 11 (NB1).
2. 7.2 or prompts 5.4: make the verdict fields the same on both sides for version 1 (NM1).
3. 7.2 and prompts 5.3: a `ready --reject` line, and where the reviewer's "rejections" come from (NM2).
4. plan.md section 3: re-split version 1 against section 13's budgets; say which PR adds outcomes and proofs (NM3, m5).
5. 4.3b: the engine fills each commit with `H`; the override keeps the checks state shown; cancel a stale gate ask (m1, m2, m4).
