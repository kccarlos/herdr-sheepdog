# Language preference (2026-10-05)

The user wants the language and script (for example Simplified vs
Traditional Chinese) to be a setting, not something the models infer: in
the 2026-10-04 phone test the voice answered a Simplified speaker in
Traditional characters. Spec: docs/dev/handover-2026-10-04.md, "Next feature:
language preference in Settings". Label: **information** (no word lists,
no code deciding which language an utterance is in or which one to answer
in).

## What it is

- The user picks one or more languages, most preferred first, from a
  built-in catalog (`internal/lang`), in the iOS app's Settings ▸
  Languages or with `PUT /api/settings {"languages": [...]}`
  (docs/dev/ws-contract.md "Settings"). Kept in the state file
  (`session.languages`) like chattiness; no config key.
- One sentence describes it (`lang.Line`), the same for the voice and the orchestrator:
  "The user's language setting, in order of preference: Chinese
  (Simplified) 简体中文, English. Answer in the language they use; write
  Chinese in Simplified characters."
- **Slow orchestrator:** a `## Language` section in the state block (after the
  window, refreshed on every call, so the cached system text is
  unchanged).
- **Fast orchestrator:** the line is appended to the voice system instruction at
  every connect (Gemini Live and GPT-Live, embedded prompt or override),
  so the next session has it. Gemini Live reads its system instruction
  only at setup, so a change also goes to the running session at once as
  silent context (`supervisor.languages`, then a `voice.push`; it is a
  briefing like the names line). Since #139 a session never resumes twice,
  so a fresh session with the new instruction follows soon in any case.
  With no setting, the prompts are exactly what they were (v5.1's "answer
  in the language and script they used last").
- **Live speech options are not set.** Gemini's Live guide says native
  audio models choose the language themselves and do not support an
  explicit language code; the transcription language is inferred.
  Setting one would risk the Chinese/English switching
  (docs/dev/gemini-live-notes.md §7 "Language settings").

## The catalog: which languages both voice providers support

Checked 2026-10-05 against the providers' current docs.

- **Gemini Live** publishes a table of 99 languages
  (https://ai.google.dev/gemini-api/docs/live-guide, "Supported
  languages"), with Chinese as `zh-Hans` and `zh-Hant`.
- **GPT-Live publishes no language list.** The gpt-live-1 model page,
  the Live guide and the Live prompting guide
  (developers.openai.com/api/docs/models/gpt-live-1,
  …/guides/live, …/guides/live-prompting) name none; the prompting guide
  only says to state the language in the instructions. The closest
  official OpenAI list is the one for its realtime speech models: "OpenAI
  only lists the languages that exceeded <50% word error rate", 57
  languages, Chinese as one entry covering both scripts. OpenAI's current
  speech-to-text guide no longer prints it; it was read tonight from
  Microsoft's copy for `gpt-realtime` / `gpt-realtime-mini`
  (https://learn.microsoft.com/en-us/azure/ai-services/speech-service/voice-live-language-support,
  updated 2026-09-24).
- Every one of those 57 is in Gemini's table, so the catalog is those 57
  (Chinese split into the two scripts, Portuguese into Brazil and
  Portugal as Gemini lists them): 59 entries, all supported by both.
  Each entry still carries `providers`, so a one-sided entry can be
  added later and shown as such.

Open: GPT-Live support is inferred from OpenAI's speech list, not from a
GPT-Live statement; revisit if OpenAI publishes one.
