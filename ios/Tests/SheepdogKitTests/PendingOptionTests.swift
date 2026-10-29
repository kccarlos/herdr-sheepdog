@testable import SheepdogKit
import XCTest

/// An option's description and the agent's recommendation arrive as their
/// own fields (docs/dev/ws-contract.md "Approve by tap"); an older daemon
/// sends the label alone.
final class PendingOptionTests: XCTestCase {
    private func decode(_ json: String) throws -> PendingOption {
        try JSONDecoder().decode(PendingOption.self, from: Data(json.utf8))
    }

    func testNewFields() throws {
        let o = try decode(#"{"index":1,"label":"点一下开始，再点发送 (推荐)","keys":["1"],"default":true,"description":"按住说话模式下，锁屏按钮改成：点一下开始说，再点一下发送。","recommended":true,"title":"点一下开始，再点发送"}"#)
        XCTAssertEqual(o.displayLabel, "点一下开始，再点发送")
        XCTAssertEqual(o.displayDetail, "按住说话模式下，锁屏按钮改成：点一下开始说，再点一下发送。")
        XCTAssertTrue(o.isRecommended)
        XCTAssertEqual(o.accessibilityLabel, "Approve: 点一下开始，再点发送, recommended. 按住说话模式下，锁屏按钮改成：点一下开始说，再点一下发送。")
    }

    func testOldDaemon() throws {
        let o = try decode(#"{"index":2,"label":"No, and tell Codex what to do differently","keys":["esc"],"default":false}"#)
        XCTAssertEqual(o.displayLabel, "No, and tell Codex what to do differently")
        XCTAssertNil(o.displayDetail)
        XCTAssertFalse(o.isRecommended)
        XCTAssertEqual(o.accessibilityLabel, "Decline: No, and tell Codex what to do differently", "unchanged without the new fields")
    }

    func testBlankFieldsFallBack() throws {
        let o = try decode(#"{"index":1,"label":"Yes","keys":["1"],"default":true,"description":"  ","title":""}"#)
        XCTAssertEqual(o.displayLabel, "Yes")
        XCTAssertNil(o.displayDetail)
    }

    func testRoundTrip() throws {
        let o = PendingOption(index: 1, label: "A (Recommended)", keys: ["1"], isDefault: true, detail: "Why A.", isRecommended: true, title: "A")
        let back = try JSONDecoder().decode(PendingOption.self, from: JSONEncoder().encode(o))
        XCTAssertEqual(back, o)
        let plain = String(decoding: try JSONEncoder().encode(PendingOption(index: 2, label: "B")), as: UTF8.self)
        XCTAssertFalse(plain.contains("recommended"), plain)
        XCTAssertFalse(plain.contains("description"), plain)
    }
}
