import XCTest

/// The file browser on the offline demo (no daemon): the ⋯ menu and the
/// space's page open it, folders and search lead to a file, and each kind of
/// file and each refusal has its screen. Uses the helpers of PaneSwitch.swift.
///
/// `TEST_RUNNER_SHEEPDOG_DEMO_SHOTS=<directory>` saves screenshots there.
final class FilesDemoUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    private func shot(_ name: String) {
        guard let dir = ProcessInfo.processInfo.environment["SHEEPDOG_DEMO_SHOTS"] else { return }
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
    }

    private func any(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: id).firstMatch
    }

    private static let largestText = ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]

    private func launch(extra: [String] = [], largest: Bool = false) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogDemoSilent"] + extra + (largest ? Self.largestText : [])
        app.launch()
        if !extra.contains("-sheepdogOpenDemo") {
            let tryDemo = app.buttons["settings.firstRun.tryDemo"]
            XCTAssertTrue(tryDemo.waitForExistence(timeout: 10))
            tryDemo.tap()
        }
        XCTAssertTrue(any(app, "demo.header").waitForExistence(timeout: 15))
        return app
    }

    /// The agent's window, then ⋯ ▸ Files (or Changes).
    private func openBrowser(_ item: String = "files", largest: Bool = false) -> XCUIApplication {
        let app = launch(largest: largest)
        showAgentsPane(app)
        let window = app.buttons.matching(identifier: "flock.agent.claude-auth.window")
        XCTAssertTrue(window.firstMatch.waitForExistence(timeout: 10))
        for i in 0 ..< window.count where window.element(boundBy: i).isTappable {
            window.element(boundBy: i).tap()
            break
        }
        XCTAssertTrue(any(app, "agentScreen.terminal").waitForExistence(timeout: 30))
        let landed = item == "files" ? any(app, "files.list") : app.buttons["changes.file.internal/auth/token.go"]
        tapMenuItem(app, menu: "agentScreen.menu", item: "agentScreen.menu.\(item)", until: landed)
        XCTAssertTrue(landed.waitForExistence(timeout: 10))
        return app
    }

    private func open(_ app: XCUIApplication, row path: String) {
        tapWhenTappable(app.buttons["files.row.\(path)"], path)
    }

    // MARK: Folders and documents

    func testBrowseAFolderAndReadAMarkdownFile() throws {
        let app = openBrowser()
        XCTAssertTrue(app.buttons["browser.tab.files"].exists, "Files | Changes")
        XCTAssertTrue(app.buttons["files.row.docs"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["files.row.README.md"].exists)
        sleep(1)
        shot("files-list")

        open(app, row: "docs")
        XCTAssertTrue(any(app, "files.crumb.current").waitForExistence(timeout: 10))
        XCTAssertEqual(any(app, "files.crumb.current").label, "Current folder, docs")
        open(app, row: "docs/reports")
        open(app, row: "docs/reports/login-loop.md")

        let document = any(app, "viewer.markdown")
        XCTAssertTrue(document.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["Login redirect loop"].waitForExistence(timeout: 5))
        let bar = app.buttons["viewer.changes"]
        XCTAssertTrue(bar.waitForExistence(timeout: 5), "this file is among the changes")
        XCTAssertTrue(bar.label.hasPrefix("New in Changes"), bar.label)
        sleep(1)
        shot("viewer-markdown")

        tapWhenTappable(app.buttons["viewer.mode.raw"], "Raw")
        XCTAssertTrue(any(app, "viewer.code").waitForExistence(timeout: 5))
        XCTAssertTrue(any(app, "# Login redirect loop").waitForExistence(timeout: 5), "the first line, as written")
        shot("viewer-raw")

        // The bar goes to this file's diff, in Changes.
        tapWhenTappable(bar, "New in Changes")
        XCTAssertTrue(any(app, "diff.lines").waitForExistence(timeout: 10))
    }

    func testTheBreadcrumbGoesBackUp() throws {
        let app = openBrowser()
        open(app, row: "docs")
        open(app, row: "docs/reports")
        XCTAssertTrue(app.buttons["files.row.docs/reports/login-loop.md"].waitForExistence(timeout: 10))
        tapWhenTappable(app.buttons["files.crumb.root"], "the root crumb")
        XCTAssertTrue(app.buttons["files.row.README.md"].waitForExistence(timeout: 10))
    }

    func testALinkInADocumentOpensTheOtherFile() throws {
        let app = openBrowser()
        open(app, row: "README.md")
        XCTAssertTrue(any(app, "viewer.markdown").waitForExistence(timeout: 10))
        let link = app.links["the install notes"]
        XCTAssertTrue(link.waitForExistence(timeout: 5))
        link.tap()
        XCTAssertTrue(app.staticTexts["Install"].waitForExistence(timeout: 10), "docs/install.md")
    }

    // MARK: Search and sort

    func testSearchFindsNamesAndClears() throws {
        let app = openBrowser()
        typeWhenFocused(app.textFields["files.search"], "login")
        let hit = app.buttons["files.row.docs/reports/login-loop.md"]
        XCTAssertTrue(hit.waitForExistence(timeout: 10))
        XCTAssertEqual(app.staticTexts["files.search.summary"].label, "3 files named “login” in shop")
        XCTAssertTrue(hit.label.contains("docs/reports"), hit.label)
        sleep(1)
        shot("files-search")

        app.buttons["files.search.clear"].tap()
        XCTAssertTrue(app.buttons["files.row.docs"].waitForExistence(timeout: 10))
    }

    func testSearchWithNoMatchSaysSo() throws {
        let app = openBrowser()
        typeWhenFocused(app.textFields["files.search"], "ledger")
        XCTAssertTrue(any(app, "files.noResults").waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["No files named “ledger”"].exists)
        XCTAssertFalse(app.staticTexts["files.search.summary"].exists, "the empty state says it; no summary repeats it")
        tapWhenTappable(app.buttons["files.search.clear.action"], "Clear search")
        XCTAssertTrue(app.buttons["files.row.docs"].waitForExistence(timeout: 10))
    }

    func testRecentlyChangedGroupsByDay() throws {
        let app = openBrowser()
        tapWhenTappable(app.buttons["files.sort"], "the sort menu")
        tapWhenTappable(app.buttons["Recently changed"], "Recently changed")
        XCTAssertTrue(app.staticTexts["Today"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["Last 7 days"].exists || app.staticTexts["Older"].exists)
        sleep(1)
        shot("files-recent")

        // A day-old file reads in days here as everywhere ("yesterday", or
        // "2 days ago" just after midnight), never as a time of day.
        open(app, row: "docs")
        let api = app.buttons["files.row.docs/api.md"]
        XCTAssertTrue(api.waitForExistence(timeout: 10))
        XCTAssertTrue(api.label.contains("yesterday") || api.label.contains("days ago"), api.label)
        XCTAssertFalse(api.label.contains("AM") || api.label.contains("PM"), api.label)
    }

    // MARK: Kinds and refusals

    func testAnImageOpensAndZooms() throws {
        let app = openBrowser()
        typeWhenFocused(app.textFields["files.search"], "login-flow")
        open(app, row: "docs/images/login-flow.png")
        let zoom = app.staticTexts["viewer.image.zoom"]
        XCTAssertTrue(zoom.waitForExistence(timeout: 10), "the image is open")
        XCTAssertEqual(zoom.label, "Pinch or double-tap to zoom")
        sleep(1)
        shot("viewer-image")

        tapWhenTappable(app.buttons["viewer.image.zoomChoice.actual"], "100%")
        let actual = NSPredicate(format: "label == '100% · double-tap to fit'")
        wait(for: [XCTNSPredicateExpectation(predicate: actual, object: zoom)], timeout: 10)
        tapWhenTappable(app.buttons["viewer.image.zoomChoice.fit"], "Fit")
        let fit = NSPredicate(format: "label == 'Pinch or double-tap to zoom'")
        wait(for: [XCTNSPredicateExpectation(predicate: fit, object: zoom)], timeout: 10)
    }

    func testWhatCannotBeShownSaysWhy() throws {
        let app = openBrowser()
        XCTAssertTrue(app.staticTexts["files.hidden"].waitForExistence(timeout: 10) || any(app, "files.hidden").exists)
        XCTAssertEqual(any(app, "files.hidden").label, "2 files hidden for safety, like .env and keys.")

        // A link out of the folder.
        open(app, row: "notes.md")
        XCTAssertTrue(any(app, "browser.state.outside").waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["This file is outside shop"].exists)
        sleep(1)
        shot("state-outside")
        tapWhenTappable(app.buttons["browser.state.back"], "Back")

        // Not text.
        open(app, row: "data.sqlite")
        XCTAssertTrue(any(app, "viewer.binary").waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["This file is not text"].exists)
        tapWhenTappable(app.buttons["browser.state.back"], "Back")

        // An image over what the phone shows.
        open(app, row: "big-photo.png")
        XCTAssertTrue(any(app, "browser.state.too_large").waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["Too big to show here"].exists)
        tapWhenTappable(app.buttons["browser.state.back"], "Back")

        // A link inside the folder is followed; its name has no .md, so it
        // opens as source.
        open(app, row: "latest")
        XCTAssertTrue(any(app, "viewer.code").waitForExistence(timeout: 10))
    }

    // MARK: Files | Changes

    func testTheSwitchShowsChangesAndComesBack() throws {
        let app = openBrowser()
        let changes = app.buttons["browser.tab.changes"]
        XCTAssertTrue(changes.waitForExistence(timeout: 10))
        let counted = NSPredicate(format: "label == 'Changes, 5 files'")
        wait(for: [XCTNSPredicateExpectation(predicate: counted, object: changes)], timeout: 10)
        tapWhenTappable(changes, "Changes")
        let file = app.buttons["changes.file.docs/reports/login-loop.md"]
        XCTAssertTrue(file.waitForExistence(timeout: 10))
        XCTAssertTrue(file.label.contains("41") || file.label.contains("added"), file.label)
        XCTAssertTrue(app.staticTexts["Everything changed in this folder since the last commit, by any agent. Read only."].exists)
        sleep(1)
        shot("changes-tab")
        tapWhenTappable(app.buttons["browser.tab.files"], "Files")
        XCTAssertTrue(app.buttons["files.row.docs"].waitForExistence(timeout: 10))
    }

    func testTheChangesEntryStillOpensOnChanges() throws {
        let app = openBrowser("changes")
        XCTAssertTrue(app.buttons["changes.file.internal/auth/token.go"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["browser.tab.files"].exists)
        tapWhenTappable(app.buttons["browser.done"], "Done")
        XCTAssertFalse(app.buttons["changes.file.internal/auth/token.go"].waitForExistence(timeout: 2))
    }

    // MARK: A space

    func testASpacesPageOpensTheBrowser() throws {
        let app = launch(extra: ["-sheepdogOpenDemo"])
        let shop = app.buttons["spaces.row.shop"]
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.spaces", until: shop)
        shop.tap()
        let files = app.buttons["space.files"]
        XCTAssertTrue(files.waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["space.changes"].exists)
        sleep(1)
        shot("space-entry")
        files.tap()
        XCTAssertTrue(any(app, "files.list").waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["files.row.docs"].waitForExistence(timeout: 10))
    }

    // MARK: From the design: the agent card and the diff's jump

    func testTheAgentCardOpensTheBrowser() throws {
        let app = launch()
        showAgentsPane(app)
        let files = app.buttons["flock.agent.claude-auth.files"]
        if !files.waitForExistence(timeout: 3) { tapWhenTappable(app.buttons["flock.agent.claude-auth.toggle"], "the card") }
        scrollToVisible(app, files)
        sleep(1)
        shot("entry-card")
        tapWhenTappable(files, "Browse files")
        XCTAssertTrue(any(app, "files.list").waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["files.row.docs"].waitForExistence(timeout: 10))
    }

    func testAFolderWithChangesIsMarkedChanged() throws {
        let app = openBrowser()
        let docs = app.buttons["files.row.docs"]
        XCTAssertTrue(docs.waitForExistence(timeout: 10))
        let marked = NSPredicate(format: "label CONTAINS 'changed'")
        wait(for: [XCTNSPredicateExpectation(predicate: marked, object: docs)], timeout: 10)
        XCTAssertFalse(app.buttons["files.row.web"].label.contains("changed"), "nothing changed in web")
        open(app, row: "docs")
        XCTAssertTrue(app.buttons["files.row.docs/api.md"].waitForExistence(timeout: 10))
        wait(for: [XCTNSPredicateExpectation(predicate: marked, object: app.buttons["files.row.docs/reports"])], timeout: 10)
    }

    func testTheDiffJumpsToAnotherFile() throws {
        let app = openBrowser("changes")
        tapWhenTappable(app.buttons["changes.file.internal/auth/token.go"], "token.go")
        XCTAssertTrue(any(app, "diff.lines").waitForExistence(timeout: 10))
        XCTAssertFalse(app.staticTexts["internal/auth/token.go"].exists, "the path is under the bar once, not again in the diff")
        tapWhenTappable(app.buttons["diff.jump"], "Go to another file")
        let other = app.buttons["diff.jump.docs/reports/login-loop.md"]
        XCTAssertTrue(other.waitForExistence(timeout: 10))
        XCTAssertTrue(other.label.contains("added"), "its counts are there: \(other.label)")
        XCTAssertFalse(other.label.contains("removed"), other.label)
        XCTAssertTrue(app.buttons["diff.jump.internal/auth/token.go"].label.hasSuffix("showing"), "the file being read is marked")
        sleep(1)
        shot("diff-jump")
        tapWhenTappable(other, "login-loop.md")
        XCTAssertTrue(app.staticTexts["login-loop.md"].waitForExistence(timeout: 10), "its diff is open")
        XCTAssertTrue(any(app, "diff.lines").exists)
    }

    // MARK: At the largest text

    func testAtTheLargestTextTheSwitchIsReadableWithItsCount() throws {
        let app = openBrowser(largest: true)
        let tab = any(app, "browser.tab")
        XCTAssertTrue(tab.waitForExistence(timeout: 10))
        let menu = tab.buttons.firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 5))
        XCTAssertTrue(menu.label.contains("Files"), menu.label)
        sleep(1)
        shot("largest-files")
        tapWhenTappable(menu, "the Files | Changes menu")
        let changes = app.buttons["browser.tab.changes"]
        XCTAssertTrue(changes.waitForExistence(timeout: 10))
        let counted = NSPredicate(format: "label CONTAINS 'Changes, 5 files'")
        wait(for: [XCTNSPredicateExpectation(predicate: counted, object: changes)], timeout: 10)
        tapWhenTappable(changes, "Changes")
        XCTAssertTrue(app.buttons["changes.file.docs/reports/login-loop.md"].waitForExistence(timeout: 10) || scrollToVisible(app, app.buttons["changes.file.docs/reports/login-loop.md"]).exists)
        let chosen = any(app, "browser.tab").buttons.firstMatch
        XCTAssertTrue(chosen.label.contains("Changes · 5"), "the choice keeps its count: \(chosen.label)")
        sleep(1)
        shot("largest-changes")
    }

    func testAtTheLargestTextAFileOpensFromTheList() throws {
        let app = openBrowser(largest: true)
        scrollToVisible(app, app.buttons["files.row.docs"])
        tapWhenTappable(app.buttons["files.row.docs"], "docs")
        XCTAssertTrue(any(app, "files.crumb.current").waitForExistence(timeout: 10))
        scrollToVisible(app, app.buttons["files.row.docs/reports"])
        tapWhenTappable(app.buttons["files.row.docs/reports"], "reports")
        scrollToVisible(app, app.buttons["files.row.docs/reports/login-loop.md"])
        tapWhenTappable(app.buttons["files.row.docs/reports/login-loop.md"], "login-loop.md")
        XCTAssertTrue(any(app, "viewer.markdown").waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["login-loop.md"].exists, "the name is the first thing on the page")
        sleep(1)
        shot("largest-viewer")
    }

    func testAtTheLargestTextTheTooBigStateKeepsItsWordsAndBackInReach() throws {
        let app = openBrowser(largest: true)
        scrollToVisible(app, app.buttons["files.row.big-photo.png"])
        tapWhenTappable(app.buttons["files.row.big-photo.png"], "big-photo.png")
        XCTAssertTrue(any(app, "browser.state.too_large").waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["Too big to show here"].exists)
        sleep(1)
        shot("largest-too-big")
        scrollToVisible(app, app.buttons["browser.state.back"])
        tapWhenTappable(app.buttons["browser.state.back"], "Back")
        XCTAssertTrue(any(app, "files.list").waitForExistence(timeout: 10))
    }

    func testAtTheLargestTextASpacesRowsKeepTheirWords() throws {
        let app = launch(extra: ["-sheepdogOpenDemo"], largest: true)
        let shop = app.buttons["spaces.row.shop"]
        tapMenuItem(app, menu: "toolbar.more", item: "toolbar.spaces", until: shop)
        tapWhenTappable(shop, "shop")
        scrollToVisible(app, app.buttons["space.changes"])
        sleep(1)
        shot("largest-space")
        let files = app.buttons["space.files"]
        if !files.isTappable {
            scrollToTop(app)
            scrollToVisible(app, files)
        }
        tapWhenTappable(files, "Files")
        XCTAssertTrue(any(app, "files.list").waitForExistence(timeout: 10))
    }
}
