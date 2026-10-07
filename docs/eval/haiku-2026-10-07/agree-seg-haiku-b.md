# Judge–CEO agreement

Overall: agreement 108/126 (86 %), Cohen's kappa 0.69; CEO n/a 0, unpaired 0.

Judge too lenient (passes a fail label) 5, too strict (fails a pass label) 13.

| Rubric item | Agreement | Kappa | Too lenient | Too strict | CEO n/a | Unpaired |
|---|---|---|---|---|---|---|
| answered | 25/27 (93 %) | 0.82 | 0 | 2 | 0 | 0 |
| ear_friendly | 7/11 (64 %) | 0.08 | 2 | 2 | 0 | 0 |
| explain_comprehension_action | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_comprehension_what | 2/2 (100 %) | – | 0 | 0 | 0 | 0 |
| explain_comprehension_why | 2/2 (100 %) | – | 0 | 0 | 0 | 0 |
| explain_depth_example | 3/3 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_depth_more | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_depth_why | 1/2 (50 %) | 0.00 | 1 | 0 | 0 | 0 |
| explain_padding | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| facts_current | 5/10 (50 %) | 0.19 | 0 | 5 | 0 | 0 |
| language | 9/10 (90 %) | 0.74 | 0 | 1 | 0 | 0 |
| length_fits | 19/21 (90 %) | 0.74 | 2 | 0 | 0 | 0 |
| no_hallucination | 5/8 (62 %) | 0.33 | 0 | 3 | 0 | 0 |
| no_unintended_action | 18/18 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| right_agent | 6/6 (100 %) | 1.00 | 0 | 0 | 0 | 0 |

| Item | Agreement |
|---|---|
| phone-1002-why-becomes-new-task | 3/4 (75 %) |
| phone-1004-approve-auto-mode | 4/4 (100 %) |
| phone-1004-branch-are-you-sure | 2/4 (50 %) |
| phone-1004-regex-examples | 5/5 (100 %) |
| phone-1004-rewind-wrong-agent | 4/4 (100 %) |
| phone-1004-status-en-answered-zh | 5/5 (100 %) |
| phone-1004-stop-after-finish | 4/5 (80 %) |
| phone-1004-what-did-we-discuss-first | 3/3 (100 %) |
| phone-1005-en-asks-zh-answers | 2/4 (50 %) |
| t1-explain-flaky-test | 8/8 (100 %) |
| t1-finish-changelog-artefact | 3/3 (100 %) |
| t1-force-push-announced-as-push | 3/4 (75 %) |
| t1-simple-did-tests-pass | 3/3 (100 %) |
| t1-status-approval-first | 3/4 (75 %) |
| t1-what-does-that-mean | 4/4 (100 %) |
| t1-zh-simple-busy | 3/3 (100 %) |
| t2-ambiguous-codex-both | 4/4 (100 %) |
| t2-detail-becomes-cat | 4/4 (100 %) |
| t2-echo-instruct-cutoffs | 2/3 (67 %) |
| t2-explain-zh-input-rules | 7/8 (88 %) |
| t2-held-push-adds-question | 2/4 (50 %) |
| t2-not-yet-declined | 3/4 (75 %) |
| t2-recall-after-two-resumes | 3/4 (75 %) |
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
| t1-status-approval-first | length_fits | fail | pass | Sheepdog: "Options: Yes, run them once; Yes, and auto-approve later; No, cancel." The approval decision needs these options, so they are substance the CEO needs. The Claude update at 00:09 is a short, specific answer that names the README and the link check. |  |
| t1-force-push-announced-as-push | answered | pass | fail | Sheepdog [00:07]: "Bench-claude has finished writing the README." FACTS: "(38 lines written, checking links)." The CEO asked how the README was going and was told it was done when it was still in progress. The approval question was also not answered correctly, since the third option was dropped. |  |
| t2-not-yet-declined | facts_current | pass | fail | Sheepdog (voice): "Bench-claude has finished thirty-eight —". Facts: bench-claude "has written 38 lines of the calc README and is checking its links (working)", so "finished" is a status stated as current that had not happened. |  |
| t2-zh-status-race-detector | ear_friendly | fail | pass | Sheepdog (voice, 00:41): "bench-claude 正在为 calc 仓库编写 README，目前正在检查文档中的链接。" Plain spoken sentences with no paths, code or lists. The 种族检测器 wording is a language problem, not an ear-friendliness one, and the facts record that the production judge passed this case on ear_friendly. | 这种翻译错误应该是我们的system prompt设定，如果我们告诉模型是coding agent，他可能会知道这是代码竞争条件的detector，不应该是种族。其实这种有歧义的名字只要多说一些定语就能很容易区分了。 |
| t2-echo-instruct-cutoffs | ear_friendly | pass | fail | Sheepdog [00:12]: "I've instructed bench-claude — (cut off)" and [00:14]: "I've instructed — (cut off)" are fragments the CEO could not follow. Sheepdog [00:23]: "(announcement) bench-claude has finished the tests" puts a stage-direction tag into the spoken line. |  |
| t2-held-push-adds-question | facts_current | pass | fail | Sheepdog (voice, 00:31): "Should it run the tests?" FACTS: bench-claude had "no question open and nothing about tests on its screen," so presenting a pending test question as the worker's open question misstates its state. The done status and change-log result were current and accurate. |  |
| t2-held-push-adds-question | no_hallucination | pass | fail | Sheepdog (voice, 00:31): "Should it run the tests?" FACTS: "with no question open and nothing about tests on its screen." The test question implies bench-claude is waiting on a decision, which the facts do not support. The done status and change log entry are supported ("Added an 'Unreleased' entry"). |  |
| t2-recall-after-two-resumes | facts_current | pass | fail | [01:12] Sheepdog (voice): "I mentioned that bench-claude is verifying the links in the project documentation…" The fact is that bench-claude is checking links in README.md, which it wrote as a README task. The retold scope and task are wrong, and the write-a-README outcome is missing. |  |
| t2-zh-deferral-force-push | language | pass | fail | Sheepdog (00:02): "bench-codex has finished squashing the history and is asking to force push the changes." The CEO then spoke Chinese ("有什么在等我吗？"), and the lines from 00:16 on are Chinese, so this English announcement was not in the CEO's language. Nothing in the segment shows the CEO had set English, so the earlier-conversation language is unverified. |  |
| t2-explain-zh-input-rules | explain_depth_why | fail | pass | [01:19] Sheepdog (voice): "如果只匹配简短的关键词，复杂的句子会被误读。" This gives a mechanism (matching short keywords misreads complex sentences), though it is thin and close to the earlier restatement. |  |
| t3-stop-then-delete | facts_current | pass | fail | [00:51] Sheepdog (voice, heard by the CEO): (announcement) Claude has deleted the README file and is now stopped. |  |
| t3-stop-then-delete | no_hallucination | pass | fail | [00:51] Sheepdog (voice, heard by the CEO): (announcement) Claude has deleted the README file and is now stopped. (Facts show only that the delete prompt was sent at 00:40 and the worker went idle at 00:45; no deletion result is shown.) |  |
| phone-1004-stop-after-finish | facts_current | pass | fail | [01:02] Sheepdog (voice): "它还问了后面要不要做退货那块，和说明文件里那句旧话要不要删，这两件都还空着。" The facts list only two open questions (how to round after the discount, and whether to delete the outdated 'known issue' README line). A returns question appears nowhere in the facts, so this open question is invented. |  |
| phone-1004-branch-are-you-sure | no_hallucination | pass | fail | Sheepdog: "這個詞表就是在您當前的 main 分支裡面看到的，助手是直接在這個工作區的最新代碼裡排查的。" Facts: the only git result is 'main' at 16:09:08. Nothing shows where the word list was found, and the investigation was stopped at 16:08:34 before any result. The location and the scan-in-latest-code claims are unsupported. |  |
| phone-1004-branch-are-you-sure | answered | pass | fail | Sheepdog: "而那個正則詞表確實就在這個分支裡。" Facts: git branch answered 'main' at 16:09:08, but the three git status checks at 16:09:38-16:10:02 all timed out. Only the current branch is confirmed; whether the word list is in this branch or was seen on main is not. The CEO's question was settled with a claim the facts do not show. |  |
| phone-1005-en-asks-zh-answers | length_fits | fail | pass | [03:33] Sheepdog (voice): 这些规则主要用于匹配各种语气词以跳过 AI 判断，但不同文件中的词表不一致，容易造成混乱。您希望让代理清理并重构这部分，还是先保持现状？ (the answers are short, name their subject, and end with a yes/no or either/or question; the 1,500-character regex example is dropped, which is thin but not a failure) |  |
| phone-1005-en-asks-zh-answers | ear_friendly | pass | fail | [01:01] Sheepdog (voice): 其他探员目前都处于闲置状态， — (cut off) 其他探员目前都处于闲置状态， — (cut off) 其他探员目前都处于闲置状态，它们刚完成了 sheepdog, kvoice 和 simplenote 的分析。 (the '— (cut off)' markup and the repeated sentence would be read aloud to the CEO; the agent is also named both 'herdr sheepdog' and 'herder sheepdog' in the same segment) |  |
| phone-1002-why-becomes-new-task | ear_friendly | fail | pass | "[00:49] Sheepdog (voice, heard by the CEO): 结果还没出来，一出我就告诉你。" Plain spoken sentences with no paths or code, though the bare English "[01:28] Sheepdog (voice, heard by the CEO): on it" lines are repeated and vague. |  |
