import Foundation

/// The pure mapping from `SessionState` to what the voice bar's three
/// controls do: the orb's tap action, whether Mute can be used, and whether
/// End (✕) shows at all. The views own the look (icon, colour, animation);
/// this is only what each control *does*.
public struct VoiceBarModel: Equatable, Sendable {
    /// What tapping the orb does. `.none` means the orb is not a button in
    /// this state (it still shows the session's look).
    public enum OrbAction: Equatable, Sendable {
        case connect
        case stop
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

    public init(state: SessionState) {
        switch state {
        case .disconnected:
            orbAction = .connect
            muteAvailability = .hidden
            endVisible = false
        case .connecting, .reconnecting, .voiceOff:
            orbAction = .none
            muteAvailability = .disabled
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
    }
}
