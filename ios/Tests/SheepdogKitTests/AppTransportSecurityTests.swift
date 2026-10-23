import XCTest

/// Every address but loopback is TLS, checked by the daemon's pin, so the
/// app needs no plaintext exception: only local networking, for plain HTTP
/// to loopback (the fixture daemons). This test pins that shape so
/// `NSAllowsArbitraryLoads` cannot come back.
final class AppTransportSecurityTests: XCTestCase {
    func testOnlyLocalNetworkingIsAllowed() throws {
        let ats = try XCTUnwrap(Bundle.main.infoDictionary?["NSAppTransportSecurity"] as? [String: Any],
                                "the host app's Info.plist has no NSAppTransportSecurity")
        XCTAssertEqual(ats["NSAllowsLocalNetworking"] as? Bool, true)
        for key in ["NSAllowsArbitraryLoads", "NSAllowsArbitraryLoadsInWebContent", "NSAllowsArbitraryLoadsForMedia", "NSExceptionDomains"] {
            XCTAssertNil(ats[key], "\(key) would allow plaintext beyond loopback")
        }
    }
}
