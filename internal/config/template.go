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

[llm]
# base_url = %[10]q
# model = %[11]q
# api_key = ""   # empty: falls back to the gemini key pool when base_url is Gemini's

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
`,
		d.DataDir, d.DebounceMS, d.SnapshotIntervalS, d.PendingTTLS,
		d.Herdr.Socket,
		d.State.Path, d.State.FullFsync,
		d.Gemini.Model, d.Gemini.Voice,
		d.LLM.BaseURL, d.LLM.Model,
		d.Web.Port,
		d.CLI.Prefer, d.CLI.CodexBin, d.CLI.ClaudeBin, d.CLI.ClaudeModel, d.CLI.CodexReasoning, d.CLI.TimeoutS, d.CLI.MaxOutputKB,
	)
}
