# Changelog

## v0.1.0 (unreleased; the date is set when the tag is cut)

First public release.

### Features

- Voice supervision: talk to your coding agents in herdr by voice through
  Gemini Live or GPT-Live (switch at runtime). A stateful orchestrator
  behind the voice keeps track of every agent, answers questions about
  their work, starts new Claude or Codex agents, and relays your
  instructions. Orchestrator providers: Gemini, DeepSeek or any
  OpenAI-compatible endpoint, with failover.
- herdr plugin: install sheepdog as a herdr plugin and drive it from
  herdr's own actions.
- `sheepdog setup`: a local web wizard (with a terminal fallback) that
  configures keys, models and the herdr connection; `sheepdog init` and
  `sheepdog doctor` check the result.
- Lifecycle commands: `sheepdog start|stop|restart|status|logs`,
  `sheepdog service install|uninstall|status|logs`, `sheepdog update`,
  `sheepdog uninstall` and `sheepdog doctor --report`, with a single
  running instance.
- `sheepdog pair`: a QR code that pairs the iPhone app with the daemon.
- iPhone app: talk to sheepdog from your phone, with notifications when an
  agent needs you. Notifications are end-to-end encrypted and travel
  through the shared push relay (a Cloudflare Worker); you can self-host
  the relay instead.
- `sheepdog demo`: try sheepdog without herdr.
- Plain error messages: account and quota problems are said once, in plain
  words, instead of as raw provider errors.
- Data retention: the event log and the orchestrator's transcripts are
  deleted after 14 days by default (`events.retention_days`, see
  [CONFIG.md](CONFIG.md)); `sheepdog data clear` deletes them now.

### Documentation

- User docs in [docs/](docs/README.md): quick start, commands, models,
  safety, privacy, remote access, the iPhone app, FAQ and troubleshooting.
- Developer docs in [docs/dev/](docs/dev/).

## Unreleased

### Breaking changes

- refactor!: remove the legacy voice-tool surface and the ask/propose
  request kinds. The brain's turn over the shepherd's words is always an
  `utterance`; the supervisor's `Briefing` / `Ask` / `Propose` /
  `Confirm` / `Cancel` / `Note` methods are gone.
- Config: `brain.propose_timeout_s` is now `brain.utterance_timeout_s`
  and `brain.enrich_timeout_s` is now `brain.announce_turn_timeout_s`
  (with `SHEEPDOG_BRAIN_UTTERANCE_TIMEOUT_S` /
  `SHEEPDOG_BRAIN_ANNOUNCE_TURN_TIMEOUT_S`). The old names are still
  read, with a `daemon.config_deprecated` note at startup;
  `brain.ask_timeout_s` / `SHEEPDOG_BRAIN_ASK_TIMEOUT_S` are ignored with
  a note.
- `sheepdog brain-test`: `--kind ask|propose` is gone; every run is an
  utterance. `--kind` and `--agent` are still accepted and ignored with a
  note.

### Features

- feat(plugin): herdr plugin shim, install/uninstall, launchd plist (5056c09)
- feat(config): env-file loading, config show/init, CONFIG.md (7a55f9a)
- feat(cli): surface claude cost and turns in cli.run (463d3af)
- feat(web/voice): browser audio page with worklet capture and playback (#13) (7d58bd3)
- feat(cli): read-only codex/claude query tool for ask (#12) (c0e2820)
- feat(web): event log, debug page, fake injection, WS server and serve (#11) (1e564f8)
- feat(supervisor): tools, pending action, cheap LLM templates (#10) (4fd7072)
- feat(voice): Gemini Live session, audio bridge, WS contract and voice-test (#9) (1398579)
- feat(herdr): socket client, debounced watcher and persisted state table (#8) (a33c347)

### Fixes

- fix(state): de-flake the full_fsync coalescing test (50a26cc)
- fix(llm): keep identifiers in spoken answers (31e51cc)

### Performance

- perf(state): single fsync and coalesced persists (38c604d)

### Documentation

- docs: rewrite README for a fresh machine, add architecture.md (0cbc157)
- docs(wp0): protocol reconnaissance — herdr socket, Gemini Live, CLI tools (#7) (36ea7e2)

### Build & CI

- build: changelog script, wire release notes, Makefile targets (2b6a487)

### Other

- chore(deps): bump actions/setup-go from 5 to 7 (#6) (c682607)
- chore(deps): bump golangci/golangci-lint-action from 8 to 9 (#5) (9078e2e)
- chore(deps): bump softprops/action-gh-release from 2 to 3 (#4) (133811b)
- chore(deps): bump actions/checkout from 4 to 7 (#3) (1ad38ee)
- chore(deps): bump actions/upload-artifact from 4 to 7 (#2) (eb7864e)
- chore: bootstrap Go module, skeleton, CI and release workflows (#1) (1dc29b1)
- Initial commit (16a3d6a)

