import Foundation
import SheepdogKit

/// What the menu needs to know about this Mac's voice session, apart from
/// what the daemon says (`MenuBarState`): the rows' enabled states and the
/// one line of trouble.
public struct VoiceMenuState: Equatable {
    /// This Mac wants the voice: from Use here until End, a replacement or a
    /// failure that ends the session.
    public var heldHere = false
    public var muted = false
    /// The microphone is refused in System Settings.
    public var micDenied = false
    /// The app's speech detector hears the user now.
    public var userTalking = false
    /// Why the session is not working, in a sentence (the banner's words on
    /// the phone), or nil.
    public var problem: String?

    public init(heldHere: Bool = false, muted: Bool = false, micDenied: Bool = false, userTalking: Bool = false, problem: String? = nil) {
        self.heldHere = heldHere
        self.muted = muted
        self.micDenied = micDenied
        self.userTalking = userTalking
        self.problem = problem
    }
}

/// What the menu and `sheepdog://` drive: the Mac's voice session.
@MainActor
public protocol VoiceControlling: AnyObject {
    var state: VoiceMenuState { get }
    /// The state changed.
    var onChange: (@MainActor () -> Void)? { get set }
    /// A `sheepdog://` route, or a menu item that is one (Talk, Mute, Stop).
    func handle(route: AppRoute)
    /// Use here: connect `/ws`, which takes the voice from the phone.
    func useHere()
    /// Hold-to-talk from the global hotkey or the daemon's `ptt` command.
    func pushToTalk(on: Bool)
    func openMicrophoneSettings()
}

/// The thin host of the Mac's `/ws` voice session: the macOS counterpart of
/// `SessionViewModel` and `VoiceEngine` around Kit's `SessionCore`
/// (docs/dev/design/desktop-client.md section 5.1). Every callback becomes a
/// `SessionEvent`, the core decides, and the effects it returns are run here
/// over `VoiceSocket`, `MacAudioEngine` and the microphone permission. What
/// has no meaning without a screen (haptics, banners, the conversation) is
/// dropped or becomes the menu's one line of trouble.
///
/// The microphone is open (open mic); Mute is the core's mute, so the panel's
/// `POST /api/voice/mute` and the menu agree. Talk is Kit's `AppRoute.talk`:
/// start the session, or unmute the one that runs. Hold-to-talk from the
/// daemon (`voice-command` `ptt`) unmutes while held and mutes again after,
/// when it was muted before.
@MainActor
final class MacVoiceHost: VoiceControlling {
    /// The one line when the microphone is refused (the menu's words).
    static let microphoneOff = "Microphone off: open System Settings"
    /// How long a lost `/ws` waits before it opens again.
    static let reopenDelay: TimeInterval = 3

    /// The session's state and decisions. Fed only through `send(_:)`.
    private(set) var core = SessionCore(talkMode: .openMic)
    private(set) var state = VoiceMenuState()
    var onChange: (@MainActor () -> Void)?
    /// The session start in flight (the probe, then the permission prompt).
    private(set) var opening: Task<Void, Never>?

    private let channel: VoiceChannel
    private let audio: VoiceAudio
    private let prober: DaemonProbing
    private let microphone: MicrophonePermission
    private let settings: SystemSettingsOpening
    private let timers: VoiceTimers
    private let log: AppLogging
    private let clock: () -> Date
    /// A hold-to-talk from the daemon unmuted the microphone: the release
    /// mutes it again.
    private var pttUnmuted = false
    private var problem: String?
    private var provider: String?

    init(
        channel: VoiceChannel,
        audio: VoiceAudio,
        prober: DaemonProbing,
        microphone: MicrophonePermission,
        settings: SystemSettingsOpening,
        timers: VoiceTimers,
        log: AppLogging,
        clock: @escaping () -> Date = { Date(timeIntervalSinceReferenceDate: ProcessInfo.processInfo.systemUptime) }
    ) {
        self.channel = channel
        self.audio = audio
        self.prober = prober
        self.microphone = microphone
        self.settings = settings
        self.timers = timers
        self.log = log
        self.clock = clock
        channel.client = .mac()
        wire()
    }

    // MARK: - Controls

    func useHere() {
        guard !core.connection.wantsConnection else { return }
        send(.connectTapped)
    }

    func handle(route: AppRoute) {
        for action in route.actions(wantsConnection: core.connection.wantsConnection, muted: core.mic.muted, talkMode: core.mic.talkMode) {
            switch action {
            case .connect: send(.connectTapped)
            case .toggleMute: send(.muteTapped)
            case .stop: send(.stopTapped)
            case .disconnect: send(.endTapped)
            // Open mic has no gate to toggle, and the conversation and the
            // agent windows are the panel's.
            case .toggleTalk, .showConversation, .openAgent, .openWindow: break
            }
        }
    }

    func openMicrophoneSettings() {
        settings.openMicrophonePrivacy()
    }

    // MARK: - Wiring

    private func wire() {
        channel.onEvent = { [weak self] event in
            self?.handle(socket: event)
        }
        audio.onFrame = { [weak self] frame in
            self?.channel.send(frame: frame)
        }
        audio.onGate = { [weak self] closed in
            self?.send(.gateChanged(closed: closed))
        }
        audio.onSpeakingChanged = { [weak self] speaking in
            self?.send(.playbackChanged(speaking: speaking))
        }
        // What the player did, straight to the daemon (like the frames).
        audio.onPlaybackReport = { [weak self] report in
            self?.channel.send(report.message)
        }
        audio.onDrain = { [weak self] drain in
            guard let self, self.core.connection.handshake.features.contains(FeedbackTag.feature) else { return }
            self.channel.send(drain.message)
        }
        audio.onMicSignal = { [weak self] noSignal in
            self?.send(.micSignal(ok: !noSignal))
        }
        audio.onStopped = { [weak self] message in
            self?.send(.audioStopped(message))
        }
        audio.onNotice = { [weak self] notice in
            self?.log.log(notice.logText)
        }
    }

    private func handle(socket event: VoiceSocket.Event) {
        switch event {
        case .opened:
            timers.cancelReopen()
            send(.socketOpened)
        case let .control(message):
            receive(control: message)
        case let .audio(data):
            guard core.acceptsPlayback else { break }
            audio.enqueue(data)
        case let .closed(reason, retrying):
            send(.socketClosed(reason: reason, willRetry: retrying))
            if retrying { reopenSoon() }
        case let .failed(error, retrying):
            send(.socketFailed(error, willRetry: retrying))
            if retrying { reopenSoon() }
        case .paused:
            // Only a link pauses the socket, and the Mac has none.
            break
        case let .handshake(state):
            send(.handshakeChanged(state))
        }
    }

    /// A lost `/ws` opens again while the session is wanted.
    private func reopenSoon() {
        guard core.connection.wantsConnection else { return }
        timers.reopen(after: Self.reopenDelay) { [weak self] in
            guard let self, self.core.connection.wantsConnection else { return }
            self.channel.connect()
        }
    }

    /// A control message from `/ws`.
    private func receive(control message: WSMessage) {
        switch message.type {
        case .status:
            send(.status(message))
        case .flush:
            send(.flush)
        case .error:
            if let text = message.message { send(.daemonError(text)) }
        case .voiceCommand:
            receive(command: message)
        // The conversation, the agent windows and the cards are the panel's.
        case .transcript, .screen, .screenError, .card:
            break
        case .pong, .mute, .silence, .speaking, .playback, .drain, .cancel, .text, .ping, .clientReady, .botReady,
             .screenSubscribe, .screenUnsubscribe, .visibility:
            break
        }
    }

    /// `voice-command` (docs/dev/ws-contract.md): another client of the
    /// daemon controls this one's voice.
    private func receive(command message: WSMessage) {
        switch message.action {
        case .stop?:
            // The daemon cut the speech and sent its `flush` already; this
            // is the same drop, for a flush that did not arrive first.
            send(.flush)
        case .ptt?:
            pushToTalk(on: message.on ?? false)
        case nil:
            break
        }
    }

    /// Hold-to-talk: the microphone is open while `on`. Muted before: unmute
    /// now and mute again at the release. Not muted: nothing to open, and
    /// nothing to close.
    func pushToTalk(on: Bool) {
        guard core.connection.wantsConnection else { return }
        if on {
            guard core.mic.muted else { return }
            pttUnmuted = true
            send(.muteTapped)
        } else if pttUnmuted {
            pttUnmuted = false
            if !core.mic.muted { send(.muteTapped) }
        }
    }

    // MARK: - The core

    /// Every session event goes through here: the core decides, then its
    /// effects run in order, then the menu gets the result.
    func send(_ event: SessionEvent) {
        let effects = core.apply(event, now: clock())
        if !core.connection.wantsConnection { pttUnmuted = false }
        // What the socket says on every open.
        channel.muted = core.connection.socketMuted
        channel.gateClosed = core.connection.socketGateClosed
        channel.speaking = core.connection.socketSpeaking
        // The playback profile follows the provider before any effect runs.
        if provider != core.voice.provider {
            provider = core.voice.provider
            audio.setProvider(provider)
        }
        for effect in effects { run(effect) }
        publish()
    }

    private func run(_ effect: SessionEffect) {
        switch effect {
        case .probe:
            opening = Task { await openSession() }
        case .openSocket:
            timers.cancelReopen()
            channel.connect()
        case .closeSocket:
            timers.cancelReopen()
            channel.disconnect(reason: "user disconnect")
        case let .send(message):
            channel.send(message)
        case .startAudio:
            startAudioIfNeeded()
        case .stopAudio, .suspendAudio:
            audio.stop()
        case .resumeAudio:
            // The core asks for it only after an interruption, which a Mac
            // does not have.
            break
        case let .setCaptureMuted(muted):
            audio.setMuted(muted)
        case let .setManualGate(manual):
            audio.setManualGate(manual)
        case .flushPlayback:
            audio.flushPlayback()
        case .startTicker:
            timers.startTicking(interval: { [weak self] in self?.tickInterval ?? 1 }, fire: { [weak self] in self?.tick() })
        case .stopTicker:
            timers.stopTicking()
        case let .showBanner(banner):
            problem = Self.words(for: banner)
            log.log(problem ?? "")
        case .clearBanner:
            problem = nil
        case let .notice(text):
            log.log(text)
        case let .log(line):
            log.log(Self.words(for: line))
        // No haptics on a Mac; the conversation, its history and the state
        // refresh are the panel's and the status monitor's.
        case .haptic, .flushTranscript, .refreshState, .syncHistory:
            break
        }
    }

    /// The `probe` effect: the auth probe first, then the microphone
    /// permission, which asks only the first time. The core ignores the
    /// answer if End came meanwhile.
    private func openSession() async {
        do {
            try await prober.probe()
        } catch let error as DaemonError {
            send(.probeFailed(error))
            return
        } catch {
            send(.probeFailed(.malformed(error.localizedDescription)))
            return
        }
        guard core.connection.wantsConnection else { return }
        let granted = await microphone.request()
        send(.probeSucceeded(micGranted: granted))
    }

    /// The `startAudio` effect: the engine, unless it already runs or the
    /// microphone is refused (the engine would start without hearing
    /// anything).
    private func startAudioIfNeeded() {
        guard !audio.isRunning else { return }
        guard microphone.access != .denied else {
            send(.audioStartFailed(Self.microphoneOff))
            return
        }
        do {
            try audio.start()
            send(.audioStarted(gateClosed: audio.gateIsClosed))
        } catch {
            send(.audioStartFailed((error as? LocalizedError)?.errorDescription ?? "The microphone would not start."))
        }
    }

    /// The core's derived values, for the menu. Told only on change.
    private func publish() {
        let next = VoiceMenuState(
            heldHere: core.connection.wantsConnection,
            muted: core.mic.muted,
            micDenied: microphone.access == .denied,
            userTalking: core.mic.speaking,
            problem: problem
        )
        guard next != state else { return }
        state = next
        onChange?()
    }

    // MARK: - The ticker

    /// True while the microphone or the player is doing something.
    private var tickInterval: TimeInterval {
        audio.isRunning || core.playbackBusy ? 0.1 : 1
    }

    /// One timer drives the playback release, the metrics and the thinking
    /// timeout. It runs fast only while audio is moving.
    func tick() {
        guard core.connection.wantsConnection else { return }
        audio.tick()
        let metrics = audio.metrics
        channel.gatedFrames = audio.gatedFrames
        channel.inputPeak = audio.inputPeak
        channel.underruns = metrics.underruns
        channel.drains = metrics.drains
        channel.bufferMs = metrics.bufferedMilliseconds
        channel.rate = metrics.playbackRate
        channel.stretchedMs = metrics.stretchedMilliseconds
        channel.audioState = audio.audioState
        if core.mic.noSignal != audio.noSignal, !audio.isRunning { send(.micSignal(ok: true)) }
        send(.tick)
    }

    // MARK: - Words

    /// The banner's sentence, for the menu (the phone's `Banner.init` has
    /// its own layout and iOS wording).
    static func words(for banner: SessionBanner) -> String {
        switch banner {
        case let .connectionFailed(error): return error.message
        case .noNetwork: return "This Mac is offline."
        case .replaced: return "Another client took the voice session."
        case .microphoneDenied: return microphoneOff
        case let .microphoneFailed(message): return message
        case let .daemonSaid(text): return "The daemon said: \(text)"
        case let .protocolMismatch(text): return text
        case let .audioResumeFailed(message): return message
        }
    }

    static func words(for line: SessionLog) -> String {
        switch line {
        case let .reconnectRetry(message): return "reconnect attempt failed, retrying: \(message)"
        case let .muteDisagreement(daemonMuted, wanted): return "daemon reports muted=\(daemonMuted) but the app wants muted=\(wanted); re-sending"
        case .pausedInBackground: return "paused in the background"
        case .resumedFromBackground: return "resumed from the background"
        }
    }
}
