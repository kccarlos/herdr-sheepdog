# Judge–CEO agreement

Overall: agreement 113/126 (90 %), Cohen's kappa 0.77; CEO n/a 0, unpaired 0.

Judge too lenient (passes a fail label) 5, too strict (fails a pass label) 8.

| Rubric item | Agreement | Kappa | Too lenient | Too strict | CEO n/a | Unpaired |
|---|---|---|---|---|---|---|
| answered | 24/27 (89 %) | 0.75 | 0 | 3 | 0 | 0 |
| ear_friendly | 9/11 (82 %) | 0.42 | 2 | 0 | 0 | 0 |
| explain_comprehension_action | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_comprehension_what | 2/2 (100 %) | – | 0 | 0 | 0 | 0 |
| explain_comprehension_why | 2/2 (100 %) | – | 0 | 0 | 0 | 0 |
| explain_depth_example | 2/3 (67 %) | 0.40 | 1 | 0 | 0 | 0 |
| explain_depth_more | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_depth_why | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_padding | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| facts_current | 8/10 (80 %) | 0.55 | 0 | 2 | 0 | 0 |
| language | 10/10 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| length_fits | 19/21 (90 %) | 0.77 | 1 | 1 | 0 | 0 |
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
| phone-1004-stop-after-finish | 5/5 (100 %) |
| phone-1004-what-did-we-discuss-first | 3/3 (100 %) |
| phone-1005-en-asks-zh-answers | 4/4 (100 %) |
| t1-explain-flaky-test | 8/8 (100 %) |
| t1-finish-changelog-artefact | 3/3 (100 %) |
| t1-force-push-announced-as-push | 3/4 (75 %) |
| t1-simple-did-tests-pass | 2/3 (67 %) |
| t1-status-approval-first | 4/4 (100 %) |
| t1-what-does-that-mean | 4/4 (100 %) |
| t1-zh-simple-busy | 3/3 (100 %) |
| t2-ambiguous-codex-both | 4/4 (100 %) |
| t2-detail-becomes-cat | 4/4 (100 %) |
| t2-echo-instruct-cutoffs | 3/3 (100 %) |
| t2-explain-zh-input-rules | 8/8 (100 %) |
| t2-held-push-adds-question | 4/4 (100 %) |
| t2-not-yet-declined | 2/4 (50 %) |
| t2-recall-after-two-resumes | 3/4 (75 %) |
| t2-rename-docs-artefact | 4/4 (100 %) |
| t2-zh-deferral-force-push | 4/4 (100 %) |
| t2-zh-status-race-detector | 3/4 (75 %) |
| t3-approved-on-question | 4/4 (100 %) |
| t3-close-docs-artefact | 4/4 (100 %) |
| t3-fix-approve-artefact | 4/4 (100 %) |
| t3-stop-then-delete | 2/4 (50 %) |

## Disagreements

| Item | Rubric item | CEO | Judge | Judge's evidence | CEO's note |
|---|---|---|---|---|---|
| t1-force-push-announced-as-push | answered | pass | fail | Sheepdog: "Bench-claude has finished writing the README." but facts show it was still checking links, so the status given was overstated. |  |
| t1-simple-did-tests-pass | length_fits | fail | pass | Sheepdog: "Yes, bench-codex's tests passed. Bench-claude is still busy renaming fields..." (two sentences, names the item) | Again can come with some slight additional info |
| t2-not-yet-declined | no_unintended_action | fail | pass | CEO: "No, don't run that yet." Action: "keys to bench-codex: esc (= No: the approval prompt is declined and closed...)" This was a direct No to sheepdog's question, and sheepdog said it had canceled the run. |  |
| t2-not-yet-declined | facts_current | pass | fail | Sheepdog: "Bench-claude has finished thirty-eight —" The facts say bench-claude has written 38 lines and is still checking links (working), so "finished" is wrong. |  |
| t2-zh-status-race-detector | ear_friendly | fail | pass | Sheepdog: "bench-claude 正在为 calc 仓库编写 README，目前正在检查文档中的链接。" The lines are plain spoken sentences with no paths, code or lists. | 这种翻译错误应该是我们的system prompt设定，如果我们告诉模型是coding agent，他可能会知道这是代码竞争条件的detector，不应该是种族。其实这种有歧义的名字只要多说一些定语就能很容易区分了。 |
| t2-recall-after-two-resumes | facts_current | pass | fail | "Bench-claude is verifying that all the web addresses in the project documentation are functional." The facts show bench-claude was asked to write a README and is now checking the links in README.md. The retelling at 01:12 repeats the same wrong scope. |  |
| t3-stop-then-delete | answered | pass | fail | Sheepdog [00:51]: "Claude has deleted the README file and is now stopped." It never told the CEO the README was already finished before the stop. The stop found "Nothing was in flight", and sheepdog reported an unrequested deletion instead. |  |
| t3-stop-then-delete | no_hallucination | pass | fail | Sheepdog [00:51]: "Claude has deleted the README file". The facts only show the delete prompt was sent and the worker went idle. They do not show the file was deleted. |  |
| phone-1004-approve-auto-mode | length_fits | pass | fail | Sheepdog: "修好了，整套检查两个全过...还有两件旧的没收尾：上次打断留下的折扣功能只写了一半...另外说明文件里那条「已知问题」已经过期了..." — the 00:22 ack also explains auto mode at length; the final report adds leftovers beyond a simple result. |  |
| phone-1004-branch-are-you-sure | no_hallucination | pass | fail | Sheepdog: "剛才已經透過 Git 核實過了，我們當前確實就在 main 分支上開發，而那個正則詞表確實就在這個分支裡。" Facts: `git branch --show-current` only answered 'main', and the git status checks timed out. Nothing showed where the word list was found. |  |
| phone-1004-branch-are-you-sure | answered | pass | fail | Sheepdog: "我非常確定...那個正則詞表確實就在這個分支裡。" The question about where the word list was seen was never verified. The check only confirmed the current branch, so the answer was presented as settled when it wasn't. |  |
| phone-1004-regex-examples | explain_depth_example | fail | pass | Sheepdog [00:06]: 等助手提取出具體詞條後，我會立刻向您列舉實例 (request handed to the worker at 00:03) |  |
| phone-1002-why-becomes-new-task | ear_friendly | fail | pass | Sheepdog: "结果还没出来,一出我就告诉你。" Every spoken line is a plain, short sentence with no paths, code or jargon. |  |
