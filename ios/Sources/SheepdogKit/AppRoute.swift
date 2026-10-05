import Foundation

/// Everything that can reach the app from outside its own screen — a
/// `sheepdog://` link, the "Talk to Sheepdog" App Intent (Siri, Shortcuts,
/// the Action Button, the Control Center control), the Live Activity's
/// Mute and End buttons, and the earbud remote — is one of these. The
/// intents and the remote speak in `sheepdog://` URLs too, so there is
/// one parser and one router.
///
/// - `sheepdog://talk` — open the app and start the voice session
/// - `sheepdog://agent/<pane id>` — open that agent's Flock card
/// - `sheepdog://mute?on=true|false`, `sheepdog://mute` (toggle)
/// - `sheepdog://stop` — the Stop button: silence the dog now
/// - `sheepdog://end` — end the voice session
public enum AppRoute: Equatable, Sendable {
    case talk
    case agent(String)
    case mute(Bool)
    case toggleMute
    case stop
    case end

    public static let scheme = "sheepdog"

    public init?(url: URL) {
        guard url.scheme?.lowercased() == Self.scheme else { return nil }
        // `sheepdog://agent/w1:p2`: the host is the verb and the path the
        // argument. `sheepdog:talk` (no slashes) is accepted as well.
        let components = URLComponents(url: url, resolvingAgainstBaseURL: false)
        var verb = (url.host ?? "").lowercased()
        var path = components?.percentEncodedPath ?? ""
        if verb.isEmpty {
            let parts = path.split(separator: "/", maxSplits: 1, omittingEmptySubsequences: true)
            verb = parts.first.map { String($0).lowercased() } ?? ""
            path = parts.count > 1 ? "/" + parts[1] : ""
        }
        let argument = String(path.drop { $0 == "/" }).removingPercentEncoding ?? ""
        switch verb {
        case "talk":
            self = .talk
        case "agent":
            let id = argument.trimmingCharacters(in: .whitespaces)
            guard !id.isEmpty else { return nil }
            self = .agent(id)
        case "mute":
            let on = components?.queryItems?.first { $0.name == "on" }?.value?.lowercased()
            switch on {
            case nil: self = .toggleMute
            case "true", "1", "yes": self = .mute(true)
            case "false", "0", "no": self = .mute(false)
            default: return nil
            }
        case "stop":
            self = .stop
        case "end":
            self = .end
        default:
            return nil
        }
    }

    public var url: URL {
        var c = URLComponents()
        c.scheme = Self.scheme
        switch self {
        case .talk: c.host = "talk"
        case let .agent(id):
            c.host = "agent"
            c.path = "/" + id
        case let .mute(on):
            c.host = "mute"
            c.queryItems = [URLQueryItem(name: "on", value: on ? "true" : "false")]
        case .toggleMute: c.host = "mute"
        case .stop: c.host = "stop"
        case .end: c.host = "end"
        }
        return c.url!
    }

    /// What the app does about a route, given the session as it is. A
    /// route that does not apply (Mute with no session, Talk while already
    /// talking unmuted) does nothing, so a double press or a stale button
    /// on the lock screen is harmless.
    public enum Action: Equatable, Sendable {
        case showConversation
        case connect
        case toggleMute
        case stop
        case disconnect
        case openAgent(String)
    }

    public func actions(wantsConnection: Bool, muted: Bool) -> [Action] {
        switch self {
        case .talk:
            // "Talk to Sheepdog" means the shepherd wants to be heard: start
            // the session, or unmute the one that runs.
            if !wantsConnection { return [.showConversation, .connect] }
            return muted ? [.showConversation, .toggleMute] : [.showConversation]
        case let .agent(id):
            return [.openAgent(id)]
        case let .mute(on):
            return wantsConnection && muted != on ? [.toggleMute] : []
        case .toggleMute:
            return wantsConnection ? [.toggleMute] : []
        case .stop:
            return wantsConnection ? [.stop] : []
        case .end:
            return wantsConnection ? [.disconnect] : []
        }
    }
}

/// The earbud / headphone remote (`MPRemoteCommandCenter`), as routes.
/// Play/pause is the microphone: AirPods send `pause` while Now Playing
/// says the session is "playing" (unmuted) and `play` while it says
/// paused (muted), and plain earbuds send `togglePlayPause`. Next track
/// is Stop.
public enum RemoteCommand: Equatable, Sendable {
    case play, pause, togglePlayPause, nextTrack

    public var route: AppRoute {
        switch self {
        case .play: return .mute(false)
        case .pause: return .mute(true)
        case .togglePlayPause: return .toggleMute
        case .nextTrack: return .stop
        }
    }

    /// Now Playing's playback rate for a mute state: the remote's next
    /// play/pause press is then the one that flips it.
    public static func playbackRate(muted: Bool) -> Double { muted ? 0 : 1 }
}
