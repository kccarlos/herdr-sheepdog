import Foundation

/// What the presence orb shows. One value derived from every signal the
/// session has, so the screen can never disagree with itself.
public enum SessionState: String, Equatable, Sendable, CaseIterable {
    /// Not connected, by choice.
    case disconnected
    /// Opening the socket.
    case connecting
    /// The socket dropped and we are backing off.
    case reconnecting
    /// The daemon runs without a voice session (`--voice=off`), or the
    /// session closed for good. Say so; never pretend to listen.
    case voiceOff
    /// iOS paused the audio (a call, Siri) and would not let it resume by
    /// itself. The orb is the resume button.
    case audioPaused
    /// The model has not reacted to the last utterance for a while.
    case stalled
    /// The dog is talking.
    case speaking
    /// A tool call is in flight; the dog is working something out.
    case thinking
    /// The microphone could not be used: permission denied, or it would
    /// not start (another app holding it). The dog can still talk.
    case noMicrophone
    /// The microphone is off.
    case muted
    /// Listening, but the silence gate is closed — nothing is being sent.
    case silent
    /// Listening with the gate open.
    case listening
    /// Connected, nothing happening.
    case idle

    /// The inputs the state is derived from.
    public struct Inputs: Equatable, Sendable {
        public var socket: SocketLife
        public var voice: WSMessage.Voice?
        public var stalled: Bool
        public var muted: Bool
        /// The silence gate is closed: nothing is being sent.
        public var gateClosed: Bool
        /// Audio is queued or playing.
        public var speaking: Bool
        /// A `voice.tool_call` is outstanding.
        public var thinking: Bool
        /// The audio engine is paused after an interruption iOS would not
        /// let it resume from.
        public var audioPaused: Bool
        /// The microphone was denied or failed to start this session.
        public var micUnavailable: Bool

        public init(
            socket: SocketLife = .idle,
            voice: WSMessage.Voice? = nil,
            stalled: Bool = false,
            muted: Bool = false,
            gateClosed: Bool = false,
            speaking: Bool = false,
            thinking: Bool = false,
            audioPaused: Bool = false,
            micUnavailable: Bool = false
        ) {
            self.socket = socket
            self.voice = voice
            self.stalled = stalled
            self.muted = muted
            self.gateClosed = gateClosed
            self.speaking = speaking
            self.thinking = thinking
            self.audioPaused = audioPaused
            self.micUnavailable = micUnavailable
        }
    }

    /// The whole state machine: one ordered set of rules, most urgent first.
    public static func resolve(_ i: Inputs) -> SessionState {
        switch i.socket {
        case .idle: return .disconnected
        case .connecting: return .connecting
        case .reconnecting: return .reconnecting
        case .open: break
        }
        if i.voice == .connecting || i.voice == .reconnecting { return .connecting }
        if i.voice == .closed { return .voiceOff }
        if i.audioPaused { return .audioPaused }
        if i.speaking { return .speaking }
        if i.stalled { return .stalled }
        if i.thinking { return .thinking }
        if i.micUnavailable { return .noMicrophone }
        if i.muted { return .muted }
        if i.gateClosed { return .silent }
        if i.voice == .connected { return .listening }
        return .idle
    }

    /// Whether the orb wears the small red slashed-mic badge: the mic is
    /// muted while the orb shows something other than "Muted" — the dog
    /// talking or thinking, which outrank mute. The mute button stays the
    /// source of truth; this only keeps the orb honest about it.
    public static func showsMuteBadge(_ state: SessionState, muted: Bool) -> Bool {
        muted && (state == .speaking || state == .thinking)
    }

    /// True while the microphone should be running.
    public var capturing: Bool {
        switch self {
        case .listening, .silent, .speaking, .thinking, .stalled, .idle: return true
        case .disconnected, .connecting, .reconnecting, .voiceOff, .audioPaused, .muted, .noMicrophone: return false
        }
    }

    /// The short label under the orb.
    public var title: String {
        switch self {
        case .disconnected: return "Not connected"
        case .connecting: return "Connecting…"
        case .reconnecting: return "Reconnecting…"
        case .voiceOff: return "Voice is off on the daemon"
        case .audioPaused: return "Audio paused by iOS — tap to resume"
        case .stalled: return "Waiting on the model"
        case .speaking: return "Speaking"
        case .thinking: return "Thinking"
        case .muted: return "Muted"
        case .noMicrophone: return "No microphone"
        case .silent: return "Listening — quiet"
        case .listening: return "Listening"
        case .idle: return "Ready"
        }
    }

    /// A plain sentence for the state, also used as the VoiceOver label.
    public func accessibilityLabel(voiceName: String) -> String {
        switch self {
        case .disconnected: return "Not connected to the daemon"
        case .connecting: return "Connecting to the daemon"
        case .reconnecting: return "Connection dropped, reconnecting"
        case .voiceOff: return "The daemon has no voice session, so \(voiceName) cannot hear you"
        case .audioPaused: return "iOS paused the audio. Tap to resume"
        case .stalled: return "\(voiceName) has not answered yet"
        case .speaking: return "\(voiceName) is speaking"
        case .thinking: return "\(voiceName) is looking something up"
        case .muted: return "Microphone muted"
        case .noMicrophone: return "No microphone. \(voiceName) cannot hear you, but can still talk"
        case .silent: return "Listening. The microphone is quiet, nothing is being sent"
        case .listening: return "Listening. \(voiceName) can hear you"
        case .idle: return "Connected and ready"
        }
    }
}

/// Hysteresis on "speaking": once audio has played, the orb keeps saying
/// so for `holdSeconds` across a playback gap, so a brief underrun (or the
/// pause between two chunks of one reply) never flips the orb to whatever
/// ranks below speaking — "Muted", when the user muted while the dog
/// talked. A flush or Stop is not a gap: `reset` drops the hold at once.
public struct SpeakingHold: Equatable, Sendable {
    public static let defaultHold: TimeInterval = 0.8

    public let holdSeconds: TimeInterval
    /// Audio is playing right now.
    public private(set) var playing = false
    /// When playback last stopped, while the hold is still running.
    public private(set) var stoppedAt: TimeInterval?

    public init(holdSeconds: TimeInterval = SpeakingHold.defaultHold) {
        self.holdSeconds = holdSeconds
    }

    /// Folds in whether audio is playing at `now` (call it on every
    /// change and on every tick); returns what the state machine should
    /// treat as speaking.
    @discardableResult
    public mutating func update(playing isPlaying: Bool, now: TimeInterval) -> Bool {
        if isPlaying {
            playing = true
            stoppedAt = nil
            return true
        }
        if playing {
            playing = false
            stoppedAt = now
        }
        return speaking(at: now)
    }

    /// What the orb should treat as speaking at `now`.
    public mutating func speaking(at now: TimeInterval) -> Bool {
        if playing { return true }
        guard let stoppedAt else { return false }
        if now - stoppedAt < holdSeconds { return true }
        self.stoppedAt = nil
        return false
    }

    /// A flush or Stop: not speaking, now.
    public mutating func reset() {
        playing = false
        stoppedAt = nil
    }
}
