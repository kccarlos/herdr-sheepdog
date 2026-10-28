import XCTest

/// The floating question, inline or compact (docs/dev/design/conversation-timeline.md,
/// "When a question goes compact"), in the offline demo: no daemon. The
/// demo's command approval fits inline; `-sheepdogDemoLongQuestion` asks a
/// three-option question with descriptions instead, which goes to the
/// bar, and the sheet answers it through the same path as a card.
///
/// With `TEST_RUNNER_SHEEPDOG_QUESTION_SHOTS=<directory>` (and
/// `TEST_RUNNER_SHEEPDOG_SHOTS_SUFFIX`, e.g. `iphone-light`) each test also
/// saves `question-<screen>-<suffix>.png` there.
final class QuestionPresentationUITests: XCTestCase {
    private let environment = ProcessInfo.processInfo.environment
    /// `TEST_RUNNER_SHEEPDOG_SHOTS_LARGE_TEXT=1`: run at the largest text
    /// size, for the screenshots.
    private var largestText: Bool { environment["SHEEPDOG_SHOTS_LARGE_TEXT"] == "1" }
    private var textSize: [String] {
        largestText ? ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] : []
    }

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    func testShortQuestionFloatsInline() throws {
        let app = launchDemo([])
        // codex-payments asks after about 16 s.
        if largestText {
            XCTAssertTrue(app.buttons["questions.answer"].waitForExistence(timeout: 40), "the largest sizes always use the bar")
        } else {
            XCTAssertTrue(app.buttons["approval.option.1"].waitForExistence(timeout: 40), "the approval floats inline")
            XCTAssertFalse(app.buttons["questions.answer"].exists, "no compact bar for a short question")
        }
        sleep(1)
        shot("short")
    }

    /// The largest text sizes: even the short approval goes to the bar
    /// (the accessibility rule, folded into the same decision).
    func testLargestTextUsesTheBar() throws {
        let app = launchDemo(["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"])
        let bar = app.buttons["questions.answer"]
        XCTAssertTrue(bar.waitForExistence(timeout: 40))
        XCTAssertFalse(app.buttons["approval.option.1"].exists)
        tapWhenTappable(bar, "the Answer bar")
        XCTAssertTrue(app.buttons["approval.option.1"].waitForExistence(timeout: 10), "the sheet shows the options")
    }

    func testLongQuestionGoesCompactAndTheSheetAnswers() throws {
        let app = launchDemo(["-sheepdogDemoLongQuestion"])
        if UIDevice.current.userInterfaceIdiom == .pad {
            // The iPad's taller column has room: the same question stays
            // inline, unless the text is at the largest sizes.
            let first = app.buttons["approval.option.1"]
            let bar = app.buttons["questions.answer"]
            let deadline = Date().addingTimeInterval(40)
            while !first.exists, !bar.exists, Date() < deadline { usleep(200_000) }
            sleep(1)
            shot("long")
            if bar.exists {
                tapWhenTappable(bar, "the Answer bar")
                XCTAssertTrue(first.waitForExistence(timeout: 10))
                sleep(1)
                shot("sheet")
            }
            XCTAssertTrue(first.label.hasPrefix("Approve: Retry with backoff, recommended."), first.label)
            return
        }
        let bar = app.buttons["questions.answer"]
        XCTAssertTrue(bar.waitForExistence(timeout: 40), "the long question collapses to the bar")
        XCTAssertTrue(bar.label.hasPrefix("codex-payments asked: codex-payments asks how the payment webhook"), bar.label)
        XCTAssertTrue(bar.label.hasSuffix("Answer."), bar.label)
        XCTAssertFalse(app.buttons["approval.option.1"].exists, "its options wait in the sheet")
        // The timeline still says a question is waiting.
        XCTAssertTrue(app.descendants(matching: .any)["bubble.pointer"].waitForExistence(timeout: 10), "the amber pointer row stays")
        sleep(1)
        shot("compact")

        tapWhenTappable(bar, "the Answer bar")
        let first = app.buttons["approval.option.1"]
        XCTAssertTrue(first.waitForExistence(timeout: 10), "the sheet shows the options")
        XCTAssertTrue(first.label.hasPrefix("Approve: Retry with backoff, recommended. Install p-retry"), first.label)
        XCTAssertTrue(app.buttons["approval.option.3"].exists)
        sleep(1)
        shot("sheet")

        tapWhenTappable(first, "the recommended option")
        let gone = NSPredicate(format: "exists == false")
        expectation(for: gone, evaluatedWith: app.buttons["questions.answer"])
        expectation(for: gone, evaluatedWith: app.buttons["approval.option.1"])
        waitForExpectations(timeout: 20)
    }

    private func launchDemo(_ extra: [String]) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogDemoSilent", "-sheepdogOpenDemo", "-sheepdogNoAnimations"] + textSize + extra
        app.launch()
        XCTAssertTrue(app.descendants(matching: .any)["demo.header"].waitForExistence(timeout: 15), "the demo opened")
        return app
    }

    private func shot(_ name: String) {
        guard let directory = environment["SHEEPDOG_QUESTION_SHOTS"] else { return }
        let suffix = environment["SHEEPDOG_SHOTS_SUFFIX"] ?? "light"
        let screenshot = XCUIScreen.main.screenshot()
        try? screenshot.pngRepresentation.write(to: URL(fileURLWithPath: directory).appendingPathComponent("question-\(name)-\(suffix).png"))
    }
}
