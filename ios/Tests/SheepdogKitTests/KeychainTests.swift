import XCTest
@testable import SheepdogKit

/// The token has to survive a round trip through the real keychain — a
/// missing entitlement shows up here rather than as "Connect does nothing".
final class KeychainTests: XCTestCase {
    private let service = "com.github.kccarlos.herdr-sheepdog-ios.tests.\(UUID().uuidString)"

    func testRoundTripAndDelete() {
        let store = KeychainSecretStore(service: service)
        XCTAssertNil(store.secret(for: "daemon.token"))
        store.setSecret("devtoken", for: "daemon.token")
        XCTAssertEqual(store.secret(for: "daemon.token"), "devtoken")
        store.setSecret("another", for: "daemon.token")
        XCTAssertEqual(store.secret(for: "daemon.token"), "another")
        store.setSecret(nil, for: "daemon.token")
        XCTAssertNil(store.secret(for: "daemon.token"))
    }
}
