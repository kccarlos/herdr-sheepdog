import AVFoundation
import Combine
import Foundation
import SheepdogKit
import SwiftUI

/// The screens Settings can push.
enum SettingsRoute: Hashable {
    case models
    case languages
}

/// Everything the main screen shows, in one place.
///
/// It owns the socket, the audio engine, the event stream and the
/// conversation. The session's decisions — connecting, reconnecting, mute,
/// the audio lifecycle, hold-to-talk, what the orb says — are
/// `SessionCore`'s: this adapts every callback into a `SessionEvent`, runs
/// the `SessionEffect`s the core returns, and publishes what it derives.
/// History, approvals, the provider picker and the hints are still here.
@MainActor
final class SessionViewModel: ObservableObject {
    struct Banner: Identifiable, Equatable {
        enum Tone { case error, warning, info }
        let id = UUID()
        var tone: Tone = .error
        var message: String
        var hint: String?
        /// Shown as a button when the user can do something about it.
        var retryTitle: String?
        /// What that button does: reconnect (the default) or open the
        /// system's microphone-mode panel.
        var action: Action = .retry

        enum Action: Equatable { case retry, microphoneModes }
    }

    // MARK: - Published state

    /// The orb, `SessionCore.sessionState` as of the last event.
    /// `offerListeningHints()` reacts here, at the one place it changes.
    @Published private(set) var state: SessionState = .disconnected {
        didSet { if state != oldValue { offerListeningHints() } }
    }
    /// The conversation as the screen lists it: history, live bubbles and
    /// the dividers between them.
    @Published private(set) var rows: [TimelineRow] = []
    /// The approval card pinned above the controls, if a worker is asking.
    @Published private(set) var approval: ApprovalFlow.Card?
    @Published private(set) var canLoadOlder = false
    @Published private(set) var loadingOlder = false
    /// The one-time caption under the controls the first time Mute is used.
    @Published private(set) var showMuteHint = false
    @Published private(set) var agents: [Agent] = []
    /// The agents as the screen groups them: running (blocked, working,
    /// idle and done) and the rest.
    var flock: Flock { Flock(agents) }
    /// Which pane the main screen shows; a notification tap switches it.
    @Published var pane: MainPane = .conversation
    /// The agent the Flock pane should scroll to (a notification tap).
    @Published var focusedAgentPaneID: String?
    @Published private(set) var pendingAction: PendingAction?
    /// Four sources feed `resolvedVoiceProvider`; each tells the audio
    /// engine's jitter-buffer profile explicitly when it changes, rather
    /// than re-deriving and re-setting it on every event regardless of
    /// whether anything provider-related moved.
    @Published private(set) var hello: Hello? {
        didSet { audio.setProvider(resolvedVoiceProvider) }
    }
    /// The input level, the audio metrics and the voice-detection
    /// diagnostics: everything that changes 10–25 times a second while
    /// audio is moving, split out so this object's own `@Published`
    /// properties (and the views that only read those) stay quiet on every
    /// tick. See `VoiceMeter`.
    let meter = VoiceMeter()
    /// The daemon connection's own health — the `/ws` socket, the `/events`
    /// stream and the token — independent of the voice state. Drives the
    /// top-left chip.
    @Published private(set) var connectionChip = ConnectionChip(.init())
    /// The `/ws` protocol handshake (`ProtocolHandshake`), for Health and
    /// the chip's sheet.
    @Published private(set) var handshake: ProtocolHandshake.State = .idle
    /// The supervisor's model, from `/api/state`'s `brain`, when the
    /// daemon reports one.
    @Published private(set) var brain: BrainInfo?
    /// Whether the ⋯ menu offers "New conversation" — learned from the
    /// daemon's own answer to `POST /api/conversation/reset` (a 404 hides
    /// it), optimistic (shown) until then.
    @Published private(set) var conversationResetSupported = true
    @Published var banner: Banner?
    /// Typed input (`POST /api/inject/utterance`): in flight, and the
    /// daemon's refusal if it had one.
    @Published private(set) var sendingTyped = false
    @Published var typedError: String?
    /// Bumped on every typed line the daemon took: the conversation jumps
    /// to its end, where the words and the answer are about to land.
    @Published private(set) var typedSent = 0

    // The Health pane.
    /// Uptime, GPT-Live's billed seconds and the recent problems.
    @Published private(set) var health = DaemonHealth()
    @Published private(set) var loadingHealth = false
    /// The brain's catalog and choice; nil until loaded, and on a daemon
    /// without the route.
    @Published private(set) var brainModels: BrainModels?
    @Published private(set) var switchingBrain = false
    @Published var brainError: String?
    /// The push chattiness; nil until loaded, and without a supervisor.
    @Published private(set) var daemonSettings: DaemonSettings?
    @Published var settingsError: String?

    // Mirrors of `core`, published for the views (see `publish()`).
    @Published private(set) var isMuted = false
    @Published private(set) var talkMode: TalkMode = .openMic
    /// Hold-to-talk: the orb is being held.
    @Published private(set) var holding = false
    /// The microphone delivers only digital zero while unmuted.
    @Published private(set) var noMicSignal = false
    /// True from the moment Connect is tapped until Disconnect.
    @Published private(set) var wantsConnection = false

    // Voice robustness: the coach mark, the microphone mode.
    /// The first-run "just talk" coach mark.
    @Published private(set) var showCoachMark = false
    /// Control Center ▸ Mic Mode, as iOS reports it.
    @Published private(set) var microphoneMode: MicrophoneModeName = .standard
    @Published private(set) var preferredMicrophoneMode: MicrophoneModeName = .standard

    // The voice provider (GET/PUT /api/voice/provider[s]).
    /// nil until loaded, and when the daemon has no provider API (404).
    @Published private(set) var voiceProviders: VoiceProviders? {
        didSet { audio.setProvider(resolvedVoiceProvider) }
    }
    @Published private(set) var switchingProvider = false
    @Published var providerError: String?
    /// From the `/ws` status message, `/api/state` and `/api/hello`.
    @Published private(set) var liveVoiceProvider: String? {
        didSet { audio.setProvider(resolvedVoiceProvider) }
    }
    @Published private(set) var stateVoiceProvider: String? {
        didSet { audio.setProvider(resolvedVoiceProvider) }
    }
    @Published private(set) var stateVoiceModel: String?
    @Published var showSettings = false
    /// Where Settings opens: nil for its top, `.models` from Health's
    /// "Change in Settings ›". Consumed (reset) by SettingsView.
    @Published var settingsRoute: SettingsRoute?

    /// Health's "Change in Settings ›": Settings, pushed to Models.
    func openModels() {
        settingsRoute = .models
        showSettings = true
    }

    /// "Gemini · gemini-3.5-flash": the brain as the catalog names it, else
    /// as `/api/state` reports it.
    var brainDisplay: String? {
        if let models = brainModels, !models.model.isEmpty {
            let provider = models.providers.first { $0.id == models.provider }?.displayName ?? models.provider
            return [provider, models.model].filter { !$0.isEmpty }.joined(separator: " · ")
        }
        return brain?.display
    }

    /// The Models row's one-line value in Settings.
    var modelsSummary: String {
        let parts = [voiceProviderDisplay, brainDisplay].compactMap { $0 }
        return parts.isEmpty ? "—" : parts.joined(separator: ", ")
    }

    var voiceName: String { hello?.displayName ?? "Sheepdog" }

    /// The line above the voice bar: what to do, not only what is happening.
    var statusLine: String {
        VoiceBarModel.statusLine(state: state, talkMode: talkMode, holding: holding, noSignal: noMicSignal)
    }

    /// The voice provider id, from whichever source knows it right now: the
    /// live `/ws` status, the provider picker's own state, `/api/state`, or
    /// `/api/hello` — in that order of freshness. One resolver instead of
    /// this fallback chain copied at each of its two call sites.
    private var resolvedVoiceProvider: String? {
        liveVoiceProvider ?? voiceProviders?.provider ?? stateVoiceProvider ?? hello?.voiceProvider
    }

    /// "Gemini Live · gemini-3.8-live", from whichever source knows.
    var voiceProviderDisplay: String? {
        let id = resolvedVoiceProvider
        let option = voiceProviders?.providers.first { $0.id == id }
        let model = (id == voiceProviders?.provider ? voiceProviders?.model : nil)
            ?? (id == stateVoiceProvider ? stateVoiceModel : nil)
            ?? (id == hello?.voiceProvider ? hello?.voiceModel : nil)
            ?? option?.model
        return VoiceProviders.display(provider: option?.displayName ?? id, model: model)
    }
    var daemonVersion: String? { hello?.version }

    /// The `/ws` protocol for Health: the handshake's word once a socket
    /// opened, otherwise what `/api/hello` says (nothing there: an older
    /// daemon, protocol 1).
    var protocolDisplay: String {
        if handshake != .idle { return handshake.summary }
        guard let hello else { return "—" }
        guard let p = hello.protocol else { return "1 (older daemon)" }
        return "\(p) (serves \(hello.minProtocol ?? p) and up)"
    }
    /// The daemon connection is up: notifications stay quiet in the
    /// foreground, the screen already shows what they would say.
    var sessionIsLive: Bool { connectionChip.status == .connected }
    var endpointDisplay: String { settings.endpoint?.display ?? "not set" }
    var isConfigured: Bool { settings.endpoint != nil }

    func pendingQuestion(for agent: Agent) -> String? {
        guard let pending = pendingAction, pending.paneID == agent.paneID else { return nil }
        return pending.question ?? pending.spokenSummary
    }

    // MARK: - Collaborators

    let settings: SettingsStore
    private var client: DaemonClient
    private var socket: VoiceSocket
    private let audio = AudioEngine()
    /// The Live Activity and the earbud remote (`AwayControls.swift`).
    let away = AwayControls()
    private var reducer = ConversationReducer()
    private var history = ConversationHistory()
    /// The brain's replies and held pushes, which the history does not carry.
    private var activity = BrainActivity()
    /// `approval` (the one thing the views read) is derived from this on
    /// every mutation — a single `didSet`, not a copy hand-written at each
    /// of the half-dozen call sites that used to change `approvalFlow`.
    private var approvalFlow = ApprovalFlow() {
        didSet { approval = approvalFlow.card }
    }
    private let cache: ConversationCache
    private var saveTask: Task<Void, Never>?
    private var historySyncTask: Task<Void, Never>?
    private var syncingHistory = false

    private var eventTask: Task<Void, Never>?
    private var refreshTask: Task<Void, Never>?
    private var ticker: Task<Void, Never>?
    private var lastSeq: UInt64 = 0

    /// The session's state and decisions. Fed only through `send(_:)`.
    private var core: SessionCore

    /// The core's clock: monotonic (uptime), as the reconnect quiet period
    /// and the speaking hold always were.
    private var now: Date { Date(timeIntervalSinceReferenceDate: ProcessInfo.processInfo.systemUptime) }

    init(settings: SettingsStore = SettingsStore(), cache: ConversationCache = .standard()) {
        self.settings = settings
        self.cache = cache
        let endpoint = settings.endpoint ?? DaemonEndpoint(host: "127.0.0.1")
        self.client = DaemonClient(endpoint: endpoint)
        self.socket = VoiceSocket(endpoint: endpoint)
        self.core = SessionCore(talkMode: settings.talkMode, keepListeningWhenLocked: settings.keepListeningWhenLocked)
        talkMode = settings.talkMode
        if talkMode == .holdToTalk { audio.setManualGate(false) }
        #if DEBUG
        if DebugLaunch.resetHints || DebugLaunch.resetState {
            for key in Self.hintKeys {
                UserDefaults.standard.removeObject(forKey: key)
            }
        }
        #endif
        wire()
        away.onRoute = { [weak self] route in self?.handle(route: route) }
        // The last conversation, at once: before the network, or without it.
        loadCache()
        conversationChanged(save: false)
    }

    /// Called once from the first `onAppear`: an unconfigured app opens
    /// Settings instead of showing a Connect button that cannot work.
    func start() {
        // Siri, the Action Button and the Live Activity's buttons reach the
        // session from here on; a route that came first (a cold launch from
        // "Talk to Sheepdog") runs now.
        SheepdogIntentBridge.install { [weak self] url in self?.open(url: url) }
        syncAway()
        if !isConfigured || !settings.hasBeenConfigured {
            showSettings = true
        } else {
            Task {
                await refreshState(quiet: true)
                await syncHistory()
                await backfillActivity()
            }
        }
    }

    /// The app came back to the foreground: what happened while it was
    /// away is on the daemon.
    func becameActive() {
        send(.scenePhase(.active))
        Task { await PushCoordinator.shared.refreshAuthorization() }
        guard isConfigured, settings.hasBeenConfigured else { return }
        Task {
            await refreshState(quiet: true)
            await syncHistory()
            await backfillActivity()
        }
    }

    /// The app is going to the background: write the cache now, not later.
    func willResignActive() {
        saveCache(now: true)
    }

    /// The app is in the background (screen locked, or another app). With
    /// Keep listening with the screen locked off, the core pauses: the audio
    /// stops and the daemon is told we are muted, so it closes a per-minute
    /// GPT-Live session. `becameActive` undoes it.
    func enteredBackground() {
        willResignActive()
        send(.keepListeningLockedChanged(settings.keepListeningWhenLocked))
        send(.scenePhase(.background))
    }

    /// Settings were saved: repoint everything without losing the conversation.
    func endpointChanged() {
        guard let endpoint = settings.endpoint else { return }
        PushCoordinator.shared.endpointChanged(from: client.endpoint)
        let sameDaemon = ConversationCache.key(for: endpoint) == ConversationCache.key(for: client.endpoint)
        if !sameDaemon { saveCache(now: true) }
        client.update(endpoint: endpoint)
        socket.update(endpoint: endpoint)
        banner = nil
        hello = nil
        if !sameDaemon {
            // Another daemon, another conversation.
            reducer.clear()
            activity.clear()
            health.clear()
            brainModels = nil
            daemonSettings = nil
            approvalFlow.reset()
            loadCache()
            conversationChanged(save: false)
            // Learned per daemon: a different one gets the benefit of the
            // doubt again rather than staying hidden from the last one's 404.
            conversationResetSupported = true
        }
        if core.connection.wantsConnection {
            disconnect()
            connect()
        } else {
            Task {
                await refreshState(quiet: true)
                await syncHistory()
            }
        }
    }

    // MARK: - Connect / disconnect

    func connect() {
        guard let endpoint = settings.endpoint else {
            showSettings = true
            return
        }
        client.update(endpoint: endpoint)
        socket.update(endpoint: endpoint)
        send(.connectTapped)
    }

    /// The `probe` effect: the auth probe first — a 401 on the WebSocket
    /// upgrade is almost unreadable, so find out here where the status code
    /// is plain — then the microphone permission.
    private func openSession() async {
        do {
            let state = try await client.state()
            apply(state)
            hello = try? await client.hello()
            await loadVoiceProviders()
        } catch let error as DaemonError {
            send(.probeFailed(error))
            return
        } catch {
            send(.probeFailed(.malformed(error.localizedDescription)))
            return
        }
        guard core.connection.wantsConnection else { return }
        let granted = await AudioEngine.requestMicrophone()
        // The permission prompt can sit there for a while; the core ignores
        // the answer if End was tapped meanwhile.
        send(.probeSucceeded(micGranted: granted))
    }

    func disconnect() {
        send(.endTapped)
        meter.setLevel(0)
    }

    /// The chip popover's Reconnect button: a clean disconnect (if one is
    /// needed) followed by a fresh connect, whatever state we are in now.
    func reconnect() {
        if core.connection.wantsConnection { disconnect() }
        connect()
    }

    // MARK: - Controls

    func toggleMute() {
        let wasMuted = core.mic.muted
        send(.muteTapped)
        if !wasMuted, core.mic.muted { offerMuteHint() }
    }

    static let muteHint = "Mute stops your microphone; Sheepdog can still talk."
    /// The one-time caption in the bar's status line: the hint, short
    /// enough for two lines beside the orb.
    var muteCaption: String { "Mic off — \(voiceName) can still talk" }
    private static let muteHintShownKey = "hint.mute.shown"
    private static let coachShownKey = "hint.coach.shown"
    private static let micTipShownKey = "hint.micmode.shown"
    /// Every one-time hint, for `-sheepdogResetHints` / `-sheepdogResetState`.
    static let hintKeys = [muteHintShownKey, coachShownKey, micTipShownKey]

    // MARK: - Talk mode, hold to talk

    func setTalkMode(_ mode: TalkMode) {
        guard mode != core.mic.talkMode else { return }
        settings.talkMode = mode
        send(.talkModeChanged(mode))
    }

    /// The orb went down in hold-to-talk. Talking over the dog stops it.
    func pressToTalk() {
        let wasHolding = core.mic.holding
        send(.holdBegan)
        #if DEBUG
        if fakeVoice, !wasHolding, core.mic.holding { send(.simulatedGate(closed: false)) }
        #endif
    }

    /// The orb came up: close the gate, the daemon hears the silence.
    func releaseToTalk() {
        let wasHolding = core.mic.holding
        send(.holdEnded)
        #if DEBUG
        if fakeVoice, wasHolding, !core.mic.holding { send(.simulatedGate(closed: true)) }
        #endif
    }

    // MARK: - Coach mark and the microphone-mode tip

    /// Once per install, the first time the dog is actually listening:
    /// "just talk". After that (the next time), the Voice Isolation tip.
    private func offerListeningHints() {
        guard core.mic.talkMode == .openMic, VoiceBarModel.isListening(state), core.connection.wantsConnection else { return }
        let defaults = UserDefaults.standard
        if !defaults.bool(forKey: Self.coachShownKey) {
            guard !showCoachMark else { return }
            defaults.set(true, forKey: Self.coachShownKey)
            showCoachMark = true
            Task { [weak self] in
                try? await Task.sleep(nanoseconds: 9_000_000_000)
                self?.dismissCoachMark()
            }
            return
        }
        guard !showCoachMark, banner == nil, !defaults.bool(forKey: Self.micTipShownKey) else { return }
        refreshMicrophoneMode()
        guard microphoneMode != .voiceIsolation else { return }
        defaults.set(true, forKey: Self.micTipShownKey)
        show(Banner(
            tone: .info,
            message: "Noisy room? Turn on Voice Isolation.",
            hint: "iOS filters out the room before \(voiceName) hears it. It is in Control Center ▸ Mic Mode, or Settings ▸ Microphone mode.",
            retryTitle: "Microphone mode",
            action: .microphoneModes
        ))
    }

    func dismissCoachMark() {
        guard showCoachMark else { return }
        showCoachMark = false
    }

    func refreshMicrophoneMode() {
        let active = MicrophoneModeName(rawValue: AVCaptureDevice.activeMicrophoneMode.rawValue)
        let preferred = MicrophoneModeName(rawValue: AVCaptureDevice.preferredMicrophoneMode.rawValue)
        if active != microphoneMode { microphoneMode = active }
        if preferred != preferredMicrophoneMode { preferredMicrophoneMode = preferred }
    }

    /// Control Center's Mic Mode panel, on top of the app.
    func showMicrophoneModes() {
        AVCaptureDevice.showSystemUserInterface(.microphoneModes)
    }

    // MARK: - Voice provider

    func loadVoiceProviders() async {
        guard settings.endpoint != nil else { return }
        do {
            let list = try await client.voiceProviders()
            #if DEBUG
            // The fixture daemon (`--voice=off`) lists no providers.
            if list?.providers.isEmpty ?? true, DebugLaunch.fakeProviders {
                voiceProviders = Self.fakeProviders(current: voiceProviders?.provider ?? "gemini")
                return
            }
            #endif
            voiceProviders = list
        } catch {
            // Hidden rather than an error: the picker is a convenience, and
            // the connection itself reports what is wrong.
            SheepdogLog.session.error("voice providers: \(String(describing: error), privacy: .public)")
            voiceProviders = nil
        }
    }

    func selectVoiceProvider(_ id: String) {
        guard id != voiceProviders?.provider, !switchingProvider else { return }
        switchingProvider = true
        providerError = nil
        Task { [weak self] in
            guard let self else { return }
            defer { self.switchingProvider = false }
            do {
                let list = try await self.client.setVoiceProvider(id)
                self.voiceProviders = list
                self.send(.providerSelected(list.provider))
                Haptics.tap()
            } catch let error as DaemonError {
                #if DEBUG
                if DebugLaunch.fakeProviders {
                    self.voiceProviders = Self.fakeProviders(current: id)
                    self.send(.providerSelected(id))
                    return
                }
                #endif
                self.providerError = error.message
            } catch {
                self.providerError = error.localizedDescription
            }
            await self.refreshState(quiet: true)
        }
    }

    #if DEBUG
    /// The contract's example, for screenshots against a daemon that lists
    /// no voice providers (the `--voice=off` fixture daemon).
    static func fakeProviders(current: String) -> VoiceProviders {
        VoiceProviders(
            provider: current,
            model: current == "openai" ? "gpt-live-1" : "gemini-3.8-live",
            providers: [
                VoiceProviderOption(id: "gemini", label: "Gemini Live", model: "gemini-3.8-live", available: true),
                VoiceProviderOption(id: "openai", label: "GPT-Live", model: "gpt-live-1", available: true, billing: "per_minute"),
            ]
        )
    }
    #endif

    /// Once per install: what Mute does and does not do.
    private func offerMuteHint() {
        guard !UserDefaults.standard.bool(forKey: Self.muteHintShownKey) else { return }
        UserDefaults.standard.set(true, forKey: Self.muteHintShownKey)
        showMuteHint = true
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 6_000_000_000)
            self?.showMuteHint = false
        }
    }

    func dismissMuteHint() {
        showMuteHint = false
    }

    /// The Stop button: silence now, and ask the daemon to cancel. It
    /// answers with a `flush`, which we also honour.
    func stop() {
        send(.stopTapped)
    }

    // MARK: - Wiring

    private func wire() {
        socket.onEvent = { [weak self] event in
            self?.handle(socket: event)
        }
        audio.onFrame = { [weak self] frame in
            self?.socket.send(frame: frame)
        }
        audio.onGate = { [weak self] closed in
            self?.send(.gateChanged(closed: closed))
        }
        audio.onLevel = { [weak self] level in
            self?.meter.setLevel(level)
        }
        audio.onSpeakingChanged = { [weak self] speaking in
            self?.send(.playbackChanged(speaking: speaking))
        }
        // What the player did, straight to the daemon (like the frames):
        // "speaking" on the daemon means the shepherd still hears it.
        audio.onPlaybackReport = { [weak self] report in
            self?.socket.send(report.message)
        }
        audio.onMicSignal = { [weak self] noSignal in
            self?.send(.micSignal(ok: !noSignal))
        }
        audio.onStopped = { [weak self] message in
            self?.send(.audioStopped(message))
        }
        audio.onPausedChanged = { [weak self] paused in
            self?.send(.interruption(paused: paused))
        }
        audio.onNotice = { [weak self] notice in
            guard let self else { return }
            self.reducer.apply(.notice(notice))
            self.conversationChanged()
        }
    }

    private func handle(socket event: VoiceSocket.Event) {
        switch event {
        case .opened:
            send(.socketOpened)
            #if DEBUG
            // The UI tests start configured; a permission alert would sit
            // on top of everything they tap.
            if DebugLaunch.address != nil || DebugLaunch.resetState, !DebugLaunch.askPush { break }
            #endif
            PushCoordinator.shared.sessionConnected()
        case let .control(message):
            handle(control: message)
        case let .audio(data):
            // Paused in the background: nothing plays, and nothing piles
            // up to play stale on return.
            guard core.acceptsPlayback else { break }
            audio.enqueue(data)
        case let .closed(reason, retrying):
            send(.socketClosed(reason: reason, willRetry: retrying))
            if retrying { probeTokenAfterSocketFailure() }
        case let .failed(error, retrying):
            send(.socketFailed(error, willRetry: retrying))
            if retrying { probeTokenAfterSocketFailure() }
        case let .handshake(state):
            send(.handshakeChanged(state))
        }
    }

    private var tokenProbe: Task<Void, Never>?

    /// A 401 on the WebSocket upgrade is not readable from
    /// `URLSessionWebSocketTask`: a token revoked mid-session looked like
    /// "could not connect" forever. After a failed reconnect, ask
    /// `/api/state` (where a 401 is plain) and end the session with "the
    /// token was refused" if that is what it is. One probe at a time.
    private func probeTokenAfterSocketFailure() {
        guard tokenProbe == nil, core.connection.wantsConnection else { return }
        tokenProbe = Task { [weak self] in
            guard let self else { return }
            defer { self.tokenProbe = nil }
            do {
                _ = try await self.client.state()
            } catch DaemonError.unauthorized {
                self.send(.tokenRefused(endpoint: self.endpointDisplay))
            } catch {
                // Unreachable or anything else: the reconnect loop's own
                // words already say it.
            }
        }
    }

    private func handle(control message: WSMessage) {
        switch message.type {
        case .status:
            var message = message
            #if DEBUG
            if fakeVoice {
                // Screenshots against a `--voice=off` daemon: pretend the
                // live session is up, without touching the microphone.
                message.voice = .connected
                message.reason = nil
            }
            #endif
            send(.status(message))
        case .transcript:
            guard let dir = message.dir, let text = message.text else { break }
            reducer.apply(.transcript(dir, text: text, final: message.final ?? false))
            conversationChanged()
            if message.final == true { scheduleHistorySync() }
        case .flush:
            send(.flush)
        case .error:
            if let text = message.message { send(.daemonError(text)) }
        case .pong, .mute, .silence, .speaking, .playback, .cancel, .text, .ping, .clientReady, .botReady:
            break
        }
    }

    // MARK: - The core

    /// Every session event goes through here: the core decides, then its
    /// effects run in order, then the views get the result. Effects may
    /// call back synchronously (`audio.setMuted` reports the gate), which
    /// re-enters here — safe, since `apply` has returned by then.
    private func send(_ event: SessionEvent) {
        let effects = core.apply(event, now: now)
        // What the socket says on every open.
        socket.muted = core.connection.socketMuted
        socket.gateClosed = core.connection.socketGateClosed
        socket.speaking = core.connection.socketSpeaking
        // The playback profile follows the provider before any effect runs.
        if liveVoiceProvider != core.voice.provider { liveVoiceProvider = core.voice.provider }
        for effect in effects { run(effect) }
        publish()
    }

    private func run(_ effect: SessionEffect) {
        switch effect {
        case .probe:
            Task { await openSession() }
        case .openSocket:
            socket.connect()
        case .closeSocket:
            socket.disconnect()
        case .startStreams:
            startEvents()
            startRefreshing()
        case .stopStreams:
            eventTask?.cancel(); eventTask = nil
            refreshTask?.cancel(); refreshTask = nil
        case let .send(message):
            socket.send(message)
        case .startAudio:
            startAudioIfNeeded()
        case .stopAudio, .suspendAudio:
            audio.stop()
        case .resumeAudio:
            do {
                try audio.resume()
            } catch {
                send(.audioResumeFailed(error.localizedDescription))
            }
        case let .setCaptureMuted(muted):
            audio.setMuted(muted)
        case let .setManualGate(manual):
            audio.setManualGate(manual)
        case .flushPlayback:
            audio.flushPlayback()
        case .flushTranscript:
            reducer.apply(.flush)
            conversationChanged()
        case .startTicker:
            startTicking()
        case .stopTicker:
            ticker?.cancel(); ticker = nil
        case let .showBanner(banner):
            show(Banner(banner))
        case .clearBanner:
            banner = nil
        case let .notice(text):
            reducer.apply(.notice(text))
            conversationChanged()
        case let .haptic(kind):
            switch kind {
            case .connected: Haptics.connected()
            case .tap: Haptics.tap()
            case .stop: Haptics.stop()
            case .answerFailed: Haptics.answerFailed()
            case .micLost: Haptics.warning()
            }
        case .openSettings:
            showSettings = true
        case .refreshState:
            refreshSoon()
        case .syncHistory:
            scheduleHistorySync()
        case let .log(line):
            switch line {
            case let .reconnectRetry(message):
                SheepdogLog.socket.info("reconnect attempt failed, retrying: \(message, privacy: .public)")
            case let .muteDisagreement(daemonMuted, wanted):
                SheepdogLog.session.error("daemon reports muted=\(daemonMuted, privacy: .public) but the app wants muted=\(wanted, privacy: .public); re-sending")
            case .pausedInBackground:
                SheepdogLog.session.info("paused in the background (keep listening when locked is off)")
            case .resumedFromBackground:
                SheepdogLog.session.info("resumed from the background")
            }
        }
    }

    /// The `startAudio` effect: the engine, unless it already runs.
    private func startAudioIfNeeded() {
        guard !audio.isRunning else { return }
        #if DEBUG
        if fakeVoice {
            send(.simulatedGate(closed: core.mic.talkMode == .holdToTalk))
            return
        }
        #endif
        do {
            try audio.start()
            meter.setMetrics(audio.metrics)
            send(.audioStarted(gateClosed: audio.gateIsClosed))
        } catch {
            send(.audioStartFailed((error as? LocalizedError)?.errorDescription ?? "The microphone would not start."))
        }
    }

    /// The core's derived values, for the views. Assigned only on change,
    /// so a quiet event does not re-render anything.
    private func publish() {
        if isMuted != core.mic.muted { isMuted = core.mic.muted }
        if talkMode != core.mic.talkMode { talkMode = core.mic.talkMode }
        if holding != core.mic.holding { holding = core.mic.holding }
        if noMicSignal != core.mic.noSignal { noMicSignal = core.mic.noSignal }
        if wantsConnection != core.connection.wantsConnection { wantsConnection = core.connection.wantsConnection }
        let chip = core.connectionChip
        if chip != connectionChip { connectionChip = chip }
        if handshake != core.connection.handshake { handshake = core.connection.handshake }
        let next = core.sessionState
        if next != state { state = next }
        syncAway()
    }

    // MARK: - Away from the screen

    /// The Live Activity and the earbud remote follow the session: there
    /// while a voice session is wanted, gone the moment it ends.
    private func syncAway() {
        #if DEBUG
        if let demo = DebugLaunch.demoLiveActivity {
            away.sync(VoiceActivityState.make(state: demo, muted: demo == .muted, waiting: 2, voiceName: voiceName), voiceName: voiceName)
            return
        }
        #endif
        let activityState = wantsConnection
            ? VoiceActivityState.make(
                state: state,
                muted: isMuted,
                waiting: flock.waitingCount,
                voiceName: voiceName,
                suspendedInBackground: core.suspension.backgroundSuspended
            )
            : nil
        away.sync(activityState, voiceName: voiceName)
    }

    /// A `sheepdog://` link, or an App Intent speaking one.
    func open(url: URL) {
        guard let route = AppRoute(url: url) else {
            SheepdogLog.session.info("ignored link \(url.absoluteString, privacy: .public)")
            return
        }
        handle(route: route)
    }

    func handle(route: AppRoute) {
        for action in route.actions(wantsConnection: wantsConnection, muted: isMuted) {
            switch action {
            case .showConversation:
                pane = .conversation
            case .connect:
                connect()
            case .toggleMute:
                toggleMute()
            case .stop:
                stop()
            case .disconnect:
                disconnect()
            case let .openAgent(id):
                // What a tap on an agent's notification does.
                PushCoordinator.shared.focus(paneID: id)
            }
        }
    }

    // MARK: - Events and state

    private func startEvents() {
        eventTask?.cancel()
        eventTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self, self.core.connection.wantsConnection else { return }
                // Every (re)open: what the stream missed is in the history
                // too, and a page is cheaper than a long replay to read.
                Task {
                    await self.syncHistory()
                    await self.backfillActivity()
                }
                self.send(.eventsChanged(.connecting))
                do {
                    for try await output in self.client.events(since: self.resumeID) {
                        if Task.isCancelled { return }
                        if self.core.connection.events != .open {
                            self.send(.eventsChanged(.open))
                        }
                        self.handle(event: output)
                    }
                } catch {
                    SheepdogLog.socket.error("event stream: \(String(describing: error), privacy: .public)")
                    if case DaemonError.unauthorized = error {
                        // A refused token does not fix itself by retrying.
                        self.send(.tokenRefused(endpoint: self.endpointDisplay))
                        return
                    }
                    #if DEBUG
                    if DebugLaunch.showStreamErrors {
                        self.reducer.apply(.notice("event stream: \(error)"))
                        self.conversationChanged()
                    }
                    #endif
                    self.send(.eventsChanged(.reconnecting))
                }
                try? await Task.sleep(nanoseconds: 2_000_000_000)
            }
        }
    }

    /// Where `/events` resumes: the last id seen (kept in the cache across
    /// launches), else the newest history item's.
    private var resumeID: UInt64 {
        if lastSeq > 0 { return lastSeq }
        if let newest = history.newestID, newest > 0 { return UInt64(newest) }
        return 0
    }

    private func handle(event output: SSEParser.Output) {
        switch output {
        case let .event(event):
            // A replay after a reconnect starts after our id, but an event
            // seen twice must still change nothing.
            if event.seq > 0, event.seq <= lastSeq { return }
            lastSeq = max(lastSeq, event.seq)
            apply(event: event)
        case .closed, .undecodable:
            break
        }
    }

    /// An event is news, worth a haptic, only when it is recent — not when
    /// a reconnect replays the last hour.
    private func isFresh(_ event: DaemonEvent) -> Bool {
        guard let ts = event.ts else { return true }
        return Date().timeIntervalSince(ts) < 60
    }

    private func apply(event: DaemonEvent) {
        let folded = history.fold(event) != nil
        if activity.fold(event) || folded {
            conversationChanged()
        }
        var nextHealth = health
        if nextHealth.fold(event) { health = nextHealth }
        switch event.type {
        case "voice.push":
            guard let text = event.string("text"), !text.isEmpty, event.string("urgency") != "silent" else { break }
            if isFresh(event) { Haptics.push() }
            refreshSoon()
        case "voice.tool_call":
            send(.toolCall)
        case "voice.tool_result":
            send(.toolResult)
        case "pending.created":
            if let pending = PendingAction(event: event) {
                applyApproval(approvalFlow.present(pending))
                // The needs-you cue: a new open question with options, once
                // per question, and only when it is news (not a replay).
                if pending.isAnswerPrompt, isFresh(event), cuedPendingIDs.insert(pending.id).inserted {
                    Haptics.needsYou()
                }
            }
            refreshSoon()
        case "conversation.reset":
            // Fires for every reset, including our own (the app resets its
            // own reducer immediately on the POST reply too, so a replay
            // here is a no-op) and ones started elsewhere (the browser,
            // voice "start over"). The divider itself came from `fold`
            // above; this only closes whatever bubble was still open.
            reducer.apply(.reset)
            conversationChanged()
        case "pending.confirmed", "pending.expired", "pending.cancelled", "pending.replaced":
            let resolution = ConversationItem.Resolution(rawValue: String(event.type.dropFirst("pending.".count))) ?? .unknown
            let id = event.type == "pending.replaced" ? event.string("old_id") : event.string("id")
            if let id {
                applyApproval(approvalFlow.resolved(pendingID: id, as: resolution, answer: event.string("answer"), via: event.string("via")))
            }
            refreshSoon()
        default:
            if event.type.hasPrefix("herdr.") || event.type.hasPrefix("supervisor.") || event.type.hasPrefix("web.inject")
                || event.type.hasPrefix("investigation.") || event.type.hasPrefix("promise.") || event.type == "daemon.start" {
                refreshSoon()
            }
        }
    }

    /// True while the microphone or the player is doing something.
    private var audioIsBusy: Bool { audio.isRunning || core.playbackBusy }

    private var pendingRefresh: Task<Void, Never>?

    /// Debounced `/api/state` fetch: many events describe one change.
    private func refreshSoon() {
        guard pendingRefresh == nil else { return }
        pendingRefresh = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 250_000_000)
            guard let self else { return }
            self.pendingRefresh = nil
            await self.refreshState(quiet: true)
        }
    }

    private func startRefreshing() {
        refreshTask?.cancel()
        refreshTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 5_000_000_000)
                guard let self, self.core.connection.wantsConnection else { return }
                await self.refreshState(quiet: true)
            }
        }
    }

    /// One timer drives the playback release, the metrics and the thinking
    /// timeout, so a missed event cannot leave the orb stuck. It runs fast
    /// only while audio is actually moving; idle (or with the daemon's voice
    /// off) it ticks once a second and leaves the CPU alone.
    private func startTicking() {
        ticker?.cancel()
        ticker = Task { [weak self] in
            while !Task.isCancelled {
                let busy = self?.audioIsBusy ?? false
                try? await Task.sleep(nanoseconds: busy ? 100_000_000 : 1_000_000_000)
                guard let self, !Task.isCancelled, self.core.connection.wantsConnection else { return }
                self.audio.tick()
                let metrics = self.audio.metrics
                self.meter.setMetrics(metrics)
                self.socket.gatedFrames = self.audio.gatedFrames
                self.socket.inputPeak = self.audio.inputPeak
                self.socket.underruns = metrics.underruns
                self.socket.drains = metrics.drains
                self.socket.bufferMs = metrics.bufferedMilliseconds
                self.socket.rate = metrics.playbackRate
                self.socket.stretchedMs = metrics.stretchedMilliseconds
                self.socket.audioState = self.audio.audioState
                self.updateDiagnostics()
                self.send(.tick)
            }
        }
    }

    @discardableResult
    func refreshState(quiet: Bool) async -> Bool {
        guard settings.endpoint != nil else { return false }
        do {
            apply(try await client.state())
            if hello == nil { hello = try? await client.hello() }
            if !quiet { banner = nil }
            return true
        } catch let error as DaemonError {
            // A loud refresh that fails ends the session, as a failed
            // Connect would.
            if !quiet { send(.probeFailed(error)) }
            SheepdogLog.session.error("state: \(String(describing: error), privacy: .public)")
            return false
        } catch {
            // Quiet even on a decode failure: the periodic and
            // event-debounced refreshes retry on their own, and a banner
            // for a transient bad payload would be more alarming than
            // useful. Still logged, so a persistent failure is visible.
            SheepdogLog.session.error("state: \(String(describing: error), privacy: .public)")
            return false
        }
    }

    private func apply(_ state: DaemonState) {
        // Blocked first (waiting on a person), then working, then idle
        // and done; what is not running last.
        var all = state.agents
        #if DEBUG
        if DebugLaunch.demoMarkdown { all.append(DebugLaunch.markdownAgent) }
        #endif
        let sorted = Flock.sorted(all)
        if sorted != agents {
            agents = sorted
            syncAway()
        }
        pendingAction = state.pendingAction
        applyApproval(approvalFlow.present(state.pendingAction))
        brain = state.brain
        stateVoiceProvider = state.voiceProvider
        stateVoiceModel = state.voiceModel
    }

    // MARK: - Conversation history

    private func conversationChanged(save: Bool = true) {
        rows = ConversationTimeline.rows(history: history.items, live: reducer.bubbles, activity: activity)
        canLoadOlder = history.canLoadOlder
        if save { saveCache(now: false) }
    }

    /// Loads the newest page and merges it: the server's copy wins, and the
    /// finished transcript bubbles it now has stop showing twice.
    func syncHistory() async {
        guard settings.endpoint != nil, !syncingHistory else { return }
        // A bubble still growing on /ws is already an item on the server
        // (from its first fragment); swapping now would show it twice. The
        // dog's `out` bubble often closes only when the next turn starts.
        guard reducer.openBubble == nil else {
            scheduleHistorySync()
            return
        }
        syncingHistory = true
        defer { syncingHistory = false }
        let sent = Date()
        do {
            let page = try await client.conversation(limit: 200)
            history.applyNewest(page, requestedAt: sent)
            reducer.dropSettledSpeech(before: sent)
            conversationChanged()
        } catch {
            // Quiet: the cache and the live stream still show something,
            // and the state probe reports what is wrong in plain words.
            SheepdogLog.socket.error("history: \(String(describing: error), privacy: .public)")
        }
    }

    /// The brain's recent replies and the held pushes from the daemon's
    /// current run (`GET /api/events`, prefix-filtered), for what the
    /// stream did not bring this time: a relaunch, or events before the
    /// stream's resume point. Quiet on failure — the stream still folds
    /// them live.
    func backfillActivity() async {
        guard settings.endpoint != nil else { return }
        var changed = false
        for type in ["voice.tool_result", "voice.push_"] {
            guard let events = try? await client.recentEvents(type: type, limit: 100) else { continue }
            for event in events where activity.fold(event) { changed = true }
        }
        if changed { conversationChanged(save: false) }
    }

    /// A voice turn just ended: once the daemon has logged it, the newest
    /// page has it as server items, which is what the cache keeps.
    private func scheduleHistorySync() {
        historySyncTask?.cancel()
        historySyncTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 2_500_000_000)
            guard !Task.isCancelled, let self else { return }
            await self.syncHistory()
        }
    }

    /// Pull-to-load-older: the page before the oldest item shown.
    func loadOlder() async {
        guard let before = history.nextBefore, !loadingOlder else { return }
        loadingOlder = true
        defer { loadingOlder = false }
        do {
            let page = try await client.conversation(limit: 100, before: before)
            history.applyOlder(page)
            conversationChanged()
        } catch let error as DaemonError {
            show(Banner(tone: .warning, message: "Couldn't load the earlier conversation.", hint: error.message, retryTitle: nil))
        } catch {}
    }

    /// Settings ▸ Clear conversation on this phone.
    func clearConversation() {
        if let endpoint = settings.endpoint { cache.clear(for: endpoint) }
        history.clear()
        reducer.clear()
        activity.clear()
        conversationChanged(save: false)
    }

    // MARK: - Health

    /// When the daemon started: its `daemon.start` event, else the newest
    /// "Sheepdog started." divider in the history (the event ring may
    /// have rolled past it on a long run).
    var daemonStartedAt: Date? {
        health.startedAt ?? history.items.last { $0.kind == .notice && $0.text.hasPrefix("Sheepdog started") }?.ts
    }

    /// Everything the Health pane shows, fetched once when it opens (and
    /// on pull to refresh); `/events` keeps the problems and the billed
    /// seconds current after that.
    func loadHealth() async {
        guard settings.endpoint != nil, !loadingHealth else { return }
        loadingHealth = true
        defer { loadingHealth = false }
        async let state: Bool = refreshState(quiet: true)
        async let models: Void = loadBrainModels()
        async let chattiness: Void = loadDaemonSettings()
        async let providers: Void = loadVoiceProviders()
        if hello == nil { hello = try? await client.hello() }
        // `/api/events` filters by one type prefix: one small request each.
        var next = health
        for type in ["daemon.start", "voice.billed_seconds", "web.client.stats", "brain.fallback", "brain.rate_limited", "voice.connect_error",
                     "voice.push_", "supervisor.echo_dropped", "web.audio_unheard", "voice.playback", "worker.session_missing"] {
            let limit = type == "web.client.stats" || type == "daemon.start" ? 1 : 30
            guard let events = try? await client.recentEvents(type: type, limit: limit) else { continue }
            for event in events { next.fold(event) }
        }
        if next != health { health = next }
        _ = await (state, models, chattiness, providers)
    }

    func loadBrainModels(provider: String? = nil) async {
        do {
            brainModels = try await client.brainModels(provider: provider)
            brainError = nil
        } catch let error as DaemonError {
            brainError = error.message
        } catch {
            brainError = error.localizedDescription
        }
    }

    /// The brain picker: provider and model for the brain's next turn.
    func selectBrain(provider: String?, model: String) {
        guard !switchingBrain else { return }
        switchingBrain = true
        brainError = nil
        Task { [weak self] in
            guard let self else { return }
            defer { self.switchingBrain = false }
            do {
                self.brainModels = try await self.client.setBrainModel(provider: provider, model: model)
                Haptics.tap()
            } catch let error as DaemonError {
                self.brainError = error.message
            } catch {
                self.brainError = error.localizedDescription
            }
            await self.refreshState(quiet: true)
        }
    }

    func loadDaemonSettings() async {
        do {
            daemonSettings = try await client.settings()
            settingsError = nil
        } catch let error as DaemonError {
            // No supervisor is a 503: hide the control, say why.
            daemonSettings = nil
            settingsError = error.message
        } catch {
            settingsError = error.localizedDescription
        }
    }

    func setChattiness(_ level: String) {
        guard level != daemonSettings?.chattiness else { return }
        Task { [weak self] in
            guard let self else { return }
            do {
                self.daemonSettings = try await self.client.setChattiness(level)
                self.settingsError = nil
                Haptics.tap()
            } catch let error as DaemonError {
                self.settingsError = error.message
            } catch {
                self.settingsError = error.localizedDescription
            }
        }
    }

    /// Sends the language preference (most preferred first); the
    /// daemon's reply replaces the local copy, so the screen shows what
    /// it kept.
    func setLanguages(_ codes: [String]) {
        guard codes != (daemonSettings?.languages ?? []) else { return }
        if var optimistic = daemonSettings {
            optimistic.languages = codes
            daemonSettings = optimistic
        }
        Task { [weak self] in
            guard let self else { return }
            do {
                self.daemonSettings = try await self.client.setLanguages(codes)
                self.settingsError = nil
                Haptics.tap()
            } catch {
                // Back to what the daemon has, then say why it refused.
                await self.loadDaemonSettings()
                self.settingsError = (error as? DaemonError)?.message ?? error.localizedDescription
            }
        }
    }

    // MARK: - Typed input

    /// The text field under the conversation: says `text` as if spoken,
    /// through `POST /api/inject/utterance`, which records it as typed
    /// shepherd input (the rail takes it like a transcript). The
    /// conversation shows it from `/events` (or the next history sync when
    /// not connected). Returns true when the daemon took it.
    func sendTyped(_ text: String) async -> Bool {
        let text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, settings.endpoint != nil, !sendingTyped else { return false }
        sendingTyped = true
        typedError = nil
        defer { sendingTyped = false }
        do {
            _ = try await client.sendUtterance(text)
            typedSent += 1
            Haptics.tap()
            if !core.connection.wantsConnection {
                // No event stream: the daemon has logged it by now.
                await syncHistory()
                await backfillActivity()
            }
            return true
        } catch let error as DaemonError {
            typedError = error.message
            Haptics.answerFailed()
        } catch {
            typedError = error.localizedDescription
        }
        return false
    }

    // MARK: - Agent window

    /// `GET /api/agents/{pane}/screen` for `AgentScreenView`. Throws when no
    /// daemon is configured.
    func agentScreen(paneID: String, hash: String?) async throws -> AgentScreen? {
        guard settings.endpoint != nil else { throw DaemonError.malformed("no daemon address set") }
        return try await client.agentScreen(paneID: paneID, hash: hash)
    }

    /// `POST /api/agents/{pane}/input` with a prompt.
    func agentInput(paneID: String, text: String) async throws -> AgentInputReply {
        guard settings.endpoint != nil else { throw DaemonError.malformed("no daemon address set") }
        return try await client.agentInput(paneID: paneID, text: text)
    }

    /// `POST /api/agents/{pane}/input` with keys.
    func agentInput(paneID: String, keys: [AgentKey]) async throws -> AgentInputReply {
        guard settings.endpoint != nil else { throw DaemonError.malformed("no daemon address set") }
        return try await client.agentInput(paneID: paneID, keys: keys)
    }

    // MARK: - New conversation

    /// The ⋯ menu's "New conversation", after the confirmation dialog.
    /// Unlike `clearConversation()` this keeps the scrollback — it only
    /// resets the supervisor's and the voice model's short-term context on
    /// the daemon, which then logs a `subkind: reset` notice we pick up
    /// the normal way (live, from `/events`, or from the next history
    /// sync), the same as any other notice.
    func startNewConversation() {
        guard settings.endpoint != nil else { return }
        Task { [weak self] in
            guard let self else { return }
            do {
                let response = try await self.client.resetConversation(via: "app")
                self.conversationResetSupported = ConversationResetAvailability(response: response).showsAction
                guard response != nil else {
                    self.show(Banner(tone: .info, message: "This daemon doesn't have New conversation yet.", hint: nil, retryTitle: nil))
                    return
                }
                // No optimistic local divider: the daemon already logged
                // the reset before answering, so the next history sync (or
                // the `conversation.reset` event, on the same event loop
                // that closes the reducer's open bubble) has it with a
                // real id — the same id-based merge every other notice
                // uses, so it cannot show twice.
                reducer.apply(.reset)
                conversationChanged()
                Haptics.newConversation()
                await self.syncHistory()
            } catch let error as DaemonError {
                self.show(Banner(tone: .warning, message: "Couldn't start a new conversation.", hint: error.hint ?? error.message, retryTitle: nil))
            } catch {
                self.show(Banner(tone: .warning, message: "Couldn't start a new conversation.", hint: error.localizedDescription, retryTitle: nil))
            }
        }
    }

    private func loadCache() {
        guard let endpoint = settings.endpoint, let snapshot = cache.load(for: endpoint) else {
            history = ConversationHistory()
            lastSeq = 0
            return
        }
        history = snapshot.history
        lastSeq = UInt64(max(0, snapshot.lastEventID ?? 0))
    }

    private func saveCache(now: Bool) {
        guard let endpoint = settings.endpoint else { return }
        saveTask?.cancel()
        let snapshot = ConversationSnapshot(endpoint: endpoint, history: history, lastEventID: lastSeq > 0 ? Int64(clamping: lastSeq) : nil)
        let cache = self.cache
        let write = {
            do { try cache.save(snapshot, for: endpoint) } catch {
                SheepdogLog.socket.error("cache: \(String(describing: error), privacy: .public)")
            }
        }
        if now {
            write()
            return
        }
        saveTask = Task.detached(priority: .utility) {
            try? await Task.sleep(nanoseconds: 1_000_000_000)
            guard !Task.isCancelled else { return }
            write()
        }
    }

    // MARK: - Approvals

    /// Questions the needs-you cue already played for.
    private var cuedPendingIDs: Set<String> = []

    private func applyApproval(_ effect: ApprovalFlow.Effect) {
        switch effect {
        case .appeared:
            // The cue itself comes from `pending.created` only.
            break
        case .settled:
            scheduleApprovalDismiss()
        case .dismissed, .none:
            break
        }
    }

    private func scheduleApprovalDismiss() {
        guard let id = approvalFlow.card?.pending.id else { return }
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 3_500_000_000)
            guard let self else { return }
            self.approvalFlow.dismiss(pendingID: id)
            // A newer question may be waiting behind the settled card.
            if self.approvalFlow.card == nil, let pending = self.pendingAction, pending.id != id {
                self.applyApproval(self.approvalFlow.present(pending))
            }
        }
    }

    /// The shepherd tapped an option on the approval card.
    func answer(option index: Int) {
        guard let pendingID = approvalFlow.card?.pending.id, let option = approvalFlow.tap(index: index) else { return }
        Haptics.tap()
        Task { [weak self] in
            guard let self else { return }
            do {
                let spoken = try await self.client.answerPending(id: pendingID, option: option.index)
                self.applyApproval(self.approvalFlow.succeeded(pendingID: pendingID, spoken: spoken))
                Haptics.answered()
            } catch DaemonError.pendingGone {
                self.applyApproval(self.approvalFlow.gone(pendingID: pendingID))
                Haptics.answerFailed()
            } catch let error as DaemonError {
                self.approvalFlow.failed(pendingID: pendingID, message: error.message)
                Haptics.answerFailed()
            } catch {
                self.approvalFlow.failed(pendingID: pendingID, message: error.localizedDescription)
            }
            await self.refreshState(quiet: true)
            // Without the event stream (not connected), the answered
            // approval item comes from the server.
            if !self.core.connection.wantsConnection { await self.syncHistory() }
        }
    }

    // MARK: - Banners

    private func show(_ banner: Banner) {
        self.banner = banner
    }

    func retry() {
        banner = nil
        reconnect()
    }

    /// The orb's "Audio paused by iOS — tap to resume".
    func resumeAudio() {
        send(.resumeTapped)
    }

    /// The banner's button.
    func bannerAction() {
        switch banner?.action ?? .retry {
        case .retry:
            retry()
        case .microphoneModes:
            banner = nil
            showMicrophoneModes()
        }
    }

    // MARK: - Diagnostics

    private func updateDiagnostics() {
        #if DEBUG
        if fakeVoice {
            fakeTick()
            return
        }
        #endif
        meter.setGateDiagnostics(audio.gateDiagnostics)
        if core.mic.noSignal != audio.noSignal, !audio.isRunning { send(.micSignal(ok: true)) }
        refreshMicrophoneMode()
    }

    #if DEBUG
    /// `-sheepdogFakeVoice`: the voice states without a live session or a
    /// microphone, for screenshots and UI tests against `--voice=off`.
    let fakeVoice = DebugLaunch.fakeVoice
    private var fakeVoiceDriver = FakeVoiceDriver()

    private func fakeTick() {
        guard core.connection.wantsConnection, core.voice.status == .connected else { return }
        let (level, diagnostics) = fakeVoiceDriver.tick(talkMode: core.mic.talkMode, holding: core.mic.holding, isMuted: core.mic.muted)
        meter.setLevel(level)
        meter.setGateDiagnostics(diagnostics)
        refreshMicrophoneMode()
    }
    #endif

    // MARK: - Connection chip popover

    /// Times a `GET /api/hello` round trip for the connection chip's
    /// popover. `nil` when there is nowhere to ask or it did not answer.
    func pingRoundTrip() async -> TimeInterval? {
        guard settings.endpoint != nil else { return nil }
        let start = Date()
        do {
            _ = try await client.hello()
            return Date().timeIntervalSince(start)
        } catch {
            return nil
        }
    }

    // MARK: - Test connection (Settings)

    struct Probe {
        var ok: Bool
        var title: String
        var lines: [String]
    }

    /// What the "Test connection" button reports. `/api/hello` is public, so
    /// it works before the token is right; `/api/state` is the token check.
    func probe(address: String, token: String) async -> Probe {
        guard let endpoint = DaemonEndpoint(text: address, token: token) else {
            return Probe(ok: false, title: "That address doesn't look like host:port.", lines: ["For example 192.168.1.20:7777."])
        }
        let probeClient = DaemonClient(endpoint: endpoint)
        do {
            let hello = try await probeClient.hello()
            var lines = [
                "\(hello.name) \(hello.version)",
                "Voice name: \(hello.displayName)",
                "Auth: \(hello.needsToken ? "token required" : "open (loopback)")",
                "Audio: \(hello.audio.inHz) Hz in / \(hello.audio.outHz) Hz out, \(hello.audio.format)",
            ]
            if hello.needsToken {
                // A hello alone would "pass" with the wrong token.
                do {
                    _ = try await probeClient.state()
                    lines.append("Token accepted.")
                } catch let error as DaemonError {
                    return Probe(ok: false, title: error.message, lines: lines + [error.hint].compactMap { $0 })
                }
            }
            return Probe(ok: true, title: "Connected to \(endpoint.display)", lines: lines)
        } catch let error as DaemonError {
            return Probe(ok: false, title: error.message, lines: [error.hint].compactMap { $0 })
        } catch {
            return Probe(ok: false, title: "Could not reach \(endpoint.display).", lines: [])
        }
    }
}

extension SessionViewModel.Banner {
    /// The words for a banner the session core raised.
    init(_ banner: SessionBanner) {
        switch banner {
        case let .connectionFailed(error):
            self.init(tone: .error, message: error.message, hint: error.hint, retryTitle: "Try again")
        case let .stillReconnecting(error):
            self.init(tone: .warning, message: error.message, hint: "Still trying every few seconds. Tap End to stop.", retryTitle: "Try now")
        case .replaced:
            self.init(
                tone: .warning,
                message: "Another client took the voice session.",
                hint: "The daemon serves one client at a time — the browser page on your Mac, maybe.",
                retryTitle: "Take it back"
            )
        case .microphoneDenied:
            self.init(tone: .warning, message: AudioEngine.Failure.microphoneDenied.errorDescription ?? "", hint: nil, retryTitle: nil)
        case let .microphoneFailed(message):
            self.init(tone: .warning, message: message, hint: "Status and what the dog says still work without it.", retryTitle: nil)
        case let .daemonSaid(text):
            self.init(tone: .warning, message: "The daemon said: \(text)", hint: nil, retryTitle: nil)
        case let .protocolMismatch(text):
            self.init(
                tone: .error,
                message: text,
                hint: "The app and the daemon ship separately; the newer one still works with the older one only down to its minimum.",
                retryTitle: nil
            )
        case let .audioResumeFailed(message):
            self.init(
                tone: .warning,
                message: "iOS would not give the audio back (\(message)).",
                hint: "Finish the call or whatever is using audio, then tap the orb again.",
                retryTitle: nil
            )
        }
    }
}
