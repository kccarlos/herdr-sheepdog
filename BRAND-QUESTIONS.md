# Brand refresh: open questions

Raised by workstream A (tokens and shared assets). Each has a default that is already in the PR.

1. **Old `assets/logo-solid.svg` and `assets/logo-transparent.svg`.** Kept as files; the README, `ios/README.md` and the `scripts/readme-media/*` scripts still use them (workstream B). Nothing in `web/` uses them any more. Can they be deleted once B and the media scripts move to `assets/brand/logo/`?
2. **User bubble text on the accent (iOS).** With `AccentColor` orange, the white text on the user's bubble in `ConversationView` failed 4.5:1, so this PR makes that one line use a new `OnAccent` colour (ink). Workstream E may fold it into `Theme.swift`.
3. **Accent as text on light surfaces (iOS).** `ModelsView` and `AgentScreenView` draw `Color.accentColor` as a foreground (icons, the send arrow). `AccentColor` light is `#D98718`, about 2.7:1 on white, fine for glyphs and large controls but below 4.5:1 for words. Is a `primaryText` colourset (`#8A5700` / `#F5B83D`, already in `colors.json`) wanted for any text drawn in the accent? Left for E.
4. **`/debug` swimlane keeps red for blocked.** Both the agent state "blocked" and errors use `--danger` for now, so the page looks as before. D flips the state to `--blocked` (amber) and keeps `--danger` for errors.
5. **`/debug` primary buttons and the user bubble stay blue (`--working`).** `/debug` never had a `--primary`; its buttons used the herdr blue. Should they become orange in D, or stay blue because the debug page is a developer tool?
6. **Link text colour.** Links on `/setup` and `/voice` use `--primary-text` (brown on light, amber on dark) rather than raw orange, to pass 4.5:1. This changes links from blue to brown/amber now.
7. **QR code backgrounds.** A QR code needs a light background in both modes, so `/setup` uses `--cream` and `/debug` keeps its inline white. Is there a token for "always light"?
8. **Lane hues with no token.** The voice amber, web orange, push pink and the latency and message-kind accents on `/debug` have no token; they keep their old values (they are existing hex, none is new). Add tokens for them in D, or drop the distinctions?
9. **Header logo.** The three pages now show `logo-color.svg` in both modes (the old picture swapped a solid and a transparent file). D replaces it with the shared mono header.
10. **Favicons.** `favicon.svg` is the mono logo with ink/cream by colour scheme; the 32px and 180px PNGs are the cream mono logo on `#1B1D22`. The 180px has 14% padding. Want a different crop?
11. **`internal/web/auth_test.go`** requests `/static/logo-color.svg` now; the one line the CEO allowed.

## Raised by workstream D (web pages)

12. **No listening orb existed on `/voice`.** The page only had status dots. D treats `#voice-dot` as the orb: `open` = listening (orange, 3px cream ring at 60 %, 2 s breathe, off under `prefers-reduced-motion`), `open speaking` = `--working` (render() adds the `speaking` class), `.thinking` = `--thinking` (the web has no thinking signal yet, so it is CSS only), `stalled` = `--blocked`. Want a larger standalone orb?
13. **`--muted` on `--code` is 4.41:1 in light.** The token is the authority, so D left it; no page puts muted text on a code background. Darken `--muted` slightly if that ever happens.
14. **Focus ring.** `--focus` (orange) is about 2:1 on the light surfaces, below the 3:1 for a control outline, so `shared.css` adds a 2px ring in the text colour inside it. Keep, or make `--focus` darker on light?
15. **Question 5 answered:** `/debug` primary buttons and the user bubble now follow `--primary` (orange, ink text). Blue stays the herdr source/lane colour. Question 7 (QR) and 8 (lane hues with no token) are unchanged: raw hues are kept, and the two that were used as text (`--web`, `--eventlog`, the You lane) are mixed toward `--fg` to pass 4.5:1.
16. **Pre-existing, not touched:** on `/debug` the swimlane toolbar shows a tall empty box between the "supervisor" and "brain" toggles (it also does on main).
