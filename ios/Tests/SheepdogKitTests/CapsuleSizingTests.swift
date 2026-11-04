@testable import SheepdogKit
import XCTest

final class CapsuleSizingTests: XCTestCase {
    func testRegularSizesKeepTheirHeightAndCap() {
        XCTAssertEqual(CapsuleSizing.height(scaled: 60, row: 60, accessibility: false), 60)
        XCTAssertEqual(CapsuleSizing.height(scaled: 80, row: 70, accessibility: false), 80)
        XCTAssertEqual(CapsuleSizing.height(scaled: 80, row: 120, accessibility: false), 120)
        XCTAssertEqual(CapsuleSizing.height(scaled: 80, row: 400, accessibility: false), 150)
        XCTAssertFalse(CapsuleSizing.scrolls(row: 150, accessibility: false))
        XCTAssertTrue(CapsuleSizing.scrolls(row: 151, accessibility: false))
    }

    func testTheBaseHeightIsAFloor() {
        XCTAssertEqual(CapsuleSizing.minimumHeight(scaled: 40, accessibility: false), 60)
    }

    func testAccessibilitySizesStayCompact() {
        // The scaled height at the largest size is about 3x the base.
        XCTAssertEqual(CapsuleSizing.height(scaled: 180, row: 70, accessibility: true), 70)
        XCTAssertEqual(CapsuleSizing.height(scaled: 180, row: 20, accessibility: true), 60)
        XCTAssertEqual(CapsuleSizing.height(scaled: 180, row: 500, accessibility: true), 96)
        XCTAssertTrue(CapsuleSizing.scrolls(row: 97, accessibility: true))
        XCTAssertFalse(CapsuleSizing.scrolls(row: 96, accessibility: true))
        // Under a third of the shortest supported phone's screen (667 pt).
        XCTAssertLessThan(CapsuleSizing.accessibilityCap, 667 / 3)
    }
}
