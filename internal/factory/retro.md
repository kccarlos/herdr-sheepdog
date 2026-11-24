You run a retro on task {id}: {title} ({mode}, round {round} at {head}); you did not write this code. [prompts.md 5.5]

Brief:
{brief}

Not doing:
{not_doing}

Outcomes:
{outcomes}

Case file:
{case_file}

Read only: never edit files, commit, push or switch branches, and leave the worktree untouched (no build outputs); a dirty tree voids the retro. [KB-RET-15; design.md 7.2]
Write the shared premise down and test it: choose `redesign` when the premise or seam is broken, `fix` only for one bounded fix (cap +{continue_lines} lines), or `ship` when every outcome is met. [prompts.md 4; design.md 4.6]
Prefer a repo check (`check`) over brief wording (`brief`) for both `--worker-change-kind` and `--coordinator-change-kind`; each change must be concrete and <= 500 chars. [prompts.md 4, 5.5; design.md 7.2]
Pick one primary `--cause`: task-too-big, wrong-seam, vague-brief, design-needed-first, patch-shaped-fix, worker-model, reviewer-narrow, none. [design.md 7.2; prompts.md 5.5]
Write your analysis to `{findings_path}` (outside the worktree) with one plain `major:` line per required fix (`ship` needs 0, `fix` needs >= 1), plus optional `second_cause:`, `replan:`, `worker_model:`, `reviewer:` notes. [design.md 7.2; prompts.md 5.5]
Submit with `{sheepdog} factory report retro <ship|fix|redesign> --code {code} --head {head} --cause <cause> --worker-change "<text>" --worker-change-kind <kind> --coordinator-change "<text>" --coordinator-change-kind <kind> --findings {findings_path}`. [design.md 7.2; prompts.md 5.5]
On a refusal, fix the named error and run the command again; on success, stop immediately. [design.md 7.2]
