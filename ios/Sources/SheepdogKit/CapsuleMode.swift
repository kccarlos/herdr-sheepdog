import Foundation

/// The floating voice capsule's look, one of the canvas's 16 modes
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
    /// The voice ended by choice (or never started) and the computer is
    /// reachable: an orange mic orb starts the session. Agents, typing and
    /// tapping an answer keep working.
    case stopped
    /// `stopped` while a question is open: the orb goes quiet so the answer
    /// button has the accent.
    case stoppedQuiet
    /// Another window of this app holds the audio (iPad): a quiet mic
    /// orb, and "Use here" moves the session to this window.
    case elsewhere
    /// No session and the computer cannot be reached: wifi-slash; the orb
    /// retries.
    case offline
    /// The daemon runs without a voice.
    case voiceOff
    /// Hold to talk, not held: the orb is the talk button.
    case hold
    /// Hold to talk, held: green ring, release to send.
    case holding

    /// - Parameters:
    ///   - computerUnreachable: the link says the computer cannot be
    ///     reached (`LinkPresentation.computerUnreachable`). Only changes
    ///     what "no session" looks like: wifi-slash, not "tap to talk".
    ///   - audioElsewhere: another window holds the audio
    ///     (`SceneCoordinator.isOwner` is false). Wins over every state,
    ///     since this window's capsule controls nothing.
    public static func resolve(
        state: SessionState,
        talkMode: TalkMode = .openMic,
        holding: Bool = false,
        questionOpen: Bool = false,
        computerUnreachable: Bool = false,
        audioElsewhere: Bool = false
    ) -> CapsuleMode {
        if audioElsewhere { return .elsewhere }
        switch state {
        case .disconnected:
            if computerUnreachable { return .offline }
            return questionOpen ? .stoppedQuiet : .stopped
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
        case .noMicrophone, .offline, .voiceOff, .hold, .holding, .stopped, .stoppedQuiet, .elsewhere: return false
        default: return true
        }
    }

    /// The mic button is shown but not usable (dimmed).
    public var micDimmed: Bool { self == .reconnecting }

    /// The orb wears the stop badge: tapping it stops the voice.
    public var showsStop: Bool { self == .speaking || self == .thinking || self == .stalled }

    /// The orb has the accent (orange fill). Only `live` and `stopped`,
    /// which never show together: one accent per screen. The cream sticker
    /// ring is `live`'s alone.
    public var hasAccent: Bool { self == .live || self == .stopped }

    /// "Use here" replaces the keyboard button: tapping it moves the voice
    /// session to this window.
    public var showsUseHere: Bool { self == .elsewhere }

    /// Words that replace the status line, for the modes whose status the
    /// session's state cannot say (the state is `disconnected`, or this
    /// window does not hold the audio).
    public var statusOverride: String? {
        switch self {
        case .stopped, .stoppedQuiet: return "Not talking · tap the orb"
        case .elsewhere: return "In another window"
        default: return nil
        }
    }
}
