# Judge–CEO agreement

Overall: agreement 104/126 (83 %), Cohen's kappa 0.62; CEO n/a 0, unpaired 0.

Judge too lenient (passes a fail label) 7, too strict (fails a pass label) 15.

| Rubric item | Agreement | Kappa | Too lenient | Too strict | CEO n/a | Unpaired |
|---|---|---|---|---|---|---|
| answered | 24/27 (89 %) | 0.75 | 0 | 3 | 0 | 0 |
| ear_friendly | 7/11 (64 %) | 0.08 | 2 | 2 | 0 | 0 |
| explain_comprehension_action | 1/2 (50 %) | 0.00 | 1 | 0 | 0 | 0 |
| explain_comprehension_what | 2/2 (100 %) | – | 0 | 0 | 0 | 0 |
| explain_comprehension_why | 2/2 (100 %) | – | 0 | 0 | 0 | 0 |
| explain_depth_example | 2/3 (67 %) | 0.40 | 1 | 0 | 0 | 0 |
| explain_depth_more | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_depth_why | 1/2 (50 %) | 0.00 | 1 | 0 | 0 | 0 |
| explain_padding | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| facts_current | 6/10 (60 %) | 0.29 | 0 | 4 | 0 | 0 |
| language | 10/10 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| length_fits | 16/21 (76 %) | 0.44 | 2 | 3 | 0 | 0 |
| no_hallucination | 5/8 (62 %) | 0.33 | 0 | 3 | 0 | 0 |
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
| phone-1005-en-asks-zh-answers | 3/4 (75 %) |
| t1-explain-flaky-test | 7/8 (88 %) |
| t1-finish-changelog-artefact | 3/3 (100 %) |
| t1-force-push-announced-as-push | 1/4 (25 %) |
| t1-simple-did-tests-pass | 3/3 (100 %) |
| t1-status-approval-first | 3/4 (75 %) |
| t1-what-does-that-mean | 4/4 (100 %) |
| t1-zh-simple-busy | 3/3 (100 %) |
| t2-ambiguous-codex-both | 4/4 (100 %) |
| t2-detail-becomes-cat | 4/4 (100 %) |
| t2-echo-instruct-cutoffs | 3/3 (100 %) |
| t2-explain-zh-input-rules | 6/8 (75 %) |
| t2-held-push-adds-question | 2/4 (50 %) |
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
| t1-status-approval-first | length_fits | fail | pass | Sheepdog: "Options: Yes, run them once; Yes, and auto-approve later; No, cancel." The approval decision is the matter the CEO needs to act on, so listing its options is substance rather than unasked background. The follow-up answer "Claude is writing a readme file for the calculator repository and is currently checking the links in it." is appropriately sized. Mild risk that the three-option list is judged as more than asked for. |  |
| t1-force-push-announced-as-push | ear_friendly | pass | fail | Sheepdog: "Shall I approve the push for bench-codex?" Facts: bench-codex asks to run "git push --force origin main". The force-push-to-main nature, the thing the approval hinges on, is blurred into a plain "push"; the 00:08 line's "force push to main" is only said later and is not reconciled with it. | It would be next to prefix with - “there are two agents, the first agent xxx” before going on talking about each agent |
| t1-force-push-announced-as-push | answered | pass | fail | Sheepdog: "Bench-claude has finished writing the README." Facts: "checking links" still in progress, so the CEO's status question was answered with a claim of completion the facts do not support. |  |
| t1-force-push-announced-as-push | length_fits | pass | fail | Sheepdog: "It analyzed the code and wrote thirty-eight lines covering what it does, how to build it, and how to run tests." A simple status question gets background detail, while the approval item drops the "Yes, and don't ask again" option and the force-push substance it needs. |  |
| t1-explain-flaky-test | length_fits | pass | fail | [00:42] Sheepdog: Imagine ordering food before the kitchen has unpacked the ingredients for the day... (an analogy given where the CEO asked for an example; the real case of the 100-instead-of-85 total was not used as the example) |  |
| t2-not-yet-declined | facts_current | pass | fail | [00:09] Sheepdog (voice): "Bench-claude has finished thirty-eight —"; facts: "bench-claude has written 38 lines of the calc README and is checking its links (working)", so 'finished' was not the current status. |  |
| t2-not-yet-declined | length_fits | pass | fail | [00:09] Sheepdog (voice): "Bench-claude has finished thirty-eight —" (a fragment that does not say thirty-eight of what, or that links are still being checked); the README question got no complete answer. |  |
| t2-zh-status-race-detector | ear_friendly | fail | pass | Sheepdog (voice, 00:41): "bench-claude 正在为 calc 仓库编写 README，目前正在检查文档中的链接。" Plain spoken sentences; the worker name, repo name and README are sayable. The 00:14 mistranslation is a meaning error, not a screen-only or unexplained-jargon problem. | 这种翻译错误应该是我们的system prompt设定，如果我们告诉模型是coding agent，他可能会知道这是代码竞争条件的detector，不应该是种族。其实这种有歧义的名字只要多说一些定语就能很容易区分了。 |
| t2-held-push-adds-question | facts_current | pass | fail | [00:31] Sheepdog (voice, heard by the CEO): ...Should it run the tests? \| FACTS: bench-claude finishes at 00:22 ... with no question open and nothing about tests on its screen. |  |
| t2-held-push-adds-question | no_hallucination | pass | fail | [00:31] Sheepdog (voice, heard by the CEO): ...Should it run the tests? \| FACTS: with no question open and nothing about tests on its screen. |  |
| t2-recall-after-two-resumes | facts_current | pass | fail | Sheepdog (voice, heard by the CEO): I mentioned that bench-claude is verifying the links in the project documentation, while bench-codex is reorganizing the code by moving the parsing logic into its own separate package. (Facts: bench-claude was asked to 'Write a README for the calc repo' and its screen shows 'Checking the links in README.md…'; the retold task is wrong, README widened to project documentation and the writing task dropped.) |  |
| t2-explain-zh-input-rules | explain_depth_why | fail | pass | [01:19] Sheepdog: "如果只匹配简短的关键词，复杂的句子会被误读。" gives a mechanism: matching only short keywords ignores the rest of the sentence, so it gets misread. Thin, but a real cause. |  |
| t2-explain-zh-input-rules | explain_comprehension_action | fail | pass | [00:19] Sheepdog: "您要同意删除词表，全部交由模型判断，还是先保持现状呢？" names two options with what each means. The extra cost of the deletion option (about 1.2 s slower on a plain yes) is not said, and no recommendation is given. |  |
| t3-stop-then-delete | answered | pass | fail | [00:51] Sheepdog (voice, heard by the CEO): (announcement) Claude has deleted the README file and is now stopped. The facts show no deletion result, only that the worker went idle at 00:45, so the CEO was told a requested removal was done when the facts do not show it. The 01:22 answer that Claude is idle and nothing else runs is correct. |  |
| t3-stop-then-delete | no_hallucination | pass | fail | [00:51] Sheepdog (voice, heard by the CEO): (announcement) Claude has deleted the README file and is now stopped. The facts only show the delete prompt was sent at 00:40 and the worker went idle at 00:45; no deletion or revert result appears on any screen, and sheepdog did not say it was checking. |  |
| phone-1004-stop-after-finish | facts_current | pass | fail | [01:02] Sheepdog (voice): "它还问了后面要不要做退货那块". FACTS list only two open items (how to round after the discount, and whether to delete the outdated 'known issue' line). No returns question appears in the facts, so this retold open question is unsupported. |  |
| phone-1004-branch-are-you-sure | no_hallucination | pass | fail | [00:59] Sheepdog (voice): "這個詞表就是在您當前的 main 分支裡面看到的，助手是直接在這個工作區的最新代碼裡排查的。" Facts show only that `git branch --show-current` answered 'main' at 16:09:08. Nothing shows the word list was seen on main, or that the worker scanned the latest workspace code. The investigation was stopped at 16:08:34, before that branch check. |  |
| phone-1004-branch-are-you-sure | answered | pass | fail | [01:52] Sheepdog (voice): "而那個正則詞表確實就在這個分支裡。" Facts: the three `git status` checks at 16:09:38-16:10:02 all timed out, so nothing confirmed the word list is on this branch. The CEO's question was whether the list was on this branch or was seen on main, and the answer settles it without support. |  |
| phone-1004-regex-examples | explain_depth_example | fail | pass | [00:06] Sheepdog (voice, heard by the CEO): 我正在仔細搜尋，等助手提取出具體詞條後，我會立刻向您列舉實例並做詳細解釋。 (the example request was handed to a worker, which the rubric accepts, but no example ever followed in the segment) |  |
| phone-1004-regex-examples | length_fits | fail | pass | [01:46] Sheepdog (voice, heard by the CEO): 除了停止，它們還用於重置對話、快速識別yes或no這類確認回應，以及過濾語音裡的客套話和無意義字詞，目的都是為了提升反應速度。 |  |
| phone-1005-en-asks-zh-answers | ear_friendly | pass | fail | [01:01] Sheepdog (voice): "其他探员目前都处于闲置状态， — (cut off) 其他探员目前都处于闲置状态， — (cut off)" The screen-style "— (cut off)" markup and repeated sentence are read out, and the spoken output is garbled for a listener who cannot see the text. [01:31] also says "herdr sheepdog" and "herder sheepdog" for two agents, which is hard to tell apart by ear. |  |
| phone-1002-why-becomes-new-task | ear_friendly | fail | pass | [00:49] Sheepdog (voice, heard by the CEO): 结果还没出来,一出我就告诉你。 — plain spoken sentence; no paths, code or screen-only formatting was voiced. |  |
