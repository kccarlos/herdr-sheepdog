@testable import SheepdogKit
import UserNotifications
import XCTest

/// End-to-end encrypted relay notifications: the Notification Service
/// Extension's decryption (Sources/SheepdogPush) against a vector sealed by
/// the daemon's own Go code path, the shared key store, the fallback and
/// the pass-through of direct-APNs pushes.
final class PushE2ETests: XCTestCase {
    /// Sealed with Go's crypto/cipher exactly like herdr-sheepdog
    /// internal/push.Seal (AES-256-GCM, 12-byte nonce, 16-byte tag appended,
    /// no associated data), by scripts/push-e2e-vector.go with a fixed key
    /// (bytes 0…31) and nonce (a0…ab). The plaintext is docs/dev/push.md's
    /// example payload.
    enum Vector {
        static let key = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
        static let nonce = "oKGio6Slpqeoqaqr"
        static let ciphertext = "nTodXTbpOMRABOu2dQ7i5AuOLXnm2ydOpixH8wvDVy3wFCib1gBpHz7pcKApG/CSNDsyJ0KibxBhOWIqhADww9ySpTZV1sSPhsKRd7DssZbveMTRq6W7QMwRW/zQ7yHgragJ32vc6vr69m80p7LPXxFqEZ/8GblVQbu71sYggjmsft5Svyf2clt7xKlmI/yzparIBc25juWLqJ/ZjaRVX6N/cMPPSHo0w1p7cCCE961VW9G6cQ7LW/LmGV5+uGR1X18irULrmYyDLTKgP0Vel37LlIlRBofRieNixyrz4BdZcx4D2Aq8UbSeeH0jfjQXKZHgT+w8FO4FyvIDARESsHZP5POC9cCVVo5mp0sZCaVdf34P0t7hENoCYK7PgbYPt2op0lRrEqOWT3R3iI3vMLxypncxV++BHsEPyPmvjyGMu2D83gVHePC9Dm21efjcWarodfM2FcpqM7amCVihIWMpgB65E1UFh7wbuz64q6CG6a+jutzcq58O"
        static var keyData: Data { Data(base64Encoded: key)! }
    }

    /// What APNs delivers from the relay (relay/src/index.ts): the
    /// placeholder alert, the clear category, and `e2e`.
    func relayContent(c: String = Vector.ciphertext, n: String = Vector.nonce) -> UNMutableNotificationContent {
        let content = UNMutableNotificationContent()
        content.title = "Sheepdog"
        content.body = "New update"
        content.categoryIdentifier = "APPROVAL"
        content.userInfo = [
            "aps": ["alert": ["title": "Sheepdog", "body": "New update"], "mutable-content": 1, "sound": "default",
                    "category": "APPROVAL", "interruption-level": "time-sensitive"] as [String: Any],
            "e2e": ["c": c, "n": n],
        ]
        return content
    }

    // MARK: - Decryption

    func testOpensTheGoSealedVector() throws {
        let object = try PushE2E.open(ciphertext: Vector.ciphertext, nonce: Vector.nonce, key: Vector.keyData)
        let aps = try XCTUnwrap(object["aps"] as? [String: Any])
        XCTAssertEqual((aps["alert"] as? [String: String])?["body"], "auth asks to run git push. Yes or no?")
        XCTAssertEqual(aps["thread-id"] as? String, "w5:p1")
        let sheepdog = try XCTUnwrap(object["sheepdog"] as? [String: Any])
        XCTAssertEqual(sheepdog["pending_id"] as? String, "ap-tm9bu0-1")
    }

    func testRefusesAWrongKeyATamperedCiphertextAndBadInput() {
        var wrong = Vector.keyData
        wrong[0] ^= 1
        XCTAssertThrowsError(try PushE2E.open(ciphertext: Vector.ciphertext, nonce: Vector.nonce, key: wrong)) {
            XCTAssertEqual($0 as? PushE2E.OpenError, .authentication)
        }
        var sealed = Data(base64Encoded: Vector.ciphertext)!
        sealed[5] ^= 0x80
        XCTAssertThrowsError(try PushE2E.open(ciphertext: sealed.base64EncodedString(), nonce: Vector.nonce, key: Vector.keyData)) {
            XCTAssertEqual($0 as? PushE2E.OpenError, .authentication)
        }
        XCTAssertThrowsError(try PushE2E.open(ciphertext: "not base64!", nonce: Vector.nonce, key: Vector.keyData)) {
            XCTAssertEqual($0 as? PushE2E.OpenError, .malformed)
        }
        XCTAssertThrowsError(try PushE2E.open(ciphertext: Vector.ciphertext, nonce: "AAAA", key: Vector.keyData)) {
            XCTAssertEqual($0 as? PushE2E.OpenError, .malformed)
        }
        XCTAssertThrowsError(try PushE2E.open(ciphertext: Vector.ciphertext, nonce: Vector.nonce, key: Data(count: 16))) {
            XCTAssertEqual($0 as? PushE2E.OpenError, .badKey)
        }
    }

    // MARK: - The notification shown

    func testDecryptedNotificationReadsLikeADirectPush() throws {
        let shown = PushE2E.rewrite(relayContent(), key: Vector.keyData)
        XCTAssertEqual(shown.title, "auth")
        XCTAssertEqual(shown.body, "auth asks to run git push. Yes or no?")
        XCTAssertEqual(shown.threadIdentifier, "w5:p1")
        XCTAssertEqual(shown.categoryIdentifier, PushActions.approvalCategory)
        XCTAssertNil(shown.userInfo["e2e"], "the sealed data is not kept")
        // The app's tap and Approve / Decline read it unchanged.
        let payload = try XCTUnwrap(PushPayload(userInfo: shown.userInfo))
        XCTAssertEqual(payload, PushPayload(kind: "approval", paneID: "w5:p1", agent: "auth", pendingID: "ap-tm9bu0-1", eventID: "evt-12",
                                            options: [PendingOption(index: 1, label: "Yes, proceed"), PendingOption(index: 2, label: "No")],
                                            category: "APPROVAL", title: "auth", body: "auth asks to run git push. Yes or no?"))
        let answer = try XCTUnwrap(PushActions.answer(actionIdentifier: PushActions.approve, payload: payload))
        XCTAssertEqual(answer.pendingID, "ap-tm9bu0-1")
        XCTAssertEqual(answer.option.index, 1)
    }

    func testFailureShowsTheNeutralLineAndNeverRawData() {
        let cases: [(String, UNNotificationContent)] = [
            ("no key", PushE2E.rewrite(relayContent(), key: nil)),
            ("wrong key", PushE2E.rewrite(relayContent(), key: Data(repeating: 7, count: 32))),
            ("damaged", PushE2E.rewrite(relayContent(c: "AAAA"), key: Vector.keyData)),
            ("timeout", PushE2E.fallback(relayContent())),
        ]
        for (name, shown) in cases {
            XCTAssertEqual(shown.title, "Sheepdog", name)
            XCTAssertEqual(shown.body, "Open the app to see the update.", name)
            XCTAssertEqual(shown.categoryIdentifier, PushActions.agentCategory, "\(name): no Approve / Decline it cannot answer")
            XCTAssertNil(shown.userInfo["e2e"], name)
            XCTAssertNil(shown.userInfo["sheepdog"], name)
            XCTAssertFalse(shown.body.contains(Vector.ciphertext.prefix(8)), name)
        }
    }

    func testDirectAPNsPushPassesThroughUntouched() {
        let content = UNMutableNotificationContent()
        content.title = "auth"
        content.body = "auth asks to run git push. Yes or no?"
        content.threadIdentifier = "w5:p1"
        content.categoryIdentifier = "APPROVAL"
        content.userInfo = PushTests.approvalUserInfo
        let shown = PushE2E.rewrite(content, key: Vector.keyData)
        XCTAssertTrue(shown === content, "a payload without e2e is handed back as it came")
        XCTAssertTrue(PushE2E.rewrite(content, key: nil) === content)
    }

    // MARK: - The shared key store

    /// The real App Group keychain (the test host is the app, with its
    /// entitlements), under a throwaway account.
    func testKeyStoreRoundTripsReplacesAndRemoves() {
        let store = PushContentKeyStore(account: "tests.\(UUID().uuidString)")
        defer { store.remove() }
        XCTAssertNil(store.key())
        XCTAssertTrue(store.store(base64: Vector.key))
        XCTAssertEqual(store.key(), Vector.keyData)
        // Re-registration with the same daemon: the same key, still there.
        XCTAssertTrue(store.store(base64: Vector.key))
        XCTAssertEqual(store.key(), Vector.keyData)
        // Another daemon: its key replaces the first.
        let other = Data(repeating: 0x42, count: 32)
        XCTAssertTrue(store.store(other))
        XCTAssertEqual(store.key(), other)
        // A value that is not 32 bytes of base64 leaves the key alone.
        XCTAssertFalse(store.store(base64: "c2hvcnQ="))
        XCTAssertFalse(store.store(base64: "not base64"))
        XCTAssertEqual(store.key(), other)
        // Leaving the daemon (or notifications turned off): gone.
        store.remove()
        XCTAssertNil(store.key())
    }

    func testKeyStoreUsesTheAppGroupTheExtensionReads() {
        XCTAssertEqual(PushContentKeyStore.appGroup, "group.com.github.kccarlos.herdr-sheepdog-ios")
    }
}
