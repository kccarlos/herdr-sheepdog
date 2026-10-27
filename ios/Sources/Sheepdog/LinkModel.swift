import Foundation
import SheepdogKit

/// What the status surfaces show about the connection to the current
/// computer: the presentation (chip, Settings, Health, the capsule's menu),
/// the Addresses rows, the `/events` stream's life, the `/ws` handshake and
/// `/api/hello`. Split out of `SessionViewModel` (phase 8), so a link change
/// re-renders the views that show it and nothing else.
///
/// These stay mirrors that `SessionViewModel` writes, not derived values:
/// `ComputerModel` keeps its link, hello and stream life `@ObservationIgnored`
/// on purpose (`lastSeen` moves on every request), and the presentation also
/// needs the app's voice and computer-count state. Every setter assigns only
/// when the value changed.
@MainActor
@Observable
final class LinkModel {
    /// What every status surface shows: the chip, Settings, the Agents
    /// page, the capsule's menu, Health.
    private(set) var presentation = LinkPresentation(ComputerLink(addresses: []))
    /// Settings ▸ Connection ▸ Addresses: each address, the preferred one,
    /// and the one in use and why.
    private(set) var addressRows: [AddressRow] = []
    /// The `/events` stream's own life, for Health ▸ Live updates (the
    /// chip does not follow it, decision 5).
    private(set) var liveUpdates: SocketLife = .idle
    /// The `/ws` protocol handshake (`ProtocolHandshake`), for Health and
    /// the chip's sheet.
    private(set) var handshake: ProtocolHandshake.State = .idle
    /// `/api/hello` of the current computer; nil until fetched.
    private(set) var hello: Hello? {
        didSet { onHelloChange?() }
    }

    /// Four sources feed the session's resolved voice provider; each tells
    /// the audio engine's jitter-buffer profile explicitly when it changes.
    @ObservationIgnored var onHelloChange: (() -> Void)?

    func setPresentation(_ next: LinkPresentation) {
        if presentation != next { presentation = next }
    }

    func setAddressRows(_ next: [AddressRow]) {
        if addressRows != next { addressRows = next }
    }

    func setLiveUpdates(_ next: SocketLife) {
        if liveUpdates != next { liveUpdates = next }
    }

    func setHandshake(_ next: ProtocolHandshake.State) {
        if handshake != next { handshake = next }
    }

    func setHello(_ next: Hello?) {
        if hello != next { hello = next }
    }

    /// The `/ws` protocol for Health: the handshake's word once a socket
    /// opened, otherwise what `/api/hello` says (nothing there: an older
    /// daemon, protocol 1).
    var protocolDisplay: String {
        if handshake != .idle { return handshake.summary }
        guard let hello else { return "—" }
        guard let p = hello.protocol else { return "1 (older daemon)" }
        return "\(p) (serves \(hello.minProtocol ?? p) and up)"
    }
}
