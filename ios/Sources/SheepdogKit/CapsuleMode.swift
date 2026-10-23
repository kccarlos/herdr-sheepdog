import Foundation

/// The floating voice capsule's look, one of the canvas's 13 modes
/// (docs/dev/design/app-redesign.md, "Voice capsule — every state"; its 15
/// tiles add two dark repeats). Pure: the view draws a mode, this decides
/// which one. What the orb *does* stays in `VoiceBarModel`.
public enum CapsuleMode: String, Equatable, Sendable, CaseIterable {
    /// Listening: the orange orb with the cream sticker ring.
    case live
    /// Listening while a question is open: the orb goes quiet so the answer
    /// button has the accent.
    case quiet
    /// The voice talks: blue, with the stop badge.
    case speaking
    /// A tool call: purple dots, with the stop badge.
    case thinking
    /// No reply for a while: amber hourglass, with the stop badge.
    case stalled
    /// The microphone is off: the red mic, a quiet orb.
    case muted
    /// iOS paused the audio: an amber play button resumes it.
    case paused
    /// The microphone was refused or would not start: an amber "!".
    case noMicrophone
    /// Connecting or reconnecting: an amber spinner, the mic dimmed.
    case reconnecting
    /// No session (ended, never started, or the daemon is unreachable):
    /// wifi-slash; the orb connects.
    case offline
    /// The daemon runs without a voice.
    case voiceOff
    /// Hold to talk, not held: the orb is the talk button.
    case hold
    /// Hold to talk, held: green ring, release to send.
    case holding

    public static func resolve(state: SessionState, talkMode: TalkMode = .openMic, holding: Bool = false, questionOpen: Bool = false) -> CapsuleMode {
        switch state {
        case .disconnected: return .offline
        case .connecting, .reconnecting: return .reconnecting
        case .voiceOff: return .voiceOff
        case .noMicrophone: return .noMicrophone
        case .audioPaused: return .paused
        case .speaking, .thinking, .stalled:
            if talkMode == .holdToTalk, holding { return .holding }
            return state == .speaking ? .speaking : (state == .thinking ? .thinking : .stalled)
        case .muted:
            if talkMode == .holdToTalk { return holding ? .holding : .hold }
            return .muted
        case .listening, .silent, .idle:
            if talkMode == .holdToTalk { return holding ? .holding : .hold }
            return questionOpen ? .quiet : .live
        }
    }

    /// Whether the mic (mute) button shows. Hidden where nothing hears the
    /// microphone or the orb itself is the talk button.
    public var showsMic: Bool {
        switch self {
        case .noMicrophone, .offline, .voiceOff, .hold, .holding: return false
        default: return true
        }
    }

    /// The mic button is shown but not usable (dimmed).
    public var micDimmed: Bool { self == .reconnecting }

    /// The orb wears the stop badge: tapping it stops the voice.
    public var showsStop: Bool { self == .speaking || self == .thinking || self == .stalled }

    /// The orb has the accent (orange fill and cream ring). Only `live`:
    /// one accent per screen.
    public var hasAccent: Bool { self == .live }
}
