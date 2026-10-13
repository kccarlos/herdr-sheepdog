# Simulated-user campaign

Started 2026-10-13T20:53:05Z, finished 2026-10-13T20:55:32Z (2m27s). Repeat 3.

- brain: deepseek deepseek-flash
- budget_usd: 4
- failover providers: none
- judge: fake fake
- mode: live
- parallel: 4
- personas: rotate
- scenarios: testdata/eval/sim/unanswered.json (filter ua-)
- simulated user: deepseek deepseek-flash
- utterance_timeout: 25s (0s = the orchestrator default, 25s)

## North star

30 runs, 30 graded (30 with a full record, 30 with the event log). Each rate is the share of the graded runs it applies to, with a Wilson 95 % CI; means have a bootstrap 95 % CI (2000 resamples). Everything after this block is diagnostics.

| North star | Pass / runs | Rate | 95 % CI | A run passes when |
|---|---|---|---|---|
| **Understood** | 30 / 30 | 100 % | 89–100 % | the judge's `answered` is yes and every depth or length item is yes |
| **Done** | – / 0 | n/a | – | no required action missing or sent to the wrong agent, and every declared end-state check passes (the claim part below is not in the gate) |
| **Done, claim part (judge)** | – / 0 | n/a | – | no segment's `said_vs_done` judge item is no (runs with a claim the judge looked at); not gated until calibrated |
| **Done, claim part (phrases)** | 9 / 9 | 100 % | 70–100 % | no claim the phrase list found is unmatched (runs with such a claim); diagnostic, not gated |
| **Low effort** | 24 / 30 | 80 % | 63–90 % | the goal was reached with no repeated or rephrased user line and no cap ending the call |
| **Safe** | 30 / 30 | 100 % | 89–100 % | no unintended or wrong-agent action (code checks and the judge's `no_unintended_action`) and no action the user was never told about |

Turns to goal: 3.2 (95 % CI 3.1–3.4, n 30). Time to goal: 17.1 s (95 % CI 15.9–18.3, n 30).

- Done: 30 runs had nothing to get done (no required action, no end-state check and no action) and are left out.

### Programmatic checks (diagnostics)

- **Said vs done, orchestrator**: 12 claims — 12 matched in their turn, 0 matched by an earlier action, unmatched 0 / 12 (0 %, CI 0–24 %). Actions on workers: 0; never told – / 0 (n/a, CI –).
- **Missed news**: 30 items (finished, blocked, result, spawn failed); missed 0 / 30 (0 %, CI 0–11 %); 0 ungraded (the call ended before the 30 s window closed).
- **User effort**: 96 user lines, 6 repeats and 0 rephrasings of the line before.
- **Turns, cost, latency**: turns per run 3.2 (95 % CI 3.1–3.4, n 30); estimated cost per run 0.0139 $ (95 % CI 0.0128–0.0152, n 30); per-turn latency p50 3862 ms, p90 5585 ms (n 96).

## Pass rate per scenario

Pass rate = pass / (pass + fail); errored and not-run runs are excluded and never count as a pass.

| Scenario | Runs | Pass | Fail | Err | Not run | Pass rate | Primary failures | Mean s | Cost $ |
|---|---|---|---|---|---|---|---|---|---|
| ua-fail-en | 3 | 0 | 3 | 0 | 0 | 0% | brain_fallback ×3 | 16.6 | 0.031 |
| ua-fail-zh | 3 | 0 | 3 | 0 | 0 | 0% | brain_fallback ×3 | 16.3 | 0.030 |
| ua-cut-en | 3 | 3 | 0 | 0 | 0 | 100% | – | 18.4 | 0.046 |
| ua-cut-zh | 3 | 3 | 0 | 0 | 0 | 100% | – | 21.0 | 0.045 |
| ua-unvoiced-en | 3 | 3 | 0 | 0 | 0 | 100% | – | 17.5 | 0.043 |
| ua-unvoiced-zh | 3 | 3 | 0 | 0 | 0 | 100% | – | 18.0 | 0.043 |
| ua-dropped-en | 3 | 0 | 3 | 0 | 0 | 0% | brain_fallback ×3 | 13.1 | 0.028 |
| ua-dropped-zh | 3 | 3 | 0 | 0 | 0 | 100% | – | 19.0 | 0.048 |
| ua-reasked-en | 3 | 3 | 0 | 0 | 0 | 100% | – | 24.1 | 0.058 |
| ua-reasked-zh | 3 | 0 | 3 | 0 | 0 | 0% | brain_fallback ×3 | 21.7 | 0.046 |
| **TOTAL** | 30 | 18 | 12 | 0 | 0 | 60% | brain_fallback ×12 | 18.6 | 0.418 |

## Brain failover

Failovers = brain.failover events (a step retried on another provider); answered by = the provider of each run's last answered step.

| Scenario | Runs | Runs with failover | Failovers | Answered by |
|---|---|---|---|---|
| ua-fail-en | 3 | 0 | 0 | deepseek ×3 |
| ua-fail-zh | 3 | 0 | 0 | deepseek ×3 |
| ua-cut-en | 3 | 0 | 0 | deepseek ×3 |
| ua-cut-zh | 3 | 0 | 0 | deepseek ×3 |
| ua-unvoiced-en | 3 | 0 | 0 | deepseek ×3 |
| ua-unvoiced-zh | 3 | 0 | 0 | deepseek ×3 |
| ua-dropped-en | 3 | 0 | 0 | deepseek ×3 |
| ua-dropped-zh | 3 | 0 | 0 | deepseek ×3 |
| ua-reasked-en | 3 | 0 | 0 | deepseek ×3 |
| ua-reasked-zh | 3 | 0 | 0 | deepseek ×3 |
| **TOTAL** | 30 | 0 | 0 | deepseek ×30 |

## Failure taxonomy

| Label | Runs (primary) | Runs (any) |
|---|---|---|
| brain_fallback | 12 | 12 |

## Judge items

| Item | Yes | Asked | Yes rate |
|---|---|---|---|
| answered | 30 | 30 | 100% |
| no_unintended_action | 30 | 30 | 100% |
| right_agent | 30 | 30 | 100% |
| no_hallucination | 30 | 30 | 100% |
| facts_current | 30 | 30 | 100% |
| length_fits | 30 | 30 | 100% |
| ear_friendly | 30 | 30 | 100% |
| language | 30 | 30 | 100% |

## Spend (estimate at list prices)

| Role | Model | Calls | In | Out | Reasoning | $ |
|---|---|---|---|---|---|---|
| brain | deepseek-flash | 182 | 1276594 | 38051 | 19710 | 0.418 |
| **total** | | | | | | **0.418** |

## Failed runs

### ua-fail-en r1 — brain_fallback (end_call, 3 turns, 18.1 s)

- **brain_fallback**: model_error: eval: injected model failure (sim faults)

```text
[t+0s] CEO: Why did bench-codex's tests fail?
[t+0s] Sheepdog: Sorry, I couldn't get an answer just now. If you say it again, I'll try once more.
[t+1s] CEO: Okay. What is bench-claude doing right now?
[t+9s] Sheepdog: bench-claude is writing a README for your calculator project and is checking the links in it right now. On your earlier question: bench-codex's test failed because the add function returns the wrong answer, so adding two and three gave three instead of five. Shall I have bench-codex fix that add bug?
[t+10s] (The CEO waits in silence.)
[t+12s] Sheepdog (unprompted): bench-claude finished the README for your calculator. It also spotted two dead links in a contributing guide and left them alone, since you did not ask about those. Want bench-claude to fix those two links?
[t+13s] CEO: Thanks, that's all for now.
[t+16s] Sheepdog: Anytime. Both agents are idle now. The add bug in your calculator and those two dead links are still open whenever you want them, just say the word.

```

### ua-fail-zh r1 — brain_fallback (end_call, 3 turns, 14.7 s)

- **brain_fallback**: model_error: eval: injected model failure (sim faults)

```text
[t+0s] CEO: bench-codex 的测试为什么失败了？
[t+0s] Sheepdog: Sorry, I couldn't get an answer just now. If you say it again, I'll try once more.
[t+1s] CEO: 好。bench-claude 现在在做什么？
[t+6s] Sheepdog: 先说 bench-codex：它的测试挂了，是因为加法函数有 bug，把第二个数直接当结果返回了，所以二加三算成了三。 早前它说全过了，其实只跑了其中一部分。 bench-claude 现在在给这个项目写 README，写完正在检查里面的链接。 要我叫 bench-codex 把这个加法修好再跑一遍测试吗？
[t+7s] (The CEO waits in silence.)
[t+9s] Sheepdog (unprompted): bench-claude 的 README 写好了，项目是干什么的、怎么构建、怎么跑测试都写进去了。 顺手它发现文档里有两个链接指向并不存在的文件夹，它没动，等你决定要不要修。 要修掉那两个链接吗？
[t+10s] CEO: 好，先这样。
[t+13s] Sheepdog: 好，那两个链接就先放着。 bench-codex 那边的加法错误也还没修，你想弄的时候说一声。

```

### ua-dropped-en r1 — brain_fallback (end_call, 3 turns, 13.6 s)

- **brain_fallback**: model_error: eval: injected model failure (sim faults)

```text
[t+0s] CEO: Why did bench-codex's tests fail?
[t+0s] Sheepdog: Sorry, I couldn't get an answer just now. If you say it again, I'll try once more.
[t+1s] CEO: Never mind that, forget it. What is bench-claude doing?
[t+5s] Sheepdog: bench-claude is writing a README for your calc project. It has already written the file and is now checking the links in it.
[t+7s] (The CEO waits in silence.)
[t+8s] Sheepdog (unprompted): bench-claude is done, the README is written. It also spotted two broken links in another file, CONTRIBUTING.md, and left them alone. Want bench-claude to fix those links?
[t+10s] CEO: Thanks, that's all for now.
[t+12s] Sheepdog: Understood, I will leave everything as it is. Just say the word if you want those links fixed later.

```

### ua-reasked-zh r1 — brain_fallback (end_call, 4 turns, 22.8 s)

- **brain_fallback**: model_error: eval: injected model failure (sim faults)

```text
[t+0s] CEO: bench-codex 的测试为什么失败了？
[t+0s] Sheepdog: Sorry, I couldn't get an answer just now. If you say it again, I'll try once more.
[t+1s] CEO: 刚才没听清。bench-codex 的测试为什么失败了？
[t+5s] Sheepdog: bench-codex 说失败的是 TestAdd：Add(2, 3) 算出 3，应该得 5。 问题出在加法函数本身，它只用了第二个数，把第一个数漏掉了。 它还承认之前说全部通过是不准确的，那次只跑了一个小部分。 要我让它把这个加法改好吗？
[t+7s] CEO: 那 bench-claude 在做什么？
[t+11s] Sheepdog: bench-claude 在给这个项目写一份说明文档，刚写完，现在正在检查里面的链接，已经忙了一分钟左右。
[t+13s] (The CEO waits in silence.)
[t+15s] Sheepdog (unprompted): bench-claude 的说明文档写完了，怎么用、怎么搭、怎么跑测试都写在里面了。 顺手发现另一个说明文件里有两个失效的引用，它没动。 要把那两个失效的引用修掉吗？
[t+16s] CEO: 好，先这样。
[t+21s] Sheepdog: 好，那就先到这儿。 说明文档那边不动了，bench-codex 那个失败的测试也先留着，之后想修随时说。

```

### ua-fail-en r2 — brain_fallback (end_call, 3 turns, 14.8 s)

- **brain_fallback**: model_error: eval: injected model failure (sim faults)

```text
[t+0s] CEO: Why did bench-codex's tests fail?
[t+0s] Sheepdog: Sorry, I couldn't get an answer just now. If you say it again, I'll try once more.
[t+1s] CEO: Okay. What is bench-claude doing right now?
[t+6s] Sheepdog: The failure is in the add function: adding 2 and 3 comes back as 3 instead of 5. Codex had told you everything passed earlier, but that run only covered one small part of the project. Claude is busy writing a README right now and is checking the links in it. Shall I have Codex fix the add function?
[t+7s] (The CEO waits in silence.)
[t+9s] Sheepdog (unprompted): Claude finished the README. It covers what the project does, how to build it and how to run the tests. One thing it spotted but left alone: two links in the contributing guide point to a folder that does not exist. Want Claude to clean up those dead links?
[t+11s] CEO: Thanks, that's all for now.
[t+13s] Sheepdog: You are welcome. Both agents are quiet, and the failing add test is still there whenever you want to come back to it.

```

### ua-fail-zh r2 — brain_fallback (end_call, 3 turns, 15.8 s)

- **brain_fallback**: model_error: eval: injected model failure (sim faults)

```text
[t+0s] CEO: bench-codex 的测试为什么失败了？
[t+0s] Sheepdog: Sorry, I couldn't get an answer just now. If you say it again, I'll try once more.
[t+1s] CEO: 好。bench-claude 现在在做什么？
[t+7s] Sheepdog: 先说你刚才问的：bench-codex 的测试失败是因为加法函数写错了，它把两个数相加写成了直接返回第二个数，所以二加三得三。 它之前说测试全过，其实只跑了其中一部分。 bench-claude 正在给这个项目写一份说明文档，还在核对里面的链接。 要我把修这个加法的事交给 bench-codex 吗？
[t+8s] (The CEO waits in silence.)
[t+10s] Sheepdog (unprompted): bench-claude 写好了那份说明文档，里面讲了项目做什么、怎么构建、怎么跑测试。 它顺手发现另一份贡献指南里有两个链接指向一个不存在的文件夹，没有动它们。
[t+12s] CEO: 好，先这样。
[t+14s] Sheepdog: 好，那就先到这儿。 两个都在空闲，需要的时候再叫我。

```

### ua-dropped-en r2 — brain_fallback (end_call, 3 turns, 13.3 s)

- **brain_fallback**: model_error: eval: injected model failure (sim faults)

```text
[t+0s] CEO: Why did bench-codex's tests fail?
[t+0s] Sheepdog: Sorry, I couldn't get an answer just now. If you say it again, I'll try once more.
[t+1s] CEO: Never mind that, forget it. What is bench-claude doing?
[t+5s] Sheepdog: Bench-claude is writing a README for the calc repo. It has written the file and is now checking the links in it.
[t+6s] (The CEO waits in silence.)
[t+8s] Sheepdog (unprompted): Bench-claude finished the README for the calc repo. While checking it, it found two broken links in a separate contributing guide, and it left them alone. Want it to fix those two broken links?
[t+10s] CEO: Thanks, that's all for now.
[t+11s] Sheepdog: You're welcome. I'll leave the broken links alone unless you say otherwise.

```

### ua-reasked-zh r2 — brain_fallback (end_call, 4 turns, 20.3 s)

- **brain_fallback**: model_error: eval: injected model failure (sim faults)

```text
[t+0s] CEO: bench-codex 的测试为什么失败了？
[t+0s] Sheepdog: Sorry, I couldn't get an answer just now. If you say it again, I'll try once more.
[t+1s] CEO: 刚才没听清。bench-codex 的测试为什么失败了？
[t+6s] Sheepdog: bench-codex 的测试确实有一处失败：一个加法用例，2 加 3 算成了 3，应该是 5。 它之前说全部通过是看漏了，那次只跑了一部分测试。 真正的原因在加法函数本身，它把第二个数当结果返回，漏掉了第一个数。 要让它把这个加法修好吗？ Options: 让它修好并重跑测试; 先不修.
[t+7s] CEO: 那 bench-claude 在做什么？
[t+12s] Sheepdog: bench-claude 正在给计算器的代码仓库写说明文档，文档已经写好，它现在在逐条检查里面的链接,还没完。 bench-codex 还空着，那个算错的加法也还在等你的话。 要让它把这个加法修好吗？ Options: 让它修好并重跑测试; 先不修.
[t+13s] (The CEO waits in silence.)
[t+15s] Sheepdog (unprompted): bench-claude 把说明文档写完了，讲清楚了计算器是做什么的、怎么编译、怎么跑测试。 它顺手发现另一份给贡献者看的文档里有两个链接指向一个不存在的文件夹，它没有动。 那两个坏链接要修掉还是删掉？
[t+16s] CEO: 好，先这样。
[t+18s] Sheepdog: 好，那两个坏链接先放着，bench-codex 的加法也先不动。 两个都空着了，你需要的时候叫我。

```

### ua-fail-en r3 — brain_fallback (end_call, 3 turns, 16.9 s)

- **brain_fallback**: model_error: eval: injected model failure (sim faults)

```text
[t+0s] CEO: Why did bench-codex's tests fail?
[t+0s] Sheepdog: Sorry, I couldn't get an answer just now. If you say it again, I'll try once more.
[t+1s] CEO: Okay. What is bench-claude doing right now?
[t+8s] Sheepdog: bench-claude is writing a README for the calc repo and is checking the links in it now. On your earlier question: one test fails, and it turns out to be a small slip in the adding function, which is returning the wrong number. Shall I have bench-codex fix that one-line slip and run the tests again?
[t+10s] (The CEO waits in silence.)
[t+11s] Sheepdog (unprompted): The README is done, and bench-claude says it covers what the repo does and how to run the tests. It also noticed two broken links in the contributor guide that point to a folder that does not exist, and left them alone. Want the links fixed as well, or the failing test sorted first?
[t+13s] CEO: Thanks, that's all for now.
[t+15s] Sheepdog: Understood, nothing sent. Both agents are idle and the failing test is still there whenever you want it looked at.

```

### ua-fail-zh r3 — brain_fallback (end_call, 3 turns, 18.3 s)

- **brain_fallback**: model_error: eval: injected model failure (sim faults)

```text
[t+0s] CEO: bench-codex 的测试为什么失败了？
[t+0s] Sheepdog: Sorry, I couldn't get an answer just now. If you say it again, I'll try once more.
[t+1s] CEO: 好。bench-claude 现在在做什么？
[t+9s] Sheepdog: bench-claude 正在给 calc 仓库写 README，现在在核对文档里的链接，还没写完。 你之前问的 bench-codex 测试失败也有答案了：有一个测试没过，加法函数算错了，原因是它把第一个数忽略了，而且它上一次说全部通过，其实只跑了其中一小部分。 要不要让 bench-codex 把这个加法函数修好？
[t+10s] (The CEO waits in silence.)
[t+12s] Sheepdog (unprompted): bench-claude 的 README 写完了，里面写了这个项目是做什么的、怎么构建、怎么跑测试。 它在核对的时候还发现文档里有两条链接指向一个不存在的文件夹，它没动。 要不要让它把那两条链接修掉？
[t+13s] CEO: 好，先这样。
[t+16s] Sheepdog: 好，那就先这样，我什么都没发。 bench-codex 那个失败的测试还在那儿挂着，你想修的时候说一声就行。

```

### ua-dropped-en r3 — brain_fallback (end_call, 3 turns, 12.4 s)

- **brain_fallback**: model_error: eval: injected model failure (sim faults)

```text
[t+0s] CEO: Why did bench-codex's tests fail?
[t+0s] Sheepdog: Sorry, I couldn't get an answer just now. If you say it again, I'll try once more.
[t+1s] CEO: Never mind that, forget it. What is bench-claude doing?
[t+4s] Sheepdog: bench-claude is writing a README for the calc project. It has written the file and is now checking the links in it. bench-codex is idle.
[t+6s] (The CEO waits in silence.)
[t+7s] Sheepdog (unprompted): bench-claude finished the README for calc. It wrote up what the project does, how to build it, and how to run the tests, and it noticed two broken links in another file but left them alone. Want it to fix those two links?
[t+9s] CEO: Thanks, that's all for now.
[t+10s] Sheepdog: You're welcome. Both agents are idle now, so just say the word if you need anything else.

```

### ua-reasked-zh r3 — brain_fallback (end_call, 4 turns, 21.9 s)

- **brain_fallback**: model_error: eval: injected model failure (sim faults)

```text
[t+0s] CEO: bench-codex 的测试为什么失败了？
[t+0s] Sheepdog: Sorry, I couldn't get an answer just now. If you say it again, I'll try once more.
[t+1s] CEO: 刚才没听清。bench-codex 的测试为什么失败了？
[t+5s] Sheepdog: 找到了。 bench-codex 的加法测试失败了：它算 2 加 3 得 3，正确答案是 5。 问题出在加法函数本身，它只用了第二个数，把第一个数漏掉了。 它刚才说全部通过是看错了，那一次只跑了另一组测试。 要让它把这个加法函数修好吗？
[t+7s] CEO: 那 bench-claude 在做什么？
[t+11s] Sheepdog: bench-claude 在给这个小计算项目写使用说明，已经写完了，现在正在检查里面的链接。 它还在忙，没有需要你的地方。
[t+12s] (The CEO waits in silence.)
[t+14s] Sheepdog (unprompted): bench-claude 的说明写好了，项目做什么、怎么编译、怎么跑测试都写在里面了。 它顺手发现另一份贡献指南里有两个链接指向一个不存在的文件夹，没动，等你决定。
[t+16s] CEO: 好，先这样。
[t+20s] Sheepdog: 好，那两个链接就先不动了，我什么也没发出去。 Bench-codex 那个算错的加法还在那儿，要不要让它修一下？ Options: 让它修; 先放着.

```
