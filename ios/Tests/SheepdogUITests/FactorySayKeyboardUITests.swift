import XCTest

/// Regression UI test for the factory project board's "Tell the <project> factory"
/// composer: a successful send dismisses the keyboard so the "Told …" confirmation
/// is visible and hittable, while a failed send keeps the draft text and focus.
///
/// `TEST_RUNNER_SHEEPDOG_FIX_TOLD_SHOTS=<dir>` saves a screenshot after send.
final class FactorySayKeyboardUITests: XCTestCase {
    override func setUpWithError() throws { continueAfterFailure = false }

    private static let runningNotes = #"[{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"running","counts":{"working":1,"done":1},"folder":"~/code/notes","coordinator_pane":"coord-notes-1"}]"#
    private static let sampleGraph = #"{"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[{"ref":"T8","title":"feat: tags","stage":"working","why_waiting":"Worker is working","after":[],"dependents":[],"layer":0,"rounds":0,"run_role":"worker","run_cli":"codex","steps":[{"name":"planned","state":"done"},{"name":"implementing","state":"now"}]},{"ref":"T14","title":"feat: search filters","stage":"done","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[{"name":"planned","state":"done"},{"name":"merged","state":"done"}]}]}"#

    private func launch(host: String) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-sheepdogResetState", "-sheepdogNoAnimations", "-sheepdogComputers", "Work Mac=\(host)"]
        app.launch()
        return app
    }

    private func shot(_ name: String) {
        guard let dir = ProcessInfo.processInfo.environment["SHEEPDOG_FIX_TOLD_SHOTS"] else { return }
        let url = URL(fileURLWithPath: dir).appendingPathComponent("\(name).png")
        try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: url)
    }

    func testSayDismissesKeyboardAndRevealsToldStatusWhileFailureKeepsText() throws {
        let stub = try StubDaemon(.computer(name: "Work Mac", agents: "[]", questions: "[]", factory: Self.runningNotes))
        stub.factoryGraphs["proj-1"] = Self.sampleGraph
        stub.sayHandler = { _, _ in (503, #"{"error":"coordinator busy"}"#) }
        let host = try stub.start()
        defer { stub.stop() }

        let app = launch(host: host)
        showAgentsPane(app)
        tapWhenTappable(scrollToVisible(app, app.descendants(matching: .any)["flock.factory.proj-1"].firstMatch), "notes head")
        XCTAssertTrue(app.descendants(matching: .any)["factoryProject.board"].waitForExistence(timeout: 15))

        let field = app.textFields["factoryProject.say.field"].firstMatch
        let send = app.buttons["factoryProject.say.send"].firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 10))

        // Failed send keeps the unsent text and focus.
        typeWhenFocused(field, "Hold the release")
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "keyboard must appear while typing")
        tapWhenTappable(send, "Send failing instruction")
        let status = app.descendants(matching: .any)["factoryProject.say.status"].firstMatch
        XCTAssertTrue(status.waitForExistence(timeout: 10))
        XCTAssertTrue(status.label.contains("coordinator busy"), "expected refusal status, got: \(status.label)")
        XCTAssertEqual(field.value as? String, "Hold the release", "failed send must keep the unsent text")
        XCTAssertGreaterThan(app.keyboards.count, 0, "failed send should keep the keyboard focused")

        // Successful send dismisses the keyboard so "Told …" is visible and hittable.
        stub.sayHandler = nil
        tapWhenTappable(send, "Send instruction")
        let told = app.descendants(matching: .any).matching(NSPredicate(format: "identifier == 'factoryProject.say.status' AND label == %@", "Told the notes factory.")).firstMatch
        XCTAssertTrue(told.waitForExistence(timeout: 10))
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5), "keyboard must dismiss after a successful send")
        XCTAssertEqual(app.keyboards.count, 0, "keyboard must be gone after send")
        XCTAssertTrue(told.isHittable, "\"Told …\" confirmation must be hittable after send")
        let value = field.value as? String
        XCTAssertTrue(value == nil || value == "" || value == "An instruction, in your own words", "field must clear after send, got: \(String(describing: value))")
        XCTAssertTrue(stub.sayBodies().contains { $0.project == "proj-1" && $0.body.contains("Hold the release") })
        shot("after-send")
    }
}
