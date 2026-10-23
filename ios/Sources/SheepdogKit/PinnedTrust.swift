import CryptoKit
import Foundation
import Security

/// The daemon's TLS pin (docs/dev/design/security.md "The app checks the
/// pin"): base64url, without padding, of SHA-256 over the certificate key's
/// SubjectPublicKeyInfo. The app checks the key by this pin, never by a
/// host name or an address, so any path to the computer works.
public enum SPKIPin {
    /// The DER prefix of a P-256 SubjectPublicKeyInfo, before the 65-byte
    /// uncompressed point the Security framework exports.
    static let p256Header: [UInt8] = [
        0x30, 0x59, 0x30, 0x13, 0x06, 0x07, 0x2A, 0x86, 0x48, 0xCE, 0x3D, 0x02, 0x01,
        0x06, 0x08, 0x2A, 0x86, 0x48, 0xCE, 0x3D, 0x03, 0x01, 0x07, 0x03, 0x42, 0x00,
    ]

    /// The pin of a P-256 public key given as its 65-byte X9.63 point.
    public static func pin(rawP256 raw: Data) -> String? {
        guard raw.count == 65, raw.first == 0x04 else { return nil }
        return base64url(Data(SHA256.hash(data: Data(p256Header) + raw)))
    }

    /// The pin of a public key (P-256 only: the daemon makes no other).
    public static func pin(of key: SecKey) -> String? {
        guard let raw = SecKeyCopyExternalRepresentation(key, nil) as Data? else { return nil }
        return pin(rawP256: raw)
    }

    /// The pin of a server trust's leaf certificate.
    public static func pin(of trust: SecTrust) -> String? {
        guard let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate],
              let leaf = chain.first,
              let key = SecCertificateCopyKey(leaf)
        else { return nil }
        return pin(of: key)
    }

    public static func base64url(_ data: Data) -> String {
        data.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}

/// The pins of whichever computer an endpoint names, read at every
/// handshake: a client or socket repointed to another computer
/// (`update(endpoint:)`) checks that computer's key, and a mismatch here
/// never touches another computer's state.
public final class EndpointTrust: @unchecked Sendable {
    private final class Box: @unchecked Sendable {
        let lock = NSLock()
        var credentials: DeviceCredentials?
        func get() -> DeviceCredentials? { lock.lock(); defer { lock.unlock() }; return credentials }
    }

    private let box: Box
    public let delegate: PinnedTrustDelegate

    public init(_ credentials: DeviceCredentials?) {
        let box = Box()
        box.credentials = credentials
        self.box = box
        delegate = PinnedTrustDelegate(pins: { box.get()?.pins ?? [] }, onMatch: { box.get()?.served(pin: $0) })
    }

    public func follow(_ credentials: DeviceCredentials?) {
        box.lock.lock()
        box.credentials = credentials
        box.lock.unlock()
    }

    /// A session with this trust.
    public func session(configuration: URLSessionConfiguration) -> URLSession {
        URLSession(configuration: configuration, delegate: delegate, delegateQueue: nil)
    }
}

/// A `URLSession` delegate that accepts a server only when its key matches
/// one of the pins (the current one, and the next one during a certificate
/// renewal). Plain HTTP to loopback (the fixture daemons) never reaches it.
/// It serves `URLSessionWebSocketTask` too, so `/ws` and `/term` are pinned.
///
/// Each accepted handshake reports the pin it matched (`onMatch`), so the
/// app notices the computer switched to its next key
/// (`DeviceCredentials.served(pin:)`) before it signs in with a pin.
///
/// It answers the challenge at the task level too: `URLSession.bytes(for:)`
/// (the `/events` stream) asks only `urlSession(_:task:didReceive:…)`.
/// With the session-level method alone the system's default trust judged
/// the self-signed certificate (-1202), `/events` never opened over TLS,
/// and the chip said "Reconnecting…" for good while `/ws` worked.
public final class PinnedTrustDelegate: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    private let pins: @Sendable () -> [String]
    private let onMatch: (@Sendable (String) -> Void)?
    private let lock = NSLock()
    private var mismatched = false

    public init(pins: @escaping @Sendable () -> [String], onMatch: (@Sendable (String) -> Void)? = nil) {
        self.pins = pins
        self.onMatch = onMatch
    }

    /// True when the last server offered a key that matches no pin: the app
    /// shows "Scan the pairing code again" instead of retrying.
    public var sawMismatch: Bool {
        lock.lock()
        defer { lock.unlock() }
        return mismatched
    }

    public func urlSession(
        _ session: URLSession,
        didReceive challenge: URLAuthenticationChallenge,
        completionHandler: @escaping @Sendable (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
    ) {
        answer(challenge, completionHandler)
    }

    public func urlSession(
        _ session: URLSession,
        task: URLSessionTask,
        didReceive challenge: URLAuthenticationChallenge,
        completionHandler: @escaping @Sendable (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
    ) {
        answer(challenge, completionHandler)
    }

    private func answer(
        _ challenge: URLAuthenticationChallenge,
        _ completionHandler: @escaping @Sendable (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
    ) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust
        else {
            completionHandler(.performDefaultHandling, nil)
            return
        }
        if let matched = Self.matchedPin(trust: trust, pins: pins()) {
            lock.lock()
            mismatched = false
            lock.unlock()
            onMatch?(matched)
            completionHandler(.useCredential, URLCredential(trust: trust))
        } else {
            lock.lock()
            mismatched = true
            lock.unlock()
            completionHandler(.cancelAuthenticationChallenge, nil)
        }
    }

    /// Whether the trust's leaf key matches one of the pins.
    public static func accepts(trust: SecTrust, pins: [String]) -> Bool {
        matchedPin(trust: trust, pins: pins) != nil
    }

    /// The pin the trust's leaf key matches, nil for none.
    public static func matchedPin(trust: SecTrust, pins: [String]) -> String? {
        matchedPin(leaf: SPKIPin.pin(of: trust), pins: pins)
    }

    /// The pin that equals the leaf key's pin, nil for none (or no key).
    public static func matchedPin(leaf: String?, pins: [String]) -> String? {
        guard let leaf, !leaf.isEmpty else { return nil }
        return pins.first { $0 == leaf }
    }
}
