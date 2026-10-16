import XCTest
@testable import SheepdogKit

final class PairingLinkTests: XCTestCase {
    /// The daemon's `sheepdog pair` link (internal/pair URL in the daemon
    /// repository): addresses in order, the token decoded.
    func testParsesTheDaemonsLink() throws {
        let link = try XCTUnwrap(PairingLink("sheepdog://pair?t=s3cr%26t%3Dx+y&u=http%3A%2F%2F192.168.1.20%3A7777&u=http%3A%2F%2F100.101.102.103%3A7777"))
        XCTAssertEqual(link.addresses, ["192.168.1.20:7777", "100.101.102.103:7777"])
        XCTAssertEqual(link.token, "s3cr&t=x y")
    }

    func testNoTokenAndDuplicates() throws {
        let link = try XCTUnwrap(PairingLink("sheepdog://pair?u=http://mac.tail.ts.net:7000&u=http://mac.tail.ts.net:7000"))
        XCTAssertEqual(link.addresses, ["mac.tail.ts.net:7000"])
        XCTAssertEqual(link.token, "")
    }

    func testRejects() {
        XCTAssertNil(PairingLink("https://example.com/?u=http://1.2.3.4:7777"))
        XCTAssertNil(PairingLink("sheepdog://talk"))
        XCTAssertNil(PairingLink("sheepdog://pair?t=only-a-token"))
        XCTAssertNil(PairingLink("sheepdog://pair?u=https://1.2.3.4:7777&t=x"), "the app speaks plain HTTP only")
        XCTAssertNil(PairingLink("not a url at all"))
    }

    /// The earliest address in the code's order that answers wins, even
    /// when a later one answers first.
    func testFirstReachableKeepsTheCodesOrder() async {
        let link = PairingLink(addresses: ["lan:1", "lan:2", "ts:3"], token: "")
        let picked = await link.firstReachable(timeout: .seconds(2)) { address in
            switch address {
            case "lan:1": return false
            case "lan:2":
                try? await Task.sleep(for: .milliseconds(200))
                return true
            default: return true
            }
        }
        XCTAssertEqual(picked, "lan:2")
    }

    /// An address that hangs costs one timeout, and none answering is nil.
    func testFirstReachableTimesOut() async {
        let link = PairingLink(addresses: ["lan:1", "ts:2"], token: "")
        let start = Date()
        let picked = await link.firstReachable(timeout: .milliseconds(300)) { address in
            if address == "lan:1" {
                try? await Task.sleep(for: .seconds(30))
                return true
            }
            return false
        }
        XCTAssertNil(picked)
        XCTAssertLessThan(Date().timeIntervalSince(start), 5)
    }

    /// Scanning needs the camera; iOS kills an app that opens it without
    /// a usage string.
    func testCameraUsageDescription() throws {
        let text = try XCTUnwrap(Bundle.main.infoDictionary?["NSCameraUsageDescription"] as? String)
        XCTAssertTrue(text.contains("pairing code"))
    }
}

/// `sheepdog://pair` opened from outside the app (the Camera app).
final class PairingDeepLinkTests: XCTestCase {
    func testReadsTheLinkAsAURL() throws {
        let url = try XCTUnwrap(URL(string: "sheepdog://pair?u=http%3A%2F%2F192.168.1.20%3A7777&u=http%3A%2F%2F100.101.102.103%3A7777&t=abc"))
        let link = try XCTUnwrap(PairingLink(url: url))
        XCTAssertEqual(link.addresses, ["192.168.1.20:7777", "100.101.102.103:7777"])
        XCTAssertEqual(link.token, "abc")
        XCTAssertNil(AppRoute(url: url), "pairing is not a session route: it needs the user's yes first")
        XCTAssertNil(PairingLink(url: URL(string: "sheepdog://talk")!))
    }

    func testConfirmationNamesTheHostAndWhatChanges() {
        let link = PairingLink(addresses: ["192.168.1.20:7777", "100.101.102.103:7777"], token: "abc")
        let ask = link.confirmation(currentAddress: "10.0.0.5:7777")
        XCTAssertEqual(ask.title, "Pair with 192.168.1.20?")
        XCTAssertTrue(ask.message.contains("192.168.1.20:7777, 100.101.102.103:7777"))
        XCTAssertTrue(ask.message.contains("replaces the daemon at 10.0.0.5:7777"))
        XCTAssertTrue(ask.message.contains("keychain"))
        XCTAssertFalse(ask.message.contains("abc"), "the token is never shown")

        let same = PairingLink(addresses: ["mac.tail.ts.net:7777"], token: "").confirmation(currentAddress: "mac.tail.ts.net:7777")
        XCTAssertEqual(same.title, "Pair with mac.tail.ts.net?")
        XCTAssertFalse(same.message.contains("replaces"))
        XCTAssertTrue(same.message.contains("no token"))

        let v6 = PairingLink(addresses: ["[fd7a::1]:7777"], token: "").confirmation(currentAddress: "")
        XCTAssertEqual(v6.title, "Pair with fd7a::1?")
    }

    /// The Camera app opens the link only if the app claims the scheme.
    func testRegistersTheSheepdogScheme() throws {
        let types = try XCTUnwrap(Bundle.main.infoDictionary?["CFBundleURLTypes"] as? [[String: Any]])
        let schemes = types.flatMap { $0["CFBundleURLSchemes"] as? [String] ?? [] }
        XCTAssertTrue(schemes.contains("sheepdog"))
    }
}
