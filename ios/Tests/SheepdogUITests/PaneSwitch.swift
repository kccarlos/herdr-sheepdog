import XCTest

extension XCUIElement {
    /// `isHittable`, but false (not a test failure) for an element that is
    /// in the tree without a place on screen yet: a menu item while the
    /// menu opens or closes has the frame {inf, inf}, and asking it
    /// `isHittable` fails the test with "Activation point invalid".
    var isTappable: Bool {
        guard exists else { return false }
        let frame = self.frame
        guard frame.minX.isFinite, frame.minY.isFinite, !frame.isEmpty else { return false }
        return isHittable
    }
}

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
            for i in 0 ..< tabs.count where tabs.element(boundBy: i).isTappable {
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
    /// With `until`, the tap counts only once that element shows up (the
    /// screen the item opens); otherwise the menu is opened again.
    func tapMenuItem(_ app: XCUIApplication, menu: String, item: String, until: XCUIElement? = nil, timeout: TimeInterval = 15, file: StaticString = #filePath, line: UInt = #line) {
        let deadline = Date().addingTimeInterval(timeout)
        func landed() -> Bool { until.map { $0.waitForExistence(timeout: 3) } ?? true }
        repeat {
            if until?.exists == true { return }
            if tapHittable(app.buttons.matching(identifier: item)), landed() { return }
            if tapHittable(app.buttons.matching(identifier: menu)) {
                if app.buttons[item].waitForExistence(timeout: 3), tapHittable(app.buttons.matching(identifier: item)), landed() { return }
            }
            usleep(300_000)
        } while Date() < deadline
        XCTFail("no \(item) in \(menu)" + (until.map { ", or it never opened \($0)" } ?? ""), file: file, line: line)
    }

    /// Taps `element` once it is on screen and hittable: a sheet or cover
    /// still moving leaves it in the tree but not tappable, and a tap then
    /// lands on nothing.
    func tapWhenTappable(_ element: XCUIElement, _ what: String, timeout: TimeInterval = 10, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertTrue(element.waitForExistence(timeout: timeout), "\(what) never appeared", file: file, line: line)
        let deadline = Date().addingTimeInterval(timeout)
        while !element.isTappable, Date() < deadline { usleep(150_000) }
        XCTAssertTrue(element.isTappable, "\(what) never became tappable", file: file, line: line)
        element.tap()
    }

    /// Types into a text field once it has the keyboard focus, tapping it
    /// if the focus has not arrived (a field focused on appear gets it a
    /// moment after it shows).
    func typeWhenFocused(_ field: XCUIElement, _ text: String, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertTrue(field.waitForExistence(timeout: 10), "\(field) never appeared", file: file, line: line)
        let focused = NSPredicate(format: "hasKeyboardFocus == true")
        let deadline = Date().addingTimeInterval(10)
        while !focused.evaluate(with: field), Date() < deadline {
            let wait = XCTNSPredicateExpectation(predicate: focused, object: field)
            if XCTWaiter().wait(for: [wait], timeout: 1) == .completed { break }
            if field.isTappable { field.tap() }
        }
        XCTAssertTrue(focused.evaluate(with: field), "\(field) never got the keyboard focus", file: file, line: line)
        field.typeText(text)
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
        closeMenu(app, item: item)
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

    /// + ▸ New agent, until its first step (Where) is up. At the
    /// accessibility sizes the rows below the fold are not in the tree, so
    /// wait for the list, not a row.
    func openNewAgent(_ app: XCUIApplication, file: StaticString = #filePath, line: UInt = #line) {
        let list = app.descendants(matching: .any)["newAgent.where"]
        for _ in 0 ..< 3 where !list.exists {
            tapMenuItem(app, menu: "toolbar.new", item: "new.agent", file: file, line: line)
            if list.waitForExistence(timeout: 10) { break }
        }
        XCTAssertTrue(list.exists, "New agent opens on Where", file: file, line: line)
    }

    /// Swipes up until `element` is on screen: lists only hold the rows
    /// they show, and at the accessibility sizes few fit.
    @discardableResult
    func scrollToVisible(_ app: XCUIApplication, _ element: XCUIElement, maxSwipes: Int = 8, file: StaticString = #filePath, line: UInt = #line) -> XCUIElement {
        for _ in 0 ..< maxSwipes {
            if element.exists, element.isTappable { return element }
            app.swipeUp(velocity: .slow)
        }
        XCTAssertTrue(element.exists && element.isTappable, "\(element) is not on screen", file: file, line: line)
        return element
    }

    /// Closes an open menu by tapping the very top of the screen.
    /// Taps until `item` (an item of the open menu) is gone, so a tap
    /// that lands while the menu still animates in is not lost.
    func closeMenu(_ app: XCUIApplication, item: String? = nil) {
        for _ in 0 ..< 4 {
            sleep(1)
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.03, dy: 0.45)).tap()
            guard let item else { sleep(1); return }
            let gone = NSPredicate(format: "exists == false")
            let wait = XCTNSPredicateExpectation(predicate: gone, object: app.buttons[item])
            if XCTWaiter().wait(for: [wait], timeout: 2) == .completed { return }
        }
    }

    private func tapHittable(_ query: XCUIElementQuery) -> Bool {
        for i in 0 ..< query.count where query.element(boundBy: i).isTappable {
            query.element(boundBy: i).tap()
            return true
        }
        return false
    }
}
