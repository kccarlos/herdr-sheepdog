import Foundation

/// What the lock-screen Live Activity and the Dynamic Island say about the
/// voice session: one aggregate state (the phase, mute, how many agents are
/// waiting for an answer) and the words for each presentation, VoiceOver
/// labels included. Pure, so the mapping from the orb's `SessionState` is
/// tested here; the app copies it into the activity's `ContentState`
/// (`Sources/SheepdogActivity`), which the widget only renders.
public struct VoiceActivityState: Equatable, Sendable {
    /// The few phases the activity distinguishes. Raw values cross the
    /// process boundary as plain strings (`ContentState.phase`): never
    /// rename one.
    public enum Phase: String, Equatable, Sendable, CaseIterable {
        /// Opening the socket or the voice session, or reconnecting.
        case connecting
        /// The microphone is on (whether or not the gate is open).
        case listening
        /// The dog is talking.
        case speaking
        /// A tool call is out, or the model has not answered yet.
        case thinking
        /// The microphone is off; the dog can still talk.
        case muted
        /// Nothing can be heard right now: iOS paused the audio, there is
        /// no microphone, or the daemon has no voice session.
        case paused
    }

    public var phase: Phase
    /// The microphone is muted. Separate from `phase` because speaking and
    /// thinking outrank muted, and the activity still has to label its
    /// Mute button right.
    public var muted: Bool
    /// Agents waiting on the shepherd (`Flock.waitingCount`).
    public var waiting: Int
    /// The headline: "Listening", "Speaking", "Muted", "Reconnecting…".
    public var title: String
    /// "2 agents waiting for an answer", or nil when none is.
    public var waitingText: String?
    /// The full sentence VoiceOver reads for the lock-screen banner and the
    /// expanded island.
    public var accessibilityLabel: String
    /// The short one for the compact and minimal island.
    public var compactAccessibilityLabel: String

    /// The activity for the session as it is now, or nil when there is no
    /// session to show (not connected): the activity ends.
    /// `suspendedInBackground`: Keep listening with the screen locked is
    /// off and the app went to the background, so the audio stopped (the
    /// orb itself does not show this; the screen is not visible).
    public static func make(state: SessionState, muted: Bool, waiting: Int, voiceName: String, suspendedInBackground: Bool = false) -> VoiceActivityState? {
        let phase: Phase
        let title: String
        switch state {
        case .disconnected:
            return nil
        case _ where suspendedInBackground && state != .connecting && state != .reconnecting:
            phase = .paused; title = "Paused while locked"
        case .connecting:
            phase = .connecting; title = "Connecting…"
        case .reconnecting:
            phase = .connecting; title = "Reconnecting…"
        case .voiceOff:
            phase = .paused; title = "Voice is off"
        case .audioPaused:
            phase = .paused; title = "Paused by iOS"
        case .noMicrophone:
            phase = .paused; title = "No microphone"
        case .speaking:
            phase = .speaking; title = "Speaking"
        case .thinking, .stalled:
            phase = .thinking; title = "Thinking"
        case .muted:
            phase = .muted; title = "Muted"
        case .listening, .silent, .idle:
            phase = .listening; title = "Listening"
        }
        let waiting = max(0, waiting)
        let waitingText = Self.waitingText(waiting)
        var sentence = Self.phaseSentence(phase, state: state, title: title, voiceName: voiceName)
        if muted, phase == .speaking || phase == .thinking { sentence += ". Microphone muted" }
        if let waitingText { sentence += ". \(waitingText)" }
        var compact = phase == .muted || !muted ? title : "\(title), muted"
        if waiting > 0 { compact += ", \(waiting) waiting" }
        return VoiceActivityState(
            phase: phase,
            muted: muted,
            waiting: waiting,
            title: title,
            waitingText: waitingText,
            accessibilityLabel: sentence,
            compactAccessibilityLabel: compact
        )
    }

    /// "1 agent waiting for an answer" / "3 agents waiting for an answer".
    public static func waitingText(_ count: Int) -> String? {
        switch count {
        case ..<1: return nil
        case 1: return "1 agent waiting for an answer"
        default: return "\(count) agents waiting for an answer"
        }
    }

    private static func phaseSentence(_ phase: Phase, state: SessionState, title: String, voiceName: String) -> String {
        switch phase {
        case .connecting:
            return state == .reconnecting ? "\(voiceName) is reconnecting" : "\(voiceName) is connecting"
        case .listening: return "\(voiceName) is listening"
        case .speaking: return "\(voiceName) is speaking"
        case .thinking: return "\(voiceName) is thinking"
        case .muted: return "Microphone muted. \(voiceName) can still talk"
        case .paused:
            switch state {
            case .voiceOff: return "The daemon has no voice session"
            case .noMicrophone: return "No microphone. \(voiceName) cannot hear you"
            case _ where title == "Paused while locked":
                return "Paused while the phone is locked. Open Sheepdog to talk again"
            default: return "iOS paused the audio. Open Sheepdog to resume"
            }
        }
    }

    /// How long an update stays fresh before the system dims it as stale.
    /// The app refreshes well inside it (`refreshInterval`) while the
    /// session runs, so only an activity orphaned by a killed app goes
    /// stale.
    public static let staleAfter: TimeInterval = 15 * 60
    public static let refreshInterval: TimeInterval = 5 * 60
}
