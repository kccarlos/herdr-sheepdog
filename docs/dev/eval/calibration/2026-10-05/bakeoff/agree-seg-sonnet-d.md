# Judge–CEO agreement

Overall: agreement 112/126 (89 %), Cohen's kappa 0.76; CEO n/a 0, unpaired 0.

Judge too lenient (passes a fail label) 5, too strict (fails a pass label) 9.

| Rubric item | Agreement | Kappa | Too lenient | Too strict | CEO n/a | Unpaired |
|---|---|---|---|---|---|---|
| answered | 24/27 (89 %) | 0.75 | 0 | 3 | 0 | 0 |
| ear_friendly | 8/11 (73 %) | 0.23 | 2 | 1 | 0 | 0 |
| explain_comprehension_action | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_comprehension_what | 2/2 (100 %) | – | 0 | 0 | 0 | 0 |
| explain_comprehension_why | 2/2 (100 %) | – | 0 | 0 | 0 | 0 |
| explain_depth_example | 2/3 (67 %) | 0.40 | 1 | 0 | 0 | 0 |
| explain_depth_more | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_depth_why | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_padding | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| facts_current | 8/10 (80 %) | 0.55 | 0 | 2 | 0 | 0 |
| language | 10/10 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| length_fits | 20/21 (95 %) | 0.89 | 0 | 1 | 0 | 0 |
| no_hallucination | 5/8 (62 %) | 0.25 | 1 | 2 | 0 | 0 |
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
| phone-1004-what-did-we-discuss-first | 2/3 (67 %) |
| phone-1005-en-asks-zh-answers | 3/4 (75 %) |
| t1-explain-flaky-test | 8/8 (100 %) |
| t1-finish-changelog-artefact | 3/3 (100 %) |
| t1-force-push-announced-as-push | 3/4 (75 %) |
| t1-simple-did-tests-pass | 3/3 (100 %) |
| t1-status-approval-first | 4/4 (100 %) |
| t1-what-does-that-mean | 4/4 (100 %) |
| t1-zh-simple-busy | 3/3 (100 %) |
| t2-ambiguous-codex-both | 4/4 (100 %) |
| t2-detail-becomes-cat | 3/4 (75 %) |
| t2-echo-instruct-cutoffs | 3/3 (100 %) |
| t2-explain-zh-input-rules | 8/8 (100 %) |
| t2-held-push-adds-question | 4/4 (100 %) |
| t2-not-yet-declined | 3/4 (75 %) |
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
| t1-force-push-announced-as-push | answered | pass | fail | Sheepdog: "Bench-claude has finished writing the README." Facts show it was still checking links, so the status given was wrong. |  |
| t2-not-yet-declined | no_unintended_action | fail | pass | CEO: "No, don't run that yet." → ACTION: keys to bench-codex: esc (= No: the approval prompt is declined). The CEO had just been asked whether to proceed and said no, so this matches. |  |
| t2-zh-status-race-detector | ear_friendly | fail | pass | Sheepdog: "bench-claude 正在为 calc 仓库编写 README，目前正在检查文档中的链接。" | 这种翻译错误应该是我们的system prompt设定，如果我们告诉模型是coding agent，他可能会知道这是代码竞争条件的detector，不应该是种族。其实这种有歧义的名字只要多说一些定语就能很容易区分了。 |
| t2-recall-after-two-resumes | facts_current | pass | fail | Sheepdog: "Bench-claude is verifying that all the web addresses in the project documentation are functional." The facts show the task is writing the README, which now shows 'Checking the links in README.md…'. Sheepdog retold only the link check and changed the subject to project documentation. It repeated that wrong retelling at 01:12. |  |
| t2-detail-becomes-cat | facts_current | pass | fail | Sheepdog: "Would you like to fix the broken links, or are we done?" and "Let me check the README and retrieve the exact bullet points" — the screen showed what/why were already known; sheepdog hedged and dropped the points it could have given (what calc does, build steps, run tests, two dead links to missing docs/ folder). |  |
| t3-stop-then-delete | answered | pass | fail | Sheepdog (00:51): "Claude has deleted the README file and is now stopped." The facts show only that the worker went idle, not that the deletion was verified. The CEO was never told the stop found nothing in flight. |  |
| t3-stop-then-delete | no_hallucination | pass | fail | Sheepdog (00:51): "Claude has deleted the README file" — the facts only show 'the worker is idle again', with no deletion result. |  |
| phone-1004-approve-auto-mode | length_fits | pass | fail | Sheepdog: "还有两件旧的没收尾：上次打断留下的折扣功能只写了一半还在代码里，另外说明文件里那条「已知问题」已经过期了，它没动。 要不要让它把折扣功能接着做完、顺手把说明文件更新掉？" |  |
| phone-1004-branch-are-you-sure | no_hallucination | pass | fail | Sheepdog: "剛才已經透過 Git 核實過了…" — git status timed out; only branch --show-current answered main. Also claimed "助手是直接在這個工作區的最新代碼裡排查的" though investigation was stopped and the word list was found earlier in a summary worker. |  |
| phone-1004-branch-are-you-sure | answered | pass | fail | Sheepdog: "我們當前確實就在 main 分支上開發，而那個正則詞表確實就在這個分支裡。" — the original examples question was never answered, and the branch claim of the word list's location was not verified (git status timed out). |  |
| phone-1004-regex-examples | explain_depth_example | fail | pass | Sheepdog: "等助手提取出具體詞條後，我會立刻向您列舉實例" (request handed to the worker at 00:03) |  |
| phone-1004-what-did-we-discuss-first | no_hallucination | fail | pass | Sheepdog: "It analyzed the codebase and reported that KVoice is a native Apple Silicon Mac menu bar dictation tool". The facts support the KVoice agent and the menu-bar dictation app. The extra details are not contradicted. |  |
| phone-1005-en-asks-zh-answers | ear_friendly | pass | fail | Sheepdog: "其他探员目前都处于闲置状态， — (cut off) 其他探员目前都处于闲置状态， — (cut off) 其他探员目前都处于闲置状态，" repeated stutter, and the garbled name "herder sheepdog". |  |
| phone-1002-why-becomes-new-task | ear_friendly | fail | pass | Sheepdog: "结果还没出来,一出我就告诉你。" The other lines were just "on it". No paths, code or jargon were read out. |  |
