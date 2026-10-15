# Prompt arms (roles split out of #155), 2026-10-07

Both dirs hold all five prompt files and work with `--prompts-dir`.

- `r`: #155's role framing and "the user" wording (one orchestration agent
  operating the user's herdr, a voice and an orchestrator, no sheepdog,
  shepherd, flock or dog metaphors) with main's length and brevity rules
  unchanged. Made from #155's prompts (f063ba8) by reverting only its
  length edits.
- `rl`: `r` plus a gentle length rule: answer first in a sentence or two,
  then reasons, an example or numbers only when they help or the user asked
  for detail; never compress the orchestrator's spoken text; on "more
  detail", "详细", "then?" add new content or hand off (rephrasing alone is
  not an answer).
