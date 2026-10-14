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
