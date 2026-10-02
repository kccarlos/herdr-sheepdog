import Foundation

/// How the voice reaches the daemon: straight to its `/ws` (`VoiceSocket`,
/// the default and the fallback), or through `sheepdog-bridge` over LiveKit
/// (WebRTC). Settings ▸ Voice transport.
public enum VoiceTransportKind: String, CaseIterable, Equatable, Sendable {
    case webSocket = "websocket"
    case liveKit = "livekit"

    public var label: String {
        switch self {
        case .webSocket: return "WebSocket"
        case .liveKit: return "LiveKit"
        }
    }
}

/// What the bridge says about its own `/ws` link to the daemon
/// (`{"type":"bridge","daemon":…}`, the bridge's docs/client-contract.md §4).
public enum BridgeLink: String, Equatable, Sendable {
    case connected
    /// The bridge is (re)connecting to the daemon.
    case disconnected
    /// Another `/ws` client (the browser `/voice` page) has the daemon; the
    /// bridge takes it back after 30 s, or at once when the phone sends
    /// any control message other than `ping`.
    case replaced

    /// Health's words for it.
    public var word: String {
        switch self {
        case .connected: return "Connected"
        case .disconnected: return "Reconnecting…"
        case .replaced: return "In use by the browser"
        }
    }
}

/// Everything a voice transport reports to the view model. `VoiceSocket`
/// calls this `VoiceSocket.Event`; the cases it never produces (`link`)
/// are LiveKit's, and LiveKit never produces `audio` (it plays the
/// bridge's track itself).
public enum VoiceTransportEvent: Sendable {
    case opened
    case control(WSMessage)
    /// PCM16 24 kHz from the daemon (WebSocket only).
    case audio(Data)
    /// The transport went away. `retrying` is false when the app will not
    /// reconnect by itself (the user disconnected, or we were replaced).
    case closed(reason: String, retrying: Bool)
    /// An attempt failed. `retrying` is true when the transport will try
    /// again by itself, false when it will not.
    case failed(DaemonError, retrying: Bool)
    /// The bridge's link to the daemon changed (LiveKit only).
    case link(BridgeLink)
}

/// The seam between `SessionViewModel` and the two transports: open and
/// close, send one control message, and report `VoiceTransportEvent`s.
///
/// The replay-on-open state (`muted`, `gateClosed`, `speaking`) is what the
/// view model wants the daemon to believe after every (re)connection; each
/// transport decides which of it to say (LiveKit never says `speaking`).
@MainActor
public protocol VoiceTransport: AnyObject {
    var onEvent: ((VoiceTransportEvent) -> Void)? { get set }
    var muted: Bool { get set }
    var gateClosed: Bool { get set }
    var speaking: Bool { get set }

    func update(endpoint: DaemonEndpoint)
    func connect()
    func disconnect(reason: String)
    @discardableResult func send(_ message: WSMessage) -> Bool
}
