import XCTest

/// The daemon speaks plain HTTP on a private network (no TLS in v1), and the
/// Mac may be reached on a Tailscale 100.64/10 address, which ATS does not
/// count as local. `NSAllowsArbitraryLoads` is the only key that permits
/// that — and ATS ignores it whenever `NSAllowsLocalNetworking` (or the
/// media / web-content variants) is also present. This test pins the shape
/// so the "iOS blocked the plain-HTTP request" error cannot come back.
final class AppTransportSecurityTests: XCTestCase {
    func testArbitraryLoadsAloneIsAllowed() throws {
        let ats = try XCTUnwrap(Bundle.main.infoDictionary?["NSAppTransportSecurity"] as? [String: Any],
                                "the host app's Info.plist has no NSAppTransportSecurity")
        XCTAssertEqual(ats["NSAllowsArbitraryLoads"] as? Bool, true)
        for key in ["NSAllowsLocalNetworking", "NSAllowsArbitraryLoadsInWebContent", "NSAllowsArbitraryLoadsForMedia"] {
            XCTAssertNil(ats[key], "\(key) makes ATS ignore NSAllowsArbitraryLoads")
        }
    }
}
