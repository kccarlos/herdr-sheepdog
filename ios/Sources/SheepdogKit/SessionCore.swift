import Foundation

/// The voice session's decisions, as a value: what the app wants, what the
/// socket, the daemon's voice, the microphone and the player are doing, and
/// what to do about each thing that happens.
///
/// `SessionViewModel` used to make these decisions inline, spread across
/// socket and audio callbacks, main-actor hops and a dozen timers, none of
/// it testable. Now it only adapts: every callback becomes a
/// `SessionEvent`, `apply(_:now:)` updates the core and returns the
/// `SessionEffect`s to run (open the socket, send a message, start the
/// engine…), and the view model publishes what the core derives — the orb
/// (`sessionState`), the connection chip and the voice bar.
///
/// History, approvals, the provider picker, the one-time hints and the
/// conversation stay out: they have their own types.
///
/// Time comes in as `now`. The view model passes a monotonic clock
/// (`ProcessInfo.systemUptime`) expressed as a `Date`, which is what the
/// reconnect quiet period and the speaking hold always used; the thinking
/// timeout is only ever compared with the same `now`.
public struct SessionCore: Equatable, Sendable {
    /// The voice's `/ws` socket. Whether the app can talk to the computer
    /// at all, and when a lost socket opens again, is not here: that is the
    /// computer's `ComputerLink`, which every status surface renders
    /// (`LinkPresentation`, docs/dev/design/ios-state-architecture.md 3.2).
    /// While the link backs off, the orb says "Reconnecting…" and the chip
    /// says why (Reconnecting…, Offline with Try now, No network); the
    /// voice raises no banner of its own for it.
    public struct Connection: Equatable, Sendable {
        /// True from Connect until End (or a failure that ends the session).
        public var wantsConnection = false
        public var socket: SocketLife = .idle
        /// The `/ws` protocol handshake (`ProtocolHandshake`), as the socket
        /// last reported it. A mismatch outlives the connection, for Health
        /// and the chip's sheet, until the next Connect.
        public var handshake: ProtocolHandshake.State = .idle
        /// The app owns mute; this answers a daemon that disagrees.
        public var muteSync = MuteSync()
        /// What `VoiceSocket` tells the daemon on every open (`mute`, and
        /// `silence(true)` when the gate is closed). The view model copies
        /// these onto the socket after every event.
        public var socketMuted = false
        public var socketGateClosed = false
        /// The speech detector's state as the socket says it on every open
        /// (`speaking`), which makes it authoritative from the first frame.
        public var socketSpeaking = false
    }

    /// The daemon's voice session, from `/ws` `status`.
    public struct Voice: Equatable, Sendable {
        public var status: WSMessage.Voice?
        public var closedReason: String?
        public var stalled = false
        /// The live provider, from `status.provider` or the picker.
        public var provider: String?
        /// The "voice is off" notice is said once per session, not per status.
        public var saidVoiceIsOff = false
    }

    /// The microphone: the user's mute, talk mode and hold, and the gate.
    public struct Mic: Equatable, Sendable {
        public var talkMode: TalkMode = .openMic
        /// The user's mute (never set in hold-to-talk).
        public var muted = false
        /// Hold-to-talk: the orb is being held.
        public var holding = false
        /// The silence gate is closed: nothing is being sent.
        public var gateClosed = false
        /// The microphone delivers only digital zero while unmuted.
        public var noSignal = false
        /// The microphone was denied or would not start this session: the
        /// orb says "No microphone" instead of pretending to listen.
        public var unavailable = false
        /// The app's speech detector is open — it hears the user — as
        /// last told to the daemon (`{"type":"speaking"}`). Opens with the
        /// gate on speech (or a hold), closes with it, and is never open
        /// while muted or without a running microphone.
        public var speaking = false
    }

    /// What the player is doing.
    public struct Playback: Equatable, Sendable {
        /// Audio is playing right now (raw, from the engine).
        public var speaking = false
        /// Hysteresis on `speaking` for the orb: 800 ms across playback gaps.
        public var hold = SpeakingHold()
        /// `hold`'s answer at the last event: what the orb treats as speaking.
        public var heldSpeaking = false
    }

    /// iOS and the app's own lifecycle taking the audio away.
    public struct AudioSuspension: Equatable, Sendable {
        /// iOS paused the audio and would not resume it by itself.
        public var pausedByIOS = false
        /// Paused in the background: Keep listening with the screen locked
        /// is off and the app left the foreground.
        public var backgroundSuspended = false
        /// Settings ▸ Keep listening with the screen locked.
        public var keepListeningWhenLocked = true
    }

    public var connection = Connection()
    public var voice = Voice()
    public var mic = Mic()
    public var playback = Playback()
    /// A `voice.tool_call` is outstanding until this moment (30 s), or
    /// until its result, a Stop, or End.
    public var thinkingExpiry: Date?
    public var suspension = AudioSuspension()

    /// How long a tool call may keep the orb on "Thinking".
    public static let thinkingTimeout: TimeInterval = 30

    public init(talkMode: TalkMode = .openMic, keepListeningWhenLocked: Bool = true) {
        mic.talkMode = talkMode
        suspension.keepListeningWhenLocked = keepListeningWhenLocked
    }

    // MARK: - Derived

    /// What the daemon should believe about mute right now.
    public var daemonMute: Bool {
        MuteSync.daemonMute(intent: mic.muted, suspended: suspension.backgroundSuspended)
    }

    public var stateInputs: SessionState.Inputs {
        SessionState.Inputs(
            socket: connection.socket,
            voice: voice.status,
            stalled: voice.stalled,
            muted: mic.muted,
            gateClosed: mic.gateClosed,
            speaking: playback.heldSpeaking,
            thinking: thinkingExpiry != nil,
            audioPaused: suspension.pausedByIOS,
            micUnavailable: mic.unavailable
        )
    }

    /// The orb.
    public var sessionState: SessionState { SessionState.resolve(stateInputs) }

    /// What the voice bar's controls do.
    public var voiceBar: VoiceBarModel { VoiceBarModel(state: sessionState, talkMode: mic.talkMode) }

    /// The line above the voice bar.
    public var statusLine: String {
        VoiceBarModel.statusLine(state: sessionState, talkMode: mic.talkMode, holding: mic.holding, noSignal: mic.noSignal)
    }

    /// Audio from `/ws` is played: not while paused in the background, so
    /// nothing piles up to play stale on return.
    public var acceptsPlayback: Bool { !suspension.backgroundSuspended }

    /// The player is busy or its hold is still running (the UI ticker runs
    /// fast while this, or the engine, is up).
    public var playbackBusy: Bool { playback.speaking || playback.hold.stoppedAt != nil }

    // MARK: - Transitions

    /// Folds one event in and returns what to do, in order. The caller runs
    /// the effects after this returns (never from inside it): an effect
    /// may call back synchronously (`AudioEngine.setMuted` reports the gate)
    /// and so feed the next event.
    public mutating func apply(_ event: SessionEvent, now: Date) -> [SessionEffect] {
        var effects: [SessionEffect] = []
        // Whether the event ends with the derivation step (the speaking
        // hold, and so the orb) — every event that used to call the view
        // model's `refresh()`.
        var derive = true
        let t = now.timeIntervalSinceReferenceDate

        switch event {
        case .connectTapped:
            effects.append(.clearBanner)
            // Nothing from a session that ended by failing survives into
            // this one: the orb must not say "Listening" off a stale status
            // before the daemon has sent a new one.
            resetSession()
            connection.wantsConnection = true
            connection.socket = .connecting
            connection.handshake = .idle
            effects.append(.probe)

        case let .probeSucceeded(micGranted):
            // End may have been tapped while the probe or the permission
            // prompt was out.
            guard connection.wantsConnection else { break }
            if !micGranted {
                // The session is still worth having: status and transcripts
                // work without a microphone.
                mic.unavailable = true
                effects.append(.showBanner(.microphoneDenied))
            }
            openSocket(&effects)

        case let .probeFailed(error):
            // A probe that lands after End is for a session nobody wants:
            // no banner, no "Offline".
            guard connection.wantsConnection else {
                derive = false
                break
            }
            fail(error, &effects)

        case .noNetwork:
            // Connect with no network: the link's words, not the address's.
            guard connection.wantsConnection else {
                derive = false
                break
            }
            end(banner: .noNetwork, &effects)

        case let .tokenRefused(endpoint):
            guard connection.wantsConnection else {
                derive = false
                break
            }
            // A refused token does not fix itself by retrying.
            fail(.unauthorized(endpoint: endpoint), &effects)

        case .endTapped:
            disconnect(&effects)

        case .socketOpened:
            connection.socket = .open
            effects.append(.clearBanner)
            connection.muteSync.reset()
            effects.append(.haptic(.connected))

        case let .socketClosed(reason, willRetry):
            // A failure may already have ended the session this attempt
            // belonged to; never show "Reconnecting…" for a dead one.
            let retrying = willRetry && connection.wantsConnection
            connection.socket = retrying ? .reconnecting : .idle
            effects.append(.stopAudio)
            quietSpeaking()
            if !retrying {
                connection.wantsConnection = false
                effects.append(.stopTicker)
                resetSession()
                if reason == "replaced" {
                    effects.append(.showBanner(.replaced))
                }
                if reason == ProtocolHandshake.closeReason, let message = connection.handshake.mismatchMessage {
                    effects.append(.showBanner(.protocolMismatch(message)))
                }
            }

        case let .socketFailed(error, willRetry):
            if case .unauthorized = error {
                // A refused token does not fix itself by retrying.
                fail(error, &effects)
            } else if willRetry && connection.wantsConnection {
                // A daemon restart: the orb says "Reconnecting…", quietly,
                // and the link says when to try again. Past its quiet
                // period the chip reads Offline, with Try now.
                effects.append(.log(.reconnectRetry(error.message)))
            } else {
                fail(error, &effects)
            }

        case let .status(message):
            voice.status = message.voice
            voice.closedReason = message.reason
            voice.stalled = message.stalled ?? false
            if let provider = message.provider { voice.provider = provider }
            // The app owns mute: a daemon that disagrees is told again,
            // never obeyed.
            let intent = daemonMute
            if case let .resend(log) = connection.muteSync.status(daemonMuted: message.muted, intent: intent) {
                if log {
                    effects.append(.log(.muteDisagreement(daemonMuted: message.muted ?? false, wanted: intent)))
                }
                effects.append(.send(.mute(intent)))
            }
            applyVoiceStatus(&effects)

        case .flush:
            effects.append(contentsOf: [.flushPlayback, .flushTranscript, .syncHistory])
            playback.speaking = false
            playback.hold.reset()

        case let .daemonError(text):
            effects.append(.showBanner(.daemonSaid(text)))

        case let .handshakeChanged(state):
            connection.handshake = state
            derive = false

        case .muteTapped:
            setMuted(!mic.muted, &effects)

        case .holdBegan:
            guard mic.talkMode == .holdToTalk, !mic.holding, voiceBar.orbAction == .holdToTalk else {
                derive = false
                break
            }
            // Talking over the voice stops it.
            let state = sessionState
            if playback.speaking || state == .speaking || state == .thinking || state == .stalled {
                stop(&effects)
            }
            mic.holding = true
            effects.append(.setManualGate(true))
            effects.append(.haptic(.tap))

        case .holdEnded:
            guard mic.holding else {
                derive = false
                break
            }
            // The orb came up: close the gate, the daemon hears the silence.
            mic.holding = false
            effects.append(.setManualGate(mic.talkMode == .holdToTalk ? false : nil))

        case let .talkModeChanged(mode):
            guard mode != mic.talkMode else {
                derive = false
                break
            }
            mic.talkMode = mode
            mic.holding = false
            if mode == .holdToTalk {
                // Not holding *is* muted in this mode; a real mute on top
                // would make the orb do nothing.
                if mic.muted { setMuted(false, &effects) }
                effects.append(.setManualGate(false))
            } else {
                effects.append(.setManualGate(nil))
            }

        case let .gateChanged(closed):
            mic.gateClosed = closed
            // A muted client sends no silence transitions (the contract's
            // "mute and silence are independent"): muting resets the gate,
            // and the unmute tells the daemon where it stands.
            guard !mic.muted else { break }
            connection.socketGateClosed = closed
            effects.append(.send(.silence(closed)))
            // The gate is the app's speech detector: open on speech (or a
            // hold), closed after the hangover. Muting reports the gate
            // open without any speech, hence the mute check.
            setShepherdSpeaking(!closed && !mic.muted, &effects)

        case let .simulatedGate(closed):
            mic.gateClosed = closed
            connection.socketGateClosed = closed

        case let .audioStarted(gateClosed):
            mic.unavailable = false
            // Hold-to-talk starts closed without a transition; tell the
            // daemon, which treats a new client as ungated.
            if gateClosed != mic.gateClosed {
                mic.gateClosed = gateClosed
                connection.socketGateClosed = gateClosed
                effects.append(.send(.silence(gateClosed)))
            }
            // A fresh engine hears nothing yet; a closed start is certainly
            // not speech.
            if gateClosed { setShepherdSpeaking(false, &effects) }

        case let .audioStartFailed(message):
            mic.unavailable = true
            effects.append(.showBanner(.microphoneFailed(message)))

        case let .audioStopped(message):
            // The engine died under us (a route change it could not
            // rebuild). Say so, and start it again once — a second
            // failure is `audioStartFailed`'s banner, not a loop.
            playback.speaking = false
            quietSpeaking()
            effects.append(.notice(message))
            startAudioIfNeeded(&effects)

        case let .playbackChanged(speaking):
            playback.speaking = speaking

        case let .micSignal(ok):
            mic.noSignal = !ok
            if !ok { effects.append(.haptic(.micLost)) }
            derive = false

        case let .interruption(paused):
            suspension.pausedByIOS = paused

        case .resumeTapped:
            effects.append(.resumeAudio)

        case let .audioResumeFailed(message):
            effects.append(.showBanner(.audioResumeFailed(message)))

        case .toolCall:
            thinkingExpiry = now.addingTimeInterval(Self.thinkingTimeout)

        case .toolResult:
            thinkingExpiry = nil
            effects.append(.refreshState)

        case .stopTapped:
            stop(&effects)

        case .scenePhase(.background):
            // With Keep listening with the screen locked off: stop the audio
            // (capture and playback) and tell the daemon we are muted, so it
            // closes a per-minute session.
            guard !suspension.backgroundSuspended,
                  AudioLifecycle.suspendsInBackground(keepListeningWhenLocked: suspension.keepListeningWhenLocked, connected: connection.wantsConnection) else {
                derive = false
                break
            }
            suspension.backgroundSuspended = true
            effects.append(.suspendAudio)
            connection.socketMuted = daemonMute
            effects.append(.send(.mute(true)))
            setShepherdSpeaking(false, &effects)
            effects.append(.log(.pausedInBackground))

        case .scenePhase(.active):
            guard suspension.backgroundSuspended else {
                derive = false
                break
            }
            suspension.backgroundSuspended = false
            connection.socketMuted = daemonMute
            effects.append(.send(.mute(daemonMute)))
            startAudioIfNeeded(&effects)
            effects.append(.log(.resumedFromBackground))

        case let .keepListeningLockedChanged(on):
            suspension.keepListeningWhenLocked = on
            derive = false

        case let .providerSelected(id):
            voice.provider = id
            derive = false

        case .tick:
            if let expiry = thinkingExpiry, expiry < now { thinkingExpiry = nil }
        }

        if derive {
            playback.heldSpeaking = playback.hold.update(playing: playback.speaking, now: t)
        }
        return effects
    }

    // MARK: - Shared steps

    private mutating func openSocket(_ effects: inout [SessionEffect]) {
        connection.socketMuted = daemonMute
        connection.socketGateClosed = false
        effects.append(contentsOf: [.openSocket, .startTicker])
    }

    /// End: everything back to "not connected".
    private mutating func disconnect(_ effects: inout [SessionEffect]) {
        connection.wantsConnection = false
        effects.append(contentsOf: [.closeSocket, .stopAudio, .stopTicker])
        connection.socket = .idle
        connection.handshake = .idle
        resetSession()
        if mic.talkMode == .holdToTalk { effects.append(.setManualGate(false)) }
    }

    /// Everything a session learned, forgotten — by End, by a failure that
    /// ends it, and at the next Connect. The user's own choices (mute, the
    /// talk mode, the lock toggle) and the connection's failure flags stay.
    private mutating func resetSession() {
        suspension.backgroundSuspended = false
        suspension.pausedByIOS = false
        connection.muteSync.reset()
        voice = Voice()
        mic.gateClosed = false
        mic.holding = false
        mic.noSignal = false
        mic.unavailable = false
        quietSpeaking()
        playback.speaking = false
        playback.hold.reset()
        playback.heldSpeaking = false
        thinkingExpiry = nil
    }

    /// A failure that ends the session: say why, and stop everything. A
    /// refusal that only a new scan fixes is the link's to present
    /// (`ComputerLink.State.refused`), not the voice's.
    private mutating func fail(_ error: DaemonError, _ effects: inout [SessionEffect]) {
        end(banner: .connectionFailed(error), &effects)
    }

    private mutating func end(banner: SessionBanner, _ effects: inout [SessionEffect]) {
        effects.append(.showBanner(banner))
        connection.wantsConnection = false
        connection.socket = .idle
        effects.append(contentsOf: [.closeSocket, .stopAudio, .stopTicker])
        resetSession()
    }

    /// Tells the daemon the speech detector opened or closed, once per change.
    private mutating func setShepherdSpeaking(_ on: Bool, _ effects: inout [SessionEffect]) {
        guard on != mic.speaking else { return }
        mic.speaking = on
        connection.socketSpeaking = on
        effects.append(.send(.speaking(on)))
    }

    /// The socket is going (or gone): the next open starts from "not
    /// speaking", with nothing to send now.
    private mutating func quietSpeaking() {
        mic.speaking = false
        connection.socketSpeaking = false
    }

    private mutating func setMuted(_ requested: Bool, _ effects: inout [SessionEffect]) {
        // Hold-to-talk hides Mute: a mute there could never be undone.
        let muted = MuteSync.intent(requested: requested, talkMode: mic.talkMode)
        guard muted != mic.muted else { return }
        mic.muted = muted
        connection.socketMuted = daemonMute
        if muted {
            // Stop the source first, so no frame can follow the message.
            effects.append(.setCaptureMuted(true))
            effects.append(.send(.mute(true)))
            setShepherdSpeaking(false, &effects)
        } else {
            // Tell the daemon first, so it does not drop the first frames.
            effects.append(.send(.mute(daemonMute)))
            // Muting reset the gate to open without telling the daemon; if
            // it last heard "closed", say "open" now, so the two agree.
            if connection.socketGateClosed {
                connection.socketGateClosed = false
                effects.append(.send(.silence(false)))
            }
            effects.append(.setCaptureMuted(false))
            // Capture starts on the running engine now, not on the
            // daemon's status echo; start the engine if it is not up.
            startAudioIfNeeded(&effects)
        }
        mic.gateClosed = false
        effects.append(.haptic(.tap))
    }

    /// The Stop button (and a hold that talks over the voice): silence now,
    /// and ask the daemon to cancel. It answers with a `flush`.
    private mutating func stop(_ effects: inout [SessionEffect]) {
        effects.append(contentsOf: [.flushPlayback, .flushTranscript, .send(.cancel)])
        playback.speaking = false
        playback.hold.reset()
        thinkingExpiry = nil
        effects.append(.haptic(.stop))
    }

    /// Capture runs only while something can hear it.
    private mutating func applyVoiceStatus(_ effects: inout [SessionEffect]) {
        if voice.status == .closed {
            effects.append(.stopAudio)
            setShepherdSpeaking(false, &effects)
            // Being replaced is the banner's to explain ("Another client took
            // the voice session", with Take it back); a notice as well would
            // say it twice.
            if !voice.saidVoiceIsOff, voice.closedReason != "replaced" {
                voice.saidVoiceIsOff = true
                let why: LocalNotice = voice.closedReason == "voice_off"
                    ? .voiceOff
                    : .voiceClosed(reason: voice.closedReason ?? "no reason given")
                effects.append(.notice(why.text))
            }
            return
        }
        voice.saidVoiceIsOff = false
        startAudioIfNeeded(&effects)
    }

    /// The engine runs whenever the voice session is up — muted or not,
    /// since it also plays what the voice says. Mute only gates capture. The
    /// caller skips the effect when the engine already runs.
    private func startAudioIfNeeded(_ effects: inout [SessionEffect]) {
        guard AudioLifecycle.engineShouldRun(socketOpen: connection.socket == .open, voice: voice.status, suspended: suspension.backgroundSuspended) else { return }
        effects.append(.startAudio)
    }
}

// MARK: - Events

/// Everything that can happen to the session: the user's taps, the socket,
/// the `/events` stream, the audio engine, the app's lifecycle, and time.
public enum SessionEvent: Equatable, Sendable {
    public enum ScenePhase: Equatable, Sendable {
        case active, background
    }

    // The user
    case connectTapped
    case endTapped
    case muteTapped
    case holdBegan
    case holdEnded
    case talkModeChanged(TalkMode)
    /// The orb's "Audio paused by iOS — tap to resume".
    case resumeTapped
    case stopTapped
    case keepListeningLockedChanged(Bool)
    /// The provider picker switched the live provider.
    case providerSelected(String?)

    // Connecting
    /// `/api/state` answered (the auth probe); `micGranted` is the
    /// microphone permission, asked right after.
    case probeSucceeded(micGranted: Bool)
    case probeFailed(DaemonError)
    /// Connect found the phone with no network: the session ends with
    /// the link's words.
    case noNetwork
    /// The computer refused this device (the link is `refused`): the
    /// voice ends too.
    case tokenRefused(endpoint: String)

    // The `/ws` socket
    case socketOpened
    case socketClosed(reason: String, willRetry: Bool)
    case socketFailed(DaemonError, willRetry: Bool)
    case status(WSMessage)
    /// The daemon's `flush`.
    case flush
    /// The daemon's `error`.
    case daemonError(String)
    /// The `/ws` protocol handshake moved.
    case handshakeChanged(ProtocolHandshake.State)

    // The audio engine
    /// The silence gate flipped (`onGate`).
    case gateChanged(closed: Bool)
    /// `-sheepdogFakeVoice` drives the gate itself, without a microphone
    /// and without telling the daemon.
    case simulatedGate(closed: Bool)
    /// `startAudio` ran; `gateClosed` is the gate's state right after.
    case audioStarted(gateClosed: Bool)
    case audioStartFailed(String)
    /// The running engine stopped by itself (a route change it could not
    /// rebuild); the words say why.
    case audioStopped(String)
    case playbackChanged(speaking: Bool)
    /// The microphone delivers signal (`ok`) or only digital zero.
    case micSignal(ok: Bool)
    /// iOS paused the audio after an interruption and would not resume it
    /// (true), or it was resumed or stopped (false).
    case interruption(paused: Bool)
    case audioResumeFailed(String)

    // `/events`
    case toolCall
    case toolResult

    // The app
    case scenePhase(ScenePhase)
    /// The UI ticker (1 s idle, 100 ms while audio moves).
    case tick
}

// MARK: - Effects

/// What the view model does for the core, in the order given.
public enum SessionEffect: Equatable, Sendable {
    /// Run the auth probe (`/api/state`, `/api/hello`, the provider list),
    /// then ask for the microphone; report `probeSucceeded`/`probeFailed`.
    case probe
    /// `VoiceSocket.connect()`.
    case openSocket
    /// `VoiceSocket.disconnect()`.
    case closeSocket
    case send(WSMessage)
    /// Start the engine unless it already runs; report `audioStarted` or
    /// `audioStartFailed`.
    case startAudio
    case stopAudio
    /// Stop the engine for the background (the same call as `stopAudio`).
    case suspendAudio
    /// Take the audio back from iOS; report `audioResumeFailed`.
    case resumeAudio
    case setCaptureMuted(Bool)
    /// Hold-to-talk: `true` pressed, `false` released, `nil` open mic.
    case setManualGate(Bool?)
    case flushPlayback
    /// Close the live transcript bubble (a Stop or the daemon's `flush`).
    case flushTranscript
    case startTicker
    case stopTicker
    case showBanner(SessionBanner)
    case clearBanner
    /// A notice in the conversation.
    case notice(String)
    case haptic(SessionHaptic)
    /// A debounced `/api/state` fetch.
    case refreshState
    /// A history sync once the daemon has logged the turn (2.5 s).
    case syncHistory
    case log(SessionLog)
}

/// The banners the session raises. The view model owns the words' layout
/// (tone, hint, button); these say which.
public enum SessionBanner: Equatable, Sendable {
    /// The session ended: "Try again".
    case connectionFailed(DaemonError)
    /// Still retrying after the quiet period: "Try now".
    /// Connect with no network (the link is `noNetwork`).
    case noNetwork
    /// Another client took the voice session: "Take it back".
    case replaced
    case microphoneDenied
    /// The engine would not start.
    case microphoneFailed(String)
    /// The daemon's `error` message.
    case daemonSaid(String)
    /// The `/ws` protocol handshake found a mismatch: update the app, or
    /// the daemon. No retry button — retrying cannot fix it.
    case protocolMismatch(String)
    /// iOS would not give the audio back.
    case audioResumeFailed(String)
}

public enum SessionHaptic: Equatable, Sendable {
    case connected, tap, stop, answerFailed
    /// The microphone stopped delivering signal: a warning, not an error.
    case micLost
}

/// Log lines the core asks for, so the view model keeps its log categories.
public enum SessionLog: Equatable, Sendable {
    case reconnectRetry(String)
    case muteDisagreement(daemonMuted: Bool, wanted: Bool)
    case pausedInBackground
    case resumedFromBackground
}
