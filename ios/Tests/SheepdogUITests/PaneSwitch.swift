import XCTest

extension XCUIElement {
    /// `isHittable`, but false (not a test failure) for an element that is
    /// in the tree without a place on screen yet: a menu item while the
    /// menu opens or closes has the frame {inf, inf}, and asking it
    /// `isHittable` fails the test with "Activation point invalid".
    var isTappable: Bool {
        // A snapshot, not `exists` then `frame`: an element that goes
        // between the two fails the test ("no matches found").
        guard let frame = try? snapshot().frame else { return false }
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
        // The pane is up once one of its agent rows can be tapped.
        let row = app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH 'flock.agent.'")).firstMatch
        let deadline = Date().addingTimeInterval(20)
        repeat {
            if row.isTappable { return }
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
            if app.buttons[item].isTappable, tapOnScreen(app, app.buttons.matching(identifier: item)), landed() { return }
            if tapOnScreen(app, app.buttons.matching(identifier: menu)) {
                if app.buttons[item].waitForExistence(timeout: 3) {
                    scrollMenuToItem(app, item)
                    if tapOnScreen(app, app.buttons.matching(identifier: item)), landed() { return }
                }
            }
            usleep(300_000)
        } while Date() < deadline
        XCTFail("no \(item) in \(menu)" + (until.map { ", or it never opened \($0)" } ?? ""), file: file, line: line)
    }

    /// At the accessibility sizes a menu is taller than the screen and
    /// scrolls: swipes up inside the open menu until `item` can be tapped.
    private func scrollMenuToItem(_ app: XCUIApplication, _ item: String) {
        let button = app.buttons[item]
        for _ in 0 ..< 4 where !button.isTappable {
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).press(forDuration: 0.05, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.2)))
        }
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
            if tapOnScreen(app, app.buttons.matching(identifier: menu)), app.buttons[item].waitForExistence(timeout: 3) {
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

    /// Swipes down until the top of the list shows, for a test that scrolled
    /// past what it needs next.
    func scrollToTop(_ app: XCUIApplication, maxSwipes: Int = 6) {
        for _ in 0 ..< maxSwipes { app.swipeDown(velocity: .fast) }
    }

    /// Asserts that the elements with these identifiers come in this order
    /// from the top of the list. At the accessibility sizes only the rows
    /// near the screen are in the tree, so each is found by scrolling down,
    /// and two rows are compared whenever both are in the tree.
    func assertListOrder(_ app: XCUIApplication, _ identifiers: [String], file: StaticString = #filePath, line: UInt = #line) {
        scrollToTop(app)
        var previous: XCUIElement?
        for id in identifiers {
            let element = app.descendants(matching: .any).matching(identifier: id).firstMatch
            for _ in 0 ..< 10 {
                if element.exists, let frame = try? element.snapshot().frame, app.frame.contains(CGPoint(x: frame.midX, y: frame.midY)) { break }
                app.swipeUp(velocity: .slow)
            }
            XCTAssertTrue(element.exists, "\(id) is in the list", file: file, line: line)
            if let previous, previous.exists, element.exists {
                XCTAssertLessThan(previous.frame.minY, element.frame.minY, "\(previous.identifier) comes before \(id)", file: file, line: line)
            }
            previous = element
        }
    }

    /// Swipes the screen up until `element` sits clear of the voice
    /// capsule floating over the bottom of the main view. On a short
    /// phone a card's last row can end under the capsule, where XCTest
    /// still calls it hittable but a tap lands on the capsule's buttons.
    func revealAboveCapsule(_ app: XCUIApplication, _ element: XCUIElement, maxSwipes: Int = 4, file: StaticString = #filePath, line: UInt = #line) -> XCUIElement {
        // The demo keeps the real main view, and its capsule, under its cover.
        let capsule = app.scrollViews.matching(identifier: "voice.capsule").firstMatch
        func clear() -> Bool {
            guard element.isTappable else { return false }
            guard capsule.exists, let top = try? capsule.snapshot().frame.minY else { return true }
            return element.frame.maxY <= top
        }
        for _ in 0 ..< maxSwipes where !clear() { app.swipeUp(velocity: .slow) }
        XCTAssertTrue(clear(), "\(element) stays under the voice capsule", file: file, line: line)
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

    /// Taps the first match that has a place on screen, at the middle of
    /// the frame it has now. A menu item can lose its frame between the
    /// check and the tap (the menu closes or is rebuilt when the state
    /// under it changes), and both `isHittable` and an element `tap()` then
    /// fail the test; a coordinate tap cannot, and `tapMenuItem(until:)`
    /// notices one that landed nowhere. The copy under the demo's cover
    /// sits at the same place, and a coordinate tap reaches the top one.
    private func tapOnScreen(_ app: XCUIApplication, _ query: XCUIElementQuery) -> Bool {
        let screen = app.frame
        for i in 0 ..< query.count {
            guard let frame = try? query.element(boundBy: i).snapshot().frame else { continue }
            guard frame.minX.isFinite, frame.minY.isFinite, !frame.isEmpty,
                  screen.contains(CGPoint(x: frame.midX, y: frame.midY)) else { continue }
            app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: frame.midX - screen.minX, dy: frame.midY - screen.minY)).tap()
            return true
        }
        return false
    }
}
