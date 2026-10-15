# Plan: onboarding tiers, README and presentation (draft for CEO review, 2026-10-13)

Status: **proposal, not started.** Queued after the harness-iteration product notes. Nothing here is built until the CEO approves the order.

Goals:
1. Lower the effort to reach the first spoken answer at every depth of commitment.
2. Write a README that spreads well.
3. Make the project presentable in an interview: history kept, design and eval story easy to follow.

## Today

A new user needs all of the following:
- Go (or a release tarball found by hand);
- herdr running;
- a hand-edited `config.toml` plus `.env`, with one to three API keys;
- for the phone: a non-loopback bind, a token, a LAN or Tailscale address typed into the iOS app, and a TestFlight invite;
- a terminal or tmux to keep `sheepdog serve` alive.

Existing pieces to build on:
- `sheepdog config init`;
- signed release tarballs (`make release-sign`);
- the `/voice` browser page;
- the iOS "Test connection" button;
- the bench's simulated world (fake agents, prompts and failures);
- `sheepdog config show`.

## Tiers

Each tier gets its own quickstart section in the README, a target time to the first spoken answer, and a check in a fresh-machine test.

| Tier | Who | Needs | Target |
|---|---|---|---|
| 0. Try without herdr | curious visitor, interviewer | sheepdog + one Gemini key; or nothing (video) | 5 min |
| 1. Local, browser | herdr user at the desk | sheepdog + herdr + one key | 10 min |
| 2. Local + iOS | daily use away from the screen | tier 1 + iOS app on the same LAN or Tailscale | 15 min |
| 3. LiveKit (future) | speakerphone, remote | tier 2 + LiveKit transport | after the LiveKit evaluation |

### Tier 0: try without herdr

- **`sheepdog demo`:** runs the daemon against the bench's simulated world instead of herdr. Scripted workers finish, ask for approvals and fail, so a visitor hears announcements, answers a prompt and asks "what's going on". It opens `/voice` in the browser. It needs only `GEMINI_API_KEY` (AI Studio free tier for voice and orchestrator).
- **iOS app demo mode:** the app connects to a `sheepdog demo` daemon like a real one. Open question: does the app also need an offline scripted demo with no daemon? App Store review usually requires the app to work for the reviewer without a private server, so this likely becomes necessary before an App Store release.
- **Zero-install path:** a 60–90 s demo video and a GIF at the top of the README. No hosted public demo instance, because of cost, key abuse and the Google adversarial-content risk.

### Tier 1: local, browser

- **Install without Go:** a Homebrew tap (`brew install kccarlos/tap/sheepdog`) fed by goreleaser from the existing signed release job; also a one-line `curl | sh` installer for Linux.
- **`sheepdog init` (interactive):**
  - detects herdr and its socket, and says how to install it if missing;
  - asks for one key (Gemini AI Studio) and validates it live; DeepSeek and the OpenAI voice stay optional;
  - writes `config.toml` and the secret store with a long random token.
- **`sheepdog doctor`:** checks each step (keys valid, quota or balance left, herdr reachable, port free, bind and token, push config) and prints the fix for each failure in one line.
- **`sheepdog service install`:** a launchd agent on macOS (systemd user unit on Linux), so tmux is no longer needed. Includes `service status`/`logs`.
- **Errors said plainly:** an invalid key, an exhausted quota or a 402 balance error is spoken once by the voice and shown on `/voice` and in the app, naming the service and the fix. Never silence or an endless "try again".

### Tier 2: local + iOS

- **QR pairing:** `sheepdog pair` (and the `/debug` page) shows a QR code with the address and token. The app scans it in Settings, so nothing has to be typed. If both a LAN and a Tailscale address exist, the code carries both, and the app picks whichever is reachable.
- **Distribution:** a public TestFlight link in the README now; the App Store later (needs the demo mode above, a privacy policy and the review notes).
- **First-run in the app:** scan the code, then test the connection and the microphone. Then a short spoken "hello", so the user hears the voice work before relying on it.
- **Push:** APNs setup stays optional; `doctor` reports whether it is configured.

### Tier 3: LiveKit (placeholder)

After the LiveKit evaluation (echo on speakerphone, possibly remote access without Tailscale): add its setup to `init`/`doctor` and its own README section. Not designed yet.

## README for spreading

Order of the top of the README:
1. Logo, a one-line pitch ("Manage your coding agents by voice, away from the screen"), demo GIF/video.
2. "Why": you run several coding agents; they block on approvals while you are away; sheepdog tells you and lets you answer by voice.
3. Quickstart by tier (0 → 2), three commands each.
4. How it works: one diagram (user ↔ voice ↔ orchestrator ↔ herdr workers), the two-brain split in two sentences, link to `docs/dev/architecture.md`.
5. Safety: what it will and will not do on its own, the approval model, the network warning.
6. How it is evaluated: a short summary of the north star (Understood / Done / Low effort / Safe), the tiers, and the Sonnet judge with its calibration. Link to `docs/dev/eval/README.md`.
7. Configuration reference, development, CI: moved below or into CONFIG.md and docs/.

## Presentable for interviews

- **History:** kept (CEO, 2026-10-13). Before going public:
  - a full-history secret scan (gitleaks);
  - rotate anything found;
  - replace the daemon token with a long one;
  - the CEO skims the committed voice transcripts for privacy.
- **One design write-up (`docs/dev/design-overview.md`), 2–3 pages:**
  - the dual-brain architecture and why;
  - "the harness informs and prevents runaways, judgment stays with the models";
  - the Live resume finding (#139);
  - the eval method and two or three iteration stories with numbers (nc rule, queue budget, finish-after-approval).
- **Repo hygiene:**
  - LICENSE (exists);
  - CONTRIBUTING and SECURITY notes;
  - issue templates;
  - a short public roadmap;
  - `docs/dev/status.md` trimmed to the public state; the handover stays as engineering history.
- **iOS repo:** the same README treatment, with screenshots and the TestFlight link.

## Order (proposal)

1. Secret scan + token rotation (prerequisite for going public).
2. Tier 1 core: brew/goreleaser, `sheepdog init`, `sheepdog doctor`, plain errors.
3. Tier 0: `sheepdog demo` on the simulated world.
4. README rewrite + demo video + design overview, then make the repo public.
5. Tier 2: QR pairing, public TestFlight, first-run flow; `service install`.
6. Fresh-machine test with 2–3 people; record time to first answer and where they got stuck; fix.
7. App demo mode / App Store; tier 3 after LiveKit.

## CEO answers (2026-10-13)

1. iOS app: build the offline demo mode; the bar is passing App Store review.
2. Homebrew: use the existing tap `kccarlos/homebrew-tap`.
3. TestFlight: publish a public link once the iOS onboarding changes are in.
4. Repo name stays `herdr-sheepdog`. Once public, CI moves back to GitHub-hosted runners (`gh variable set CI_RUNNER --body ubuntu-latest`).

5. **Providers:** the voice supports only Gemini Live (3.8) and GPT-Live. The orchestrator (brain) must accept any OpenAI-compatible provider the user picks. `internal/brain/openaicompat.go` already speaks the protocol, but it is wired only to DeepSeek. Add a generic `[brain.openai_compat]` (name, base_url, api_key env, model) or a list of them. `init` asks for it, `doctor` runs a tool-calling probe turn against it, and the `/debug` picker lists it. The README says that orchestrator quality depends on the model and names the models we have evaluated.
6. Merge policy: PRs of this plan merge on green CI; making the repo public is the CEO's own step.

Order: the two product notes from the harness iteration first, then this plan.
