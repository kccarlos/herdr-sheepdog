import SheepdogKit
import XCTest

/// The phone shell (docs/dev/design/phone-terminal.md section 5, "Shell"):
/// the window's decisions for a shell, its words, the hub's shell target
/// and `term-ready.shell`, on the demo's transport. No daemon.
final class PhoneShellTests: XCTestCase {
    typealias S = AgentWindowState

    private func opening() -> S {
        var s = S(paneID: "shell:~/code/app", isShell: true)
        XCTAssertEqual(s.start(.yes), [])
        XCTAssertEqual(s.sizeChanged(cols: 58, rows: 30), [.open(mode: .control, takeover: false)])
        return s
    }

    func testAShellGoesLiveAndTakesRawInput() {
        var s = opening()
        s.opened(mode: .control)
        XCTAssertEqual(s.frame(), [])
        XCTAssertEqual(s.phase, .live)
        XCTAssertTrue(s.takesRawInput)
    }

    func testShellRefusalsEndTheWindowWithTheirOwnWords() {
        var off = opening()
        XCTAssertEqual(off.closed(.refused("shell_off")), [])
        XCTAssertEqual(off.phase, .ended)
        XCTAssertEqual(off.notice, .shellOff)
        XCTAssertEqual(PhoneShell.endedDescription(off.notice), PhoneShell.offExplanation)
        XCTAssertTrue(PhoneShell.offExplanation.contains("Tailscale"))
        XCTAssertTrue(PhoneShell.offExplanation.contains("setup page"))

        var folder = opening()
        _ = folder.closed(.refused("outside_projects"))
        XCTAssertEqual(folder.notice, .shellFolder)
        XCTAssertTrue(PhoneShell.endedDescription(folder.notice).contains("project_roots"))

        var gone = opening()
        _ = gone.closed(.gone)
        XCTAssertEqual(gone.notice, .shellClosed)
        XCTAssertEqual(gone.notice?.text, "This shell closed.")
    }

    func testAShellNeverFallsBackToTheSnapshot() {
        var herdr = opening()
        XCTAssertEqual(herdr.closed(.herdr), [], "a shell is no agent: no screen to poll")
        XCTAssertEqual(herdr.notice, .herdrDown)
        XCTAssertFalse(herdr.snapshotRunning)
        XCTAssertEqual(herdr.retry(), [.open(mode: .control, takeover: false)])

        var lost = opening()
        XCTAssertEqual(lost.connectionLost(), [])
        XCTAssertEqual(lost.notice, .reconnecting)
        XCTAssertFalse(lost.snapshotRunning)

        // Back from the background after a refusal: reopen, never poll.
        var back = opening()
        _ = back.closed(.refused("too_many"))
        _ = back.enteredBackground()
        XCTAssertEqual(back.becameActive(), [.open(mode: .control, takeover: false)])
    }

    func testAnAgentWindowIsUnchanged() {
        var s = S(paneID: "w1:p2")
        _ = s.start(.yes)
        _ = s.sizeChanged(cols: 58, rows: 30)
        XCTAssertEqual(s.closed(.gone), [])
        XCTAssertEqual(s.notice, .gone)
        var h = S(paneID: "w1:p2")
        _ = h.start(.yes)
        _ = h.sizeChanged(cols: 58, rows: 30)
        XCTAssertEqual(h.closed(.herdr), [.startSnapshot])
    }

    func testTitles() {
        XCTAssertEqual(PhoneShell.title(cwd: "/Users/me/code/sheepdog-bench"), "Shell · sheepdog-bench")
        XCTAssertEqual(PhoneShell.title(cwd: "~/Documents/shop/"), "Shell · shop")
        XCTAssertEqual(PhoneShell.title(cwd: nil), "Shell")
        XCTAssertEqual(PhoneShell.title(cwd: " "), "Shell")
        XCTAssertEqual(PhoneShell.title(cwd: "/"), "Shell")
    }

    func testAgentsCarryTheirFolder() throws {
        let json = #"{"pane_id":"w1:p2","alias":"auth","state":"working","cwd":"/Users/me/code/app"}"#
        let agent = try JSONDecoder().decode(Agent.self, from: Data(json.utf8))
        XCTAssertEqual(agent.cwd, "/Users/me/code/app")
        let old = try JSONDecoder().decode(Agent.self, from: Data(#"{"pane_id":"w1:p2","alias":"auth","state":"idle"}"#.utf8))
        XCTAssertNil(old.cwd)
    }

    // MARK: Hub and demo

    @MainActor
    func testTheHubOpensAShellAndReportsTheOffer() async throws {
        let world = DemoWorld(now: Date())
        let hub = TermHub(endpoint: DemoDaemon.endpoint, makeTransport: { _ in
            DemoTermTransport { pane in world.screen(paneID: pane) }
        })
        var offered: [Bool] = []
        hub.onReady = { offered.append($0.shell) }
        XCTAssertNil(hub.shellOffered)
        var events: [TermSocket.Event] = []
        let ch = try XCTUnwrap(hub.open(target: .newShell(cwd: "~/Documents/shop"), mode: .control, cols: 40, rows: 12) { events.append($0) })
        try await waitUntil { events.contains { if case .frame = $0 { return true }; return false } }
        XCTAssertEqual(offered, [true])
        XCTAssertEqual(hub.shellOffered, true)
        let opened = events.compactMap { if case let .opened(_, o) = $0 { return o }; return nil }.first
        XCTAssertEqual(opened?.kind, "shell")
        XCTAssertTrue(opened?.paneID.hasPrefix("demo-shell-") == true)

        // Typing a command answers like a shell would (the demo runs none).
        events.removeAll()
        XCTAssertTrue(hub.send(input: Array("ls\r".utf8), ch: ch))
        try await waitUntil { events.contains { if case .frame = $0 { return true }; return false } }
        hub.close(ch: ch)
        hub.disconnectIfIdle()
    }

    @MainActor
    func testAWarmUpLearnsTheOfferWithoutAWindow() async throws {
        let world = DemoWorld(now: Date())
        let hub = TermHub(socket: TermSocket(endpoint: DemoDaemon.endpoint, makeTransport: { _ in
            DemoTermTransport(behavior: .noShell) { pane in world.screen(paneID: pane) }
        }), linger: 0.2)
        var offered: [Bool] = []
        hub.onReady = { offered.append($0.shell) }
        hub.warmUp()
        try await waitUntil { !offered.isEmpty }
        XCTAssertEqual(offered, [false])
        XCTAssertEqual(hub.shellOffered, false)
        // No window opened: the connection goes after the linger.
        try await waitUntil { hub.state == .idle }

        // A shell asked for anyway is refused, not opened.
        var events: [TermSocket.Event] = []
        _ = hub.open(target: .newShell(cwd: nil), mode: .control, cols: 40, rows: 12) { events.append($0) }
        try await waitUntil { events.contains { if case .closed = $0 { return true }; return false } }
        let reason = events.compactMap { if case let .closed(_, r, _) = $0 { return r }; return nil }.first
        XCTAssertEqual(reason, .refused("shell_off"))
    }

    private func waitUntil(timeout: TimeInterval = 3, _ condition: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(timeout)
        while !condition() {
            if Date() > deadline { throw URLError(.timedOut) }
            try await Task.sleep(nanoseconds: 20_000_000)
        }
    }
}
