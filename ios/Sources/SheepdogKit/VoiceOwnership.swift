import Foundation

/// Who holds the daemon's one `/ws` voice session, as the voice capsule
/// shows it. The daemon names the holder in `/api/state voice.client`.
public struct VoiceHolderPresentation: Equatable, Sendable {
    /// Another client holds `/ws`, so this app's capsule offers "Use here".
    public var isElsewhere: Bool
    /// "Voice on Mac", or nil when this app holds (or nobody holds) it.
    public var status: String?

    public var showsUseHere: Bool { isElsewhere }

    /// - Parameters:
    ///   - client: `/api/state`'s `voice.client`.
    ///   - localPlatform: the platform this build reports on `/ws`.
    ///   - localVoiceActive: this app has asked for the voice and has not
    ///     been ended or replaced.
    public init(client: DaemonState.VoiceInfo.Client?, localPlatform: String = "ios", localVoiceActive: Bool) {
        guard !localVoiceActive, let client, client.platform.lowercased() != localPlatform.lowercased() else {
            isElsewhere = false
            status = nil
            return
        }
        isElsewhere = true
        switch client.platform.lowercased() {
        case "macos", "mac":
            status = "Voice on Mac"
        case "ios":
            status = "Voice on iPhone"
        case "ipados":
            status = "Voice on iPad"
        case "windows":
            status = "Voice on Windows"
        case "android":
            status = "Voice on Android"
        case "web", "browser":
            status = "Voice in the browser"
        default:
            status = "Voice on another device"
        }
    }
}
