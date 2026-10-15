# Judge–CEO agreement

Overall: agreement 106/126 (84 %), Cohen's kappa 0.65; CEO n/a 0, unpaired 0.

Judge too lenient (passes a fail label) 8, too strict (fails a pass label) 12.

| Rubric item | Agreement | Kappa | Too lenient | Too strict | CEO n/a | Unpaired |
|---|---|---|---|---|---|---|
| answered | 22/27 (81 %) | 0.54 | 2 | 3 | 0 | 0 |
| ear_friendly | 10/11 (91 %) | 0.79 | 0 | 1 | 0 | 0 |
| explain_comprehension_action | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_comprehension_what | 2/2 (100 %) | – | 0 | 0 | 0 | 0 |
| explain_comprehension_why | 2/2 (100 %) | – | 0 | 0 | 0 | 0 |
| explain_depth_example | 3/3 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| explain_depth_more | 1/2 (50 %) | 0.00 | 1 | 0 | 0 | 0 |
| explain_depth_why | 1/2 (50 %) | 0.00 | 1 | 0 | 0 | 0 |
| explain_padding | 2/2 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| facts_current | 6/10 (60 %) | 0.29 | 0 | 4 | 0 | 0 |
| language | 10/10 (100 %) | 1.00 | 0 | 0 | 0 | 0 |
| length_fits | 18/21 (86 %) | 0.63 | 2 | 1 | 0 | 0 |
| no_hallucination | 5/8 (62 %) | 0.25 | 1 | 2 | 0 | 0 |
| no_unintended_action | 16/18 (89 %) | 0.75 | 1 | 1 | 0 | 0 |
| right_agent | 6/6 (100 %) | 1.00 | 0 | 0 | 0 | 0 |

| Item | Agreement |
|---|---|
| phone-1002-why-becomes-new-task | 4/4 (100 %) |
| phone-1004-approve-auto-mode | 4/4 (100 %) |
| phone-1004-branch-are-you-sure | 4/4 (100 %) |
| phone-1004-regex-examples | 5/5 (100 %) |
| phone-1004-rewind-wrong-agent | 3/4 (75 %) |
| phone-1004-status-en-answered-zh | 5/5 (100 %) |
| phone-1004-stop-after-finish | 2/5 (40 %) |
| phone-1004-what-did-we-discuss-first | 3/3 (100 %) |
| phone-1005-en-asks-zh-answers | 3/4 (75 %) |
| t1-explain-flaky-test | 7/8 (88 %) |
| t1-finish-changelog-artefact | 3/3 (100 %) |
| t1-force-push-announced-as-push | 2/4 (50 %) |
| t1-simple-did-tests-pass | 3/3 (100 %) |
| t1-status-approval-first | 4/4 (100 %) |
| t1-what-does-that-mean | 4/4 (100 %) |
| t1-zh-simple-busy | 3/3 (100 %) |
| t2-ambiguous-codex-both | 2/4 (50 %) |
| t2-detail-becomes-cat | 3/4 (75 %) |
| t2-echo-instruct-cutoffs | 3/3 (100 %) |
| t2-explain-zh-input-rules | 5/8 (62 %) |
| t2-held-push-adds-question | 1/4 (25 %) |
| t2-not-yet-declined | 2/4 (50 %) |
| t2-recall-after-two-resumes | 3/4 (75 %) |
| t2-rename-docs-artefact | 4/4 (100 %) |
| t2-zh-deferral-force-push | 4/4 (100 %) |
| t2-zh-status-race-detector | 4/4 (100 %) |
| t3-approved-on-question | 4/4 (100 %) |
| t3-close-docs-artefact | 4/4 (100 %) |
| t3-fix-approve-artefact | 4/4 (100 %) |
| t3-stop-then-delete | 4/4 (100 %) |

## Disagreements

| Item | Rubric item | CEO | Judge | Judge's evidence | CEO's note |
|---|---|---|---|---|---|
| t1-force-push-announced-as-push | ear_friendly | pass | fail | Sheepdog: (announcement) bench-codex asks to run git push. | It would be next to prefix with - “there are two agents, the first agent xxx” before going on talking about each agent |
| t1-force-push-announced-as-push | length_fits | pass | fail | Sheepdog: (announcement) Codex wants to force push to main to publish a squashed history. Would you like me to proceed with the force push? |  |
| t1-explain-flaky-test | no_hallucination | fail | pass | The facts confirm the test fails about 1 in 12 times, blocked two deployments for 25 minutes each, and results in a total of 100 instead of 85 due to the missing 15% discount. |  |
| t2-ambiguous-codex-both | answered | pass | fail | ACTION (sent to a worker): prompt to docs-codex: "run the tests." (sent; docs-codex starts working) |  |
| t2-ambiguous-codex-both | no_hallucination | pass | fail | ACTION (sent to a worker): prompt to docs-codex: "run the tests." (sent; docs-codex starts working) |  |
| t2-not-yet-declined | no_unintended_action | fail | pass | CEO: No, don't run that yet. ... ACTION (sent to a worker): keys to bench-codex: esc |  |
| t2-not-yet-declined | answered | fail | pass | CEO: What else needs my attention? Sheepdog: Nothing else needs your attention, and I'll let you know when bench-claude finishes the README. | Yes find a time to answer: regarding your earlier question about readme etc |
| t2-held-push-adds-question | answered | pass | fail | Sheepdog (voice, heard by the CEO): bench-claude is done and has added a change log entry for the fix. Should it run the tests? |  |
| t2-held-push-adds-question | facts_current | pass | fail | Sheepdog (voice, heard by the CEO): bench-claude is done and has added a change log entry for the fix. Should it run the tests? |  |
| t2-held-push-adds-question | no_hallucination | pass | fail | Sheepdog (voice, heard by the CEO): bench-claude is done and has added a change log entry for the fix. Should it run the tests? FACTS: bench-claude is working on 'Add a CHANGELOG entry for the Add() fix'; it finishes at 00:22 ('Added an "Unreleased" entry: "Fix Add() ignoring its first argument."'), with no question open and nothing about tests on its screen. |  |
| t2-recall-after-two-resumes | facts_current | pass | fail | FACTS: bench-claude was asked to 'Write a README for the calc repo'; it wrote 38 lines to README.md and its screen now shows 'Checking the links in README.md…'. Sheepdog (voice, heard by the CEO): 'Bench-claude is verifying all the links in the project documentation' |  |
| t2-detail-becomes-cat | facts_current | pass | fail | Sheepdog (voice, heard by the CEO): The README includes the specific instructions for building and testing the project. FACTS: The README is written: what calc does, build steps, and how to run the tests. |  |
| t2-explain-zh-input-rules | explain_depth_more | fail | pass | 比如用户说“好的，但先别动数据库”，旧规则可能只匹配了“好的”就直接通过了，而模型能理解后面的保留条件。 |  |
| t2-explain-zh-input-rules | explain_depth_why | fail | pass | Sheepdog (voice, heard by the CEO): 如果只匹配简短的关键词，复杂的句子会被误读。 让模型来判断，能全面分析语境，确保确认和取消都更安全准确。 |  |
| t2-explain-zh-input-rules | length_fits | fail | pass | 比如用户说“好的，但先别动数据库”，旧规则可能只匹配了“好的”就直接通过了，而模型能理解后面的保留条件。 | 用户很明显想知道详情，supervisor和voice应该去gather facts，想清楚怎么解释，再回答。不一定要马上回答 |
| phone-1004-stop-after-finish | facts_current | pass | fail |  |  |
| phone-1004-stop-after-finish | answered | pass | fail | Sheepdog (voice, heard by the CEO): 批木零那条也做完了... 它还问了后面要不要做退货那块，和说明文件里那句旧话要不要删，这两件都还空着。 |  |
| phone-1004-stop-after-finish | no_unintended_action | pass | fail | Sheepdog (voice, heard by the CEO): 抹零那条也做完了，现在打折后的总价往下取整，买家不会多付，七项检查全过。 它还问了后面要不要做退货那块，和说明文件里那句旧话要不要删，这两件都还空着。 |  |
| phone-1004-rewind-wrong-agent | answered | fail | pass | Sheepdog (voice, heard by the CEO): 我已经让 herder sheepdog 项目总结继续工作了，它正在审计笔记和硬编码约束。 | 语音模型如果有不确定应该先问 |
| phone-1005-en-asks-zh-answers | length_fits | fail | pass | Sheepdog (voice, heard by the CEO): 这些规则主要用于匹配各种语气词以跳过 AI 判断，但不同文件中的词表不一致，容易造成混乱。您希望让代理清理并重构这部分，还是先保持现状？ |  |
