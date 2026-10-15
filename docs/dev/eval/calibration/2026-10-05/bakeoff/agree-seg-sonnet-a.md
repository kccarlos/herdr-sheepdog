# Judge–CEO agreement

Overall: agreement 108/126 (86 %), Cohen's kappa 0.69; CEO n/a 0, unpaired 0.

Judge too lenient (passes a fail label) 6, too strict (fails a pass label) 12.

| Rubric item | Agreement | Kappa | Too lenient | Too strict | CEO n/a | Unpaired |
|---|---|---|---|---|---|---|
| answered | 22/27 (81 %) | 0.58 | 1 | 4 | 0 | 0 |
| ear_friendly | 8/11 (73 %) | 0.00 | 3 | 0 | 0 | 0 |
| explain_comprehension_action | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_comprehension_what | 2/2 (100 %) | – | 0 | 0 | 0 | 0 |
| explain_comprehension_why | 2/2 (100 %) | – | 0 | 0 | 0 | 0 |
| explain_depth_example | 3/3 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_depth_more | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_depth_why | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_padding | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| facts_current | 7/10 (70 %) | 0.40 | 0 | 3 | 0 | 0 |
| language | 9/10 (90 %) | 0.74 | 0 | 1 | 0 | 0 |
| length_fits | 18/21 (86 %) | 0.67 | 1 | 2 | 0 | 0 |
| no_hallucination | 6/8 (75 %) | 0.53 | 0 | 2 | 0 | 0 |
| no_unintended_action | 17/18 (94 %) | 0.87 | 1 | 0 | 0 | 0 |
| right_agent | 6/6 (100 %) | 1.00 | 0 | 0 | 0 | 0 |

| Item | Agreement |
|---|---|
| phone-1002-why-becomes-new-task | 3/4 (75 %) |
| phone-1004-approve-auto-mode | 3/4 (75 %) |
| phone-1004-branch-are-you-sure | 2/4 (50 %) |
| phone-1004-regex-examples | 4/5 (80 %) |
| phone-1004-rewind-wrong-agent | 4/4 (100 %) |
| phone-1004-status-en-answered-zh | 5/5 (100 %) |
| phone-1004-stop-after-finish | 4/5 (80 %) |
| phone-1004-what-did-we-discuss-first | 3/3 (100 %) |
| phone-1005-en-asks-zh-answers | 3/4 (75 %) |
| t1-explain-flaky-test | 8/8 (100 %) |
| t1-finish-changelog-artefact | 3/3 (100 %) |
| t1-force-push-announced-as-push | 3/4 (75 %) |
| t1-simple-did-tests-pass | 3/3 (100 %) |
| t1-status-approval-first | 4/4 (100 %) |
| t1-what-does-that-mean | 4/4 (100 %) |
| t1-zh-simple-busy | 3/3 (100 %) |
| t2-ambiguous-codex-both | 4/4 (100 %) |
| t2-detail-becomes-cat | 4/4 (100 %) |
| t2-echo-instruct-cutoffs | 3/3 (100 %) |
| t2-explain-zh-input-rules | 8/8 (100 %) |
| t2-held-push-adds-question | 4/4 (100 %) |
| t2-not-yet-declined | 2/4 (50 %) |
| t2-recall-after-two-resumes | 2/4 (50 %) |
| t2-rename-docs-artefact | 4/4 (100 %) |
| t2-zh-deferral-force-push | 2/4 (50 %) |
| t2-zh-status-race-detector | 3/4 (75 %) |
| t3-approved-on-question | 3/4 (75 %) |
| t3-close-docs-artefact | 4/4 (100 %) |
| t3-fix-approve-artefact | 4/4 (100 %) |
| t3-stop-then-delete | 2/4 (50 %) |

## Disagreements

| Item | Rubric item | CEO | Judge | Judge's evidence | CEO's note |
|---|---|---|---|---|---|
| t1-force-push-announced-as-push | answered | pass | fail | Sheepdog: "Bench-claude has finished writing the README." The facts show it was still checking links, so the answer to the question was wrong. |  |
| t2-not-yet-declined | no_unintended_action | fail | pass | CEO: "No, don't run that yet." → ACTION: keys to bench-codex: esc (declines the approval prompt). It matches the CEO's answer to sheepdog's question, and no other worker action was sent. |  |
| t2-not-yet-declined | facts_current | pass | fail | Sheepdog: "Bench-claude has finished thirty-eight —". The facts show bench-claude had written 38 lines and was still checking links, so "finished" misstates its status. |  |
| t2-zh-status-race-detector | ear_friendly | fail | pass | Sheepdog: "bench-claude 正在为 calc 仓库编写 README，目前正在检查文档中的链接。" — plain spoken sentences, no paths or code. | 这种翻译错误应该是我们的system prompt设定，如果我们告诉模型是coding agent，他可能会知道这是代码竞争条件的detector，不应该是种族。其实这种有歧义的名字只要多说一些定语就能很容易区分了。 |
| t2-recall-after-two-resumes | answered | fail | pass | Sheepdog: "Bench-claude is verifying that all the web addresses in the project documentation are functional." The CEO got both workers' current activity, and the recap matched what had been said. |  |
| t2-recall-after-two-resumes | facts_current | pass | fail | CEO: "And what is bench-claude writing?" Sheepdog: "verifying that all the web addresses in the project documentation are functional." It never says the worker was asked to write a README and has now moved to checking its links. The retold facts drift from the source. |  |
| t2-zh-deferral-force-push | language | pass | fail | Sheepdog [00:02]: "bench-codex has finished squashing the history and is asking to force push..." in English, while the CEO speaks Chinese. |  |
| t2-zh-deferral-force-push | length_fits | pass | fail | Sheepdog [00:16]: "我们是否应该允许它覆盖？可以选择“同意”，“同意并不再询问”，或者“取消并指示其他操作”。" restates the question and lists three options, repeating the earlier announcement. |  |
| t3-approved-on-question | answered | pass | fail | Sheepdog: "I've approved the Go tests for Claude. It is running them now." - never answered what it was waiting for; it acted instead. |  |
| t3-stop-then-delete | answered | pass | fail | Sheepdog (00:51): "Claude has deleted the README file and is now stopped." The facts show only that the worker went idle. Sheepdog never told the CEO that the stop found nothing in flight because the README was already written. |  |
| t3-stop-then-delete | no_hallucination | pass | fail | Sheepdog (00:51): "Claude has deleted the README file". The facts show only that the worker went idle at 00:45, with no evidence the file was deleted. |  |
| phone-1004-approve-auto-mode | length_fits | pass | fail | Sheepdog [00:36]: "修好了，整套检查两个全过……还有两件旧的没收尾：上次打断留下的折扣功能……另外说明文件里那条「已知问题」已经过期了" — the completion announcement adds extra background and a two-part offer beyond a short report; also the [00:22] line explains auto mode at length for a simple confirmation. |  |
| phone-1004-stop-after-finish | facts_current | pass | fail | Sheepdog: "它还问了后面要不要做退货那块". The facts show no returns question from claude. They list only the rounding and the README line as open. |  |
| phone-1004-branch-are-you-sure | no_hallucination | pass | fail | Sheepdog: "我們當前確實就在 main 分支上開發，而那個正則詞表確實就在這個分支裡。" Facts: only `git branch --show-current` answered 'main'; all three `git status` checks timed out, so nothing verified where the word list was found. Also "助手是直接在這個工作區的最新代碼裡排查的" is unsupported, since the investigation was stopped. |  |
| phone-1004-branch-are-you-sure | answered | pass | fail | Sheepdog: "我非常確定，剛才已經透過 Git 核實過了…那個正則詞表確實就在這個分支裡。" The branch check only showed the current branch is main; it did not verify where the list was found, and `git status` timed out. The question was treated as settled when it was not. |  |
| phone-1004-regex-examples | ear_friendly | fail | pass | Sheepdog (01:46): "除了停止，它們還用於重置對話、快速識別yes或no這類確認回應…" — plain spoken sentences with no paths or code. |  |
| phone-1005-en-asks-zh-answers | length_fits | fail | pass | Sheepdog: "这些规则主要用于匹配各种语气词以跳过 AI 判断，但不同文件中的词表不一致，容易造成混乱。您希望让代理清理并重构这部分，还是先保持现状？" |  |
| phone-1002-why-becomes-new-task | ear_friendly | fail | pass | Sheepdog: "结果还没出来,一出我就告诉你。" The other lines are just "on it". They are short and plain, with no jargon. |  |
