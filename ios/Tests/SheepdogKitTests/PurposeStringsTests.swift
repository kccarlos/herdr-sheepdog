import XCTest

/// App Review 5.1.1: every permission the app asks for has a purpose string
/// that names what it is for, and none promises a server the app does not
/// have. Reads the host app's Info.plist.
final class PurposeStringsTests: XCTestCase {
    private func purpose(_ key: String) throws -> String {
        try XCTUnwrap(Bundle.main.infoDictionary?[key] as? String, "\(key) is missing")
    }

    func testEveryPermissionSaysWhatItIsFor() throws {
        let expectations: [String: [String]] = [
            "NSMicrophoneUsageDescription": ["talk", "daemon", "demo"],
            "NSSpeechRecognitionUsageDescription": ["demo", "on-device"],
            "NSCameraUsageDescription": ["pairing code", "Nothing is recorded"],
            "NSLocalNetworkUsageDescription": ["daemon", "your own computer"],
        ]
        for (key, words) in expectations {
            let text = try purpose(key)
            XCTAssertGreaterThan(text.count, 40, "\(key) is too short to explain itself")
            for word in words {
                XCTAssertTrue(text.contains(word), "\(key) should mention \"\(word)\": \(text)")
            }
        }
    }

    /// The daemon runs on Linux too: the strings say "computer", not "Mac".
    func testPurposeStringsDoNotAssumeAMac() throws {
        for key in ["NSMicrophoneUsageDescription", "NSCameraUsageDescription", "NSLocalNetworkUsageDescription"] {
            XCTAssertFalse(try purpose(key).contains("Mac"), key)
        }
    }
}
