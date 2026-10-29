import XCTest
@testable import SheepdogKit

final class CapsuleFrameTests: XCTestCase {
    func testWidthIs366WhereThereIsRoomAndShrinksBelow390() {
        XCTAssertEqual(CapsuleFrame.width(content: 820), 366)
        XCTAssertEqual(CapsuleFrame.width(content: 390), 366)
        XCTAssertEqual(CapsuleFrame.width(content: 389), 365)
        XCTAssertEqual(CapsuleFrame.width(content: 320), 296)
        XCTAssertEqual(CapsuleFrame.width(content: 10), 0, "never negative")
    }

    func testTheSidebarTakesItsWidthOutOfTheContent() {
        XCTAssertEqual(CapsuleFrame.content(window: 1180, sidebarShown: true), 790)
        XCTAssertEqual(CapsuleFrame.content(window: 1180, sidebarShown: false), 1180)
        XCTAssertEqual(CapsuleFrame.content(window: 300, sidebarShown: true), 0)
    }

    func testTheCentreIsTheContentsCentre() {
        // Landscape with the sidebar: 390 + 790 / 2.
        XCTAssertEqual(CapsuleFrame.centerX(window: 1180, sidebarShown: true), 785)
        // Portrait, sidebar hidden: the window's centre.
        XCTAssertEqual(CapsuleFrame.centerX(window: 820, sidebarShown: false), 410)
        // Portrait with the sidebar over the detail: right of it.
        XCTAssertEqual(CapsuleFrame.centerX(window: 820, sidebarShown: true), 605)
    }

    func testTheCapsuleNeverReachesIntoTheSidebar() {
        for window in stride(from: CGFloat(320), through: 1400, by: 20) {
            for shown in [true, false] {
                let content = CapsuleFrame.content(window: window, sidebarShown: shown)
                let left = CapsuleFrame.centerX(window: window, sidebarShown: shown) - CapsuleFrame.width(content: content) / 2
                XCTAssertGreaterThanOrEqual(left, window - content - 0.001, "window \(window), sidebar \(shown)")
            }
        }
    }
}
