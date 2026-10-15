# Judge–CEO agreement

Overall: agreement 110/126 (87 %), Cohen's kappa 0.72; CEO n/a 0, unpaired 0.

Judge too lenient (passes a fail label) 6, too strict (fails a pass label) 10.

| Rubric item | Agreement | Kappa | Too lenient | Too strict | CEO n/a | Unpaired |
|---|---|---|---|---|---|---|
| answered | 23/27 (85 %) | 0.65 | 1 | 3 | 0 | 0 |
| ear_friendly | 8/11 (73 %) | 0.00 | 3 | 0 | 0 | 0 |
| explain_comprehension_action | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_comprehension_what | 2/2 (100 %) | – | 0 | 0 | 0 | 0 |
| explain_comprehension_why | 2/2 (100 %) | – | 0 | 0 | 0 | 0 |
| explain_depth_example | 2/3 (67 %) | 0.40 | 1 | 0 | 0 | 0 |
| explain_depth_more | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_depth_why | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_padding | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| facts_current | 6/10 (60 %) | 0.29 | 0 | 4 | 0 | 0 |
| language | 9/10 (90 %) | 0.74 | 0 | 1 | 0 | 0 |
| length_fits | 20/21 (95 %) | 0.88 | 1 | 0 | 0 | 0 |
| no_hallucination | 6/8 (75 %) | 0.53 | 0 | 2 | 0 | 0 |
| no_unintended_action | 18/18 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| right_agent | 6/6 (100 %) | 1.00 | 0 | 0 | 0 | 0 |

| Item | Agreement |
|---|---|
| phone-1002-why-becomes-new-task | 3/4 (75 %) |
| phone-1004-approve-auto-mode | 4/4 (100 %) |
| phone-1004-branch-are-you-sure | 2/4 (50 %) |
| phone-1004-regex-examples | 3/5 (60 %) |
| phone-1004-rewind-wrong-agent | 4/4 (100 %) |
| phone-1004-status-en-answered-zh | 5/5 (100 %) |
| phone-1004-stop-after-finish | 4/5 (80 %) |
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
| t2-explain-zh-input-rules | 8/8 (100 %) |
| t2-held-push-adds-question | 4/4 (100 %) |
| t2-not-yet-declined | 3/4 (75 %) |
| t2-recall-after-two-resumes | 2/4 (50 %) |
| t2-rename-docs-artefact | 4/4 (100 %) |
| t2-zh-deferral-force-push | 3/4 (75 %) |
| t2-zh-status-race-detector | 3/4 (75 %) |
| t3-approved-on-question | 4/4 (100 %) |
| t3-close-docs-artefact | 4/4 (100 %) |
| t3-fix-approve-artefact | 4/4 (100 %) |
| t3-stop-then-delete | 2/4 (50 %) |

## Disagreements

| Item | Rubric item | CEO | Judge | Judge's evidence | CEO's note |
|---|---|---|---|---|---|
| t1-status-approval-first | length_fits | fail | pass | Sheepdog: "Claude is writing a readme file for the calculator repository and is currently checking the links in it." |  |
| t1-force-push-announced-as-push | answered | pass | fail | Sheepdog: "Bench-claude has finished writing the README." The facts show it was still checking links, so the status answer was wrong. |  |
| t2-not-yet-declined | facts_current | pass | fail | Sheepdog: "Bench-claude has finished thirty-eight —" The facts say bench-claude had written 38 lines and was still checking links, so it had not finished. |  |
| t2-zh-status-race-detector | ear_friendly | fail | pass | Sheepdog: "bench-claude 正在为 calc 仓库编写 README，目前正在检查文档中的链接。" The lines are plain spoken sentences with no paths, code or lists. | 这种翻译错误应该是我们的system prompt设定，如果我们告诉模型是coding agent，他可能会知道这是代码竞争条件的detector，不应该是种族。其实这种有歧义的名字只要多说一些定语就能很容易区分了。 |
| t2-recall-after-two-resumes | answered | fail | pass | Sheepdog: "Bench-claude is verifying that all the web addresses in the project documentation are functional." Each question got a relevant answer. |  |
| t2-recall-after-two-resumes | facts_current | pass | fail | Sheepdog: "bench-claude is verifying the links in the project documentation" – the README was retold as generic documentation, and the README writing task was never mentioned. |  |
| t2-zh-deferral-force-push | language | pass | fail | [00:02] Sheepdog: "bench-codex has finished squashing the history and is asking to force push the changes. Should we let it overwrite the remote main branch?" This was English, and the CEO then spoke Chinese. |  |
| t2-detail-becomes-cat | facts_current | pass | fail | Sheepdog: "Would you like to fix the broken links, or are we done?" and "The README includes the specific instructions..." — the screen says what calc does, build steps and how to run tests; the first line claimed it "created the README" and said the worker "asks" about fixing, though the worker only left a decision open. The later line implies it was retrieving content that had not arrived. |  |
| t3-stop-then-delete | answered | pass | fail | Sheepdog: "Claude has deleted the README file and is now stopped." The facts show only that the worker went idle after the delete prompt, so the deletion is not confirmed. Sheepdog never told the CEO the stop found nothing in flight because the README was already written. |  |
| t3-stop-then-delete | no_hallucination | pass | fail | Sheepdog: "Claude has deleted the README file and is now stopped." The facts show only the delete prompt being sent and the worker going idle. No screen shows the file was deleted. |  |
| phone-1004-stop-after-finish | facts_current | pass | fail | Sheepdog: "它还问了后面要不要做退货那块". The facts show no returns question from claude. The only open items were the rounding and the README line. |  |
| phone-1004-branch-are-you-sure | no_hallucination | pass | fail | Sheepdog: "剛才已經透過 Git 核實過了…" — facts: git branch answered main, but all three git status checks timed out; also "那個正則詞表確實就在這個分支裡" and "助手是直接在這個工作區的最新代碼裡排查的" though the investigation was stopped after 11s. |  |
| phone-1004-branch-are-you-sure | answered | pass | fail | Sheepdog: "我非常確定，剛才已經透過 Git 核實過了，我們當前確實就在 main 分支上開發，而那個正則詞表確實就在這個分支裡。" — claims verification via git status that timed out; the word list's branch was never verified since the scan was stopped. |  |
| phone-1004-regex-examples | explain_depth_example | fail | pass | Sheepdog (00:06): 我正在仔細搜尋，等助手提取出具體詞條後，我會立刻向您列舉實例並做詳細解釋。 (handed to a worker) |  |
| phone-1004-regex-examples | ear_friendly | fail | pass | Sheepdog (00:52): 用了一千五百字符的正在匹配中英文的停止字詞 — a slip for 正则, but the lines are plain spoken sentences with no paths or code. |  |
| phone-1002-why-becomes-new-task | ear_friendly | fail | pass | Sheepdog: "结果还没出来,一出我就告诉你。" The other lines were the short "on it". All are plain and easy to follow by ear. |  |
