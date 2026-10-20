import SheepdogKit
import UIKit
import XCTest

/// The live agent window's decisions (`AgentWindowState`), its grid
/// arithmetic, its theme, and the shared `/term` connection (`TermHub`)
/// on the demo's transport. No daemon.
final class AgentWindowTests: XCTestCase {
    typealias S = AgentWindowState

    private func live(_ availability: TermAvailability = .yes) -> S {
        var s = S(paneID: "w1:p2")
        XCTAssertEqual(s.start(availability), [])
        XCTAssertEqual(s.sizeChanged(cols: 58, rows: 40), [.open(mode: .control, takeover: false)])
        s.opened(mode: .control)
        XCTAssertEqual(s.frame(), [])
        return s
    }

    // MARK: Fallback selection by features

    func testAvailabilityComesFromTheHandshake() {
        XCTAssertEqual(TermAvailability.from(handshake: .ready(protocol: 1, daemonVersion: "1", features: ["screen", "term"]), remembered: nil), .yes)
        XCTAssertEqual(TermAvailability.from(handshake: .ready(protocol: 1, daemonVersion: "1", features: ["screen"]), remembered: ["term"]), .no)
        XCTAssertEqual(TermAvailability.from(handshake: .legacy, remembered: nil), .no)
        // No voice session: what this daemon said last time, else try.
        XCTAssertEqual(TermAvailability.from(handshake: .idle, remembered: ["term"]), .yes)
        XCTAssertEqual(TermAvailability.from(handshake: .idle, remembered: ["screen"]), .unknown)
        XCTAssertEqual(TermAvailability.from(handshake: .waiting, remembered: nil), .unknown)
    }

    func testNoTermFeatureDrawsTheSnapshot() {
        var s = S(paneID: "p")
        XCTAssertEqual(s.start(.no), [.startSnapshot])
        XCTAssertEqual(s.phase, .snapshot)
        XCTAssertEqual(s.notice, .snapshotOnly)
        XCTAssertEqual(s.sizeChanged(cols: 58, rows: 40), [], "never opens a channel")
        XCTAssertTrue(s.canOperate, "the snapshot posts keys and prompts")
        XCTAssertFalse(s.takesRawInput)
    }

    func testTheChannelOpensOnceTheSurfaceHasASize() {
        var s = S(paneID: "p")
        XCTAssertEqual(s.start(.yes), [], "no size yet")
        XCTAssertTrue(s.showsSkeleton)
        XCTAssertEqual(s.sizeChanged(cols: 58, rows: 40), [.open(mode: .control, takeover: false)])
        XCTAssertEqual(s.sizeChanged(cols: 50, rows: 40), [], "later sizes are resizes, not opens")
        XCTAssertFalse(s.canOperate, "nothing drawn yet")
        _ = s.frame()
        XCTAssertEqual(s.phase, .live)
        XCTAssertTrue(s.takesRawInput)
        XCTAssertFalse(s.showsSkeleton)
    }

    func testAnUnknownDaemonThatDoesNotAnswerFallsBack() {
        var s = S(paneID: "p")
        _ = s.start(.unknown)
        _ = s.sizeChanged(cols: 58, rows: 40)
        XCTAssertEqual(s.connectionLost(), [.startSnapshot])
        XCTAssertEqual(s.notice, .snapshotOnly)
        // The socket keeps trying; a frame later makes it live.
        XCTAssertEqual(s.frame(), [.stopSnapshot])
        XCTAssertEqual(s.phase, .live)
        XCTAssertNil(s.notice)
    }

    func testADaemonWithTermThatCannotConnectSaysHerdrIsDown() {
        var s = S(paneID: "p")
        _ = s.start(.yes)
        _ = s.sizeChanged(cols: 58, rows: 40)
        XCTAssertEqual(s.connectionLost(), [.startSnapshot])
        XCTAssertEqual(s.notice, .herdrDown)
    }

    func testALiveWindowKeepsItsScreenWhileReconnecting() {
        var s = live()
        XCTAssertEqual(s.connectionLost(), [])
        XCTAssertEqual(s.notice, .reconnecting)
        XCTAssertEqual(s.phase, .live)
        _ = s.frame()
        XCTAssertNil(s.notice)
    }

    func testTheHandshakeArrivingLaterUpgradesASnapshot() {
        var s = S(paneID: "p")
        _ = s.start(.no)
        _ = s.sizeChanged(cols: 58, rows: 40)
        XCTAssertEqual(s.availabilityChanged(.yes), [.open(mode: .control, takeover: false)])
        XCTAssertEqual(s.frame(), [.stopSnapshot])
        XCTAssertEqual(s.phase, .live)
    }

    // MARK: Close reasons

    func testBusyAndTakenOverObserveAndOfferTakeover() {
        for reason in [TermCloseReason.refused("busy"), .takenOver] {
            var s = live()
            XCTAssertEqual(s.closed(reason, canTakeOver: true), [.open(mode: .observe, takeover: false)])
            XCTAssertEqual(s.notice, .otherDevice)
            XCTAssertEqual(s.notice?.action, "Take over")
            s.opened(mode: .observe)
            _ = s.frame()
            XCTAssertEqual(s.phase, .observing)
            XCTAssertEqual(s.notice, .otherDevice, "stays while watching")
            XCTAssertFalse(s.canOperate)
            // Take over: close the observer, open control with takeover.
            XCTAssertEqual(s.takeOver(), [.close, .open(mode: .control, takeover: true)])
            s.opened(mode: .control)
            _ = s.frame()
            XCTAssertEqual(s.phase, .live)
            XCTAssertNil(s.notice)
        }
    }

    func testGoneEndsTheWindow() {
        var s = live()
        XCTAssertEqual(s.closed(.gone), [])
        XCTAssertEqual(s.phase, .ended)
        XCTAssertEqual(s.notice, .gone)
        XCTAssertEqual(s.notice?.text, "This agent closed.")
        XCTAssertFalse(s.canOperate)
        XCTAssertEqual(s.becameActive(), [], "nothing reopens")
        XCTAssertEqual(s.retry(), [])
    }

    func testNotTrackedEnds() {
        var s = live()
        _ = s.closed(.refused("not_tracked"))
        XCTAssertEqual(s.phase, .ended)
        XCTAssertEqual(s.notice, .notTracked)
    }

    func testHerdrFallsBackToTheSnapshotAndCanRetry() {
        var s = live()
        XCTAssertEqual(s.closed(.herdr), [.startSnapshot])
        XCTAssertEqual(s.phase, .snapshot)
        XCTAssertEqual(s.notice?.text, "herdr is not running: start herdr on your computer.")
        XCTAssertEqual(s.retry(), [.open(mode: .control, takeover: false)])
        XCTAssertEqual(s.frame(), [.stopSnapshot])
        XCTAssertEqual(s.phase, .live)
    }

    func testTooManyAndUnknownReasonsFallBack() {
        var s = live()
        XCTAssertEqual(s.closed(.refused("too_many")), [.startSnapshot])
        XCTAssertEqual(s.notice, .tooMany)
        var t = live()
        XCTAssertEqual(t.closed(.other("something new")), [.startSnapshot])
        XCTAssertEqual(t.notice, .snapshotOnly)
        var u = live()
        XCTAssertEqual(u.closed(.released), [])
    }

    // MARK: Background and foreground

    func testBackgroundDetachesAndForegroundReattaches() {
        var s = live()
        XCTAssertEqual(s.enteredBackground(), [.close])
        XCTAssertFalse(s.channelOpen)
        XCTAssertEqual(s.enteredBackground(), [], "once")
        XCTAssertEqual(s.becameActive(), [.open(mode: .control, takeover: false)])
        XCTAssertEqual(s.becameActive(), [], "once")
    }

    func testObservingReattachesAsAnObserver() {
        var s = live()
        _ = s.closed(.takenOver)
        s.opened(mode: .observe)
        _ = s.frame()
        XCTAssertEqual(s.enteredBackground(), [.close])
        XCTAssertEqual(s.becameActive(), [.open(mode: .observe, takeover: false)])
    }

    func testTheSnapshotStopsInTheBackground() {
        var s = S(paneID: "p")
        _ = s.start(.no)
        XCTAssertEqual(s.enteredBackground(), [.stopSnapshot])
        XCTAssertEqual(s.becameActive(), [.startSnapshot])
    }

    func testClosingTheWindowReleasesEverything() {
        var s = live()
        XCTAssertEqual(s.disappeared(), [.close])
        var t = S(paneID: "p")
        _ = t.start(.no)
        XCTAssertEqual(t.disappeared(), [.stopSnapshot])
    }

    // MARK: Scrolling

    func testDraggingDownShowsOlderOutputAndLatestReturns() {
        var s = live()
        XCTAssertFalse(s.showsLatest)
        XCTAssertEqual(s.dragged(lines: 3), [.scroll(.up, lines: 3)], "finger down = older = dir up")
        XCTAssertEqual(s.dragged(lines: 2), [.scroll(.up, lines: 2)])
        XCTAssertTrue(s.showsLatest)
        XCTAssertEqual(s.dragged(lines: -1), [.scroll(.down, lines: 1)])
        XCTAssertEqual(s.scrolledUp, 4)
        XCTAssertEqual(s.latest(), [.scroll(.down, lines: 4)])
        XCTAssertFalse(s.showsLatest)
        XCTAssertEqual(s.latest(), [])
        XCTAssertEqual(s.dragged(lines: -5), [.scroll(.down, lines: 5)])
        XCTAssertEqual(s.scrolledUp, 0, "never below the bottom")
    }

    func testInputGoesBackToTheBottomFirst() {
        var s = live()
        _ = s.dragged(lines: 6)
        XCTAssertEqual(s.willSendInput(), [.scroll(.down, lines: 6)])
        XCTAssertEqual(s.willSendInput(), [])
    }

    func testNoScrollingWithoutAChannel() {
        var s = S(paneID: "p")
        _ = s.start(.no)
        XCTAssertEqual(s.dragged(lines: 3), [])
    }

    // MARK: Keys

    func testKeyBarBytes() {
        XCTAssertEqual(AgentKey.bar, [.esc, .tab, .up, .down, .ctrlC, .enter])
        XCTAssertEqual(AgentKey.esc.bytes, [0x1b])
        XCTAssertEqual(AgentKey.tab.bytes, [0x09])
        XCTAssertEqual(AgentKey.up.bytes, Array("\u{1b}[A".utf8))
        XCTAssertEqual(AgentKey.down.bytes, Array("\u{1b}[B".utf8))
        XCTAssertEqual(AgentKey.ctrlC.bytes, [0x03])
        XCTAssertEqual(AgentKey.enter.bytes, [0x0d])
        XCTAssertEqual(AgentKey.ctrlC.accessibilityLabel, "Control C")
        XCTAssertEqual(AgentKey.esc.accessibilityLabel, "Escape")
    }

    // MARK: Grid

    func testGridArithmetic() {
        let g = TerminalGeometry(cellWidth: 6.6, cellHeight: 14)
        XCTAssertEqual(g.grid(for: CGSize(width: 390, height: 600)).cols, 59)
        XCTAssertEqual(g.grid(for: CGSize(width: 390, height: 600)).rows, 42)
        XCTAssertEqual(g.grid(for: .zero).cols, 1, "never empty")
        XCTAssertEqual(g.cell(at: CGPoint(x: 13.3, y: 29), cols: 59, rows: 42).col, 2)
        XCTAssertEqual(g.cell(at: CGPoint(x: 13.3, y: 29), cols: 59, rows: 42).row, 2)
        XCTAssertEqual(g.cell(at: CGPoint(x: 9999, y: -5), cols: 59, rows: 42).col, 58, "clamped")
        XCTAssertEqual(g.cell(at: CGPoint(x: 9999, y: -5), cols: 59, rows: 42).row, 0)
        XCTAssertEqual(g.lines(forDrag: 30), 2)
        XCTAssertEqual(g.lines(forDrag: -30), -2)
        XCTAssertEqual(g.lines(forDrag: 13), 0)
    }

    /// SF Mono 11 pt fits about 58 columns across an iPhone 17/18 Pro in
    /// portrait (402 pt less the window's 12 pt of padding).
    func testElevenPointFitsAboutFiftyEightColumns() {
        let cols = TerminalRenderer.geometry(fontSize: 11).grid(for: CGSize(width: 390, height: 600)).cols
        XCTAssertTrue((55 ... 60).contains(cols), "\(cols) columns")
        let bigger = TerminalRenderer.geometry(fontSize: 14).grid(for: CGSize(width: 390, height: 600)).cols
        XCTAssertLessThan(bigger, cols, "pinch out: fewer columns")
    }

    @MainActor
    func testTheSurfaceReportsTheGridTheGeometryPredicts() {
        let surface = TerminalRenderer.makeSurface(fontSize: 11)
        var reported: (Int, Int)?
        surface.onSizeChange = { reported = ($0, $1) }
        let size = CGSize(width: 390, height: 600)
        surface.view.frame = CGRect(origin: .zero, size: size)
        surface.view.layoutIfNeeded()
        surface.view.subviews.first?.layoutIfNeeded()
        let predicted = TerminalRenderer.geometry(fontSize: 11).grid(for: size)
        XCTAssertEqual(surface.cols, predicted.cols)
        XCTAssertEqual(surface.rows, predicted.rows)
        XCTAssertEqual(reported?.0, predicted.cols)
        // A new font size is a new grid, reported once.
        surface.setFontSize(14)
        let bigger = TerminalRenderer.geometry(fontSize: 14).grid(for: size)
        XCTAssertEqual(surface.cols, bigger.cols)
        XCTAssertEqual(reported?.0, bigger.cols)
        XCTAssertEqual(reported?.1, bigger.rows)
    }

    // MARK: Snapshot in the terminal

    @MainActor
    func testTheSnapshotDrawsItsLastRows() {
        let bytes = SnapshotScreen.bytes(text: "one\ntwo\nthree\nfour\n", rows: 3)
        let grid = TerminalRenderer.makeGrid(cols: 20, rows: 3)
        grid.feed(bytes)
        XCTAssertEqual(grid.snapshotText(), "two\nthree\nfour")
        // A line wider than the grid is clipped, not wrapped.
        let wide = TerminalRenderer.makeGrid(cols: 5, rows: 2)
        wide.feed(SnapshotScreen.bytes(text: "abcdefghij\nxy", rows: 2))
        XCTAssertEqual(wide.lineText(1), "xy")
    }

    // MARK: Theme

    func testThePaletteReadsOnBothBackgrounds() {
        for theme in [TerminalTheme.light, .dark] {
            XCTAssertEqual(theme.ansi.count, 16)
            XCTAssertGreaterThanOrEqual(theme.foreground.contrast(with: theme.background), 7)
            // Black on light and the dimmest colours on dark are allowed to
            // be quiet (they are dim text by design); the rest must read.
            for (i, colour) in theme.ansi.enumerated() {
                let ratio = colour.contrast(with: theme.background)
                XCTAssertGreaterThanOrEqual(ratio, 3, "colour \(i) on \(theme == .light ? "light" : "dark"): \(ratio)")
            }
        }
        XCTAssertNotEqual(TerminalTheme.light.background, TerminalRGB(255, 255, 255), "no pure white")
        XCTAssertNotEqual(TerminalTheme.dark.background, TerminalRGB(0, 0, 0), "no pure black")
    }

    // MARK: Hub and demo

    @MainActor
    func testTheHubSharesOneConnectionAndRoutesEachChannel() async throws {
        let world = DemoWorld(now: Date())
        let hub = TermHub(endpoint: DemoDaemon.endpoint, makeTransport: { _ in
            DemoTermTransport { pane in world.screen(paneID: pane) }
        })
        var a: [TermSocket.Event] = []
        var b: [TermSocket.Event] = []
        let chA = try XCTUnwrap(hub.open(paneID: DemoWorld.authPane, mode: .control, cols: 40, rows: 20) { a.append($0) })
        let chB = try XCTUnwrap(hub.open(paneID: DemoWorld.docsPane, mode: .control, cols: 30, rows: 10) { b.append($0) })
        XCTAssertNotEqual(chA, chB)
        try await waitUntil { a.contains { if case .frame = $0 { return true }; return false } && b.contains { if case .frame = $0 { return true }; return false } }
        for case let .frame(f) in a { XCTAssertEqual(f.ch, chA) }
        for case let .frame(f) in b { XCTAssertEqual(f.ch, chB); XCTAssertEqual(f.cols, 30) }
        XCTAssertEqual(hub.openChannels.count, 2)

        // A resize comes back as a full frame at the new size.
        a.removeAll()
        hub.resize(ch: chA, cols: 44, rows: 20)
        try await waitUntil { a.contains { if case let .frame(f) = $0 { return f.full && f.cols == 44 }; return false } }

        hub.close(ch: chA)
        hub.close(ch: chB)
        XCTAssertEqual(hub.openChannels, [])
        hub.disconnectIfIdle()
        XCTAssertEqual(hub.state, .idle)
    }

    @MainActor
    func testTheHubRefusesPastTheChannelLimit() throws {
        let world = DemoWorld(now: Date())
        let hub = TermHub(endpoint: DemoDaemon.endpoint, makeTransport: { _ in
            DemoTermTransport { pane in world.screen(paneID: pane) }
        })
        for _ in 0 ..< 4 {
            XCTAssertNotNil(hub.open(paneID: DemoWorld.authPane, mode: .observe, cols: 40, rows: 20) { _ in })
        }
        XCTAssertNil(hub.open(paneID: DemoWorld.authPane, mode: .observe, cols: 40, rows: 20) { _ in })
    }

    func testTheDemoScreenFitsTheGrid() {
        let world = DemoWorld(now: Date())
        let screen = try! XCTUnwrap(world.screen(paneID: DemoWorld.authPane))
        let lines = DemoTerminalScreen.lines(for: screen, cols: 30, rows: 12, typed: "hi", tick: 3)
        XCTAssertEqual(lines.count, 12)
        let grid = TerminalRenderer.makeGrid(cols: 30, rows: 12)
        for (i, line) in lines.enumerated() { grid.feed(Array("\u{1b}[\(i + 1);1H\(line)".utf8)) }
        XCTAssertTrue(grid.snapshotText().contains("> hi"))
        XCTAssertTrue(grid.snapshotText().contains("Fix the login"))
    }

    private func waitUntil(timeout: TimeInterval = 3, _ condition: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(timeout)
        while !condition() {
            if Date() > deadline { throw URLError(.timedOut) }
            try await Task.sleep(nanoseconds: 20_000_000)
        }
    }
}
