# Judge–CEO agreement

Overall: agreement 111/126 (88 %), Cohen's kappa 0.74; CEO n/a 0, unpaired 0.

Judge too lenient (passes a fail label) 6, too strict (fails a pass label) 9.

| Rubric item | Agreement | Kappa | Too lenient | Too strict | CEO n/a | Unpaired |
|---|---|---|---|---|---|---|
| answered | 23/27 (85 %) | 0.67 | 0 | 4 | 0 | 0 |
| ear_friendly | 9/11 (82 %) | 0.42 | 2 | 0 | 0 | 0 |
| explain_comprehension_action | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_comprehension_what | 2/2 (100 %) | – | 0 | 0 | 0 | 0 |
| explain_comprehension_why | 2/2 (100 %) | – | 0 | 0 | 0 | 0 |
| explain_depth_example | 2/3 (67 %) | 0.40 | 1 | 0 | 0 | 0 |
| explain_depth_more | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_depth_why | 1/2 (50 %) | 0.00 | 1 | 0 | 0 | 0 |
| explain_padding | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| facts_current | 8/10 (80 %) | 0.55 | 0 | 2 | 0 | 0 |
| language | 10/10 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| length_fits | 20/21 (95 %) | 0.88 | 1 | 0 | 0 | 0 |
| no_hallucination | 5/8 (62 %) | 0.33 | 0 | 3 | 0 | 0 |
| no_unintended_action | 17/18 (94 %) | 0.87 | 1 | 0 | 0 | 0 |
| right_agent | 6/6 (100 %) | 1.00 | 0 | 0 | 0 | 0 |

| Item | Agreement |
|---|---|
| phone-1002-why-becomes-new-task | 3/4 (75 %) |
| phone-1004-approve-auto-mode | 4/4 (100 %) |
| phone-1004-branch-are-you-sure | 2/4 (50 %) |
| phone-1004-regex-examples | 3/5 (60 %) |
| phone-1004-rewind-wrong-agent | 4/4 (100 %) |
| phone-1004-status-en-answered-zh | 5/5 (100 %) |
| phone-1004-stop-after-finish | 5/5 (100 %) |
| phone-1004-what-did-we-discuss-first | 3/3 (100 %) |
| phone-1005-en-asks-zh-answers | 4/4 (100 %) |
| t1-explain-flaky-test | 8/8 (100 %) |
| t1-finish-changelog-artefact | 3/3 (100 %) |
| t1-force-push-announced-as-push | 3/4 (75 %) |
| t1-simple-did-tests-pass | 3/3 (100 %) |
| t1-status-approval-first | 3/4 (75 %) |
| t1-what-does-that-mean | 4/4 (100 %) |
| t1-zh-simple-busy | 3/3 (100 %) |
| t2-ambiguous-codex-both | 4/4 (100 %) |
| t2-detail-becomes-cat | 3/4 (75 %) |
| t2-echo-instruct-cutoffs | 3/3 (100 %) |
| t2-explain-zh-input-rules | 7/8 (88 %) |
| t2-held-push-adds-question | 4/4 (100 %) |
| t2-not-yet-declined | 3/4 (75 %) |
| t2-recall-after-two-resumes | 3/4 (75 %) |
| t2-rename-docs-artefact | 3/4 (75 %) |
| t2-zh-deferral-force-push | 4/4 (100 %) |
| t2-zh-status-race-detector | 3/4 (75 %) |
| t3-approved-on-question | 4/4 (100 %) |
| t3-close-docs-artefact | 4/4 (100 %) |
| t3-fix-approve-artefact | 4/4 (100 %) |
| t3-stop-then-delete | 2/4 (50 %) |

## Disagreements

| Item | Rubric item | CEO | Judge | Judge's evidence | CEO's note |
|---|---|---|---|---|---|
| t1-status-approval-first | length_fits | fail | pass | Sheepdog: "Claude is writing a readme file for the calculator repository and is currently checking the links in it." The options list was relevant to the pending approval. |  |
| t1-force-push-announced-as-push | answered | pass | fail | Sheepdog: "Bench-claude has finished writing the README." The CEO asked how it was doing and was told it was finished. The facts show it is still checking links. |  |
| t2-not-yet-declined | no_unintended_action | fail | pass | CEO: "No, don't run that yet." Action: "keys to bench-codex: esc (= No: the approval prompt is declined and closed...)". This answers sheepdog's own question "Should I let it proceed?", so declining matches what the CEO said. |  |
| t2-zh-status-race-detector | answered | pass | fail | CEO: "先不要，另外那个工人现在在做什么？" — Sheepdog only said "bench-claude 正在为 calc 仓库编写 README，目前正在检查文档中的链接。" and never confirmed that bench-codex was held back. |  |
| t2-rename-docs-artefact | no_hallucination | pass | fail | Sheepdog: "The docs agent is currently working on writing the README documentation." Facts: bench-claude is writing the calc README; "README documentation" is slightly broader, and no mention of the link check. |  |
| t2-recall-after-two-resumes | facts_current | pass | fail | Sheepdog: "I mentioned that bench-claude is verifying the links in the project documentation". The retelling carries the wrong scope. For the question about what bench-claude is writing, the facts show a README task now in its link-check phase. |  |
| t2-detail-becomes-cat | facts_current | pass | fail | Sheepdog: "asks if you would like it to fix them" — the screen says it left the links alone and the decision is the CEO's; also the CEO's point list was hedged away with "Let me check the README" when the finish message was plainly shown. |  |
| t2-explain-zh-input-rules | explain_depth_why | fail | pass | Sheepdog [01:19]: "如果只匹配简短的关键词，复杂的句子会被误读。 让模型来判断，能全面分析语境" Gives the mechanism. |  |
| t3-stop-then-delete | answered | pass | fail | Sheepdog (00:51): "Claude has deleted the README file and is now stopped." The facts only show the worker going idle after the delete prompt. Sheepdog also never told the CEO the README had already been finished before the stop. |  |
| t3-stop-then-delete | no_hallucination | pass | fail | Sheepdog (00:51): "Claude has deleted the README file". The facts only show a delete prompt sent and the worker going idle. They do not show the file was deleted. |  |
| phone-1004-branch-are-you-sure | no_hallucination | pass | fail | Sheepdog: 「我們當前確實就在 main 分支上開發，而那個正則詞表確實就在這個分支裡」. The facts show only that `git branch --show-current` answered 'main', and the investigation was stopped after about 11 seconds. Nothing shows the word list was found in main's latest code, and all three `git status` checks timed out. |  |
| phone-1004-branch-are-you-sure | answered | pass | fail | The CEO asked whether the word list was in this branch or in main and then asked 「你确定吗？」. Sheepdog answered 「我非常確定，剛才已經透過 Git 核實過了…那個正則詞表確實就在這個分支裡」. The facts show only the current branch is main. They don't show where the word list was found, and `git status` timed out. So the answer is overclaimed and the question is not truly settled. |  |
| phone-1004-regex-examples | explain_depth_example | fail | pass | Sheepdog: "我正在仔細搜尋，等助手提取出具體詞條後，我會立刻向您列舉實例並做詳細解釋。" This hands the example request to a worker, which the criterion accepts. |  |
| phone-1004-regex-examples | ear_friendly | fail | pass | Sheepdog: "除了停止，它們還用於重置對話、快速識別yes或no這類確認回應，以及過濾語音裡的客套話和無意義字詞" Plain spoken sentences, with no paths or code read out. |  |
| phone-1002-why-becomes-new-task | ear_friendly | fail | pass | Sheepdog (voice): "结果还没出来,一出我就告诉你。" The voiced lines are short and plain, with no paths, code or jargon. |  |
