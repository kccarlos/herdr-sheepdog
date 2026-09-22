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
    /// The model has not reacted to the last utterance for a while.
    case stalled
    /// The dog is talking.
    case speaking
    /// A tool call is in flight; the dog is working something out.
    case thinking
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
        public enum Socket: String, Equatable, Sendable {
            case closed, connecting, open, reconnecting
        }

        public var socket: Socket
        public var voice: WSMessage.Voice?
        /// `reason` of a `closed` status; `voice_off` is the honest one.
        public var closedReason: String?
        public var stalled: Bool
        public var muted: Bool
        /// The silence gate is closed: nothing is being sent.
        public var gateClosed: Bool
        /// Audio is queued or playing.
        public var speaking: Bool
        /// A `voice.tool_call` is outstanding.
        public var thinking: Bool

        public init(
            socket: Socket = .closed,
            voice: WSMessage.Voice? = nil,
            closedReason: String? = nil,
            stalled: Bool = false,
            muted: Bool = false,
            gateClosed: Bool = false,
            speaking: Bool = false,
            thinking: Bool = false
        ) {
            self.socket = socket
            self.voice = voice
            self.closedReason = closedReason
            self.stalled = stalled
            self.muted = muted
            self.gateClosed = gateClosed
            self.speaking = speaking
            self.thinking = thinking
        }
    }

    /// The whole state machine: one ordered set of rules, most urgent first.
    public static func resolve(_ i: Inputs) -> SessionState {
        switch i.socket {
        case .closed: return .disconnected
        case .connecting: return .connecting
        case .reconnecting: return .reconnecting
        case .open: break
        }
        if i.voice == .connecting || i.voice == .reconnecting { return .connecting }
        if i.voice == .closed { return .voiceOff }
        if i.speaking { return .speaking }
        if i.stalled { return .stalled }
        if i.thinking { return .thinking }
        if i.muted { return .muted }
        if i.gateClosed { return .silent }
        if i.voice == .connected { return .listening }
        return .idle
    }

    /// True while the microphone should be running.
    public var capturing: Bool {
        switch self {
        case .listening, .silent, .speaking, .thinking, .stalled, .idle: return true
        case .disconnected, .connecting, .reconnecting, .voiceOff, .muted: return false
        }
    }

    /// The short label under the orb.
    public var title: String {
        switch self {
        case .disconnected: return "Not connected"
        case .connecting: return "Connecting…"
        case .reconnecting: return "Reconnecting…"
        case .voiceOff: return "Voice is off on the daemon"
        case .stalled: return "Waiting on the model"
        case .speaking: return "Speaking"
        case .thinking: return "Thinking"
        case .muted: return "Muted"
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
        case .stalled: return "\(voiceName) has not answered yet"
        case .speaking: return "\(voiceName) is speaking"
        case .thinking: return "\(voiceName) is looking something up"
        case .muted: return "Microphone muted"
        case .silent: return "Listening. The microphone is quiet, nothing is being sent"
        case .listening: return "Listening. \(voiceName) can hear you"
        case .idle: return "Connected and ready"
        }
    }
}
