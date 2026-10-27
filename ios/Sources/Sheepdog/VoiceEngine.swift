import AVFoundation
import Foundation
import SheepdogKit

/// What the voice hands back to the app around it (today
/// `SessionViewModel`): the effects that touch the conversation, banners
/// and refreshes, the socket's non-voice traffic, and the moments the app
/// must follow the voice (docs/dev/design/ios-state-architecture.md 3.1).
@MainActor
protocol VoiceEngineHost: AnyObject {
    /// Every effect, before the engine runs it. True: the host ran it
    /// (the offline demo's on-device voice, the probe, the conversation,
    /// banners, refreshes) and the engine does nothing more.
    func voiceEngine(_ engine: VoiceEngine, run effect: SessionEffect) -> Bool
    /// `wantsConnection` changed (already readable on the engine).
    func voiceEngineWantsConnectionChanged(_ engine: VoiceEngine)
    /// The `/ws` handshake the core holds changed.
    func voiceEngine(_ engine: VoiceEngine, handshakeChanged handshake: ProtocolHandshake.State)
    /// The end of every event, after the engine's state is current.
    func voiceEngineDidPublish(_ engine: VoiceEngine)
    /// The socket's lifecycle, for what rides `/ws` besides the voice
    /// (the agent window, cards, push): opened and handshake after the
    /// core heard them, closed, failed and paused before.
    func voiceEngine(_ engine: VoiceEngine, socket event: VoiceSocket.Event)
    /// A control message that is not the voice's: transcripts, the agent
    /// window, cards.
    func voiceEngine(_ engine: VoiceEngine, control message: WSMessage)
    /// The audio engine's own line for the conversation.
    func voiceEngine(_ engine: VoiceEngine, notice: String)
    /// The daemon takes the player's drain records (feature `feedback`).
    var voiceEngineSendsDrains: Bool { get }
    /// No banner is up, so the Voice Isolation tip may show.
    var voiceEngineCanShowTip: Bool { get }
    /// Show the Voice Isolation tip.
    func voiceEngineShowMicrophoneTip(_ engine: VoiceEngine)
}

/// The voice: `SessionCore` (every decision), the `AudioEngine`, the `/ws`
/// `VoiceSocket` and the `VoiceMeter`, plus the voice's view state — the
/// orb, mute, talk mode, hold, the microphone modes and the one-time hints.
///
/// It adapts every callback into a `SessionEvent`, runs the core's
/// effects it owns (socket, audio, ticker, haptics, logs) and hands the
/// rest to its host. Views read its observable state; the heavy audio work
/// stays where it was (the engine's render thread, `AudioEngine`).
@MainActor
@Observable
final class VoiceEngine {
    weak var host: VoiceEngineHost?

    /// The orb, `SessionCore.sessionState` as of the last event.
    /// `offerListeningHints()` reacts here, at the one place it changes.
    private(set) var state: SessionState = .disconnected {
        didSet { if state != oldValue { offerListeningHints() } }
    }
    // Mirrors of `core`, for the views (see `publish()`).
    private(set) var isMuted = false
    private(set) var talkMode: TalkMode = .openMic
    /// Hold-to-talk: the orb is being held.
    private(set) var holding = false
    /// The microphone delivers only digital zero while unmuted.
    private(set) var noMicSignal = false
    /// True from the moment Connect is tapped until Disconnect.
    private(set) var wantsConnection = false
    /// The one-time caption under the controls the first time Mute is used.
    private(set) var showMuteHint = false
    /// The first-run "just talk" coach mark.
    private(set) var showCoachMark = false
    /// Control Center ▸ Mic Mode, as iOS reports it.
    private(set) var microphoneMode: MicrophoneModeName = .standard
    private(set) var preferredMicrophoneMode: MicrophoneModeName = .standard
    /// The provider the live `/ws` status names.
    private(set) var liveVoiceProvider: String? {
        didSet { providerSourcesChanged() }
    }

    /// The input level, the audio metrics and the voice-detection
    /// diagnostics: everything that changes 10–25 times a second while
    /// audio is moving, observed per property (`VoiceMeter`).
    let meter = VoiceMeter()

    /// The session's state and decisions. Fed only through `send(_:)`.
    @ObservationIgnored private(set) var core: SessionCore
    @ObservationIgnored private let socket: VoiceSocket
    @ObservationIgnored private let audio = AudioEngine()
    @ObservationIgnored private var ticker: Task<Void, Never>?
    @ObservationIgnored private let defaults: UserDefaults
    @ObservationIgnored private let readMicrophoneModes: @MainActor () -> (active: MicrophoneModeName, preferred: MicrophoneModeName)
    /// The voice provider from every source the host knows, freshest first:
    /// the audio engine's jitter-buffer profile follows it.
    @ObservationIgnored var resolveProvider: () -> String? = { nil }

    #if DEBUG
    /// `-sheepdogFakeVoice`: the voice states without a live session or a
    /// microphone, for screenshots and UI tests against `--voice=off`.
    @ObservationIgnored let fakeVoice = DebugLaunch.fakeVoice
    @ObservationIgnored private var fakeVoiceDriver = FakeVoiceDriver()
    #endif

    static let muteHint = "Mute stops your microphone; Sheepdog can still talk."
    private static let muteHintShownKey = "hint.mute.shown"
    private static let coachShownKey = "hint.coach.shown"
    private static let micTipShownKey = "hint.micmode.shown"
    /// Every one-time hint, for `-sheepdogResetHints` / `-sheepdogResetState`.
    static let hintKeys = [muteHintShownKey, coachShownKey, micTipShownKey]

    /// The core's clock: monotonic (uptime), as the reconnect quiet period
    /// and the speaking hold always were.
    private var now: Date { Date(timeIntervalSinceReferenceDate: ProcessInfo.processInfo.systemUptime) }

    init(endpoint: DaemonEndpoint, talkMode: TalkMode, keepListeningWhenLocked: Bool, defaults: UserDefaults = .standard,
         microphoneModes: @escaping @MainActor () -> (active: MicrophoneModeName, preferred: MicrophoneModeName) = VoiceEngine.systemMicrophoneModes) {
        socket = VoiceSocket(endpoint: endpoint)
        core = SessionCore(talkMode: talkMode, keepListeningWhenLocked: keepListeningWhenLocked)
        self.defaults = defaults
        readMicrophoneModes = microphoneModes
        self.talkMode = talkMode
        if talkMode == .holdToTalk { audio.setManualGate(false) }
        wire()
    }

    /// Control Center's Mic Mode, as iOS reports it now.
    static func systemMicrophoneModes() -> (active: MicrophoneModeName, preferred: MicrophoneModeName) {
        (MicrophoneModeName(rawValue: AVCaptureDevice.activeMicrophoneMode.rawValue),
         MicrophoneModeName(rawValue: AVCaptureDevice.preferredMicrophoneMode.rawValue))
    }

    // MARK: - The link channel

    /// `/ws` follows the current computer, at the address its link uses.
    func update(endpoint: DaemonEndpoint) {
        socket.update(endpoint: endpoint)
    }

    /// The voice's `/ws` is the current computer's channel: its link alone
    /// opens and reopens it.
    func attach(to model: ComputerModel?) {
        model?.attach(.ws, socket)
    }

    /// The computer is no longer current: its link lets go of `/ws`.
    func detach(from model: ComputerModel) {
        model.attach(.ws, nil)
    }

    /// Something else that rides `/ws` (the agent window, card visibility).
    func send(message: WSMessage) {
        socket.send(message)
    }

    /// One of the provider's sources changed: the playback profile follows.
    func providerSourcesChanged() {
        audio.setProvider(resolveProvider())
    }

    // MARK: - Controls

    func toggleMute() {
        let wasMuted = core.mic.muted
        send(.muteTapped)
        if !wasMuted, core.mic.muted { offerMuteHint() }
    }

    /// The orb went down in hold-to-talk. Talking over the voice stops it.
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

    /// The Stop button: silence now, and ask the daemon to cancel. It
    /// answers with a `flush`, which we also honour.
    func stop() {
        send(.stopTapped)
    }

    // MARK: - Coach mark, the mute hint and the microphone-mode tip

    /// Once per install, the first time the voice is actually listening:
    /// "just talk". After that (the next time), the Voice Isolation tip.
    private func offerListeningHints() {
        guard core.mic.talkMode == .openMic, VoiceBarModel.isListening(state), core.connection.wantsConnection else { return }
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
        guard !showCoachMark, host?.voiceEngineCanShowTip ?? false, !defaults.bool(forKey: Self.micTipShownKey) else { return }
        refreshMicrophoneMode()
        guard microphoneMode != .voiceIsolation else { return }
        defaults.set(true, forKey: Self.micTipShownKey)
        host?.voiceEngineShowMicrophoneTip(self)
    }

    func dismissCoachMark() {
        guard showCoachMark else { return }
        showCoachMark = false
    }

    func refreshMicrophoneMode() {
        let (active, preferred) = readMicrophoneModes()
        if active != microphoneMode { microphoneMode = active }
        if preferred != preferredMicrophoneMode { preferredMicrophoneMode = preferred }
    }

    /// Control Center's Mic Mode panel, on top of the app.
    func showMicrophoneModes() {
        AVCaptureDevice.showSystemUserInterface(.microphoneModes)
    }

    /// Once per install: what Mute does and does not do.
    private func offerMuteHint() {
        guard !defaults.bool(forKey: Self.muteHintShownKey) else { return }
        defaults.set(true, forKey: Self.muteHintShownKey)
        showMuteHint = true
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 6_000_000_000)
            self?.showMuteHint = false
        }
    }

    func dismissMuteHint() {
        showMuteHint = false
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
        audio.onOutputLevel = { [weak self] level in
            self?.meter.setOutputLevel(level)
        }
        audio.onSpeakingChanged = { [weak self] speaking in
            self?.send(.playbackChanged(speaking: speaking))
        }
        // What the player did, straight to the daemon (like the frames):
        // "speaking" on the daemon means the user still hears it.
        audio.onPlaybackReport = { [weak self] report in
            self?.socket.send(report.message)
        }
        // Every dry ring, for diagnosing a marked pause: only to a daemon
        // that takes it (an older one answers an unknown type with an error).
        audio.onDrain = { [weak self] drain in
            guard let self, self.host?.voiceEngineSendsDrains == true else { return }
            self.socket.send(drain.message)
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
            self.host?.voiceEngine(self, notice: notice)
        }
    }

    private func handle(socket event: VoiceSocket.Event) {
        switch event {
        case .opened:
            // The socket tells the link itself (its reporter is tagged with
            // the computer and epoch it was opened for).
            send(.socketOpened)
            host?.voiceEngine(self, socket: event)
        case let .control(message):
            receive(control: message)
        case let .audio(data):
            // Paused in the background: nothing plays, and nothing piles
            // up to play stale on return.
            guard core.acceptsPlayback else { break }
            audio.enqueue(data)
        case let .closed(reason, retrying):
            host?.voiceEngine(self, socket: event)
            // A lost socket has told the link, which probes once (to tell a
            // restart from a refusal: a 401 on the upgrade is unreadable)
            // and says when to open it again.
            send(.socketClosed(reason: reason, willRetry: retrying))
        case let .failed(error, retrying):
            host?.voiceEngine(self, socket: event)
            send(.socketFailed(error, willRetry: retrying))
        case .paused:
            // The link closed it (offline, no network, the background
            // with Keep listening off); it reopens when the link allows.
            host?.voiceEngine(self, socket: event)
            send(.socketClosed(reason: "paused", willRetry: true))
        case let .handshake(state):
            send(.handshakeChanged(state))
            host?.voiceEngine(self, socket: event)
        }
    }

    /// A control message from `/ws` (or the demo's stand-in for one).
    func receive(control message: WSMessage) {
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
        case .flush:
            send(.flush)
        case .error:
            if let text = message.message { send(.daemonError(text)) }
        case .transcript, .screen, .screenError, .card:
            host?.voiceEngine(self, control: message)
        case .pong, .mute, .silence, .speaking, .playback, .drain, .cancel, .text, .ping, .clientReady, .botReady,
             .screenSubscribe, .screenUnsubscribe, .visibility:
            break
        }
    }

    // MARK: - The core

    /// Every session event goes through here: the core decides, then its
    /// effects run in order, then the views get the result. Effects may
    /// call back synchronously (`audio.setMuted` reports the gate), which
    /// re-enters here — safe, since `apply` has returned by then.
    func send(_ event: SessionEvent) {
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
        if host?.voiceEngine(self, run: effect) == true { return }
        switch effect {
        case .openSocket:
            socket.connect()
        case .closeSocket:
            socket.disconnect()
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
        case .startTicker:
            startTicking()
        case .stopTicker:
            ticker?.cancel(); ticker = nil
        case let .haptic(kind):
            switch kind {
            case .connected: Haptics.connected()
            case .tap: Haptics.tap()
            case .stop: Haptics.stop()
            case .answerFailed: Haptics.answerFailed()
            case .micLost: Haptics.warning()
            }
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
        case .probe, .flushTranscript, .showBanner, .clearBanner, .notice, .refreshState, .syncHistory:
            // The host's (the probe, the conversation, banners, refreshes).
            break
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
        if wantsConnection != core.connection.wantsConnection {
            wantsConnection = core.connection.wantsConnection
            host?.voiceEngineWantsConnectionChanged(self)
        }
        host?.voiceEngine(self, handshakeChanged: core.connection.handshake)
        let next = core.sessionState
        if next != state { state = next }
        host?.voiceEngineDidPublish(self)
    }

    // MARK: - The ticker

    /// True while the microphone or the player is doing something.
    private var audioIsBusy: Bool { audio.isRunning || core.playbackBusy }

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
    private func fakeTick() {
        guard core.connection.wantsConnection, core.voice.status == .connected else { return }
        let (level, diagnostics) = fakeVoiceDriver.tick(talkMode: core.mic.talkMode, holding: core.mic.holding, isMuted: core.mic.muted)
        meter.setLevel(level)
        meter.setGateDiagnostics(diagnostics)
        refreshMicrophoneMode()
    }
    #endif
}
