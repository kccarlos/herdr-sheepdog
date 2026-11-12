import XCTest

/// Opening another paired computer's agent terminal window without moving the
/// voice session or changing the current computer. Uses the two fixture daemons
/// when running (`Fixture.host` and `Fixture.pairHost`) and falls back to two
/// `StubDaemon` instances otherwise.
final class RemoteAgentWindowUITests: XCTestCase {
    private let pane = "p-remote-shared"
    private var usingFixtures = false
    private var workStub: StubDaemon?
    private var homeStub: StubDaemon?

    override func setUpWithError() throws {
        continueAfterFailure = false
        if status(Fixture.host, "GET", "/api/hello") == 200,
           status(Fixture.pairHost, "GET", "/api/hello") == 200 {
            usingFixtures = true
            status(Fixture.host, "POST", "/api/inject/remove", ["pane_id": pane])
            status(Fixture.pairHost, "POST", "/api/inject/remove", ["pane_id": pane])
        }
    }

    override func tearDownWithError() throws {
        if usingFixtures {
            status(Fixture.host, "POST", "/api/inject/remove", ["pane_id": pane])
            status(Fixture.pairHost, "POST", "/api/inject/remove", ["pane_id": pane])
        }
        workStub?.stop()
        homeStub?.stop()
        workStub = nil
        homeStub = nil
        try super.tearDownWithError()
    }

    func testOpenRemoteComputerAgentWindowWithoutMovingVoice() throws {
        let workScreen = "WORK_MAC_TERMINAL_111\n• Running work suite\n"
        let homeScreen = "HOME_MAC_TERMINAL_222\n• Running home suite\n"

        let workHost: String
        let homeHost: String
        if usingFixtures {
            status(Fixture.host, "POST", "/api/inject/blocked", ["pane_id": pane, "alias": "work-auth", "kind": "codex", "screen": workScreen])
            status(Fixture.host, "POST", "/api/inject/state", ["pane_id": pane, "state": "working", "screen": workScreen])
            status(Fixture.pairHost, "POST", "/api/inject/blocked", ["pane_id": pane, "alias": "home-auth", "kind": "codex", "screen": homeScreen])
            status(Fixture.pairHost, "POST", "/api/inject/state", ["pane_id": pane, "state": "working", "screen": homeScreen])
            workHost = Fixture.host
            homeHost = Fixture.pairHost
        } else {
            let work = try StubDaemon(.computer(
                name: "Work Mac",
                agents: #"[{"pane_id":"\#(pane)","alias":"work-auth","kind":"codex","state":"working","workspace_label":"webapp"}]"#,
                questions: "[]"
            ))
            work.screens[pane] = workScreen
            let home = try StubDaemon(.computer(
                name: "Home Mac",
                agents: #"[{"pane_id":"\#(pane)","alias":"home-auth","kind":"codex","state":"working","workspace_label":"blog"}]"#,
                questions: "[]"
            ))
            home.screens[pane] = homeScreen
            workStub = work
            homeStub = home
            workHost = try work.start()
            homeHost = try home.start()
        }

        let app = XCUIApplication()
        app.launchArguments = [
            "-sheepdogResetState",
            "-sheepdogNoAnimations",
            "-sheepdogComputers",
            "Work Mac=\(workHost);Home Mac=\(homeHost)",
        ]
        app.launch()

        showAgentsPane(app)

        let workMarker = app.descendants(matching: .any).matching(identifier: "computer.voiceMarker.fixture-1").firstMatch
        let homeMarker = app.descendants(matching: .any).matching(identifier: "computer.voiceMarker.fixture-2").firstMatch
        XCTAssertTrue(workMarker.waitForExistence(timeout: 10))
        XCTAssertTrue(workMarker.isSelected)
        XCTAssertEqual(workMarker.label, "Voice is on Work Mac")
        XCTAssertEqual(homeMarker.label, "Move the voice to Home Mac")

        let openHome = app.buttons["flock.agent.home-auth.window"]
        XCTAssertTrue(openHome.waitForExistence(timeout: 15), "Home Mac's agent card has an Open window button")
        tapWhenTappable(openHome, "Open Home Mac agent window")

        let terminal = app.descendants(matching: .any)["agentScreen.terminal"]
        XCTAssertTrue(terminal.waitForExistence(timeout: 10))
        expectation(for: NSPredicate(format: "value CONTAINS 'HOME_MAC_TERMINAL_222'"), evaluatedWith: terminal)
        waitForExpectations(timeout: 15)
        XCTAssertFalse((terminal.value as? String ?? "").contains("WORK_MAC_TERMINAL_111"))

        let windowComputer = app.descendants(matching: .any).matching(identifier: "computerLabel.window").firstMatch
        XCTAssertTrue(windowComputer.waitForExistence(timeout: 5))
        XCTAssertTrue(windowComputer.label.contains("Home Mac"), windowComputer.label)

        // Pop back to Agents and verify the voice marker never moved off Work Mac.
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(workMarker.waitForExistence(timeout: 10))
        XCTAssertTrue(workMarker.isSelected)
        XCTAssertEqual(workMarker.label, "Voice is on Work Mac")
        XCTAssertEqual(homeMarker.label, "Move the voice to Home Mac")
    }

    @discardableResult
    private func status(_ host: String, _ method: String, _ path: String, _ body: [String: Any]? = nil) -> Int {
        var request = URLRequest(url: URL(string: "http://\(host)\(path)")!)
        request.httpMethod = method
        request.timeoutInterval = 2
        if let body {
            request.httpBody = try? JSONSerialization.data(withJSONObject: body)
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        var code = 0
        let done = expectation(description: "\(method) \(host)\(path)")
        URLSession.shared.dataTask(with: request) { _, response, _ in
            code = (response as? HTTPURLResponse)?.statusCode ?? 0
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 5)
        return code
    }
}
