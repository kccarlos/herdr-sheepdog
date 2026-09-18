# Changelog

## Unreleased

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

