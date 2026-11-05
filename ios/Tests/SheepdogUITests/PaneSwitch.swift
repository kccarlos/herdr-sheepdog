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
        // A finite frame can still have no activation point (an element laid
        // out beside the visible pane, or between two layouts), and
        // `isHittable` then records "Failed to determine hittability" as a
        // failure, which stops a test with `continueAfterFailure = false`
        // at once. Here that means "not tappable yet".
        var hittable = false
        let options = XCTExpectedFailure.Options()
        options.isStrict = false
        options.issueMatcher = { $0.compactDescription.contains("determine hittability") }
        XCTExpectFailure("an element without an activation point is not tappable", options: options) {
            hittable = isHittable
        }
        return hittable
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
        // True once the pane is up. On a Mac whose app is busy one query can
        // take many seconds, so the deadline can pass during the tap that
        // opened the pane: look once more before giving up.
        func isUp() -> Bool {
            if row.isTappable { return true }
            for i in 0 ..< tabs.count where tabs.element(boundBy: i).isTappable {
                let tab = tabs.element(boundBy: i)
                if tab.isSelected { return true }
                tab.tap()
                break
            }
            return false
        }
        repeat {
            if isUp() { return }
            usleep(300_000)
        } while Date() < deadline
        if isUp() { return }
        XCTFail("the Agents pane never opened", file: file, line: line)
    }

    /// Opens one of the main screen's menus (`toolbar.new`, `toolbar.more`
    /// or the capsule's `voice.menu`) and taps one of its items. The demo
    /// keeps the real main view under its cover, so both look for the
    /// copy that is on screen, and the menu is opened again if a tap was
    /// lost while something still animated.
    /// With `until`, the tap counts only once that element shows up (the
    /// screen the item opens); otherwise the menu is opened again.
    func tapMenuItem(_ app: XCUIApplication, menu: String, item: String, until: XCUIElement? = nil, timeout: TimeInterval = 40, file: StaticString = #filePath, line: UInt = #line) {
        let deadline = Date().addingTimeInterval(timeout)
        let items = app.buttons.matching(identifier: item)
        func landed() -> Bool { until.map { $0.waitForExistence(timeout: max(1, min(10, deadline.timeIntervalSinceNow))) } ?? true }
        repeat {
            if until?.exists == true { return }
            // A menu the caller (or an earlier round) already opened has its
            // item on screen: tapping the menu button again would close it
            // while it animates, and the round after would find it half
            // closed. Open it only when no item can be tapped yet.
            var open = isAnyTappable(items)
            if !open {
                let menuButtons = app.buttons.matching(identifier: menu)
                if tapElementTappable(menuButtons) || tapTappable(app, menuButtons) {
                    let itemDeadline = Date().addingTimeInterval(8)
                    while !items.firstMatch.exists && Date() < itemDeadline { usleep(100_000) }
                    open = items.firstMatch.exists
                }
            }
            if open {
                scrollMenuToItem(app, items)
                if (tapElementTappable(items) || tapTappable(app, items) || tapOnScreen(app, items)), landed() { return }
            }
            usleep(300_000)
        } while Date() < deadline
        XCTFail("no \(item) in \(menu)" + (until.map { ", or it never opened \($0)" } ?? ""), file: file, line: line)
    }

    /// Brings `items` (the open menu's item) into the menu's tappable area.
    /// A menu still opening has its items in the tree before UIKit finishes
    /// placing them, so wait briefly first: a drag across the middle of the
    /// screen closes a small menu it was meant to scroll. At the
    /// accessibility sizes a menu is taller than its platter and clips lower
    /// items even while their unclipped frames sit inside `app.frame`, so
    /// check hittability (`isTappable`) rather than screen bounds and swipe
    /// up inside the open menu until the item can be tapped.
    private func scrollMenuToItem(_ app: XCUIApplication, _ items: XCUIElementQuery) {
        let settled = Date().addingTimeInterval(1)
        while !isAnyTappable(items), Date() < settled { usleep(150_000) }
        for _ in 0 ..< 4 {
            if isAnyTappable(items) { break }
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).press(forDuration: 0.05, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.2)))
        }
    }

    /// Whether some match of `query` is hittable on screen. Indexing each
    /// match (`element(boundBy:)`) avoids the ambiguity `app.buttons[id]`
    /// hits when a cover keeps a second copy in the tree, while still asking
    /// UIKit's hit test so an item clipped below an open menu's scroll view
    /// is not mistaken for visible.
    private func isAnyTappable(_ query: XCUIElementQuery) -> Bool {
        tappableCenter(query) != nil
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

    /// Opens an agent's window from the Agents list and waits for its
    /// terminal (or `until`, for a window without one). The window button
    /// can be below the fold (at the largest text only a few rows fit) or
    /// under the voice capsule, and a tap
    /// that lands while the cover or list still settles is lost on a slow
    /// Mac: so scroll to the button, tap it, and look for the terminal; if
    /// it did not come up, look for the button again and retry.
    func openAgentWindow(_ app: XCUIApplication, alias: String, terminalTimeout: TimeInterval = 30, until: XCUIElement? = nil, file: StaticString = #filePath, line: UInt = #line) {
        let terminal = until ?? app.descendants(matching: .any)["agentScreen.terminal"]
        let windows = app.buttons.matching(identifier: "flock.agent.\(alias).window")
        let capsule = app.scrollViews.matching(identifier: "voice.capsule").firstMatch
        XCTAssertTrue(windows.firstMatch.waitForExistence(timeout: 15), "\(alias)'s window button", file: file, line: line)
        let deadline = Date().addingTimeInterval(90)
        var swipes = 0
        repeat {
            if terminal.exists { return }
            let capsuleTop = capsule.exists ? (try? capsule.snapshot().frame.minY) : nil
            var tapped = false
            for i in (0 ..< windows.count).reversed() {
                let button = windows.element(boundBy: i)
                guard button.isTappable, let frame = try? button.snapshot().frame else { continue }
                if let capsuleTop, frame.midY >= capsuleTop { continue }
                button.tap()
                tapped = true
                break
            }
            if tapped {
                if terminal.waitForExistence(timeout: terminalTimeout) { return }
                continue
            }
            if swipes < 6 {
                app.swipeUp(velocity: .slow)
                swipes += 1
            } else {
                scrollToTop(app)
                swipes = 0
            }
        } while Date() < deadline
        XCTAssertTrue(terminal.exists, "\(alias)'s window never reached its expected state", file: file, line: line)
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
        guard let point = onScreenCenter(app, query) else { return false }
        let screen = app.frame
        app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: point.x - screen.minX, dy: point.y - screen.minY)).tap()
        return true
    }

    /// The middle of the first match that has a place on screen.
    private func onScreenCenter(_ app: XCUIApplication, _ query: XCUIElementQuery) -> CGPoint? {
        let screen = app.frame
        for i in 0 ..< query.count {
            guard let frame = try? query.element(boundBy: i).snapshot().frame else { continue }
            guard frame.minX.isFinite, frame.minY.isFinite, !frame.isEmpty else { continue }
            let center = CGPoint(x: frame.midX, y: frame.midY)
            if screen.contains(center) { return center }
        }
        return nil
    }

    /// Taps the first match that UIKit reports as hittable, by coordinate.
    private func tapTappable(_ app: XCUIApplication, _ query: XCUIElementQuery) -> Bool {
        guard let point = tappableCenter(query) else { return false }
        let screen = app.frame
        app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: point.x - screen.minX, dy: point.y - screen.minY)).tap()
        return true
    }

    private func tappableCenter(_ query: XCUIElementQuery) -> CGPoint? {
        // When the demo covers the real app, both copies can report as
        // hittable at the same coordinates. SwiftUI exposes the covered
        // copy first, so prefer the last match (the visible demo copy).
        for i in (0 ..< query.count).reversed() {
            let element = query.element(boundBy: i)
            guard element.isTappable, let frame = try? element.snapshot().frame else { continue }
            return CGPoint(x: frame.midX, y: frame.midY)
        }
        return nil
    }

    /// Activates the matched accessibility element itself. This preserves
    /// which copy was selected when the demo and the covered app share an
    /// identifier; a coordinate tap would hit whichever copy is visually on
    /// top, which may not be the menu whose item was queried.
    private func tapElementTappable(_ query: XCUIElementQuery) -> Bool {
        for i in (0 ..< query.count).reversed() {
            let element = query.element(boundBy: i)
            if element.isTappable {
                element.tap()
                return true
            }
        }
        return false
    }
}
