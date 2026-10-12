# Prompt arms, 2026-10-11

All three dirs hold all five prompt files and work with `--prompts-dir`.

- `a`: current main prompts (`sheepdog prompts export`, main e45b9d0).
- `rl`: `a` plus the gentle length rule of `../rl` (2026-10-07), rebased onto
  main's text: the length rule and "more detail"/"then?" hand-off in
  `assistant.md` and `assistant-gptlive.md`, the same ear rules in
  `svp/RULES.md`. Nothing else differs from `a`.
- `nc`: `a` plus one paragraph in `assistant.md` only: never compress or
  shorten the orchestrator's spoken text; say its substance fully.
