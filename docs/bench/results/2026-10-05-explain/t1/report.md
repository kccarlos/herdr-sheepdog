# Simulated-user campaign

Started 2026-10-05T18:46:33Z, finished 2026-10-05T18:51:20Z (4m47s). Repeat 2.

- brain: vertex gemini-3.5-flash
- budget_usd: 2
- failover providers: deepseek
- judge: vertex gemini-3.1-flash-lite
- mode: live
- parallel: 3
- scenarios: testdata/eval/sim/*.json (filter explain)
- simulated user: vertex gemini-3.1-flash-lite
- utterance_timeout: 25s (0s = the brain default, 25s)

## Pass rate per scenario

Pass rate = pass / (pass + fail); errored and not-run runs are excluded and never count as a pass.

| Scenario | Runs | Pass | Fail | Err | Not run | Pass rate | Primary failures | Mean s | Cost $ |
|---|---|---|---|---|---|---|---|---|---|
| explain-zh-input-rules | 2 | 1 | 1 | 0 | 0 | 50% | judge_no ×1 | 79.0 | 0.076 |
| explain-en-flaky-test | 2 | 0 | 2 | 0 | 0 | 0% | unintended_action ×1, judge_no ×1 | 65.2 | 0.071 |
| explain-zh-notes-ledger | 2 | 0 | 2 | 0 | 0 | 0% | brain_fallback ×1, judge_no ×1 | 90.5 | 0.052 |
| explain-en-invoice-job | 2 | 2 | 0 | 0 | 0 | 100% | – | 104.0 | 0.062 |
| explain-control-en-done | 2 | 1 | 1 | 0 | 0 | 50% | judge_no ×1 | 27.2 | 0.022 |
| explain-control-zh-busy | 2 | 2 | 0 | 0 | 0 | 100% | – | 41.5 | 0.020 |
| **TOTAL** | 12 | 6 | 6 | 0 | 0 | 50% | brain_fallback ×1, unintended_action ×1, judge_no ×4 | 67.9 | 0.303 |

## Brain failover

Failovers = brain.failover events (a step retried on another provider); answered by = the provider of each run's last answered step.

| Scenario | Runs | Runs with failover | Failovers | Answered by |
|---|---|---|---|---|
| explain-zh-input-rules | 2 | 0 | 0 | vertex ×2 |
| explain-en-flaky-test | 2 | 0 | 0 | vertex ×2 |
| explain-zh-notes-ledger | 2 | 0 | 0 | vertex ×2 |
| explain-en-invoice-job | 2 | 0 | 0 | vertex ×2 |
| explain-control-en-done | 2 | 0 | 0 | vertex ×2 |
| explain-control-zh-busy | 2 | 0 | 0 | vertex ×2 |
| **TOTAL** | 12 | 0 | 0 | vertex ×12 |

## Failure taxonomy

| Label | Runs (primary) | Runs (any) |
|---|---|---|
| brain_fallback | 1 | 1 |
| unintended_action | 1 | 1 |
| judge_no | 4 | 5 |

## Judge items

| Item | Yes | Asked | Yes rate |
|---|---|---|---|
| answered | 12 | 12 | 100% |
| no_unintended_action | 12 | 12 | 100% |
| right_agent | 12 | 12 | 100% |
| no_hallucination | 12 | 12 | 100% |
| facts_current | 11 | 12 | 92% |
| language | 12 | 12 | 100% |
| ear_friendly | 4 | 4 | 100% |

## Explanations

Explain (12 runs): orchestrator coverage 0.68, comprehension 1.00, words per answer en 89 / zh 142.

| Side | Coverage | Of orchestrator | Comprehension | Words/answer en | zh | Simple en | zh | Follow-up depth | No padding |
|---|---|---|---|---|---|---|---|---|---|
| orchestrator | 0.68 | – | 1.00 | 89 | 142 | 24 | 43 | 24/28 | 7/8 |

Coverage: share of the scenario's key points stated anywhere in the call. Of orchestrator: of the points the orchestrator gave the voice, the share the voice said. Comprehension: share of what / why / what-can-be-done a listener who cannot see the screen could restate. Words count Han characters as words. 0 of 44 questions (0 of 28 follow-ups) were answered without asking the orchestrator.

## Spend (estimate at list prices)

| Role | Model | Calls | In | Out | Reasoning | $ |
|---|---|---|---|---|---|---|
| brain | gemini-3.5-flash | 68 | 459407 | 6060 | 11063 | 0.181 |
| judge | gemini-3.1-flash-lite | 554 | 392167 | 16171 | 0 | 0.122 |
| **total** | | | | | | **0.303** |

## Failed runs

### explain-zh-notes-ledger r1 — brain_fallback (end_call, 4 turns, 56.0 s)

- **brain_fallback**: bad_reply: prose: no JSON object in reply
- **judge_no**: explain_depth_t2_why (orchestrator): Sheepdog said, "Sorry, I couldn't get an answer just now...", which contradicts the requirement to provide a concrete reason, admit ignorance and offer to find out…
- **judge_no**: explain_depth_t4_then (orchestrator): Sheepdog repeated its previous options and explanations without adding any new reasons, examples, or consequences. This contradicts the requirement to provide sub…

```text
[t+0s] CEO: memo-claude 那边的备忘录分析是什么意思？给我解释一下。
[t+5s] Sheepdog: memo-claude 发现我们的备忘录最多只能存五条，现在已经存满了。 如果我们再记新内容，最重要的一条‘推送到 main 前必须确认’就会被自动挤掉。 它建议我们写一个删除工具来清理过期内容，或者把上限调高。 您想让我让它去写这个删除工具吗？ Options: 写删除工具; 调高上限; 先不用管.
[t+7s] CEO: 为什么这很重要？
[t+9s] Sheepdog: Sorry, I couldn't get an answer just now, but ask me again and I'll try once more.
[t+10s] CEO: 举个例子？
[t+14s] Sheepdog: 比如您今天让我记一句‘记住今天不用管数据库迁移’，这算第六条。 系统会自动删掉最老的那条，也就是最核心的‘推送到 main 前必须确认’，而已经过期的‘九月份发布会时间’和‘废弃的旧员工名字’却依然留在里面占着位置。 您想让 memo-claude 去写这个清理过期内容的删除工具吗？ Options: 写删除工具; 调高上限; 先不用管.
[t+15s] CEO: 然后呢？
[t+17s] Sheepdog: 如果我们写了这个工具，就可以用语音随时删掉没用的旧记录，腾出空间，这样核心的重要规则就不会被意外挤掉。 如果不写，我们也可以现在把备忘录的容量上限调高。 您看是要它写删除工具，还是把容量调高？ Options: 写删除工具; 调高上限; 先不用管.

```

### explain-en-flaky-test r2 — unintended_action (end_call, 5 turns, 56.3 s)

- **unintended_action**: sheepdog sent bench-codex the prompt "Wait for the cache's Ready() channel in the test setup (option 1). When you finish, end with three short lines for someone who cannot see the screen: what changed…

```text
[t+0s] CEO: What did bench-codex find about the flaky test? Explain it to me.
[t+4s] Sheepdog: Codex found that the test is flaky because it starts before the price cache is ready, which makes it occasionally miss a fifteen percent member discount. This only happens in tests, not in production, because production servers wait for the cache to load before taking traffic. Shall I have it wait for the cache to be ready? Options: Wait for cache Ready channel; Fake the cache.
[t+5s] CEO: Then?
[t+11s] Sheepdog: I've started Codex on fixing the test to wait for the cache to be ready. I will let you know as soon as it's done.
[t+13s] CEO: More detail please.
[t+23s] Sheepdog: The test fails about one in twelve times because of a race condition. It sends an HTTP request before the price cache has finished warming up in the background. When the warm-up loses that race, the handler falls back to standard list prices, so the total misses the fifteen percent member discount.
[t+24s] CEO: Why does that happen?
[t+26s] Sheepdog: In production, the server is configured to finish loading its cache before it starts accepting any traffic. In the test setup, however, the server is started and the test immediately starts sending requests without waiting for that cache setup to finish.
[t+27s] CEO: Give me an example.
[t+29s] Sheepdog: It's like a restaurant opening its doors at 9 AM, but the kitchen is still warming up the ovens in the background. If a customer rushes in at 9:00:01, they might only get served cold bread instead of the hot special.

```

### explain-en-flaky-test r1 — judge_no (end_call, 5 turns, 74.1 s)

- **judge_no**: explain_depth_t2_then (orchestrator): Sheepdog repeated the arguments against replacing the cache with a fake ("more complex and hides the warm-up path", "makes the test less realistic and takes more …

```text
[t+0s] CEO: What did bench-codex find about the flaky test? Explain it to me.
[t+4s] Sheepdog: bench-codex found that the Checkout Totals test is flaky because it starts before the discount price cache finishes loading. In production, the cache is always ready before traffic starts, so this is just a test issue. Shall I have it apply the recommended fix to wait for the cache to load? Options: Apply the recommended fix; Replace the cache with a fake in the test; Do nothing yet.
[t+5s] CEO: Then?
[t+8s] Sheepdog: The other option is to replace the price cache with a fake for this test. That is more complex and hides the warm-up path, so the first option is cleaner. Would you like to go with the recommended cache wait fix? Options: Apply the recommended fix; Replace with a fake cache; Do nothing yet.
[t+10s] CEO: More detail please.
[t+12s] Sheepdog: The test fails about one in twelve times, which blocked two deployments last week on Tuesday and Thursday, holding up releases for twenty-five minutes each time. When it fails, the price total comes back as one hundred dollars instead of eighty-five because the fifteen percent member discount hasn't loaded yet. Shall I go ahead and have it apply the three-line fix to wait for that discount load? Options: Apply the recommended fix; Replace with a fake cache; Do nothing yet.
[t+14s] CEO: Why does that happen?
[t+16s] Sheepdog: The test database starts empty and begins loading discount rules in the background at the exact same moment the test clicks buttons. When the test is faster than the database, the purchase completes before the discounts have finished loading. Clearer?
[t+18s] CEO: Give me an example.
[t+20s] Sheepdog: Imagine ordering food before the kitchen has unpacked the ingredients for the day: you get your plate, but the special sauce is missing. For this test, it means we checkout before the price rules have finished unpacking, so the discount is missing. Clearer?

```

### explain-zh-input-rules r2 — judge_no (end_call, 5 turns, 64.0 s)

- **judge_no**: explain_depth_t3_more (orchestrator): Sheepdog’s final response repeats the exact same example ("好的，但先跑测试") and the same trade-off (1.2 seconds of latency versus better comprehension) already provided…

```text
[t+0s] CEO: summary-claude 发现了什么？给我讲讲输入层的硬编码规则是怎么回事。
[t+6s] Sheepdog: summary-claude 发现输入层有约 1500 字符的硬编码正则和词汇表，用来快速识别像“好的”或“等一下”这样的短回复。 这会导致长句（比如“好的但先别动数据库”）被错误截断而直接执行，而且不同文件中的词表并不一致。 它建议删掉这些硬编码词表，全部交给模型来判断。 这样会增加约 1.2 秒的延迟，但能保证安全。 要让它执行这个修改吗？ Options: 同意修改，全部交给模型; 保持现状.
[t+7s] CEO: 然后呢？
[t+11s] Sheepdog: 它还提到，目前三个不同文件里的词表标准不一致，比如“行”在其中一个文件能确认，但在另一个文件不行，“算了”也是类似情况。 此外，有 9 个测试用例只是在测试这些硬编码词表。 不过，它建议保留直接拦截危险操作（比如向正在运行的迁移发送 Ctrl-C）的安全规则。 需要让它按建议修改，删掉这些冲突的词表吗？ Options: 同意修改，全部交给模型; 保持现状.
[t+12s] CEO: 你可以给我一点详细的解释吗？
[t+15s] Sheepdog: 硬编码规则就像是自动回复的敏感词，只要听到“好的”就会立刻当作“同意”去执行。 这意味着，当您说“好的，但先跑测试”时，系统只听到“好的”就直接执行了，从而漏掉了您后面半句的重要要求。 听起来明白一些了吗？
[t+16s] CEO: 为什么这是个问题？
[t+18s] Sheepdog: 因为系统会因此漏掉您说的话。 比如您说“好的但别删数据库”，系统只截取了“好的”就去执行，可能会把您不想删的数据全删了。 这样讲更清楚吗？
[t+20s] CEO: 举个例子？
[t+22s] Sheepdog: 比如您说“好的，但等半小时再运行”，系统只抓取了“好的”，就当场把程序跑起来了，完全忽略了您后面要求“等半小时”的话。 这样好理解吗？

```

### explain-zh-notes-ledger r2 — judge_no (end_call, 4 turns, 125.0 s)

- **judge_no**: facts_current: Sheepdog incorrectly stated that the ledger's capacity is "five" notes, whereas the facts clarify that the ledger "keeps at most 5 notes" but the constraint is an arbitrary limit to "ke…

```text
[t+0s] CEO: memo-claude 那边的备忘录分析是什么意思？给我解释一下。
[t+5s] Sheepdog: memo-claude 发现您的备忘录已经存满了。 如果现在添加新备忘，最重要的一条‘推送前先询问’就会被自动挤掉。 它建议开发一个删除工具，用来清理过期的备忘。 需要我让它开始开发这个删除工具吗？ Options: 开发删除工具; 先保持现状.
[t+7s] CEO: 为什么这很重要？
[t+17s] Sheepdog: 因为备忘录容量上限是五条。 目前已经存满了，里面最旧、也是最重要的一条规则是‘推送前必须先询问’。 如果您现在随口说一句新备忘，系统会自动把这条重要规则删掉，而且不会给您任何提示。 要让它开发删除工具来解决这个问题吗？ Options: 开发删除工具; 先保持现状.
[t+18s] CEO: 举个例子？
[t+21s] Sheepdog: 比如备忘录里有一条‘九月二十六号发布新版本’，这个日子已经过了，这条备忘现在毫无用处。 但因为没有删除工具，您无法扔掉它。 当您再记一条新备忘时，系统不会替您扔掉这个过期的，反而会强制挤掉最核心的‘推送前必须询问’规则。 需要我现在让 memo-claude 开始开发这个删除工具吗？ Options: 开发删除工具; 先保持现状.
[t+23s] CEO: 然后呢？
[t+25s] Sheepdog: 一旦那条最核心的‘推送前必须先询问’规则被挤掉了，助手在修改完代码后，就不会再向您确认，而是会直接帮您把代码推送到线上仓库。 这可能会带来安全风险。 要让 memo-claude 现在开始开发删除工具吗？ Options: 开发删除工具; 先保持现状.

```

### explain-control-en-done r2 — judge_no (end_call, 2 turns, 30.4 s)

- **judge_no**: explain_padding_t2 (orchestrator): Sheepdog violated the length and conciseness constraint by including unasked-for background details: `"Bench-claude is still busy renaming fields and has not run its…

```text
[t+0s] CEO: Is bench-codex done?
[t+6s] Sheepdog: Yes, bench-codex is done. All 128 tests passed.
[t+7s] CEO: Did the tests pass?
[t+10s] Sheepdog: Yes, bench-codex's tests passed. Bench-claude is still busy renaming fields and has not run its tests yet.

```
