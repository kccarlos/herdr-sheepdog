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
# stall_after_s = %[18]d   # no model response this long after an utterance logs voice.stalled; twice recycles the connection

[voice]
# name = %[38]q   # what the dog calls itself, substituted into the voice prompt as {name}

[deepseek]
# The second supervisor-brain provider (paid per token). api_key is
# normally left unset here: DEEPSEEK_API_KEY is read from the
# environment instead. With a key, deepseek appears in the /debug model
# picker whatever brain.provider says.
# api_key = ""
# base_url = %[34]q
# thinking = %[35]t   # DeepSeek's thinking mode
# reasoning_effort = %[36]q   # "low" | "high" | "max"; low fits the 8 s ask budget

[web]
# port = %[10]d
# dev = false
# dev_testdata_dir = ""   # default: testdata/audio relative to the working directory

[cli]
# prefer = %[11]q   # "codex" | "claude" | "auto"
# codex_bin = %[12]q
# claude_bin = %[13]q
# claude_model = %[14]q
# codex_reasoning = %[15]q
# timeout_s = %[16]d
# max_output_kb = %[17]d
# codex_cmd / claude_cmd and codex_resume_cmd / claude_resume_cmd (argv
# templates; TOML-only, see CONFIG.md) keep their built-in defaults
# unless set here.

[supervisor]
# chattiness = %[19]q   # "quiet" | "normal" | "chatty"; the /debug switch overrides it at runtime
# recent_minutes = %[20]d   # "normal" announces a finish only for agents you dealt with this recently
# promise_ttl_s = %[31]d   # how long "tell me when it's done" waits for its event before it expires silently
# cancel_window_s = %[39]d   # how soon after an instruction a "stop" still interrupts the agent with Ctrl-C

[brain]
# enabled = %[21]t   # the stateful supervisor brain behind ask/propose; serve --brain=off overrides
# provider = %[37]q   # "gemini" | "deepseek": the provider the brain starts on; the /debug picker switches at runtime
# model = %[22]q   # a function-calling model of that provider
# max_steps = %[23]d   # model calls per turn before the daemon answers for itself
# ask_timeout_s = %[24]d
# propose_timeout_s = %[25]d
# window_tokens = %[26]d   # compact the oldest half of the window above this (estimated tokens)
# prune_after_turns = %[27]d   # turns a screen/CLI dump stays verbatim in the window
# step_reserve_s = %[28]d   # 0: default; below this much time left, the next step is answer-only
# final_grace_s = %[29]d   # 0: default; how far past the timeout the answer-only step may run
# enrich_timeout_s = %[32]d   # budget of the brain's announce turn on a block or finish
# announce_wait_s = %[33]d   # how long a finish waits for the brain's line before the template sentence is spoken
# investigate_max_s = %[40]d   # sanity ceiling of a background investigation: the only limit on how long its answer may take
# investigate_session_ttl_s = %[41]d   # how long a finished investigation's sub-agent session stays resumable for a follow-up

[prompts]
# dir = %[30]q   # ~/.config/sheepdog/prompts/<name> overrides the matching embedded prompt file
`,
		d.DataDir, d.DebounceMS, d.SnapshotIntervalS, d.PendingTTLS,
		d.Herdr.Socket,
		d.State.Path, d.State.FullFsync,
		d.Gemini.Model, d.Gemini.Voice,
		d.Web.Port,
		d.CLI.Prefer, d.CLI.CodexBin, d.CLI.ClaudeBin, d.CLI.ClaudeModel, d.CLI.CodexReasoning, d.CLI.TimeoutS, d.CLI.MaxOutputKB,
		d.Gemini.StallAfterS,
		d.Supervisor.Chattiness, d.Supervisor.RecentMinutes,
		d.Brain.Enabled, d.Brain.Model, d.Brain.MaxSteps, d.Brain.AskTimeoutS, d.Brain.ProposeTimeoutS, d.Brain.WindowTokens, d.Brain.PruneAfterTurns,
		d.Brain.StepReserveS, d.Brain.FinalGraceS,
		d.Prompts.Dir,
		d.Supervisor.PromiseTTLS, d.Brain.EnrichTimeoutS,
		d.Brain.AnnounceWaitS,
		d.DeepSeek.BaseURL, d.DeepSeek.Thinking, d.DeepSeek.ReasoningEffort,
		d.Brain.Provider,
		d.Voice.Name, d.Supervisor.CancelWindowS,
		d.Brain.InvestigateMaxS, d.Brain.InvestigateSessionTTLS,
	)
}
