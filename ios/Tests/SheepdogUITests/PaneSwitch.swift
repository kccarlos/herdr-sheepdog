import XCTest

extension XCTestCase {
    /// Switches the main view to Agents and waits until that pane is the
    /// selected one. A tap while the demo's cover or the first-run sheet
    /// still animates can be lost on a slow Mac, which left the Agents
    /// rows in the tree but never hittable; so tap the visible tab again
    /// until it reports itself selected.
    func showAgentsPane(_ app: XCUIApplication, file: StaticString = #filePath, line: UInt = #line) {
        let tabs = app.buttons.matching(identifier: "main.pane.flock")
        XCTAssertTrue(tabs.firstMatch.waitForExistence(timeout: 10), "main.pane.flock", file: file, line: line)
        let deadline = Date().addingTimeInterval(15)
        repeat {
            for i in 0 ..< tabs.count where tabs.element(boundBy: i).isHittable {
                let tab = tabs.element(boundBy: i)
                if tab.isSelected { return }
                tab.tap()
                break
            }
            usleep(300_000)
        } while Date() < deadline
        XCTFail("the Agents pane never opened", file: file, line: line)
    }
}
