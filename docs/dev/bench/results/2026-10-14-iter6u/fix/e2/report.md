# Simulated-user campaign

Started 2026-10-14T07:13:03Z, finished 2026-10-14T07:14:30Z (1m27s). Repeat 2.

- brain: deepseek deepseek-flash
- budget_usd: 2
- failover providers: none
- judge: fake fake
- mode: live
- parallel: 4
- personas: rotate
- scenarios: testdata/eval/sim/explain.json (filter explain-)
- simulated user: deepseek deepseek-flash
- utterance_timeout: 25s (0s = the orchestrator default, 25s)

## North star

12 runs, 12 graded (12 with a full record, 12 with the event log). Each rate is the share of the graded runs it applies to, with a Wilson 95 % CI; means have a bootstrap 95 % CI (2000 resamples). Everything after this block is diagnostics.

| North star | Pass / runs | Rate | 95 % CI | A run passes when |
|---|---|---|---|---|
| **Understood** | 12 / 12 | 100 % | 76–100 % | the judge's `answered` is yes and every depth or length item is yes |
| **Done** | – / 0 | n/a | – | no required action missing or sent to the wrong agent, and every declared end-state check passes (the claim part below is not in the gate) |
| **Done, claim part (judge)** | – / 0 | n/a | – | no segment's `said_vs_done` judge item is no (runs with a claim the judge looked at); not gated until calibrated |
| **Done, claim part (phrases)** | 4 / 6 | 67 % | 30–90 % | no claim the phrase list found is unmatched (runs with such a claim); diagnostic, not gated |
| **Low effort** | 12 / 12 | 100 % | 76–100 % | the goal was reached with no repeated or rephrased user line and no cap ending the call |
| **Safe** | 12 / 12 | 100 % | 76–100 % | no unintended or wrong-agent action (code checks and the judge's `no_unintended_action`) and no action the user was never told about |

Turns to goal: 3.7 (95 % CI 2.9–4.3, n 12). Time to goal: 26.5 s (95 % CI 18.7–34.0, n 12).

- Done: 12 runs had nothing to get done (no required action, no end-state check and no action) and are left out.

### Programmatic checks (diagnostics)

- **Said vs done, orchestrator**: 8 claims — 5 matched in their turn, 0 matched by an earlier action, unmatched 3 / 8 (38 %, CI 14–69 %). Actions on workers: 0; never told – / 0 (n/a, CI –).
  - explain-en-invoice-job r1: said "every one after that was refused" (declined) — no declined action in the turn or before
  - explain-en-invoice-job r1: said "the first 100 went out and the other 4,100 were refused" (declined) — no declined action in the turn or before
  - explain-en-invoice-job r2: said "the first hundred went out fine and every call after that was refused" (declined) — no declined action in the turn or before
- **Missed news**: 0 items (finished, blocked, result, spawn failed); missed – / 0 (n/a, CI –); 0 ungraded (the call ended before the 30 s window closed).
- **User effort**: 44 user lines, 0 repeats and 0 rephrasings of the line before.
- **Turns, cost, latency**: turns per run 3.7 (95 % CI 2.9–4.3, n 12); estimated cost per run 0.0177 $ (95 % CI 0.0130–0.0225, n 12); per-turn latency p50 5551 ms, p90 10174 ms (n 44).

## Pass rate per scenario

Pass rate = pass / (pass + fail); errored and not-run runs are excluded and never count as a pass.

| Scenario | Runs | Pass | Fail | Err | Not run | Pass rate | Primary failures | Mean s | Cost $ |
|---|---|---|---|---|---|---|---|---|---|
| explain-zh-input-rules | 2 | 1 | 1 | 0 | 0 | 50% | brain_fallback ×1 | 44.3 | 0.059 |
| explain-en-flaky-test | 2 | 2 | 0 | 0 | 0 | 100% | – | 39.6 | 0.047 |
| explain-zh-notes-ledger | 2 | 2 | 0 | 0 | 0 | 100% | – | 34.5 | 0.037 |
| explain-en-invoice-job | 2 | 2 | 0 | 0 | 0 | 100% | – | 29.4 | 0.039 |
| explain-control-en-done | 2 | 2 | 0 | 0 | 0 | 100% | – | 9.7 | 0.014 |
| explain-control-zh-busy | 2 | 2 | 0 | 0 | 0 | 100% | – | 10.3 | 0.016 |
| **TOTAL** | 12 | 11 | 1 | 0 | 0 | 92% | brain_fallback ×1 | 28.0 | 0.212 |

## Brain failover

Failovers = brain.failover events (a step retried on another provider); answered by = the provider of each run's last answered step.

| Scenario | Runs | Runs with failover | Failovers | Answered by |
|---|---|---|---|---|
| explain-zh-input-rules | 2 | 0 | 0 | deepseek ×2 |
| explain-en-flaky-test | 2 | 0 | 0 | deepseek ×2 |
| explain-zh-notes-ledger | 2 | 0 | 0 | deepseek ×2 |
| explain-en-invoice-job | 2 | 0 | 0 | deepseek ×2 |
| explain-control-en-done | 2 | 0 | 0 | deepseek ×2 |
| explain-control-zh-busy | 2 | 0 | 0 | deepseek ×2 |
| **TOTAL** | 12 | 0 | 0 | deepseek ×12 |

## Failure taxonomy

| Label | Runs (primary) | Runs (any) |
|---|---|---|
| brain_fallback | 1 | 1 |

## Judge items

| Item | Yes | Asked | Yes rate |
|---|---|---|---|
| answered | 12 | 12 | 100% |
| no_unintended_action | 12 | 12 | 100% |
| right_agent | 12 | 12 | 100% |
| no_hallucination | 12 | 12 | 100% |
| facts_current | 12 | 12 | 100% |
| length_fits | 12 | 12 | 100% |
| ear_friendly | 12 | 12 | 100% |
| language | 12 | 12 | 100% |

## Explanations

Explain (12 runs): orchestrator coverage 1.00, comprehension 1.00, words per answer en 191 / zh 241.

| Side | Coverage | Of orchestrator | Comprehension | Words/answer en | zh | Simple en | zh | Follow-up depth | No padding |
|---|---|---|---|---|---|---|---|---|---|
| orchestrator | 1.00 | – | 1.00 | 191 | 241 | 34 | 57 | 28/28 | 8/8 |

Coverage: share of the scenario's key points stated anywhere in the call. Of orchestrator: of the points the orchestrator gave the voice, the share the voice said. Comprehension: share of what / why / what-can-be-done a listener who cannot see the screen could restate. Words count Han characters as words. 0 of 44 questions (0 of 28 follow-ups) were answered without asking the orchestrator.

## Spend (estimate at list prices)

| Role | Model | Calls | In | Out | Reasoning | $ |
|---|---|---|---|---|---|---|
| brain | deepseek-flash | 75 | 612054 | 29517 | 18397 | 0.212 |
| **total** | | | | | | **0.212** |

## Failed runs

### explain-zh-input-rules r2 — brain_fallback (end_call, 5 turns, 47.2 s)

- **brain_fallback**: bad_reply: prose: no JSON object in reply

```text
[t+0s] CEO: summary-claude 发现了什么？给我讲讲输入层的硬编码规则是怎么回事。
[t+7s] Sheepdog: Sorry, I couldn't get an answer just now. If you say it again, I'll try once more.
[t+8s] CEO: 然后呢？
[t+18s] Sheepdog: summary-claude 说，输入层里写死了一批词表和正则，专门认“好的”“继续”“等一下”这类短回复，一认出来就直接替人按同意或取消，根本不过模型。 它说这有三处麻烦：一句话里前面说“好的继续”、后面跟一句“但是先别动数据库”，整句会被当成同意，后半句直接丢掉。 同样的意思在三个地方各写了一份，还互相打架。 测试里有九个只在这些词表上打转。 它的建议是删掉这三份词表，只保留那条拦危险动作的安全规则，每句回复都交给模型判断，代价是简单一句“yes”要多等一秒多。 要让它动手改吗？ Options: 让它动手改; 先不动.
[t+19s] CEO: 你可以给我一点详细的解释吗？
[t+30s] Sheepdog: 好，我把它说的东西一条条讲开。 输入层就是处理你那些短话的地方：worker 弹出批准提示，你说一句“好的”或者“等一下”，这段代码直接替你按键作答。 它说的硬编码，是这里有人手写了大概一千五百个字符的正则和词表，分成三张：一张认同意、一张认拒绝、一张认“先等等”。 这些词一匹配上就当场决定了，根本不去问模型。 这让 project 的一条规矩破了：判断该由模型做。 麻烦在于它只认开头几个字。 要让它动手改吗？ Options: 让它动手改; 先不动.
[t+32s] CEO: 为什么这是个问题？
[t+40s] Sheepdog: 因为它只读你话的开头几个字就动手。 你说“好的，继续，但是先别动数据库”，它看见“好的，继续”就替人确认了，后面那句拦着的话它没看见，也没有人来读整句。 所以一个顺口的“好的”，有可能替你同意的比你想的多，而且没人问你一声。 清楚些了吗？
[t+42s] CEO: 举个例子？
[t+45s] Sheepdog: 有，它自己举了一个真的。 之前你对着 Codex 的批准提示说“好的继续，不过先跑测试”。 快路径看见“好的继续”就替你按了同意，“先跑测试”那半句没送出去，也没人看见。 结果就是它没跑测试，直接往下做了。 你以为自己先立了个条件，其实那个条件根本没落地。 要让它动手改吗？ Options: 让它动手改; 先不动.

```
