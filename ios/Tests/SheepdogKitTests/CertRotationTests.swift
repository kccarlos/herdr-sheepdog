import Foundation
import Security
import XCTest
@testable import SheepdogKit

/// The app's half of the certificate rotation (docs/dev/design/security.md
/// "Certificate rotation"): a handshake that matches the next pin switches
/// to it and keeps the ticket, sign-in signs with the pin the server's key
/// matched (once more with the other one on bad_signature), and a key that
/// matches no pin says "renewed while away" only once the computer may
/// have switched.
final class CertRotationTests: XCTestCase {
    private let expires = ISO8601DateFormatter().date(from: "2027-10-21T00:00:00Z")!

    private func paired(_ creds: DeviceCredentials, pin: String = "old", next: String? = "new") {
        creds.computer = PairedComputer(daemonID: "d", name: "Studio", addresses: ["192.168.1.20:7777"], pin: pin, nextPin: next,
                                        deviceID: "dev", pairedAt: Date(), certExpires: expires)
    }

    func testTheNextPinTakesOverAndTheTicketStays() {
        let secrets = InMemorySecretStore()
        let creds = DeviceCredentials(daemonID: "d", secrets: secrets)
        paired(creds)
        creds.adopt(SessionTicket(ticket: "t", expires: Date().addingTimeInterval(3600)))
        XCTAssertEqual(creds.pins, ["old", "new"])

        creds.served(pin: "old") // before the switch: nothing changes
        XCTAssertEqual(creds.computer?.pin, "old")
        XCTAssertEqual(creds.computer?.nextPin, "new")

        creds.served(pin: "new")
        XCTAssertEqual(creds.computer?.pin, "new")
        XCTAssertNil(creds.computer?.nextPin)
        XCTAssertNil(creds.computer?.certExpires, "the new certificate's expiry comes with the next sign-in")
        XCTAssertEqual(creds.pins, ["new"])
        XCTAssertEqual(creds.header(), "Bearer sds_t", "the switch keeps the ticket")
        XCTAssertEqual(DeviceCredentials(daemonID: "d", secrets: secrets).computer?.pin, "new", "saved")

        creds.served(pin: "someone-else") // never trusted, never adopted
        XCTAssertEqual(creds.computer?.pin, "new")
    }

    func testAMismatchIsRenewedWhileAwayOnlyAfterTheSwitchDate() {
        let lead = PairingProblem.certificateSwitchLead
        XCTAssertEqual(PairingProblem.forPinMismatch(certExpires: nil, now: expires), .pinMismatch)
        XCTAssertEqual(PairingProblem.forPinMismatch(certExpires: expires, now: expires.addingTimeInterval(-30 * 86400)), .pinMismatch)
        XCTAssertEqual(PairingProblem.forPinMismatch(certExpires: expires, now: expires.addingTimeInterval(-lead - 1)), .pinMismatch)
        XCTAssertEqual(PairingProblem.forPinMismatch(certExpires: expires, now: expires.addingTimeInterval(-lead)), .renewedWhileAway)
        XCTAssertEqual(PairingProblem.forPinMismatch(certExpires: expires, now: expires.addingTimeInterval(86400)), .renewedWhileAway)
        XCTAssertEqual(lead, 7 * 86400)
    }

    func testOnlyAMatchingPinIsReported() {
        XCTAssertEqual(PinnedTrustDelegate.matchedPin(leaf: "new", pins: ["old", "new"]), "new")
        XCTAssertNil(PinnedTrustDelegate.matchedPin(leaf: "other", pins: ["old", "new"]))
        XCTAssertNil(PinnedTrustDelegate.matchedPin(leaf: nil, pins: ["old"]))
        XCTAssertNil(PinnedTrustDelegate.matchedPin(leaf: "", pins: [""]))
    }

    // MARK: - Signing in across the switch

    private var keyID = ""

    override func setUp() {
        super.setUp()
        keyID = "rotation-\(UUID().uuidString)"
        RotationStub.reset()
    }

    override func tearDown() {
        DeviceKey.delete(daemonID: keyID)
        RotationStub.reset()
        super.tearDown()
    }

    private func auth() -> DeviceAuth {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [RotationStub.self]
        return DeviceAuth(base: URL(string: "https://192.168.1.20:7777/")!, session: URLSession(configuration: config))
    }

    private func key() throws -> SecKey {
        let key = try DeviceKey.loadOrCreate(daemonID: keyID)
        RotationStub.publicKey = SecKeyCopyPublicKey(key)
        return key
    }

    /// After the switch a phone that still lists the old pin first signs
    /// once more with the new one and gets a ticket with the new
    /// certificate's expiry.
    func testSignInRetriesOnceWithTheOtherTrustedPin() async throws {
        let key = try key()
        RotationStub.accepted = "new"
        let ticket = try await auth().signIn(daemonID: "d", pins: { ["old", "new"] }, deviceID: "dev", key: key)
        XCTAssertEqual(ticket.ticket, "tk")
        XCTAssertEqual(ticket.certExpires, expires)
        XCTAssertEqual(RotationStub.signedPins, ["old", "new"])
    }

    /// The pins are read after the challenge's handshake, so a switch seen
    /// there signs with the new pin at once.
    func testSignInReadsThePinsAfterTheChallenge() async throws {
        let key = try key()
        RotationStub.accepted = "new"
        let ticket = try await auth().signIn(daemonID: "d", pins: { RotationStub.challenges > 0 ? ["new"] : ["old"] }, deviceID: "dev", key: key)
        XCTAssertEqual(ticket.ticket, "tk")
        XCTAssertEqual(RotationStub.signedPins, ["new"])
    }

    /// No second try when no other pin is trusted, and never on a refusal
    /// that needs a new scan.
    func testSignInGivesUpWithoutAnotherPin() async throws {
        let key = try key()
        RotationStub.accepted = "new"
        do {
            _ = try await auth().signIn(daemonID: "d", pin: "old", deviceID: "dev", key: key)
            XCTFail("signed in with the retired pin")
        } catch let DaemonError.rejected(status, _) {
            XCTAssertEqual(status, 401)
        }
        XCTAssertEqual(RotationStub.signedPins, ["old"])

        RotationStub.reset()
        RotationStub.publicKey = SecKeyCopyPublicKey(key)
        RotationStub.reason = "device_revoked"
        do {
            _ = try await auth().signIn(daemonID: "d", pins: { ["old", "new"] }, deviceID: "dev", key: key)
            XCTFail("a revoked device signed in")
        } catch let problem as PairingProblem {
            XCTAssertEqual(problem, .revoked)
        }
        XCTAssertEqual(RotationStub.signedPins.count, 1)
    }
}

/// The daemon's sign-in routes in a `URLProtocol`: a session signature
/// over `accepted` gets a ticket, any other one `401 bad_signature` (or
/// `reason`, when set).
final class RotationStub: URLProtocol {
    private static let lock = NSLock()
    nonisolated(unsafe) private static var state = (accepted: "", reason: "", challenges: 0, signedPins: [String](), publicKey: SecKey?.none)

    static var accepted: String {
        get { lock.withLock { state.accepted } }
        set { lock.withLock { state.accepted = newValue } }
    }
    static var reason: String {
        get { lock.withLock { state.reason } }
        set { lock.withLock { state.reason = newValue } }
    }
    static var publicKey: SecKey? {
        get { lock.withLock { state.publicKey } }
        set { lock.withLock { state.publicKey = newValue } }
    }
    static var challenges: Int { lock.withLock { state.challenges } }
    static var signedPins: [String] { lock.withLock { state.signedPins } }

    static func reset() {
        lock.withLock { state = (accepted: "", reason: "", challenges: 0, signedPins: [], publicKey: nil) }
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func stopLoading() {}

    override func startLoading() {
        let path = request.url?.path ?? ""
        if path == "/api/auth/challenge" {
            let n = Self.lock.withLock { () -> Int in
                Self.state.challenges += 1
                return Self.state.challenges
            }
            answer(200, ["nonce": "nonce-\(n)", "daemon_id": "d", "expires_in": 60])
            return
        }
        guard path == "/api/auth/session",
              let body = Self.body(request),
              let obj = try? JSONSerialization.jsonObject(with: body) as? [String: Any],
              let nonce = obj["nonce"] as? String, let time = obj["time"] as? Int64,
              let sig = (obj["signature"] as? String).flatMap({ Data(base64Encoded: $0) })
        else {
            answer(400, ["error": "bad request"])
            return
        }
        let pin = ["old", "new", "someone-else"].first { candidate in
            guard let pub = Self.publicKey else { return false }
            let text = SignedText.session(daemonID: "d", pin: candidate, deviceID: "dev", nonce: nonce, unix: time)
            return SecKeyVerifySignature(pub, .ecdsaSignatureMessageX962SHA256, Data(text.utf8) as CFData, sig as CFData, nil)
        } ?? "?"
        Self.lock.withLock { Self.state.signedPins.append(pin) }
        if !Self.reason.isEmpty {
            answer(401, ["error": "unauthorized", "reason": Self.reason])
        } else if pin == Self.accepted {
            answer(200, ["ticket": "tk", "expires_in": 43200, "device": ["id": "dev", "name": "Phone"], "cert_expires": "2027-10-21T00:00:00Z"])
        } else {
            answer(401, ["error": "unauthorized", "reason": "bad_signature"])
        }
    }

    private func answer(_ status: Int, _ object: [String: Any]) {
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: (try? JSONSerialization.data(withJSONObject: object)) ?? Data())
        client?.urlProtocolDidFinishLoading(self)
    }

    private static func body(_ request: URLRequest) -> Data? {
        if let body = request.httpBody { return body }
        guard let stream = request.httpBodyStream else { return nil }
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 4096)
        while stream.hasBytesAvailable {
            let n = stream.read(&buffer, maxLength: buffer.count)
            if n <= 0 { break }
            data.append(buffer, count: n)
        }
        return data
    }
}
