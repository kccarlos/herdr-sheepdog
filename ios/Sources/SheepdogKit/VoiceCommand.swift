import Foundation

/// What the daemon's `voice-command` asks this client to do
/// (docs/dev/ws-contract.md "Voice commands over HTTP").
public enum VoiceCommandAction: Equatable, Sendable {
    /// The daemon already cut the speech. Drop what is playing.
    case stopPlayback
    /// Hold-to-talk: open the microphone while `on`, close it after.
    case pushToTalk(Bool)
    /// The desktop's mute took effect: make it this client's own intent.
    case mute(Bool)
    /// A command this build does not know, or a malformed one.
    case ignore

    /// Map one control message without changing session state.
    public static func resolve(_ message: WSMessage) -> Self {
        guard message.type == .voiceCommand else { return .ignore }
        switch message.action {
        case .stop?:
            return .stopPlayback
        case .ptt?:
            guard let on = message.on else { return .ignore }
            return .pushToTalk(on)
        case .mute?:
            guard let on = message.on else { return .ignore }
            return .mute(on)
        case nil:
            return .ignore
        }
    }
}
