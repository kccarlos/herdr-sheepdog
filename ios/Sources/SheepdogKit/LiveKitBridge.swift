import Foundation

/// The pure half of the LiveKit transport: the bridge's address, its token
/// endpoint, and the control packets on the data channel. The LiveKit SDK
/// itself is only linked by the app target (`LiveKitTransport`); everything
/// here is testable without it.
///
/// The spec is herdr-sheepdog-bridge's docs/client-contract.md.
public enum LiveKitBridge {
    /// The bridge's HTTP port (`/livekit/token`, `/healthz`).
    public static let defaultPort = 7778
    /// The data topic every control message travels on, both ways.
    public static let controlTopic = "sheepdog.control"
    /// The bridge's participant identity. Its presence in the room is how
    /// the app knows the bridge is up.
    public static let bridgeIdentity = "sheepdog-bridge"
    /// The track the bridge publishes the daemon's voice on.
    public static let voiceTrackName = "sheepdog-voice"
    /// The name the phone publishes its microphone under.
    public static let microphoneTrackName = "microphone"

    /// Where the bridge is: the address typed in Settings ▸ Bridge address
    /// (default port 7778), or, when that is empty, the daemon's host on
    /// port 7778 (the bridge runs next to the daemon). The token is the
    /// daemon's: the bridge accepts it as the bearer for `/livekit/token`.
    public static func endpoint(bridgeAddress: String, daemon: DaemonEndpoint?) -> DaemonEndpoint? {
        let typed = bridgeAddress.trimmingCharacters(in: .whitespacesAndNewlines)
        if !typed.isEmpty {
            return DaemonEndpoint(text: typed, defaultPort: defaultPort, token: daemon?.token ?? "")
        }
        guard let daemon else { return nil }
        var bridge = DaemonEndpoint(host: daemon.host, port: defaultPort, token: daemon.token)
        bridge.secure = daemon.secure
        return bridge
    }

    /// The placeholder Settings shows under an empty Bridge address.
    public static func defaultAddress(daemon: DaemonEndpoint?) -> String {
        guard let daemon else { return "daemon host:\(defaultPort)" }
        return "\(daemon.host):\(defaultPort)"
    }

    /// `GET {bridge}/livekit/token[?identity=…]` with the bearer.
    public static func tokenRequest(bridge: DaemonEndpoint, identity: String?) -> URLRequest? {
        let query = identity.flatMap { isValidIdentity($0) ? [URLQueryItem(name: "identity", value: $0)] : nil } ?? []
        guard let url = bridge.url(path: "/livekit/token", query: query) else { return nil }
        var request = URLRequest(url: url)
        request.timeoutInterval = 10
        if !bridge.token.isEmpty {
            request.setValue("Bearer \(bridge.token)", forHTTPHeaderField: "Authorization")
        }
        return request
    }

    /// `[A-Za-z0-9_.:-]{1,64}`, not starting with the bridge's own identity.
    public static func isValidIdentity(_ identity: String) -> Bool {
        guard (1 ... 64).contains(identity.count), !identity.hasPrefix(bridgeIdentity) else { return false }
        return identity.unicodeScalars.allSatisfy { scalar in
            scalar.isASCII && (CharacterSet.alphanumerics.contains(scalar) || "_.:-".unicodeScalars.contains(scalar))
        }
    }

    /// A stable per-device identity (`phone-` and the first 8 hex digits of
    /// a device id), so a reconnect rejoins as the same participant.
    public static func identity(deviceID: UUID?) -> String? {
        guard let deviceID else { return nil }
        let hex = deviceID.uuidString.replacingOccurrences(of: "-", with: "").lowercased()
        return "phone-\(hex.prefix(8))"
    }

    /// Reads `/livekit/token`'s answer, mapping its failures onto the
    /// words the app already has for the daemon.
    public static func tokenResponse(status: Int, body: Data, bridge: DaemonEndpoint) throws -> BridgeToken {
        switch status {
        case 200:
            do {
                return try DaemonClient.decoder.decode(BridgeToken.self, from: body)
            } catch {
                throw DaemonError.malformed("the bridge's token answer: \(error.localizedDescription)")
            }
        case 401:
            throw DaemonError.unauthorized(endpoint: bridge.display)
        case 404:
            // Something answered, but not a bridge (the daemon's own port?).
            throw DaemonError.http(status: 404, endpoint: bridge.display)
        default:
            let message = (try? JSONDecoder().decode([String: String].self, from: body))?["error"] ?? ""
            throw DaemonError.rejected(status: status, message: message.isEmpty ? "" : "bridge: \(message)")
        }
    }
}

/// `/livekit/token`'s `200` body.
public struct BridgeToken: Codable, Equatable, Sendable {
    /// What to pass to `Room.connect(url:token:)`.
    public var url: String
    public var token: String
    public var room: String
    public var identity: String
    public var expiresAt: Date

    enum CodingKeys: String, CodingKey {
        case url, token, room, identity
        case expiresAt = "expires_at"
    }

    public init(url: String, token: String, room: String, identity: String, expiresAt: Date) {
        self.url = url
        self.token = token
        self.room = room
        self.identity = identity
        self.expiresAt = expiresAt
    }

    /// Still good for a (re)join at `now`, with a minute to spare.
    public func isFresh(at now: Date, margin: TimeInterval = 60) -> Bool {
        expiresAt.timeIntervalSince(now) > margin
    }
}

/// One control packet on `sheepdog.control`: a daemon message forwarded
/// verbatim, or the bridge's own link status.
public enum ControlPacket: Equatable, Sendable {
    case message(WSMessage)
    case bridge(BridgeLink)

    /// Decodes one data packet. The `bridge` type is the bridge's own and
    /// not a `WSMessage` (whose decoder rejects unknown types, as the
    /// daemon's does); everything else is exactly `ws-contract.md`'s JSON.
    public static func decode(_ data: Data) throws -> ControlPacket {
        struct Peek: Decodable {
            var type: String
            var daemon: String?
        }
        let peek = try JSONDecoder().decode(Peek.self, from: data)
        if peek.type == "bridge" {
            guard let raw = peek.daemon, let link = BridgeLink(rawValue: raw) else {
                throw WSMessageError.badValue("bridge", "daemon")
            }
            return .bridge(link)
        }
        return .message(try WSMessage.decode(data))
    }

    /// The bytes the phone sends for `message`, or nil for what must not go
    /// over the bridge: `playback` (the bridge owns playback reports) and
    /// `speaking` (one `speaking` from the app would switch off the
    /// bridge's active-speaker detection for good, and the app has no
    /// speech detector on this path).
    public static func outgoing(_ message: WSMessage) -> Data? {
        switch message.type {
        case .playback, .speaking:
            return nil
        default:
            return try? message.encode()
        }
    }
}
