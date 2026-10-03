import Foundation

/// The `/ws` protocol handshake, client side (the daemon's
/// docs/ws-contract.md "Protocol version and handshake").
///
/// The app and the daemon ship separately. On every open the socket sends
/// `client-ready` first; the daemon answers `bot-ready` with the protocol it
/// speaks and its features. Nothing waits for that answer — audio and the
/// mute state go out right behind `client-ready` — so an older daemon that
/// never answers costs nothing:
///
/// * an older daemon answers `client-ready` with
///   `error "wsproto: unknown type \"client-ready\""`, which is swallowed
///   (it is not a problem to show) and means protocol 1, as does
///   `ProtocolHandshake.timeout` without a `bot-ready`;
/// * a daemon that refuses this app as too old sends its words as an
///   `error`, then `status closed` with reason `protocol_too_old`, and
///   closes — the socket must not reconnect in a loop;
/// * a `bot-ready` below `minDaemonProtocol` means the daemon is too old
///   for this app: close, and say so.
///
/// Pure, so `ProtocolHandshakeTests` walks every transition.
public struct ProtocolHandshake: Equatable, Sendable {
    /// The `/ws` protocol this app speaks.
    public static let protocolVersion = 1
    /// The oldest daemon protocol this app works with.
    public static let minDaemonProtocol = 1
    /// A daemon that has not sent `bot-ready` by then is protocol 1.
    public static let timeout: TimeInterval = 2
    /// The `status` reason a daemon refuses a too-old client with.
    public static let reasonTooOld = "protocol_too_old"
    /// The reason `VoiceSocket` reports when it closes over a mismatch.
    public static let closeReason = "protocol"

    public enum State: Equatable, Sendable {
        /// No connection.
        case idle
        /// `client-ready` sent, nothing back yet.
        case waiting
        /// `bot-ready` arrived.
        case ready(protocol: Int, daemonVersion: String, features: [String])
        /// An older daemon without the handshake: protocol 1.
        case legacy
        /// The daemon refused this app; the words are the daemon's, or ours.
        case appTooOld(String)
        /// The daemon speaks a protocol older than this app supports.
        case daemonTooOld(protocol: Int)
    }

    /// What the socket does with a message after the handshake saw it.
    public enum Action: Equatable, Sendable {
        /// Hand it on as usual.
        case forward
        /// Handled here; the session never sees it.
        case swallow
        /// Close, do not reconnect, and show `mismatchMessage`.
        case close
    }

    public private(set) var state: State = .idle
    /// The last `error` text while waiting: a refusal's own words.
    private var lastError: String?

    public init() {}

    /// A new connection: `client-ready` is going out.
    public mutating func start() {
        state = .waiting
        lastError = nil
    }

    /// The connection is gone. A mismatch is kept, so the chip and Health
    /// can keep saying what to update.
    public mutating func reset() {
        if !isMismatch { state = .idle }
        lastError = nil
    }

    /// The timer ran out without a `bot-ready`.
    public mutating func timedOut() {
        if state == .waiting { state = .legacy }
    }

    /// Folds one control message in.
    public mutating func receive(_ message: WSMessage) -> Action {
        switch message.type {
        case .botReady:
            let p = message.protocol ?? 1
            guard p >= ProtocolHandshake.minDaemonProtocol else {
                state = .daemonTooOld(protocol: p)
                return .close
            }
            state = .ready(
                protocol: min(p, ProtocolHandshake.protocolVersion),
                daemonVersion: message.daemon?.version ?? "",
                features: message.features ?? []
            )
            return .swallow
        case .error:
            guard state == .waiting, let text = message.message else { return .forward }
            if text.contains("client-ready") {
                // An older daemon that does not know the handshake.
                state = .legacy
                return .swallow
            }
            lastError = text
            return .forward
        case .status:
            guard message.voice == .closed, message.reason == ProtocolHandshake.reasonTooOld else { return .forward }
            state = .appTooOld(lastError ?? "This app is older than the daemon's /ws protocol. Update the app.")
            return .close
        default:
            return .forward
        }
    }

    public var isMismatch: Bool { state.isMismatch }
}

extension ProtocolHandshake.State {
    /// The protocol in use, once known.
    public var negotiatedProtocol: Int? {
        switch self {
        case let .ready(p, _, _): return p
        case .legacy: return 1
        default: return nil
        }
    }

    /// The daemon's features from `bot-ready` (empty for an older daemon).
    public var features: [String] {
        if case let .ready(_, _, f) = self { return f }
        return []
    }

    public var isMismatch: Bool {
        switch self {
        case .appTooOld, .daemonTooOld: return true
        default: return false
        }
    }

    /// What to tell the shepherd about a mismatch, or nil.
    public var mismatchMessage: String? {
        switch self {
        case let .appTooOld(text): return text
        case let .daemonTooOld(p):
            return "The daemon speaks /ws protocol \(p); this app needs \(ProtocolHandshake.minDaemonProtocol) or newer. Update the daemon."
        default: return nil
        }
    }

    /// One line for Health: "1 (daemon 0.4.2)", "1 (older daemon)".
    public var summary: String {
        switch self {
        case .idle: return "—"
        case .waiting: return "Waiting for the daemon"
        case let .ready(p, version, _): return version.isEmpty ? "\(p)" : "\(p) (daemon \(version))"
        case .legacy: return "1 (older daemon, no handshake)"
        case .appTooOld: return "Update the app"
        case .daemonTooOld: return "Update the daemon"
        }
    }
}
