import Foundation

/// How the microphone reaches the dog.
public enum TalkMode: String, Equatable, Sendable, CaseIterable {
    /// Always listening; the detector decides what is speech. Mute is the
    /// only mic control.
    case openMic
    /// Hold the orb to talk; nothing is sent otherwise. Mute is hidden —
    /// not holding *is* muted.
    case holdToTalk
}

/// The pure mapping from `SessionState` to what the voice bar's three
/// controls do: the orb's action, whether Mute can be used, and whether
/// End (✕) shows at all — plus the words around them (the status line and
/// the mic button's label), which are where "press to talk" confusion
/// starts. The views own the look (icon, colour, animation); this is what
/// each control *does* and *says*.
public struct VoiceBarModel: Equatable, Sendable {
    /// What the orb does. `.none` means the orb is not a button in this
    /// state (it still shows the session's look).
    public enum OrbAction: Equatable, Sendable {
        case connect
        case stop
        /// Resume audio iOS paused and would not resume by itself.
        case resume
        /// Hold-to-talk: press and hold opens the gate, release closes it.
        /// Pressing while the dog talks also stops it (barge-in).
        case holdToTalk
        case none
    }

    public enum MuteAvailability: Equatable, Sendable {
        case enabled
        case disabled
        case hidden
    }

    public var orbAction: OrbAction
    public var muteAvailability: MuteAvailability
    public var endVisible: Bool

    public init(state: SessionState, talkMode: TalkMode = .openMic) {
        switch state {
        case .disconnected:
            orbAction = .connect
            muteAvailability = .hidden
            endVisible = false
        case .connecting, .reconnecting, .voiceOff:
            orbAction = .none
            muteAvailability = .disabled
            endVisible = true
        case .audioPaused:
            orbAction = .resume
            muteAvailability = .enabled
            endVisible = true
        case .idle, .listening, .silent, .muted:
            orbAction = .none
            muteAvailability = .enabled
            endVisible = true
        case .thinking, .stalled, .speaking:
            orbAction = .stop
            muteAvailability = .enabled
            endVisible = true
        }
        if talkMode == .holdToTalk {
            if state != .disconnected { muteAvailability = .hidden }
            switch state {
            case .idle, .listening, .silent, .muted, .thinking, .stalled, .speaking:
                orbAction = .holdToTalk
            case .disconnected, .connecting, .reconnecting, .voiceOff, .audioPaused:
                break
            }
        }
    }

    // MARK: - Words

    /// The line above the bar. Says what to *do*, not only what is
    /// happening: the mic button used to read as "press to talk", and
    /// people muted themselves to speak.
    public static func statusLine(state: SessionState, talkMode: TalkMode = .openMic, holding: Bool = false, noSignal: Bool = false) -> String {
        let live: Set<SessionState> = [.listening, .silent, .idle]
        if noSignal, live.contains(state) {
            return "No microphone signal — check the mic"
        }
        switch talkMode {
        case .openMic:
            switch state {
            case .listening, .silent: return "Listening — just talk"
            case .muted: return "Muted — tap to unmute"
            default: return state.title
            }
        case .holdToTalk:
            switch state {
            case .listening, .silent, .idle, .muted:
                return holding ? "Talking — release to send" : "Hold the orb to talk"
            case .thinking, .stalled, .speaking:
                return holding ? "Talking — release to send" : state.title
            case .disconnected, .connecting, .reconnecting, .voiceOff, .audioPaused:
                return state.title
            }
        }
    }

    /// The mic button's title (also its VoiceOver label): always the action
    /// it performs, never a bare "Mic".
    public static func muteLabel(muted: Bool) -> String {
        muted ? "Unmute microphone" : "Mute microphone"
    }

    /// The first-run coach mark, shown the first time the dog is listening.
    public static func coachMark(voiceName: String) -> String {
        "Just talk — \(voiceName) is listening. Tap the mic only to mute."
    }

    /// Whether the microphone is open and the dog can hear (the coach mark
    /// waits for this).
    public static func isListening(_ state: SessionState) -> Bool {
        state == .listening || state == .silent
    }
}
