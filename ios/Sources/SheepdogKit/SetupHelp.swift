import Foundation

/// The words and links a first-time user needs when the app cannot reach
/// their daemon. The app has no server and no account: everything it needs
/// is a sheepdog daemon on the user's own computer, set up with
/// `sheepdog setup`. The links point at the daemon repository's user docs.
public enum SetupHelp {
    /// Where everything starts: the daemon, installed from the herdr plugin
    /// marketplace.
    public static let projectURL = URL(string: "https://github.com/kccarlos/herdr-sheepdog")!
    /// The iPhone app's page in the daemon's user docs (install, pair).
    public static let appGuideURL = URL(string: "https://github.com/kccarlos/herdr-sheepdog/blob/main/docs/iphone-app.md")!
    /// Same Wi-Fi, Tailscale, the token: what to check when the phone
    /// cannot reach the computer.
    public static let remoteAccessURL = URL(string: "https://github.com/kccarlos/herdr-sheepdog/blob/main/docs/remote-access.md")!

    /// What the phone needs from the computer, in one sentence.
    public static let requirement = "Sheepdog needs the sheepdog daemon running on your computer. Install it from the herdr plugin marketplace and run `sheepdog setup`: its last step shows a pairing code to scan."

    /// The plain fix for a daemon the phone cannot reach.
    public static let unreachableFix = "Check that sheepdog is running on your computer (`sheepdog status`) and that this phone is on the same network as your computer (the same Wi-Fi, or a VPN such as Tailscale)."
    /// The plain fix for a token the daemon turned down.
    public static let tokenFix = "The token changed or was mistyped. Run `sheepdog pair` on your computer and scan the code again."
    /// Something answered, but not sheepdog.
    public static let notSheepdogFix = "Check the address: scan the code from `sheepdog pair` instead of typing it."

    /// The page that explains the fix for this error, if one does.
    public static func helpURL(for error: DaemonError) -> URL? {
        switch error {
        case .unreachable, .unauthorized, .notSheepdog: return remoteAccessURL
        case .http, .malformed, .pendingGone, .rejected: return nil
        }
    }
}
