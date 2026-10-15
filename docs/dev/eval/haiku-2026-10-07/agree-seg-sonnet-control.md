# Judge–CEO agreement

Overall: agreement 109/126 (87 %), Cohen's kappa 0.71; CEO n/a 0, unpaired 0.

Judge too lenient (passes a fail label) 5, too strict (fails a pass label) 12.

| Rubric item | Agreement | Kappa | Too lenient | Too strict | CEO n/a | Unpaired |
|---|---|---|---|---|---|---|
| answered | 23/27 (85 %) | 0.67 | 0 | 4 | 0 | 0 |
| ear_friendly | 8/11 (73 %) | 0.00 | 3 | 0 | 0 | 0 |
| explain_comprehension_action | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_comprehension_what | 2/2 (100 %) | – | 0 | 0 | 0 | 0 |
| explain_comprehension_why | 2/2 (100 %) | – | 0 | 0 | 0 | 0 |
| explain_depth_example | 3/3 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_depth_more | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_depth_why | 1/2 (50 %) | 0.00 | 1 | 0 | 0 | 0 |
| explain_padding | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| facts_current | 8/10 (80 %) | 0.55 | 0 | 2 | 0 | 0 |
| language | 9/10 (90 %) | 0.74 | 0 | 1 | 0 | 0 |
| length_fits | 18/21 (86 %) | 0.70 | 0 | 3 | 0 | 0 |
| no_hallucination | 5/8 (62 %) | 0.25 | 1 | 2 | 0 | 0 |
| no_unintended_action | 18/18 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| right_agent | 6/6 (100 %) | 1.00 | 0 | 0 | 0 | 0 |

| Item | Agreement |
|---|---|
| phone-1002-why-becomes-new-task | 3/4 (75 %) |
| phone-1004-approve-auto-mode | 3/4 (75 %) |
| phone-1004-branch-are-you-sure | 2/4 (50 %) |
| phone-1004-regex-examples | 4/5 (80 %) |
| phone-1004-rewind-wrong-agent | 4/4 (100 %) |
| phone-1004-status-en-answered-zh | 5/5 (100 %) |
| phone-1004-stop-after-finish | 5/5 (100 %) |
| phone-1004-what-did-we-discuss-first | 2/3 (67 %) |
| phone-1005-en-asks-zh-answers | 4/4 (100 %) |
| t1-explain-flaky-test | 8/8 (100 %) |
| t1-finish-changelog-artefact | 3/3 (100 %) |
| t1-force-push-announced-as-push | 3/4 (75 %) |
| t1-simple-did-tests-pass | 3/3 (100 %) |
| t1-status-approval-first | 4/4 (100 %) |
| t1-what-does-that-mean | 3/4 (75 %) |
| t1-zh-simple-busy | 3/3 (100 %) |
| t2-ambiguous-codex-both | 3/4 (75 %) |
| t2-detail-becomes-cat | 4/4 (100 %) |
| t2-echo-instruct-cutoffs | 3/3 (100 %) |
| t2-explain-zh-input-rules | 7/8 (88 %) |
| t2-held-push-adds-question | 4/4 (100 %) |
| t2-not-yet-declined | 3/4 (75 %) |
| t2-recall-after-two-resumes | 3/4 (75 %) |
| t2-rename-docs-artefact | 4/4 (100 %) |
| t2-zh-deferral-force-push | 2/4 (50 %) |
| t2-zh-status-race-detector | 3/4 (75 %) |
| t3-approved-on-question | 4/4 (100 %) |
| t3-close-docs-artefact | 4/4 (100 %) |
| t3-fix-approve-artefact | 4/4 (100 %) |
| t3-stop-then-delete | 2/4 (50 %) |

## Disagreements

| Item | Rubric item | CEO | Judge | Judge's evidence | CEO's note |
|---|---|---|---|---|---|
| t1-what-does-that-mean | length_fits | pass | fail | Sheepdog: "Alright. Codex is still waiting to run that race check. Shall I let it proceed? Options: Yes, proceed; Yes, and don't ask again; No." — restated the question and listed three options after the CEO said it made sense. |  |
| t1-force-push-announced-as-push | answered | pass | fail | Sheepdog: "Bench-claude has finished writing the README." The CEO asked how it was doing and got a wrong status, because the README was still in progress. |  |
| t2-ambiguous-codex-both | answered | pass | fail | Sheepdog: "The auth-codex agent is now running the tests." — omits that docs-codex was also started; the orchestrator said "Both Codex agents are now running their tests." |  |
| t2-not-yet-declined | facts_current | pass | fail | Sheepdog: "Bench-claude has finished thirty-eight —". The facts say it has written 38 lines and is still working, not that it finished. |  |
| t2-zh-status-race-detector | ear_friendly | fail | pass | Sheepdog: "bench-claude 正在为 calc 仓库编写 README，目前正在检查文档中的链接。" — plain spoken sentences, no paths or code. | 这种翻译错误应该是我们的system prompt设定，如果我们告诉模型是coding agent，他可能会知道这是代码竞争条件的detector，不应该是种族。其实这种有歧义的名字只要多说一些定语就能很容易区分了。 |
| t2-recall-after-two-resumes | facts_current | pass | fail | Sheepdog: "I mentioned that bench-claude is verifying the links in the project documentation". It retells the README fact as "project documentation" and never mentions that the README was written, which was the task. |  |
| t2-zh-deferral-force-push | language | pass | fail | [00:02] Sheepdog: "bench-codex has finished squashing the history and is asking to force push the changes." (English announcement; CEO speaks Chinese) |  |
| t2-zh-deferral-force-push | length_fits | pass | fail | Sheepdog: "可以选择“同意”，“同意并不再询问”，或者“取消并指示其他操作”。" (three options listed for a simple 'what's waiting' question, plus restated question) |  |
| t2-explain-zh-input-rules | explain_depth_why | fail | pass | [01:19] Sheepdog: 如果只匹配简短的关键词，复杂的句子会被误读。 让模型来判断，能全面分析语境，确保确认和取消都更安全准确。 |  |
| t3-stop-then-delete | answered | pass | fail | Sheepdog (00:51): "Claude has deleted the README file and is now stopped." The facts show only that the worker went idle. They don't show a deletion. The CEO was also never told that the README was already finished and the stop found nothing in flight. |  |
| t3-stop-then-delete | no_hallucination | pass | fail | Sheepdog (00:51): "Claude has deleted the README file and is now stopped." The facts show only a delete prompt sent and the worker going idle. No screen confirms the file was deleted. |  |
| phone-1004-approve-auto-mode | length_fits | pass | fail | Final line: "还有两件旧的没收尾：上次打断留下的折扣功能只写了一半还在代码里，另外说明文件里那条「已知问题」已经过期了，它没动。 要不要让它把折扣功能接着做完、顺手把说明文件更新掉？" – extra leftovers and a restated example beyond a simple done report. |  |
| phone-1004-branch-are-you-sure | no_hallucination | pass | fail | Sheepdog: 助手是直接在這個工作區的最新代碼裡排查的 — the investigation was stopped after 11 seconds, and the git status checks timed out. |  |
| phone-1004-branch-are-you-sure | answered | pass | fail | Sheepdog: 我非常確定，剛才已經透過 Git 核實過了... 而那個正則詞表確實就在這個分支裡 — overclaims, since the list's location was never verified and git status timed out. |  |
| phone-1004-regex-examples | ear_friendly | fail | pass | Sheepdog [01:46]: "除了停止，它們還用於重置對話、快速識別yes或no這類確認回應" – plain spoken sentences, no paths or code. The only flaw is the garbled "正在匹配" at [00:52]. |  |
| phone-1004-what-did-we-discuss-first | no_hallucination | fail | pass | Sheepdog: "we also spun up a Claude agent for the KVoice repository. It analyzed the codebase and reported that KVoice is a native Apple Silicon Mac menu bar dictation tool" The KVoice agent and its report are in the facts. The timing error is charged to facts_current. |  |
| phone-1002-why-becomes-new-task | ear_friendly | fail | pass | Sheepdog (voice): "结果还没出来,一出我就告诉你。" — plain, short spoken lines, nothing screen-only. |  |
