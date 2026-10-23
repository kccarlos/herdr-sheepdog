import XCTest

/// Every address but loopback is TLS, checked by the daemon's pin
/// (PinnedTrustDelegate). ATS cannot express a pin: with ATS on, iOS fails
/// a Tailscale or LAN address with "ATS failed system trust" even after the
/// delegate accepts the key, and pairing ends at "None of the code's
/// addresses answered" (docs/dev/design/security.md). So the app turns ATS
/// off, and nothing may switch that back off by accident.
final class AppTransportSecurityTests: XCTestCase {
    func testArbitraryLoadsSoThePinIsTheCheck() throws {
        let ats = try XCTUnwrap(Bundle.main.infoDictionary?["NSAppTransportSecurity"] as? [String: Any],
                                "the host app's Info.plist has no NSAppTransportSecurity")
        XCTAssertEqual(ats["NSAllowsArbitraryLoads"] as? Bool, true)
        // When any of these is present iOS ignores NSAllowsArbitraryLoads.
        for key in ["NSAllowsLocalNetworking", "NSAllowsArbitraryLoadsInWebContent", "NSAllowsArbitraryLoadsForMedia"] {
            XCTAssertNil(ats[key], "\(key) would make iOS ignore NSAllowsArbitraryLoads")
        }
    }
}
