import AVFoundation
import Combine
import Foundation
import SheepdogKit
import SwiftUI

/// Everything the main screen shows, in one place.
///
/// It owns the socket, the audio engine, the event stream and the
/// conversation, and it is the only thing that decides what the orb says —
/// the views just render it.
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

    @Published private(set) var state: SessionState = .disconnected
    @Published private(set) var bubbles: [ConversationReducer.Bubble] = []
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
    @Published private(set) var pendingAction: PendingAction?
    @Published private(set) var hello: Hello?
    @Published private(set) var micLevel: Float = 0
    @Published private(set) var metrics = AudioMetrics()
    /// The daemon connection's own health — the `/ws` socket, the `/events`
    /// stream and the token — independent of the voice state. Drives the
    /// top-left chip.
    @Published private(set) var connectionChip = ConnectionChip(.init())
    /// The supervisor's model, from `/api/state`'s `brain`, when the
    /// daemon reports one.
    @Published private(set) var brain: BrainInfo?
    @Published var banner: Banner?
    @Published var isMuted = false

    // Voice robustness: talk mode, the gate's diagnostics, the microphone.
    @Published private(set) var talkMode: TalkMode = .openMic
    /// Hold-to-talk: the orb is being held.
    @Published private(set) var holding = false
    /// The microphone delivers only digital zero while unmuted.
    @Published private(set) var noMicSignal = false
    @Published private(set) var gateDiagnostics = CaptureGate.Diagnostics()
    @Published private(set) var detector: CaptureGate.Detector = .voiceActivity
    /// The first-run "just talk" coach mark.
    @Published private(set) var showCoachMark = false
    /// Control Center ▸ Mic Mode, as iOS reports it.
    @Published private(set) var microphoneMode: MicrophoneModeName = .standard
    @Published private(set) var preferredMicrophoneMode: MicrophoneModeName = .standard

    // The voice provider (GET/PUT /api/voice/provider[s]).
    /// nil until loaded, and when the daemon has no provider API (404).
    @Published private(set) var voiceProviders: VoiceProviders?
    @Published private(set) var switchingProvider = false
    @Published var providerError: String?
    /// From the `/ws` status message, `/api/state` and `/api/hello`.
    @Published private(set) var liveVoiceProvider: String?
    @Published private(set) var stateVoiceProvider: String?
    @Published private(set) var stateVoiceModel: String?
    /// True from the moment Connect is tapped until Disconnect.
    @Published private(set) var wantsConnection = false
    @Published var showSettings = false

    var voiceName: String { hello?.displayName ?? "Sheepdog" }

    /// The line above the voice bar: what to do, not only what is happening.
    var statusLine: String {
        VoiceBarModel.statusLine(state: state, talkMode: talkMode, holding: holding, noSignal: noMicSignal)
    }

    /// "Gemini Live · gemini-3.8-live", from whichever source knows.
    var voiceProviderDisplay: String? {
        let id = liveVoiceProvider ?? voiceProviders?.provider ?? stateVoiceProvider ?? hello?.voiceProvider
        let option = voiceProviders?.providers.first { $0.id == id }
        let model = (id == voiceProviders?.provider ? voiceProviders?.model : nil)
            ?? (id == stateVoiceProvider ? stateVoiceModel : nil)
            ?? (id == hello?.voiceProvider ? hello?.voiceModel : nil)
            ?? option?.model
        return VoiceProviders.display(provider: option?.displayName ?? id, model: model)
    }
    var daemonVersion: String? { hello?.version }
    var endpointDisplay: String { settings.endpoint?.display ?? "not set" }
    var isConfigured: Bool { settings.endpoint != nil }

    /// The agent the flock strip highlights: the one that is blocked.
    var blockedAgent: Agent? { agents.first { $0.state == .blocked } }

    func pendingQuestion(for agent: Agent) -> String? {
        guard let pending = pendingAction, pending.paneID == agent.paneID else { return nil }
        return pending.question ?? pending.spokenSummary
    }

    // MARK: - Collaborators

    let settings: SettingsStore
    private var client: DaemonClient
    private var socket: VoiceSocket
    private let audio = AudioEngine()
    private var reducer = ConversationReducer()
    private var history = ConversationHistory()
    private var approvalFlow = ApprovalFlow()
    private let cache: ConversationCache
    private var saveTask: Task<Void, Never>?
    private var historySyncTask: Task<Void, Never>?
    private var syncingHistory = false

    private var eventTask: Task<Void, Never>?
    private var refreshTask: Task<Void, Never>?
    private var ticker: Task<Void, Never>?
    private var thinkingExpiry: Date?
    private var lastSeq: UInt64 = 0
    private var voiceStatus: WSMessage.Voice?
    private var closedReason: String?
    private var stalled = false
    private var gateClosed = false
    private var speaking = false
    private var daemonVoiceOff = false
    /// The "voice is off" notice is said once per session, not per status.
    private var saidVoiceIsOff = false
    private var socketLife: SessionState.Inputs.Socket = .closed
    /// The `/events` stream's own life, for the connection chip — separate
    /// from the voice socket, since either can be up while the other isn't.
    private var eventsLife: ConnectionChip.Inputs.State = .idle
    /// A connection attempt ended in a failure `VoiceSocket` will not retry
    /// by itself (set in `fail(_:)`, cleared on the next attempt or once
    /// the socket opens).
    private var connectionFailed = false
    /// The daemon refused the token.
    private var tokenRefused = false

    init(settings: SettingsStore = SettingsStore(), cache: ConversationCache = .standard()) {
        self.settings = settings
        self.cache = cache
        let endpoint = settings.endpoint ?? DaemonEndpoint(host: "127.0.0.1")
        self.client = DaemonClient(endpoint: endpoint)
        self.socket = VoiceSocket(endpoint: endpoint)
        talkMode = settings.talkMode
        detector = settings.detector
        audio.setDetector(detector)
        if talkMode == .holdToTalk { audio.setManualGate(false) }
        #if DEBUG
        if ProcessInfo.processInfo.arguments.contains("-sheepdogResetHints") {
            for key in [Self.muteHintShownKey, Self.coachShownKey, Self.micTipShownKey] {
                UserDefaults.standard.removeObject(forKey: key)
            }
        }
        #endif
        wire()
        // The last conversation, at once: before the network, or without it.
        loadCache()
        conversationChanged(save: false)
    }

    /// Called once from the first `onAppear`: an unconfigured app opens
    /// Settings instead of showing a Connect button that cannot work.
    func start() {
        if !isConfigured || !settings.hasBeenConfigured {
            showSettings = true
        } else {
            Task {
                await refreshState(quiet: true)
                await syncHistory()
            }
        }
    }

    /// The app came back to the foreground: what happened while it was
    /// away is on the daemon.
    func becameActive() {
        guard isConfigured, settings.hasBeenConfigured else { return }
        Task {
            await refreshState(quiet: true)
            await syncHistory()
        }
    }

    /// The app is going to the background: write the cache now, not later.
    func willResignActive() {
        saveCache(now: true)
    }

    /// Settings were saved: repoint everything without losing the conversation.
    func endpointChanged() {
        guard let endpoint = settings.endpoint else { return }
        let sameDaemon = ConversationCache.key(for: endpoint) == ConversationCache.key(for: client.endpoint)
        if !sameDaemon { saveCache(now: true) }
        client.update(endpoint: endpoint)
        socket.update(endpoint: endpoint)
        banner = nil
        hello = nil
        if !sameDaemon {
            // Another daemon, another conversation.
            reducer.clear()
            approvalFlow.reset()
            approval = nil
            loadCache()
            conversationChanged(save: false)
        }
        if wantsConnection {
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
        banner = nil
        wantsConnection = true
        socketLife = .connecting
        eventsLife = .connecting
        connectionFailed = false
        tokenRefused = false
        refresh()
        Task { await openSession() }
    }

    private func openSession() async {
        // The auth probe first: a 401 on the WebSocket upgrade is almost
        // unreadable, so find out here where the status code is plain.
        do {
            let state = try await client.state()
            apply(state)
            hello = try? await client.hello()
            await loadVoiceProviders()
        } catch let error as DaemonError {
            fail(error)
            return
        } catch {
            fail(.malformed(error.localizedDescription))
            return
        }
        guard wantsConnection else { return }

        guard await AudioEngine.requestMicrophone() else {
            show(Banner(tone: .warning, message: AudioEngine.Failure.microphoneDenied.errorDescription ?? "", hint: nil, retryTitle: nil))
            // The session is still worth having: status and transcripts work
            // without a microphone.
            openSocket()
            return
        }
        openSocket()
    }

    private func openSocket() {
        socket.muted = isMuted
        socket.gateClosed = false
        socket.connect()
        startEvents()
        startRefreshing()
        startTicking()
        refresh()
    }

    func disconnect() {
        wantsConnection = false
        socket.disconnect()
        audio.stop()
        eventTask?.cancel(); eventTask = nil
        refreshTask?.cancel(); refreshTask = nil
        ticker?.cancel(); ticker = nil
        socketLife = .closed
        eventsLife = .idle
        connectionFailed = false
        tokenRefused = false
        voiceStatus = nil
        saidVoiceIsOff = false
        closedReason = nil
        stalled = false
        gateClosed = false
        speaking = false
        thinkingExpiry = nil
        micLevel = 0
        holding = false
        noMicSignal = false
        liveVoiceProvider = nil
        if talkMode == .holdToTalk { audio.setManualGate(false) }
        refresh()
    }

    func toggleConnection() {
        wantsConnection ? disconnect() : connect()
    }

    /// The chip popover's Reconnect button: a clean disconnect (if one is
    /// needed) followed by a fresh connect, whatever state we are in now.
    func reconnect() {
        if wantsConnection { disconnect() }
        connect()
    }

    // MARK: - Controls

    func toggleMute() {
        setMuted(!isMuted)
    }

    private func setMuted(_ muted: Bool) {
        guard muted != isMuted else { return }
        if muted { offerMuteHint() }
        isMuted = muted
        socket.muted = muted
        if muted {
            // Stop the source first, so no frame can follow the message.
            audio.setMuted(true)
            socket.send(.mute(true))
        } else {
            // Tell the daemon first, so it does not drop the first frames.
            socket.send(.mute(false))
            audio.setMuted(false)
        }
        gateClosed = false
        socket.gateClosed = false
        Haptics.tap()
        refresh()
    }

    static let muteHint = "Mute stops your microphone; Sheepdog can still talk."
    private static let muteHintShownKey = "hint.mute.shown"
    private static let coachShownKey = "hint.coach.shown"
    private static let micTipShownKey = "hint.micmode.shown"

    // MARK: - Talk mode, hold to talk, the detector

    func setTalkMode(_ mode: TalkMode) {
        guard mode != talkMode else { return }
        talkMode = mode
        settings.talkMode = mode
        holding = false
        if mode == .holdToTalk {
            // Not holding *is* muted in this mode; a real mute on top would
            // make the orb do nothing.
            if isMuted { setMuted(false) }
            audio.setManualGate(false)
        } else {
            audio.setManualGate(nil)
        }
        refresh()
    }

    /// The orb went down in hold-to-talk. Talking over the dog stops it.
    func pressToTalk() {
        guard talkMode == .holdToTalk, !holding, VoiceBarModel(state: state, talkMode: talkMode).orbAction == .holdToTalk else { return }
        if speaking || state == .thinking || state == .stalled { stop() }
        holding = true
        audio.setManualGate(true)
        #if DEBUG
        if fakeVoice { setFakeGate(closed: false) }
        #endif
        Haptics.tap()
        refresh()
    }

    /// The orb came up: close the gate, the daemon hears the silence.
    func releaseToTalk() {
        guard holding else { return }
        holding = false
        audio.setManualGate(talkMode == .holdToTalk ? false : nil)
        #if DEBUG
        if fakeVoice { setFakeGate(closed: true) }
        #endif
        refresh()
    }

    func setDetector(_ detector: CaptureGate.Detector) {
        guard detector != self.detector else { return }
        self.detector = detector
        settings.detector = detector
        audio.setDetector(detector)
    }

    // MARK: - Coach mark and the microphone-mode tip

    /// Once per install, the first time the dog is actually listening:
    /// "just talk". After that (the next time), the Voice Isolation tip.
    private func offerListeningHints() {
        guard talkMode == .openMic, VoiceBarModel.isListening(state), wantsConnection else { return }
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
            if list == nil, ProcessInfo.processInfo.arguments.contains("-sheepdogFakeProviders") {
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
                self.liveVoiceProvider = list.provider
                Haptics.tap()
            } catch let error as DaemonError {
                #if DEBUG
                if ProcessInfo.processInfo.arguments.contains("-sheepdogFakeProviders") {
                    self.voiceProviders = Self.fakeProviders(current: id)
                    self.liveVoiceProvider = id
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
    /// The contract's example, for screenshots against a daemon that does
    /// not serve `/api/voice/providers` yet.
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
        audio.flushPlayback()
        reducer.apply(.flush)
        conversationChanged()
        socket.send(.cancel)
        speaking = false
        Haptics.stop()
        refresh()
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
            guard let self else { return }
            self.gateClosed = closed
            self.socket.gateClosed = closed
            self.socket.send(.silence(closed))
            self.refresh()
        }
        audio.onLevel = { [weak self] level in
            self?.micLevel = level
        }
        audio.onSpeakingChanged = { [weak self] speaking in
            guard let self else { return }
            self.speaking = speaking
            self.refresh()
        }
        audio.onMicSignal = { [weak self] noSignal in
            guard let self else { return }
            self.noMicSignal = noSignal
            if noSignal { Haptics.answerFailed() }
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
            socketLife = .open
            connectionFailed = false
            tokenRefused = false
            banner = nil
            Haptics.connected()
        case let .control(message):
            handle(control: message)
        case let .audio(data):
            audio.enqueue(data)
        case let .closed(reason, retrying):
            socketLife = retrying ? .reconnecting : .closed
            audio.stop()
            if !retrying {
                wantsConnection = false
                if reason == "replaced" {
                    show(Banner(
                        tone: .warning,
                        message: "Another client took the voice session.",
                        hint: "The daemon serves one client at a time — the browser page on your Mac, maybe.",
                        retryTitle: "Take it back"
                    ))
                }
            }
        case let .failed(error):
            fail(error)
        }
        refresh()
    }

    private func handle(control message: WSMessage) {
        switch message.type {
        case .status:
            voiceStatus = message.voice
            closedReason = message.reason
            stalled = message.stalled ?? false
            if let provider = message.provider { liveVoiceProvider = provider }
            #if DEBUG
            if fakeVoice {
                // Screenshots against a `--voice=off` daemon: pretend the
                // live session is up, without touching the microphone.
                voiceStatus = .connected
                closedReason = nil
            }
            #endif
            if let muted = message.muted, muted != isMuted {
                // The daemon's view wins: it is the one dropping frames.
                isMuted = muted
                socket.muted = muted
                audio.setMuted(muted)
                gateClosed = false
                socket.gateClosed = false
            }
            applyVoiceStatus()
        case .transcript:
            guard let dir = message.dir, let text = message.text else { break }
            reducer.apply(.transcript(dir, text: text, final: message.final ?? false))
            conversationChanged()
            if message.final == true { scheduleHistorySync() }
        case .flush:
            audio.flushPlayback()
            reducer.apply(.flush)
            conversationChanged()
            scheduleHistorySync()
            speaking = false
        case .error:
            if let text = message.message {
                show(Banner(tone: .warning, message: "The daemon said: \(text)", hint: nil, retryTitle: nil))
            }
        case .pong, .mute, .silence, .cancel, .text, .ping:
            break
        }
        refresh()
    }

    /// Capture runs only while something can hear it.
    private func applyVoiceStatus() {
        let closed = voiceStatus == .closed
        if closed {
            if audio.isRunning { audio.stop() }
            daemonVoiceOff = true
            if !saidVoiceIsOff {
                saidVoiceIsOff = true
                let why = closedReason == "voice_off"
                    ? "Voice is off on the daemon (`--voice=off`), so it cannot hear the microphone. Status, transcripts and pushes still work."
                    : "The daemon's voice session closed (\(closedReason ?? "no reason given"))."
                reducer.apply(.notice(why))
                conversationChanged()
            }
            return
        }
        daemonVoiceOff = false
        saidVoiceIsOff = false
        guard !audio.isRunning, !isMuted else { return }
        #if DEBUG
        if fakeVoice {
            setFakeGate(closed: talkMode == .holdToTalk)
            return
        }
        #endif
        do {
            try audio.start(background: settings.keepListeningWhenLocked)
            metrics = audio.metrics
            // Hold-to-talk starts closed without a transition; tell the
            // daemon, which treats a new client as ungated.
            let closed = audio.gateIsClosed
            if closed != gateClosed {
                gateClosed = closed
                socket.gateClosed = closed
                socket.send(.silence(closed))
            }
        } catch {
            show(Banner(
                tone: .warning,
                message: (error as? LocalizedError)?.errorDescription ?? "The microphone would not start.",
                hint: "Status and what the dog says still work without it.",
                retryTitle: nil
            ))
        }
    }

    // MARK: - Events and state

    private func startEvents() {
        eventTask?.cancel()
        eventTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self, self.wantsConnection else { return }
                // Every (re)open: what the stream missed is in the history
                // too, and a page is cheaper than a long replay to read.
                Task { await self.syncHistory() }
                self.eventsLife = .connecting
                self.refresh()
                do {
                    for try await output in self.client.events(since: self.resumeID) {
                        if Task.isCancelled { return }
                        if self.eventsLife != .open {
                            self.eventsLife = .open
                            self.refresh()
                        }
                        self.handle(event: output)
                    }
                } catch {
                    SheepdogLog.socket.error("event stream: \(String(describing: error), privacy: .public)")
                    #if DEBUG
                    if ProcessInfo.processInfo.arguments.contains("-sheepdogShowStreamErrors") {
                        self.reducer.apply(.notice("event stream: \(error)"))
                        self.conversationChanged()
                    }
                    #endif
                    self.eventsLife = .reconnecting
                    self.refresh()
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
        if history.fold(event) != nil {
            conversationChanged()
        }
        switch event.type {
        case "voice.push":
            guard let text = event.string("text"), !text.isEmpty, event.string("urgency") != "silent" else { break }
            let needsUser = event.bool("needs_user") ?? (event.string("urgency") == "interrupt")
            if isFresh(event) { Haptics.push(needsUser: needsUser) }
            refreshSoon()
        case "voice.tool_call":
            thinkingExpiry = Date().addingTimeInterval(30)
        case "voice.tool_result":
            thinkingExpiry = nil
            refreshSoon()
        case "pending.created":
            if let pending = PendingAction(event: event) {
                applyApproval(approvalFlow.present(pending), haptic: isFresh(event))
            }
            refreshSoon()
        case "pending.confirmed", "pending.expired", "pending.cancelled", "pending.replaced":
            let resolution = ConversationItem.Resolution(rawValue: String(event.type.dropFirst("pending.".count))) ?? .unknown
            let id = event.type == "pending.replaced" ? event.string("old_id") : event.string("id")
            if let id {
                applyApproval(approvalFlow.resolved(pendingID: id, as: resolution, answer: event.string("answer"), via: event.string("via")), haptic: false)
            }
            refreshSoon()
        default:
            if event.type.hasPrefix("herdr.") || event.type.hasPrefix("supervisor.") || event.type.hasPrefix("web.inject")
                || event.type.hasPrefix("investigation.") || event.type.hasPrefix("promise.") || event.type == "daemon.start" {
                refreshSoon()
            }
        }
        refresh()
    }

    /// True while the microphone or the player is doing something.
    private var audioIsBusy: Bool { audio.isRunning || speaking }

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
                guard let self, self.wantsConnection else { return }
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
                guard let self else { return }
                self.audio.tick()
                self.metrics = self.audio.metrics
                self.socket.gatedFrames = self.audio.gatedFrames
                self.socket.inputPeak = self.audio.inputPeak
                self.updateDiagnostics()
                if let expiry = self.thinkingExpiry, expiry < Date() { self.thinkingExpiry = nil }
                self.refresh()
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
            if !quiet { fail(error) }
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
        agents = state.agents.sorted { lhs, rhs in
            // The blocked agents first: they are the ones waiting on a person.
            if (lhs.state == .blocked) != (rhs.state == .blocked) { return lhs.state == .blocked }
            return lhs.alias.localizedCaseInsensitiveCompare(rhs.alias) == .orderedAscending
        }
        pendingAction = state.pendingAction
        applyApproval(approvalFlow.present(state.pendingAction), haptic: true)
        daemonVoiceOff = state.voice.isOff || state.voice.status == "closed"
        brain = state.brain
        stateVoiceProvider = state.voiceProvider
        stateVoiceModel = state.voiceModel
    }

    // MARK: - Conversation history

    private func conversationChanged(save: Bool = true) {
        bubbles = reducer.bubbles
        rows = ConversationTimeline.rows(history: history.items, live: reducer.bubbles)
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
        conversationChanged(save: false)
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

    private func applyApproval(_ effect: ApprovalFlow.Effect, haptic: Bool) {
        approval = approvalFlow.card
        switch effect {
        case .appeared:
            if haptic { Haptics.approvalAppeared() }
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
                self.applyApproval(self.approvalFlow.present(pending), haptic: true)
            } else {
                self.approval = self.approvalFlow.card
            }
        }
    }

    /// The shepherd tapped an option on the approval card.
    func answer(option index: Int) {
        guard let pendingID = approvalFlow.card?.pending.id, let option = approvalFlow.tap(index: index) else { return }
        approval = approvalFlow.card
        Haptics.tap()
        Task { [weak self] in
            guard let self else { return }
            do {
                let spoken = try await self.client.answerPending(id: pendingID, option: option.index)
                self.applyApproval(self.approvalFlow.succeeded(pendingID: pendingID, spoken: spoken), haptic: false)
                Haptics.answered()
            } catch DaemonError.pendingGone {
                self.applyApproval(self.approvalFlow.gone(pendingID: pendingID), haptic: false)
                Haptics.answerFailed()
            } catch let error as DaemonError {
                self.approvalFlow.failed(pendingID: pendingID, message: error.message)
                self.approval = self.approvalFlow.card
                Haptics.answerFailed()
            } catch {
                self.approvalFlow.failed(pendingID: pendingID, message: error.localizedDescription)
                self.approval = self.approvalFlow.card
            }
            await self.refreshState(quiet: true)
            // Without the event stream (not connected), the answered
            // approval item comes from the server.
            if !self.wantsConnection { await self.syncHistory() }
        }
    }

    // MARK: - Banners

    private func fail(_ error: DaemonError) {
        show(Banner(tone: .error, message: error.message, hint: error.hint, retryTitle: "Try again"))
        connectionFailed = true
        if case .unauthorized = error {
            tokenRefused = true
            showSettingsForToken()
        }
        wantsConnection = false
        socketLife = .closed
        eventsLife = .idle
        socket.disconnect()
        audio.stop()
        refresh()
    }

    private func showSettingsForToken() {
        showSettings = true
    }

    private func show(_ banner: Banner) {
        self.banner = banner
    }

    func retry() {
        banner = nil
        connect()
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
        let next = audio.gateDiagnostics
        if next != gateDiagnostics { gateDiagnostics = next }
        if noMicSignal != audio.noSignal, !audio.isRunning { noMicSignal = false }
        refreshMicrophoneMode()
    }

    #if DEBUG
    /// `-sheepdogFakeVoice`: the voice states without a live session or a
    /// microphone, for screenshots and UI tests against `--voice=off`.
    let fakeVoice = ProcessInfo.processInfo.arguments.contains("-sheepdogFakeVoice")
    private var fakePhase = 0.0

    private func setFakeGate(closed: Bool) {
        gateClosed = closed
        socket.gateClosed = closed
        refresh()
    }

    private func fakeTick() {
        guard wantsConnection, voiceStatus == .connected else { return }
        fakePhase += 0.35
        let speakingNow = !isMuted && (talkMode == .openMic || holding)
        let level = speakingNow ? Float(0.25 + 0.2 * sin(fakePhase)) : 0
        micLevel = level
        gateDiagnostics = CaptureGate.Diagnostics(
            detector: detector,
            levelDBFS: speakingNow ? -31 + 4 * sin(fakePhase) : -58,
            floorDBFS: detector == .voiceActivity ? -62 : nil,
            marginDB: detector == .voiceActivity ? 6 : nil,
            isOpen: speakingNow,
            opens: 4,
            spuriousOpens: 0,
            gatedFrames: 812,
            peak: speakingNow ? 9_400 : 30,
            manual: talkMode == .holdToTalk ? holding : nil
        )
        refreshMicrophoneMode()
    }
    #endif

    // MARK: - Derivation

    private func refresh() {
        let inputs = SessionState.Inputs(
            socket: socketLife,
            voice: voiceStatus,
            closedReason: closedReason,
            stalled: stalled,
            muted: isMuted,
            gateClosed: gateClosed,
            speaking: speaking,
            thinking: thinkingExpiry != nil
        )
        let next = SessionState.resolve(inputs)
        if next != state {
            state = next
            offerListeningHints()
        }
        refreshConnectionChip()
    }

    private func refreshConnectionChip() {
        let inputs = ConnectionChip.Inputs(
            socket: chipSocketState,
            events: eventsLife,
            failed: connectionFailed,
            tokenRefused: tokenRefused
        )
        let chip = ConnectionChip(inputs)
        if chip != connectionChip { connectionChip = chip }
    }

    private var chipSocketState: ConnectionChip.Inputs.State {
        switch socketLife {
        case .closed: return .idle
        case .connecting: return .connecting
        case .open: return .open
        case .reconnecting: return .reconnecting
        }
    }

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
            return Probe(ok: false, title: "That address doesn't look like host:port.", lines: ["Try 192.168.8.141:7777."])
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
