import XCTest
import SheepdogKit
@testable import SheepdogMac

final class DaemonLocatorTests: XCTestCase {
    private func defaults() -> UserDefaults {
        let suite = "sheepdog-mac-tests-\(UUID().uuidString)"
        let d = UserDefaults(suiteName: suite)!
        d.removePersistentDomain(forName: suite)
        return d
    }

    func testDefaultsToLoopbackOnTheDefaultPort() {
        let e = DaemonLocator(defaults: defaults()).endpoint
        XCTAssertEqual(e.host, "127.0.0.1")
        XCTAssertEqual(e.port, 7777)
    }

    func testUserDefaultsOverrideHostAndPort() {
        let d = defaults()
        d.set("localhost", forKey: DaemonLocator.hostKey)
        d.set(7801, forKey: DaemonLocator.portKey)
        let e = DaemonLocator(defaults: d).endpoint
        XCTAssertEqual(e.host, "localhost")
        XCTAssertEqual(e.port, 7801)
    }

    func testANonsensePortOrEmptyHostFallsBack() {
        let d = defaults()
        d.set("", forKey: DaemonLocator.hostKey)
        d.set(70000, forKey: DaemonLocator.portKey)
        let e = DaemonLocator(defaults: d).endpoint
        XCTAssertEqual(e.host, "127.0.0.1")
        XCTAssertEqual(e.port, 7777)
    }
}

final class PanelOpenerTests: XCTestCase {
    func testRunsHerdrAppDoModOpenThroughEnvWithAnInstallSearchPath() throws {
        let launcher = FakeLauncher()
        try PanelOpener(launcher: launcher, environment: ["PATH": "/usr/bin:/bin"]).openPanel()
        let launch = try XCTUnwrap(launcher.launches.first)
        XCTAssertEqual(launch.executable, "/usr/bin/env")
        XCTAssertEqual(launch.arguments, ["herdr-app", "do", "mod.open", "--name", "sheepdog"])
        let path = try XCTUnwrap(launch.path)
        XCTAssertTrue(path.hasPrefix("/opt/homebrew/bin:/usr/local/bin:"))
        XCTAssertTrue(path.hasSuffix(":/usr/bin:/bin"))
    }

    func testALaunchFailureIsThrown() {
        let launcher = FakeLauncher()
        launcher.failure = Boom()
        XCTAssertThrowsError(try PanelOpener(launcher: launcher, environment: [:]).openPanel())
    }
}

final class RouteHandlerTests: XCTestCase {
    func testTalkOnlyLogs() {
        let log = FakeLog()
        RouteHandler(log: log).handle(URL(string: "sheepdog://talk")!)
        XCTAssertEqual(log.lines, ["sheepdog://talk: voice arrives in PR 9"])
    }

    func testOtherRoutesAreIgnoredWithAReason() {
        let log = FakeLog()
        RouteHandler(log: log).handle(URL(string: "sheepdog://stop")!)
        XCTAssertEqual(log.lines.count, 1)
        XCTAssertTrue(log.lines[0].contains("not handled"))
    }

    func testAForeignURLIsIgnored() {
        let log = FakeLog()
        RouteHandler(log: log).handle(URL(string: "https://example.invalid/talk")!)
        XCTAssertEqual(log.lines, ["ignored a URL that is not a sheepdog route"])
    }
}

final class StatusItemControllerTests: XCTestCase {
    private func make(login: FakeLoginItem = FakeLoginItem(), panel: FakePanel = FakePanel(), log: FakeLog = FakeLog(), quit: @escaping () -> Void = {}) -> (StatusItemController, FakeStatusSource) {
        let source = FakeStatusSource()
        return (StatusItemController(source: source, panel: panel, loginItem: login, log: log, quit: quit), source)
    }

    func testTheMenuLayoutAndWhatIsDisabled() {
        let rows = StatusItemController.rows(for: MenuBarState(state: nil), loginItemEnabled: true)
        XCTAssertEqual(rows.first, .status("Sheepdog isn't running"))
        let items = rows.compactMap { row -> (String, Bool, Bool)? in
            if case let .item(title, _, enabled, checked) = row { return (title, enabled, checked) }
            return nil
        }
        XCTAssertEqual(items.map(\.0), ["Open panel", "Talk", "Mute", "Stop", "Use here", "Launch at login", "Quit Sheepdog"])
        XCTAssertEqual(items.map(\.1), [true, false, false, false, false, true, true])
        XCTAssertEqual(items.first { $0.0 == "Launch at login" }?.2, true)
    }

    func testOpenPanelRunsThePanelOpener() {
        let panel = FakePanel()
        let (c, _) = make(panel: panel)
        c.perform(.openPanel)
        XCTAssertEqual(panel.opened, 1)
    }

    func testAnOpenPanelFailureIsLoggedNotThrown() {
        let panel = FakePanel()
        panel.failure = Boom()
        let log = FakeLog()
        let (c, _) = make(panel: panel, log: log)
        c.perform(.openPanel)
        XCTAssertEqual(log.lines.count, 1)
    }

    func testLaunchAtLoginToggles() {
        let login = FakeLoginItem()
        let (c, _) = make(login: login)
        c.perform(.toggleLoginItem)
        c.perform(.toggleLoginItem)
        XCTAssertEqual(login.calls, [true, false])
    }

    func testALoginItemFailureIsLogged() {
        let login = FakeLoginItem()
        login.failure = Boom()
        let log = FakeLog()
        let (c, _) = make(login: login, log: log)
        c.perform(.toggleLoginItem)
        XCTAssertFalse(login.isEnabled)
        XCTAssertEqual(log.lines.count, 1)
    }

    func testQuitStopsTheSourceAndQuits() {
        var quit = 0
        let (c, source) = make(quit: { quit += 1 })
        source.start()
        c.perform(.quit)
        XCTAssertFalse(source.running)
        XCTAssertEqual(quit, 1)
    }

    func testTheVoiceRowsDoNothingYet() {
        let (c, source) = make()
        c.perform(.voice)
        XCTAssertFalse(source.running)
    }

    func testUpdateFollowsTheDaemon() {
        let (c, _) = make()
        XCTAssertEqual(c.state.kind, .daemonDown)
        c.update(DaemonState(agents: [], voice: DaemonState.VoiceInfo(mode: "off", status: "off")))
        XCTAssertEqual(c.state.kind, .off)
    }
}
