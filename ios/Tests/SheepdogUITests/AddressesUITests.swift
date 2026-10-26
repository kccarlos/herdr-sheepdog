import XCTest

/// Settings ▸ Connection ▸ Addresses on made-up addresses
/// (`-sheepdogAddressesScreen`, no daemon): it says which address is in use
/// and why, which is preferred, and Prefer moves both.
final class AddressesUITests: XCTestCase {
    override func setUp() {
        continueAfterFailure = false
    }

    func testShowsTheAddressInUseAndPreferMovesIt() {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogNoAnimations", "-sheepdogAddressesScreen", "list"]
        app.launch()

        let rows = app.buttons.matching(identifier: "addresses.row")
        XCTAssertTrue(rows.firstMatch.waitForExistence(timeout: 15), "the address rows")
        XCTAssertEqual(rows.count, 3)
        XCTAssertEqual(rows.element(boundBy: 0).label, "100.101.102.103:7777, preferred")
        XCTAssertEqual(rows.element(boundBy: 1).label, "192.168.1.20:7777, In use on Wi-Fi")
        XCTAssertEqual(rows.element(boundBy: 2).label, "100.101.1.1:7777, Answered with another key")

        // Tapping an address prefers it.
        tapWhenTappable(rows.element(boundBy: 1), "the LAN row")

        let first = rows.element(boundBy: 0)
        let moved = NSPredicate(format: "label == %@", "192.168.1.20:7777, preferred, In use on Wi-Fi")
        expectation(for: moved, evaluatedWith: first)
        waitForExpectations(timeout: 10)
        XCTAssertEqual(rows.element(boundBy: 1).label, "100.101.102.103:7777")
    }
}
