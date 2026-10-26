import XCTest

/// App Store submission: every bundle that calls a required-reason API ships a
/// privacy manifest that names the reason (ITMS-91053). Reads the manifests
/// from the built host app, its extensions and the embedded SheepdogKit.
final class PrivacyManifestTests: XCTestCase {
    private func manifest(at bundleURL: URL) throws -> [String: Any] {
        let url = bundleURL.appendingPathComponent("PrivacyInfo.xcprivacy")
        let data = try Data(contentsOf: url)
        let plist = try PropertyListSerialization.propertyList(from: data, format: nil)
        return try XCTUnwrap(plist as? [String: Any], "\(url.lastPathComponent) is not a dictionary")
    }

    private func reasons(_ manifest: [String: Any]) -> [String: Set<String>] {
        var out: [String: Set<String>] = [:]
        for entry in manifest["NSPrivacyAccessedAPITypes"] as? [[String: Any]] ?? [] {
            let type = entry["NSPrivacyAccessedAPIType"] as? String ?? ""
            out[type] = Set(entry["NSPrivacyAccessedAPITypeReasons"] as? [String] ?? [])
        }
        return out
    }

    private let defaults = "NSPrivacyAccessedAPICategoryUserDefaults"
    private let bootTime = "NSPrivacyAccessedAPICategorySystemBootTime"

    private func checkNoTracking(_ m: [String: Any], _ name: String) {
        XCTAssertEqual(m["NSPrivacyTracking"] as? Bool, false, name)
        XCTAssertEqual((m["NSPrivacyTrackingDomains"] as? [Any])?.count, 0, name)
        XCTAssertEqual((m["NSPrivacyCollectedDataTypes"] as? [Any])?.count, 0, name)
    }

    func testAppDeclaresUserDefaultsAndBootTime() throws {
        let m = try manifest(at: Bundle.main.bundleURL)
        checkNoTracking(m, "app")
        XCTAssertEqual(reasons(m)[defaults], ["CA92.1"])
        XCTAssertEqual(reasons(m)[bootTime], ["35F9.1"])
    }

    func testKitDeclaresAppAndAppGroupUserDefaults() throws {
        let kit = Bundle.main.bundleURL.appendingPathComponent("Frameworks/SheepdogKit.framework")
        let m = try manifest(at: kit)
        checkNoTracking(m, "kit")
        XCTAssertEqual(reasons(m)[defaults], ["CA92.1", "1C8F.1"])
    }

    func testExtensionsShipManifests() throws {
        let plugIns = Bundle.main.bundleURL.appendingPathComponent("PlugIns")
        let nse = try manifest(at: plugIns.appendingPathComponent("SheepdogNotificationService.appex"))
        checkNoTracking(nse, "notification service")
        XCTAssertEqual(reasons(nse)[defaults], ["1C8F.1"])
        let widgets = try manifest(at: plugIns.appendingPathComponent("SheepdogWidgets.appex"))
        checkNoTracking(widgets, "widgets")
        XCTAssertTrue(reasons(widgets).isEmpty)
    }
}
