# App Store review audit (iPhone and iPad app)

An audit of the iPhone and iPad app in [ios/](../../ios/README.md) against Apple's App
Store rules, done before the first App Store submission. The goal is to pass review the first
time. This page does not change product code. It lists what passes, what needs a fix, and what
the CEO must decide, plus drafts for every App Store Connect form.

Audited on 2026-10-25 against `main` at `7347491d` (app 0.1.20). Sources were fetched the same
day:

- the [App Review Guidelines](https://developer.apple.com/app-store/review/guidelines/);
- [Upcoming requirements](https://developer.apple.com/news/upcoming-requirements/);
- [App privacy details](https://developer.apple.com/app-store/app-privacy-details/);
- [Age ratings values and definitions](https://developer.apple.com/help/app-store-connect/reference/app-information/age-ratings-values-and-definitions/);
- [Screenshot specifications](https://developer.apple.com/help/app-store-connect/reference/app-information/screenshot-specifications/);
- [Overview of export compliance](https://developer.apple.com/help/app-store-connect/manage-app-information/overview-of-export-compliance/);
- the [required reason API](https://developer.apple.com/documentation/bundleresources/describing-use-of-required-reason-api) list.

App Store Connect was only read (with `asc`), never changed. Nothing has been submitted.

**Status words.** **pass**: nothing to do except say it in the reviewer notes. **fix**: a change
in the repository. **CEO**: a decision or an App Store Connect action that only the CEO can
take. **SVM** marks a fix that edits `SessionViewModel` (or `SessionViewModel+*.swift`). Those
fixes wait for the architecture phases that are running in parallel
([ios-state-architecture.md](design/ios-state-architecture.md)).

This page replaces the reviewer notes in [ios/docs/app-review-notes.md](../../ios/docs/app-review-notes.md)
once PR 3 (below) lands. Until then, that file is stale: it says the only ATS key is
`NSAllowsLocalNetworking`, which is no longer true.

## 1. Top rejection risks, in order

| # | Risk | Guideline | Status |
|---|---|---|---|
| 1 | The decided name "Sheepdog — voice for Herdr agents" has **33 characters**. The limit is 30, so App Store Connect will not accept it. | 2.3.7 | CEO |
| 2 | "Herdr" is another developer's product name (herdr, `github.com/herdrdev/herdr`). Putting it in the app name needs that developer's approval. Subtitles may not reference other apps. | 4.1(c), 5.2.1, 5.2.2, 2.3.7 | CEO |
| 3 | The app has **no consent for third-party AI**. The voice audio, the transcript text, agent screens and attached photos go from the phone to the user's daemon, and the daemon passes them on to Google, OpenAI, Anthropic or DeepSeek. The microphone purpose string says the voice goes "only to the sheepdog daemon", which leaves the AI out. | 5.1.2(i), 5.1.1(ii) | fix (SVM) |
| 4 | A reviewer cannot use the real product: it needs a self-hosted daemon. The demo covers the flow. A video of real use would remove the doubt. | 2.1(a), 4.2.3(i) | CEO (record) + pass |
| 5 | The app has **no privacy policy link**. The policy is also out of date: it says the connection is plain HTTP, and it does not mention photo attachments or AI consent. | 5.1.1(i) | fix |
| 6 | No target has a **privacy manifest** (`PrivacyInfo.xcprivacy`), although the code calls required-reason APIs: `UserDefaults` and `ProcessInfo.systemUptime`. | Upcoming requirements, May 1 2024 | fix |
| 7 | The drafts reviewers will read contradict the binary. `app-review-notes.md` describes ATS wrongly, and `testflight-metadata.md` says "plain HTTP". | 2.3.1, 2.1 | fix (docs) |
| 8 | `NSAllowsArbitraryLoads = true`, which reviewers ask about. | ATS justification | pass, with notes |
| 9 | The `audio` background mode, with "Keep listening with the screen locked" on by default. | 2.5.4 | pass, with notes |
| 10 | The phone shell runs commands on the user's computer. | 2.5.2, 4.2.7 | pass, with notes |
| 11 | EU **trader status** (DSA) has to be declared, because the app ships to the EU. | DSA, Feb 2025 | CEO |
| 12 | The app is available on Apple silicon Macs and Vision Pro by default, where nobody has tested it. | 2.1, 2.4 | CEO |
| 13 | The demo asks for the microphone and speech recognition as soon as it starts, before the user asks for anything that needs them. | 5.1.1(iv), HIG | fix (small) |

## 2. Checklist

One row per requirement. The evidence column gives `file:line` on `main` at `7347491d`. The
fix column says what to change. "PR n" refers to the PR list in section 3.

### Performance (2.x)

| Guideline: requirement | Status | Evidence | Fix |
|---|---|---|---|
| 2.1(a): the app is complete, with no placeholder content and working URLs | pass | The first-run screen explains what the app needs and offers the demo ([launch-review.md](../../ios/docs/dev/launch-review.md) P0 1). The demo's Settings show no empty daemon pages (P1 7). The URLs in `ios/Sources/SheepdogKit/SetupHelp.swift:10-15` resolve: the repository is public (`gh repo view`: PUBLIC). | None. Check every URL again on the submission day. |
| 2.1(a): if the app has a login, give a demo account, or a demo mode with Apple's prior approval | pass | There is no login or account. Pairing is a QR code from the user's own computer. The built-in demo needs no network (`ios/Sources/SheepdogKit/DemoDaemon.swift`, `DemoWorld.swift`). | Reviewer notes (section 4) walk through the demo. Because there is no account, the demo-account rule does not apply. |
| 2.1(a): the reviewer must be able to see the real features | CEO | Real use needs a sheepdog daemon on a Mac or Linux computer that runs herdr. The launch video in the README is a synthetic cut ([launch-video.md](launch-video.md)). | Record a 1 to 2 minute screen recording on a real iPhone: scan the pairing code, start a voice session, approve a prompt, open an agent window and a shell. Link it in the reviewer notes (an unlisted video, or a GitHub release asset). Offering reviewers a reachable test daemon is possible but exposes a real computer. Not recommended. |
| 2.1(a): no crashes on device; built with the release SDK | pass | `ios.yml` runs the unit and UI tests on Xcode 27.1 (`.github/workflows/ios.yml:158`). Xcode Cloud archives with "Latest Release" ([xcode-cloud.md](../../ios/docs/dev/xcode-cloud.md)). | Before submitting, install the exact TestFlight build on one iPhone and one iPad and walk the reviewer notes. |
| SDK minimum: built with Xcode 26 or later and the iOS 26 SDK (since 2026-04-28) | pass | Xcode 27.1 locally and in CI. The deployment target is iOS 26.0 (`ios/project.yml:4-6`). | None. |
| 2.3.1, 2.3.7: accurate metadata; name of 30 characters or fewer; no trademark stuffing; the subtitle does not reference other apps | CEO | The decided name has 33 characters. The App Store Connect record is still named "Sheepdog for herdr" (read with `asc apps list`). | Choose a name of 30 characters or fewer (section 7). Keep Claude, Codex, Gemini and herdr out of the keywords. Rename the App Store Connect record (a mutation, so the CEO does it). |
| 2.3.3, 2.3.4: screenshots show the app in use | pass (plan) | `LaunchReviewScreenshotsUITests` already drives the demo without a daemon ([launch-review.md](../../ios/docs/dev/launch-review.md)). | Section 8 gives the plan. The demo is labelled "Demo" on screen, which is honest. |
| 2.3.8: metadata is suitable for 4+ | pass | The icon and screens contain no mature content. | None. |
| 2.3.10: no references to other platforms in metadata | pass | The description mentions Mac and Linux only as the computer the daemon runs on. | Keep it that way; do not mention Android. |
| 2.4.1: iPhone apps should run on iPad | pass | `TARGETED_DEVICE_FAMILY: "1,2"` (`ios/project.yml:98`). There is an iPad layout ([iphone-app.md](../iphone-app.md#on-ipad)). | None. iPad screenshots are required (section 8). |
| iPad multitasking: all four orientations, several scenes, no `UIRequiresFullScreen` | pass | `ios/project.yml:136-144`. | None. Fix the open iPad flake `testOpenInNewWindow` (a separate task) before the review build. |
| 2.4 (Mac and visionOS): an iPad app is offered on Apple silicon Macs and Vision Pro unless you opt out | CEO | Not tested there. Microphone, Live Activity and APNs behave differently on those devices. | Recommended: in App Store Connect ▸ Pricing and Availability, untick "Make this app available" for Mac and for Apple Vision Pro for 1.0. |
| 2.5.1: public APIs only | pass | No `NSClassFromString`, `dlopen` or private selectors in `ios/Sources` (grep). | None. |
| 2.5.2: no downloading or running code that changes the app | pass | The phone shell and agent windows send keystrokes to a terminal on the user's own computer and draw its output (`ios/Sources/SheepdogKit/PhoneShell.swift`, SwiftTerm). Nothing is executed on the phone, and the app's features do not change. This works like the SSH clients on the store (Termius is 4+). | Say so in the reviewer notes. |
| 2.5.4: background modes only for their purpose | pass | `UIBackgroundModes: audio` (`ios/project.yml:156-157`). During a voice session the app records and plays live two-way audio, like a call. With "Keep listening with the screen locked" off, the audio stops in the background (`ios/Sources/Sheepdog/SessionViewModel.swift:454-463`, `ios/Sources/SheepdogKit/SettingsStore.swift:47-49`, default on). The microphone indicator shows while it records. | Explain it in the reviewer notes. Keep the default only if the CEO accepts it. Off by default would be safer but changes the product (CEO, low risk either way). |
| 2.5.6: web browsing uses WebKit | pass | No web view. Help links open in Safari (`ios/Sources/Sheepdog/Banners.swift:27`). | None. |
| 2.5.16 / ActivityKit: Live Activities and widgets show related, current content | pass | The Live Activity mirrors the voice session and the agents waiting. It ends with the session (`ios/Sources/Sheepdog/AwayControls.swift:133-140`), has a stale date (`:193`) and contains no ads. | None. |
| Live Activity push updates | fix (comment) | `Activity.request(…, pushType: .token)` (`ios/Sources/Sheepdog/AwayControls.swift:109-113`), so the daemon updates it through the relay. The comment at `ios/project.yml:158-159` says "no push updates", which is wrong. No extra entitlement is needed: `aps-environment` covers it. | Fix the comment (PR 1). Mention it in the reviewer notes. |

### Design (4.x)

| Guideline: requirement | Status | Evidence | Fix |
|---|---|---|---|
| 4.1(c): no other developer's product name in the app name or icon without approval | CEO | "Herdr" is in the decided name. The icon has no herdr mark. | Either get written permission from herdr's maintainers (keep the email in case Apple asks, per 5.2.2), or leave Herdr out of the name and subtitle and say "works with herdr" in the description. |
| 4.2: minimum functionality, more than a repackaged website | pass | A native SwiftUI app: two-way live voice, a Live Activity, a control widget, approval cards, a terminal renderer, iPad scenes, App Intents. | None. |
| 4.2.3(i): works without installing another app | pass, with notes | The daemon is a computer program, not an iOS app, but real use needs it. The demo works on its own. | The reviewer notes state the requirement first and point to the demo and the video (risk 4). |
| 4.2.7: remote desktop clients | pass | Not a screen mirror. The app shows text terminals of the user's own computer, and the user pairs from that computer. Connections are LAN or Tailscale. | None. If a reviewer cites 4.2.7(a) ("local and LAN-based"), answer that Tailscale is the user's own private network to their own computer. |
| 4.5.4: notifications are not required; no sensitive data in pushes; no marketing | pass | Permission is asked only when the daemon reports push as enabled, never in the demo (`ios/Sources/Sheepdog/PushNotifications.swift:133`; [launch-review.md](../../ios/docs/dev/launch-review.md) P0 4). The content is encrypted end to end, so the relay and APNs see ciphertext ([push.md](push.md)). The app has no marketing pushes. | None. |
| Time-sensitive notifications are only for things that need action now | pass | `com.apple.developer.usernotifications.time-sensitive` (`ios/project.yml:120`) is used for "an agent needs your approval". | Mention it in the notes. |
| 4.8: offer Sign in with Apple when third-party login is used | pass | The app has no login. | None. |

### Legal and privacy (5.x)

| Guideline: requirement | Status | Evidence | Fix |
|---|---|---|---|
| 5.1.1(i): a privacy policy link in App Store Connect **and inside the app** | fix | `AboutView` (`ios/Sources/Sheepdog/SettingsScreens.swift:401-452`) and the first-run screen have no privacy link. `SetupHelp` has no privacy URL (`ios/Sources/SheepdogKit/SetupHelp.swift:10-15`). | PR 2: add `SetupHelp.privacyURL` and a "Privacy policy" link to About, the first-run screen and the consent screen. |
| 5.1.1(i): the policy says what is collected, how it is used, retention and deletion, how to withdraw consent, and that third parties give equal protection | fix | [privacy.md](../privacy.md) has four problems. (1) Its iPhone section still says the connection is "plain HTTP … because the daemon has no TLS". (2) Its camera row leaves out the photo attachments. (3) Its notification row says permission is asked "after the first connection", but it is asked only when the daemon can send. (4) It does not say that the AI providers are the user's choice with the user's own keys, how to withdraw consent, or the third-party protection statement. It also names the app "Sheepdog for herdr". | PR 3: rewrite the app section of privacy.md with those facts, and add an "AI services" section that the consent screen links to. |
| 5.1.1(ii): consent for data use; an easy way to withdraw it; accurate purpose strings | fix | The microphone string (`ios/project.yml:167-170`) says the voice goes only to the daemon, which leaves out the AI step after it. The app has no withdraw switch. | PR 4: purpose strings. PR 5: consent with a withdraw switch in Settings. |
| 5.1.1(iii): data minimisation; prefer out-of-process pickers | pass | Photos come through `PhotosPicker` and files through `fileImporter` (`ios/Sources/Sheepdog/AttachButton.swift:16,68`), so there is no photo-library permission. The camera is used only for the QR code and for a photo the user takes. | None. |
| 5.1.1(iv): respect permissions; ask in context | fix (small) | The demo asks for speech recognition (`ios/Sources/Sheepdog/DemoVoice.swift:61`) and the microphone when it starts ([launch-review.md](../../ios/docs/dev/launch-review.md) P2). The demo works when both are declined. | PR 6: ask on the first tap of the microphone in the demo. |
| 5.1.1(v): offer account deletion if the app creates accounts | pass | The app creates no accounts. A paired computer is removed in Settings, and its keys are deleted from the keychain and the Secure Enclave ([security.md](design/security.md#12-removing-a-computer)). | None. |
| 5.1.2(i): **clearly disclose sharing with third-party AI and get explicit permission first** | fix (SVM) | No consent exists (grep for "consent" in `ios/Sources`: nothing). Data path: phone → user's daemon → voice provider (Gemini Live or GPT-Live), and → orchestrator provider (Gemini, DeepSeek, Anthropic, an OpenAI-compatible endpoint, or local Ollama) ([privacy.md](../privacy.md#what-goes-to-which-provider)). The CEO approved a first-launch consent screen with Agree and Learn more. | PR 5 (section 3). |
| 5.1.2(i): App Tracking Transparency | pass | No tracking, no ads, no analytics SDK. | None. Set `NSPrivacyTracking` to false (PR 1). |
| 5.1.2(i): system features (push and others) are not required to use the app | pass | Every permission is optional, and the demo works with all of them declined ([launch-review.md](../../ios/docs/dev/launch-review.md)). | None. |
| 5.2.1, 5.2.2: third-party trademarks and services | CEO | herdr is named in the description and in the app. The app uses SwiftTerm (MIT, `ios/project.yml:23-26`). | Get herdr's permission for the name (row 4.1(c)). Add a licence notice for SwiftTerm in About (part of PR 2). |
| App Store Connect: content rights declaration | CEO | `contentRightsDeclaration: null` (read with `asc`). | Recommended answer: "No, it does not contain, show, or access third-party content". The app shows only the user's own terminals and the answers of the user's own AI accounts. |

### Technical requirements

| Requirement | Status | Evidence | Fix |
|---|---|---|---|
| Privacy manifest with the reasons for required-reason APIs, in each bundle that calls them | fix | No `*.xcprivacy` anywhere in `ios/`. Calls: `UserDefaults.standard` (app, Kit), `UserDefaults(suiteName:)` for the App Group (`ios/Sources/SheepdogPush/PushContentKeyStore.swift:123,132`, compiled into the app's Kit and into the Notification Service Extension), and `ProcessInfo.systemUptime` (`ios/Sources/Sheepdog/SessionViewModel.swift:343`, `ios/Sources/SheepdogKit/SessionCore.swift`). No file-timestamp, disk-space or keyboard APIs. TestFlight uploads go through today, but App Store submission can fail with ITMS-91053. | PR 1: `PrivacyInfo.xcprivacy` in Sheepdog, SheepdogKit, SheepdogWidgets and SheepdogNotificationService. `NSPrivacyTracking` false, no tracking domains, no collected data types. API types: UserDefaults with `CA92.1` (and `1C8F.1` where the App Group suite is used), and SystemBootTime with `35F9.1` (app and Kit). |
| Third-party SDKs: listed SDKs need their own manifest and signature | pass | The only package in the app is SwiftTerm 1.99.0. It is not on Apple's list and has no manifest. Its iOS sources call no required-reason API: the `UserDefaults` uses are in `Sources/SwiftTerm/Mac/` only (checked on the v1.99.0 tag). swift-png and swift-argument-parser are build-tool dependencies and are not shipped. | None. Check again when the SwiftTerm pin changes. |
| ATS: an exception needs a reason | pass, with notes | `NSAllowsArbitraryLoads: true` (`ios/project.yml:182-193`). The code still refuses plain HTTP to anything but loopback and the demo host: `DaemonEndpoint.allowsConnection` (`ios/Sources/SheepdogKit/DaemonClient.swift:78`), checked before every request and socket (`:109-127`, `VoiceSocket.swift:132`, `TermSocket.swift:177`). TLS to the daemon is pinned to the certificate from the pairing code (`PinnedTrust.swift`; [security.md](design/security.md#34-the-app-checks-the-pin)). ATS cannot express "a self-signed certificate on a LAN or Tailscale IP address, checked by pin": exception domains cannot name IP addresses, and ATS runs a system trust check even after the delegate accepts the pin. | The reviewer notes explain it (section 4). If Apple asks for the ATS justification, answer: "connects only to the user's own self-hosted server at a private IP address, over TLS pinned to that server's self-signed certificate". |
| Export compliance (`ITSAppUsesNonExemptEncryption`) | pass | `false` in the app and the Notification Service Extension (`ios/project.yml:201,276`). The app only uses the encryption built into iOS: URLSession TLS, CryptoKit AES-GCM for push content, and Secure Enclave P-256 signatures for device login (`DeviceKey.swift`, `PushE2E.swift`). It has no cryptography of its own and no other libraries. Apple's export rules exempt that. | None. Section 6 gives the answer. |
| Entitlements match capabilities on the App ID | pass | Push, time-sensitive notifications and App Groups are enabled on the App ID. TestFlight 0.1.20 was archived with them ([xcode-cloud.md](../../ios/docs/dev/xcode-cloud.md)). | None. |
| App Store Connect: age rating answered (new questions since 2026-01-31) | CEO | `asc age-rating view` shows only override fields, so the new questions are unanswered. | Fill it in with section 6.3. |
| App Store Connect: review contact, copyright, category, availability | CEO | Not filled. | Section 7. |
| EU Digital Services Act: trader status | CEO | Required for distribution in the EU. A trader's address, phone number and email are shown on the product page. | Decide trader or non-trader in App Store Connect ▸ Business. An individual account shows its legal name as the seller, which matters given the wish to keep the full name out of the project. |
| Accessibility Nutrition Labels | optional | VoiceOver labels, Dynamic Type and dark mode are covered ([launch-review.md](../../ios/docs/dev/launch-review.md)). | Optional for 1.0. If filled: VoiceOver, Larger Text, Dark Interface, Sufficient Contrast, Reduced Motion. Claim only what was tested. |

## 3. Fix PRs

Each PR is small and can land on its own, in this order of rejection risk. None needs another
except PR 5, which uses the URL from PR 2 and the wording from PR 3.

| PR | Title | What | Touches SessionViewModel? | Risk it removes |
|---|---|---|---|---|
| 1 | `fix(ios): privacy manifests for the app and its extensions` | Add `PrivacyInfo.xcprivacy` to the four targets (section 2, technical table). Add them as resources in `ios/project.yml`. Fix the Live Activity comment at `ios/project.yml:158`. Test: a unit test that loads each bundle's manifest and checks the API types; inspect an archive. | No | 6 |
| 2 | `feat(ios): privacy policy and licences in About and Welcome` | `SetupHelp.privacyURL`, a Privacy policy link in About and on the first-run screen, a SwiftTerm licence row, and an About title that uses the store name. UI test on the About screen. | No (`SettingsScreens.swift` reads the model but does not change it) | 5 |
| 3 | `docs: privacy policy and reviewer notes for the App Store` | privacy.md: TLS with a pinned certificate, the camera for photos, the real notification timing, an "AI services" section (who gets what, the user's own keys, how to withdraw consent), the third-party protection statement, the new app name. Replace the notes in `ios/docs/app-review-notes.md` with section 4. Remove "plain HTTP" from `testflight-metadata.md`. Add a Support section with the contact email to iphone-app.md, and update the store name there. | No | 5, 7 |
| 4 | `fix(ios): purpose strings name the AI step` | Microphone: say that the daemon passes the voice to the AI voice service the user chose. Camera: already covers photos. Update `PurposeStringsTests`. | No | 3 (part) |
| 5 | `feat(ios): ask before voice and screens go to AI services` | A one-time consent screen before the first real pairing or voice session, never in the demo. It names the services the connected daemon uses: the voice provider and the orchestrator provider, read from the daemon's model settings, with a generic list when they are unknown. It has **Agree** and **Learn more** (privacy.md "AI services"). Settings ▸ Privacy ▸ "AI services" shows the choice and can withdraw it. Withdrawing stops the voice and disconnects until the user agrees again. The choice is stored per device and asked again if the policy version changes. Test: UI tests for agree, decline and withdraw. Reuses `PermissionPrimer.swift` styling. | **Yes**: starting the voice and connecting must check consent. Wait for the architecture phases. | 3 |
| 6 | `fix(ios): demo asks for the microphone on first use` | Move the speech and microphone requests from the start of the demo to the first tap of the microphone (`DemoVoice.swift`). Remove the demo UI tests' alert dismissals. | Probably not; check whether the demo start path goes through SessionViewModel. If it does, it waits too. | 13 |

CEO and App Store Connect tasks, no PR:

1. Choose the name (risk 1).
2. Get herdr's permission, or drop Herdr from the name (risk 2).
3. Record the real-use video (risk 4).
4. EU trader status (risk 11).
5. Mac and Vision Pro availability (risk 12).
6. Rename the App Store Connect record; fill in the content rights, the age rating, the privacy label, the review contact and the copyright.
7. Submit. Submission and every App Store Connect change need the CEO's OK.

## 4. Reviewer notes (draft for App Review Information ▸ Notes)

Limit: 4,000 characters. This draft has about 3,500.

> No sign-in is needed. The app has no accounts.
>
> WHAT IT IS. Sheepdog is a voice remote for sheepdog, a free open-source program that people
> run on their own Mac or Linux computer to supervise coding agents (Claude Code, Codex and
> others) in the herdr terminal: https://github.com/kccarlos/herdr-sheepdog. The app talks only
> to that program on the user's own computer. We run no server for it, except an optional relay
> that forwards end-to-end encrypted notifications to APNs.
>
> HOW TO REVIEW WITHOUT A COMPUTER. Tap "Try the demo" on the first screen. The demo runs
> entirely on the device, with no network. Four scripted agents work on a sample project. After
> about 15 seconds, codex-payments asks for approval: tap "Yes, proceed". Open Agents for the
> list, tap an agent and "Open window" to see its (scripted) terminal, and tap a suggested
> question such as "What's going on?" to hear a spoken answer. iOS may ask for the microphone
> and speech recognition (on-device only). Both are optional; tap or type instead. "Leave"
> returns to setup. A recording of real use with a computer: <VIDEO LINK>.
>
> AI SERVICES AND CONSENT. In real use, the user's own computer sends the voice and the
> conversation to the AI services the user configured there with their own API keys (Google
> Gemini or OpenAI for the voice; Google Gemini, DeepSeek, Anthropic, an OpenAI-compatible
> service or a local model for answers). Before the first real connection, the app shows a
> consent screen that names these services. Nothing is sent until the user taps Agree, and
> Settings > Privacy withdraws consent. The demo uses no AI service and sends nothing.
>
> THE SHELL AND AGENT WINDOWS. An agent window shows the terminal of a coding agent on the
> user's own computer. "New shell" opens a terminal there. The app sends keystrokes and draws the
> text, like an SSH client. Nothing is downloaded or run on the iPhone, and the app's features
> do not change. The user can turn the shell off on their computer.
>
> NETWORK SECURITY (ATS). NSAllowsArbitraryLoads is on because the app connects to the user's
> own computer at a private IP address (home Wi-Fi or Tailscale), whose certificate is
> self-signed. ATS cannot pin a self-signed certificate on an IP address, so the app checks the
> certificate itself against the fingerprint in the pairing QR code, and rejects any other
> certificate. The app refuses plain HTTP to any address except this device (127.0.0.1, used in
> development). It never loads web content.
>
> BACKGROUND AUDIO. A voice session is live two-way audio with the user's computer, like a
> call. The audio mode keeps it going with the screen locked, if the user leaves "Keep listening
> with the screen locked" on in Settings. The microphone indicator shows while it listens, and
> Stop or the Live Activity ends it.
>
> NOTIFICATIONS AND LIVE ACTIVITY. The app asks for notification permission only after it
> connects to a computer that can send notifications, never in the demo. Notifications are
> encrypted end to end with a key only the phone and the user's computer hold. "Time sensitive"
> is used only when an agent is waiting for the user's approval. The Live Activity shows the
> voice session and the agents waiting. The user's computer updates it, and it ends with the
> session.
>
> PERMISSIONS. Microphone: the voice session (demo: spoken questions). Speech recognition: demo
> only, on device. Camera: scanning the pairing code, or a photo the user attaches for an agent.
> Local network: reaching the user's computer. All are optional.

Two blanks to fill before pasting: `<VIDEO LINK>`, and the "Settings > Privacy" wording once
PR 5 lands. The contact name, phone and email go in App Store Connect, not in this repository.

## 5. App Privacy (nutrition label) draft

**Recommended answer: "Data Not Collected".** In Apple's definition, "collect" means sending
data off the device so that the developer or its third-party partners can access it for longer
than it takes to serve the request in real time. That is not the case here:

- **The daemon** is software the user runs on their own computer. The developer cannot reach
  it.
- **The AI providers** are called by that daemon with the user's own API keys, under the user's
  own accounts. No AI SDK is in the app, and the app talks to no provider directly. Apple
  counts as third-party partners the vendors "whose code you've added to your app", and no
  provider fits that.
- **The push relay** (operated by the developer on Cloudflare) sees the device push token, the
  daemon's IP address, the time, the size and the kind of each notification. It never sees the
  content, which is end-to-end encrypted. It keeps no database, and Workers observability is
  off (`relay/wrangler.toml`: `[observability] enabled = false`). The IP address is used only
  as a rate-limit key (`relay/src/index.ts:73`). Data that is sent with a request and not
  retained does not have to be declared.
- **No analytics, no crash reporting SDK, no ads, no tracking.**

**CEO decision: whether to choose the conservative option.** Risk: the consent screen tells
users that their voice goes to Google or OpenAI, and a reviewer may then expect the label to
say so too. The conservative answer is to declare these, each as *Not linked to the user*, *Not
used for tracking*, purpose *App Functionality*:

- **Audio Data** (the voice);
- **Other User Content** (transcripts, terminal text, attached photos);
- **Photos or Videos** (attachments).

Declaring them costs nothing in review, but the store page then says "Data Not Linked to You"
instead of "Data Not Collected". Recommendation: "Data Not Collected", and keep the
conservative answer ready if the reviewer asks.

## 6. Questionnaires

### 6.1 Export compliance

- `ITSAppUsesNonExemptEncryption = false` is already in the Info.plist of the app and the
  Notification Service Extension. App Store Connect does not ask again.
- If App Store Connect asks anyway: the app uses encryption; it uses only encryption that is
  built into Apple's operating system (HTTPS through URLSession, CryptoKit, Secure Enclave
  keys), for authentication and data protection; it does not implement its own or a
  non-standard algorithm. Result: exempt, no documentation upload.
- France: no separate declaration, because only the operating system's encryption is used.

### 6.2 Content rights

"Does your app contain, show, or access third-party content?": **No** (see the 5.2 row).

### 6.3 Age rating (expected result 4+)

These are the questions App Store Connect asks today (the fields of `asc age-rating edit`). All
of them are answered with the lowest value.

| Section | Question | Answer |
|---|---|---|
| In-app controls | Parental controls | No |
| | Age assurance | No |
| Capabilities | Unrestricted web access | No. The app has no browser or web view. The shell is a terminal on the user's own computer, as in SSH clients rated 4+. |
| | User-generated content (shared with other users) | No. Nothing a user makes is shown to other users. |
| | Social media; social media restricted to 13 and over | No; No |
| | Messaging and chat (between users) | No. The voice and the agents are the user's own tools, not other people. |
| | Advertising | No |
| Mature themes | Profanity or crude humour; Horror or fear; Alcohol, tobacco or drugs | None |
| Medical or wellness | Medical or treatment information; Health or wellness topics | None; No |
| Sexuality or nudity | Mature or suggestive themes; Sexual content or nudity; Graphic sexual content | None |
| Violence | Cartoon or fantasy; Realistic; Prolonged graphic; Guns or other weapons | None |
| Chance | Gambling; Simulated gambling; Contests; Loot boxes | No; None; None; No |
| Kids | Made for Kids / kids age band | Not set |
| Overrides | Age rating override | None |

## 7. Metadata

| Field | Draft | Notes |
|---|---|---|
| Name (≤ 30) | **CEO decision.** (a) `Sheepdog: Voice for Herdr` (25) if herdr's maintainers agree in writing. (b) `Sheepdog: Voice for AI Agents` (29) otherwise. | The decided `Sheepdog — voice for Herdr agents` is 33 characters, too long. "Sheepdog" alone is too generic to be unique: "Iron Sheepdog" and "SheepDogAG" are on the store. The home-screen label stays `Sheepdog`. |
| Subtitle (≤ 30) | `Manage coding agents by voice` (29) | The tagline "Manage your Herdr agents by voice." has 34 characters, and subtitles may not reference other apps. |
| Keywords (≤ 100, comma-separated) | `voice,coding,agent,terminal,developer,ai,assistant,approval,cli,remote,shell,supervise,hands-free` (97) | No trademarks (Claude, Codex, Gemini, herdr). Words already in the name and subtitle are not repeated. |
| Promotional text (≤ 170) | `Hear what your coding agents did, answer their approval prompts by tap or by voice, and look at any agent's terminal from your iPhone or iPad. Free and open source.` (164) | Can change without review. |
| Description (≤ 4,000) | See below. | |
| Support URL | `https://github.com/kccarlos/herdr-sheepdog/blob/main/docs/iphone-app.md#support` | The anchor exists after PR 3 adds a Support section with the contact email. Until then, use `…/docs/iphone-app.md#troubleshooting`. |
| Marketing URL | `https://github.com/kccarlos/herdr-sheepdog` | Optional. |
| Privacy policy URL | `https://github.com/kccarlos/herdr-sheepdog/blob/main/docs/privacy.md` | Resolves: the repository is public. |
| Category | Primary **Developer Tools**, secondary **Productivity** | |
| Price | Free, no in-app purchases | |
| Availability | Every country or region except mainland China | China mainland would also need an ICP filing. EU needs trader status (risk 11). |
| Mac and Vision Pro | Off for 1.0 (CEO) | Risk 12. |
| Copyright | `2026 <seller name>` (CEO) | |
| Version | `1.0` or keep `0.1.x` (CEO) | `MARKETING_VERSION` is 0.1.20 (`ios/project.yml:40`). Either is allowed. |
| Review contact | CEO fills in App Store Connect | Not in the repository. |

Description draft (about 1,500 characters):

> Sheepdog lets you manage the coding agents running on your computer by voice: Claude Code,
> Codex and others, in the herdr terminal.
>
> Talk to Sheepdog to hear what your agents did. Answer their approval prompts by tap or by
> voice. Open any agent's terminal, or a shell on your computer, when you are away from your
> desk. A Live Activity keeps the voice session on the Lock Screen and in the Dynamic Island,
> and notifications tell you when an agent needs you.
>
> Sheepdog works with sheepdog, a free open-source program you run on your own Mac or Linux
> computer. Install it from the herdr plugin marketplace, run "sheepdog setup", and scan the
> pairing code it shows. The app connects only to your computer, over your Wi-Fi or Tailscale,
> with an encrypted connection checked against that code.
>
> Your computer uses the AI services you choose, with your own API keys: Google Gemini or OpenAI
> for the voice, and Gemini, DeepSeek, Anthropic, an OpenAI-compatible service or a local model
> for answers. The app asks before anything goes to them. There is no account, no tracking and
> no ads. Notifications are encrypted end to end.
>
> No computer at hand? Tap "Try the demo": scripted agents run on your iPhone, one asks for
> your approval, and the voice reads the news aloud. No network needed.
>
> On iPad: a sidebar of agents, several windows, and drag and drop.
>
> Source code and setup: github.com/kccarlos/herdr-sheepdog

The provider names in the description are statements of what the app works with, which
2.3.7 allows. They stay out of the name, the subtitle and the keywords.

## 8. Screenshot plan

**Sizes.** iPad screenshots are required, because the app runs on iPad. For iPhone, Apple
requires the large Face ID set unless the large Dynamic Island set is provided, and the
screenshot page also asks for at least one medium Dynamic Island screenshot. Providing both
Dynamic Island sets covers every rule:

| Set | Simulator (already exists; do not create one) | Pixels (portrait) |
|---|---|---|
| iPhone, Dynamic Island, large | iPhone Air (or any iPhone 17 Pro Max) | 1260 × 2736 (Air) / 1320 × 2868 (Pro Max) |
| iPhone, Dynamic Island, medium | iPhone 17 | 1206 × 2622 |
| iPad 13" | iPad Pro 13-inch (M5) | 2064 × 2752 |

Up to 10 per set. PNG or JPEG, with no alpha channel. Smaller devices are scaled from these.
`LaunchReviewScreenshotsUITests` takes its shots on iPhone 17 Pro, so add the three
destinations above.

**Screens (5 per set, from the demo, light mode, in this order):**

1. **Conversation with the voice speaking:** the orb, an announcement, the agent strip.
   Caption: "Hear what your agents did."
2. **Approval card** (codex-payments): "Yes, proceed". Caption: "Approve by tap or by voice."
3. **All agents:** four agents with their states. Caption: "Every agent at a glance."
4. **Agent window:** the scripted terminal and the key bar. Caption: "Open any agent's
   terminal."
5. **Lock Screen Live Activity** ([live-activity-light.png](../../ios/docs/screenshots/live-activity-light.png)
   style). Caption: "Stays with you on the Lock Screen." On iPad, use the sidebar with the
   agent list and a window side by side instead.

Optional sixth: dark mode of shot 1.

Rules: show the "Demo" label, as it is on screen. Show no real names, addresses or tokens.
Captions, if any, go in a plain band above the screenshot in brand colours
([brand.md](brand.md)). Show no prices and no other apps' icons. An app preview video is
optional; skip it for 1.0.
