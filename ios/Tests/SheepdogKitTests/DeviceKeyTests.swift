import CryptoKit
import Security
import XCTest
@testable import SheepdogKit

final class DeviceKeyTests: XCTestCase {
    /// The daemon's pairing link (internal/pair Link): the raw fragment,
    /// "+" and "%26" decoded, addresses in order without duplicates.
    func testPairingCodeReadsTheDaemonsLink() throws {
        let link = "https://100.101.102.103:7777/voice#pair=1&d=AAAAAAAAAAAAAAAAAAAAAA&e=c0de&k=pin_-x&n=Studio+Mac+%26+co&u=100.101.102.103%3A7777&u=192.168.1.20%3A7777&u=192.168.1.20%3A7777"
        let code = try XCTUnwrap(PairingCode(link))
        XCTAssertEqual(code.daemonID, "AAAAAAAAAAAAAAAAAAAAAA")
        XCTAssertEqual(code.name, "Studio Mac & co")
        XCTAssertEqual(code.pin, "pin_-x")
        XCTAssertEqual(code.code, "c0de")
        XCTAssertEqual(code.addresses, ["100.101.102.103:7777", "192.168.1.20:7777"])
        // The voice page's hand-off to the app carries the same fragment.
        XCTAssertEqual(PairingCode("sheepdog://pair#" + link.split(separator: "#")[1]), code)
    }

    func testPairingCodeRejects() {
        XCTAssertNil(PairingCode("https://1.2.3.4:7777/voice"))
        XCTAssertNil(PairingCode("https://1.2.3.4:7777/voice#pair=1&d=x&k=y"))
        XCTAssertNil(PairingCode("sheepdog://pair?u=http://1.2.3.4:7777&t=tok"))
        XCTAssertNil(PairingCode("https://1.2.3.4:7777/voice#pair=1&d=x&k=y&e=z"))
    }

    /// The pin is SHA-256 over the P-256 SubjectPublicKeyInfo, as Go's
    /// x509 writes it: CryptoKit's DER export is the reference.
    func testPinMatchesTheDaemonsDefinition() throws {
        let key = P256.Signing.PrivateKey()
        let want = SPKIPin.base64url(Data(SHA256.hash(data: key.publicKey.derRepresentation)))
        XCTAssertEqual(SPKIPin.pin(rawP256: key.publicKey.x963Representation), want)
        XCTAssertEqual(want.count, 43)
        XCTAssertNil(SPKIPin.pin(rawP256: Data([1, 2, 3])))
    }

    /// The signed text has the daemon's exact form.
    func testSignedText() {
        XCTAssertEqual(SignedText.session(daemonID: "d", pin: "p", deviceID: "dev", nonce: "n", unix: 1_700_000_000),
                       "sheepdog-auth-v1\nd\np\ndev\nn\n1700000000\nsession")
    }

#if os(iOS)
    /// A device key signs so the daemon's ECDSA check passes (CryptoKit
    /// verifies the same DER signature), and is gone after delete.
    func testDeviceKeySignsAndDeletes() throws {
        let id = "test-\(UUID().uuidString)"
        defer { DeviceKey.delete(daemonID: id) }
        let key = try DeviceKey.loadOrCreate(daemonID: id)
        XCTAssertNotNil(DeviceKey.load(daemonID: id))
        let pub = try XCTUnwrap(DeviceKey.publicKey(key))
        var b64 = pub.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
        while b64.count % 4 != 0 { b64 += "=" }
        let raw = try XCTUnwrap(Data(base64Encoded: b64))
        let verifier = try P256.Signing.PublicKey(x963Representation: raw)
        let text = SignedText.session(daemonID: "d", pin: "p", deviceID: "dev", nonce: "n", unix: 1)
        let sig = try P256.Signing.ECDSASignature(derRepresentation: DeviceKey.sign(key, text))
        XCTAssertTrue(verifier.isValidSignature(sig, for: Data(text.utf8)))
        DeviceKey.delete(daemonID: id)
        XCTAssertNil(DeviceKey.load(daemonID: id))
    }
#endif

    func testProblemsSayWhy() {
        XCTAssertEqual(PairingProblem(reason: "device_revoked"), .revoked)
        XCTAssertEqual(PairingProblem(reason: "unknown_device"), .unknownDevice)
        XCTAssertNil(PairingProblem(reason: "session_expired"))
        XCTAssertEqual(PairingProblem.codeUsed.title, "This code has expired or was used")
        XCTAssertTrue(PairingProblem.pinMismatch.message.contains("do not continue on this network"))
    }
}
