package config

import "fmt"

// Template returns the commented config.toml template `sheepdog config
// init` writes. Every key is present but commented out, showing its
// built-in default (Default()) or a placeholder for secrets, so the file
// is a safe starting point: uncommenting a line is the only way to
// change anything. See CONFIG.md for what each key does and its
// SHEEPDOG_* environment override, if it has one.
func Template() string {
	d := Default()
	return fmt.Sprintf(`# sheepdog config — see CONFIG.md for every key, its SHEEPDOG_* env
# override (env always wins over this file) and its default. Every line
# below is commented out with its built-in default; uncomment and edit to
# change it. This file is optional: sheepdog runs on defaults alone.

# data_dir = %[1]q
# debounce_ms = %[2]d
# snapshot_interval_s = %[3]d
# pending_ttl_s = %[4]d

# Spoken names for panes, keyed by herdr pane id (TOML-only, no env
# override). Example:
# [aliases]
# "w9:p1" = "auth"

[herdr]
# socket = %[5]q

[state]
# path = %[6]q
# full_fsync = %[7]t   # hardware-durable fsync; slower, off by default

[gemini]
# api_keys is normally left unset here: GEMINI_API_KEY (and
# GEMINI_API_KEY_1 .. GEMINI_API_KEY_9 for a rotation pool) are read from
# the environment instead, so keys never sit in a checked-in file.
# api_keys = ["AIza..."]
# model = %[8]q
# voice = %[9]q
# stall_after_s = %[20]d   # no model response this long after an utterance logs voice.stalled; twice recycles the connection

[llm]
# base_url = %[10]q
# model = %[11]q
# api_key = ""   # empty: falls back to the gemini key pool when base_url is Gemini's
# timeout_s = %[23]d   # per-attempt timeout; raise it for a slow local model

[web]
# port = %[12]d
# dev = false
# dev_testdata_dir = ""   # default: testdata/audio relative to the working directory

[cli]
# prefer = %[13]q   # "codex" | "claude" | "auto"
# codex_bin = %[14]q
# claude_bin = %[15]q
# claude_model = %[16]q
# codex_reasoning = %[17]q
# timeout_s = %[18]d
# max_output_kb = %[19]d
# codex_cmd / claude_cmd (argv templates; TOML-only, see CONFIG.md) keep
# their built-in defaults unless set here.

[supervisor]
# chattiness = %[21]q   # "quiet" | "normal" | "chatty"; the /debug switch overrides it at runtime
# recent_minutes = %[22]d   # "normal" announces a finish only for agents you dealt with this recently

[brain]
# enabled = %[24]t   # the stateful supervisor brain behind ask/propose; serve --brain=off overrides
# model = %[25]q   # a function-calling Gemini model
# max_steps = %[26]d   # model calls per turn before falling back to the template path
# ask_timeout_s = %[27]d
# propose_timeout_s = %[28]d
# window_tokens = %[29]d   # compact the oldest half of the window above this (estimated tokens)
# prune_after_turns = %[30]d   # turns a screen/CLI dump stays verbatim in the window
# step_reserve_s = %[31]d   # 0: default; below this much time left, the next step is answer-only
# final_grace_s = %[32]d   # 0: default; how far past the timeout the answer-only step may run

[prompts]
# dir = %[33]q   # ~/.config/sheepdog/prompts/<name> overrides the matching embedded prompt file
`,
		d.DataDir, d.DebounceMS, d.SnapshotIntervalS, d.PendingTTLS,
		d.Herdr.Socket,
		d.State.Path, d.State.FullFsync,
		d.Gemini.Model, d.Gemini.Voice,
		d.LLM.BaseURL, d.LLM.Model,
		d.Web.Port,
		d.CLI.Prefer, d.CLI.CodexBin, d.CLI.ClaudeBin, d.CLI.ClaudeModel, d.CLI.CodexReasoning, d.CLI.TimeoutS, d.CLI.MaxOutputKB,
		d.Gemini.StallAfterS,
		d.Supervisor.Chattiness, d.Supervisor.RecentMinutes,
		d.LLM.TimeoutS,
		d.Brain.Enabled, d.Brain.Model, d.Brain.MaxSteps, d.Brain.AskTimeoutS, d.Brain.ProposeTimeoutS, d.Brain.WindowTokens, d.Brain.PruneAfterTurns,
		d.Brain.StepReserveS, d.Brain.FinalGraceS,
		d.Prompts.Dir,
	)
}
