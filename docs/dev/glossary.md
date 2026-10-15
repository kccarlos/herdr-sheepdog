# Glossary

One set of words for living docs, code comments and anything a person
reads. The product and binary stay **sheepdog**. The contract behind the
words is [`roles.md`](roles.md); the models read the same words in
`prompts/`.

| Term | Meaning |
|---|---|
| **User** | The person, talking by voice. Owns intent and decisions. Never "CEO", "shepherd" or "boss". |
| **Orchestration agent** | The one agent the user talks to, operating their herdr. It has two parts that are one character to the user: the voice and the orchestrator. |
| **Voice** | The part that talks with the user and owns the conversation (Gemini Live or GPT-Live; two tools, `to_supervisor` and `stop`). Formerly the fast brain, the dog, the Gemini Live layer, "assistant". |
| **Orchestrator** | The part that reads screens, investigates, decides, remembers and acts behind the rails: one stateful agent with tools (`internal/brain`). Formerly the slow brain, the brain, the supervisor brain, SVP. |
| **Agents / workers** | The coding agents (Claude Code, Codex, agy) running in herdr panes. Say "agents" or "workers", never "flock". |
| **herdr** | The terminal multiplexer that hosts the panes and reports each agent's state. |
| **Briefing** | A labelled message the orchestrator or the daemon hands to the voice (a function response, a silent context line) so it can tell the user in its own words. |
| **Rail** | A check in code that no model can talk its way past, for example "an approval prompt is answered only on the user's own fresh words". Rails make the orchestrator safe to give tools. |
| **Chattiness** | The user's setting (`quiet`, `normal`, `chatty`) that caps how much the voice volunteers unasked. |

## Former names

Old word on the left, current word on the right.

| Former | Now |
|---|---|
| shepherd, CEO, boss | user |
| flock | agents, workers |
| dog, fast brain, Gemini Live layer | voice |
| slow brain, brain, supervisor brain, SVP | orchestrator |
| chief of staff | orchestration agent |

## What keeps the old words

The rename covers prose: docs, comments, log lines and UI text meant for
people. These keep their names, because changing them would break
stored data, clients or history:

- Go identifiers and file names (`internal/brain`, `Brain`, `braintest`),
  tests and metric ids.
- Event types and payload fields (`brain.turn`, `ceo_say`, `trigger: ceo`),
  JSON and wire fields, state-file fields, config keys and env vars
  (`[brain]`, `SHEEPDOG_BRAIN_*`).
- History: `docs/dev/history/`, dated reports under `docs/dev/eval/`,
  `docs/dev/research/` and `docs/dev/bench/results/`, `docs/handover-*.md`, the
  changelog and the history sections of design docs. They say what was
  true when written.
