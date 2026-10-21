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

    /// Opens one of the main screen's menus (`toolbar.new`, `toolbar.more`
    /// or the capsule's `voice.menu`) and taps one of its items. The demo
    /// keeps the real main view under its cover, so both look for the
    /// copy that is on screen, and the menu is opened again if a tap was
    /// lost while something still animated.
    func tapMenuItem(_ app: XCUIApplication, menu: String, item: String, timeout: TimeInterval = 15, file: StaticString = #filePath, line: UInt = #line) {
        let deadline = Date().addingTimeInterval(timeout)
        repeat {
            if tapHittable(app.buttons.matching(identifier: item)) { return }
            if tapHittable(app.buttons.matching(identifier: menu)) {
                if app.buttons[item].waitForExistence(timeout: 3), tapHittable(app.buttons.matching(identifier: item)) { return }
            }
            usleep(300_000)
        } while Date() < deadline
        XCTFail("no \(item) in \(menu)", file: file, line: line)
    }

    /// The label of a menu item: opens the menu, reads it, closes the menu.
    func menuItemLabel(_ app: XCUIApplication, menu: String, item: String) -> String {
        let deadline = Date().addingTimeInterval(15)
        var label = ""
        repeat {
            if tapHittable(app.buttons.matching(identifier: menu)), app.buttons[item].waitForExistence(timeout: 3) {
                label = app.buttons[item].label
                break
            }
            usleep(300_000)
        } while Date() < deadline
        closeMenu(app)
        return label
    }

    /// Polls a menu item's label until it reads `label`.
    func waitForMenuItem(_ app: XCUIApplication, menu: String, item: String, label: String, timeout: TimeInterval) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        repeat {
            if menuItemLabel(app, menu: menu, item: item) == label { return true }
            sleep(1)
        } while Date() < deadline
        return false
    }

    /// Closes an open menu by tapping the very top of the screen.
    func closeMenu(_ app: XCUIApplication) {
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.03)).tap()
        sleep(1)
    }

    private func tapHittable(_ query: XCUIElementQuery) -> Bool {
        for i in 0 ..< query.count where query.element(boundBy: i).isHittable {
            query.element(boundBy: i).tap()
            return true
        }
        return false
    }
}
