import Foundation
import SheepdogKit

/// Where the daemon is. v1 is this Mac's own loopback daemon on the default
/// port; `defaults write … sheepdogDaemonPort -int N` (or a host) moves it,
/// for a daemon on another port. Several computers are v2
/// (docs/dev/design/desktop-client.md section 5.2).
public protocol DaemonLocating {
    var endpoint: DaemonEndpoint { get }
}

public struct DaemonLocator: DaemonLocating {
    public static let hostKey = "sheepdogDaemonHost"
    public static let portKey = "sheepdogDaemonPort"
    public static let defaultHost = "127.0.0.1"
    public static let defaultPort = 7777

    private let defaults: UserDefaults

    public init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    public var endpoint: DaemonEndpoint {
        let host = defaults.string(forKey: Self.hostKey).flatMap { $0.isEmpty ? nil : $0 } ?? Self.defaultHost
        let port = defaults.integer(forKey: Self.portKey)
        return DaemonEndpoint(host: host, port: (1 ... 65535).contains(port) ? port : Self.defaultPort)
    }
}
