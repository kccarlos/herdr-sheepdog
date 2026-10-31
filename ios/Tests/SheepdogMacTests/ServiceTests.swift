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

@MainActor
final class RouteHandlerTests: XCTestCase {
    func testTheVoiceRoutesGoToTheVoice() {
        let voice = FakeVoice()
        let handler = RouteHandler(voice: voice, log: FakeLog())
        ["talk", "stop", "end", "mute", "mute?on=true"].forEach { handler.handle(URL(string: "sheepdog://\($0)")!) }
        XCTAssertEqual(voice.routes, [.talk, .stop, .end, .toggleMute, .mute(true)])
    }

    func testRoutesTheMacDoesNotHaveAreIgnoredWithAReason() {
        let voice = FakeVoice()
        let log = FakeLog()
        RouteHandler(voice: voice, log: log).handle(URL(string: "sheepdog://agent/w1:p2")!)
        XCTAssertTrue(voice.routes.isEmpty)
        XCTAssertEqual(log.lines.count, 1)
        XCTAssertTrue(log.lines[0].contains("not handled"))
    }

    func testAForeignURLIsIgnored() {
        let voice = FakeVoice()
        let log = FakeLog()
        RouteHandler(voice: voice, log: log).handle(URL(string: "https://example.invalid/talk")!)
        XCTAssertTrue(voice.routes.isEmpty)
        XCTAssertEqual(log.lines, ["ignored a URL that is not a sheepdog route"])
    }
}

@MainActor
final class StatusItemControllerTests: XCTestCase {
    private func make(voice: FakeVoice? = nil, login: FakeLoginItem = FakeLoginItem(), panel: FakePanel = FakePanel(), log: FakeLog = FakeLog(), quit: @escaping () -> Void = {}) -> (StatusItemController, FakeStatusSource) {
        let source = FakeStatusSource()
        return (StatusItemController(source: source, voice: voice ?? FakeVoice(), panel: panel, loginItem: login, log: log, quit: quit), source)
    }

    private func items(_ rows: [MenuItemSpec.Row]) -> [(title: String, enabled: Bool, checked: Bool)] {
        rows.compactMap { row in
            if case let .item(title, _, enabled, checked) = row { return (title, enabled, checked) }
            return nil
        }
    }

    private let listening = DaemonState(agents: [], voice: DaemonState.VoiceInfo(mode: "gemini", status: "connected", clientConnected: true))

    func testTheMenuLayoutAndWhatIsDisabledWithTheDaemonDown() {
        let rows = StatusItemController.rows(for: MenuBarState(state: nil), loginItemEnabled: true)
        XCTAssertEqual(rows.first, .status("Sheepdog isn't running"))
        let items = items(rows)
        XCTAssertEqual(items.map(\.title), ["Open panel", "Talk", "Mute", "Stop", "Use here", "Launch at login", "Quit Sheepdog"])
        XCTAssertEqual(items.map(\.enabled), [true, false, false, false, false, true, true])
        XCTAssertEqual(items.first { $0.title == "Launch at login" }?.checked, true)
    }

    func testTheVoiceRowsWithTheDaemonUpAndNoSessionHere() {
        let rows = StatusItemController.rows(for: MenuBarState(state: listening), loginItemEnabled: false)
        let voice = items(rows).filter { ["Talk", "Mute", "Stop", "Use here"].contains($0.title) }
        XCTAssertEqual(voice.map(\.enabled), [true, false, false, true])
    }

    func testTheVoiceRowsWhileListeningHere() {
        let rows = StatusItemController.rows(for: MenuBarState(state: listening), loginItemEnabled: false, voice: VoiceMenuState(heldHere: true))
        let voice = items(rows).filter { ["Talk", "Mute", "Stop", "Use here"].contains($0.title) }
        // Talk has nothing to do while the microphone is open.
        XCTAssertEqual(voice.map(\.enabled), [false, true, true, false])
        XCTAssertEqual(voice.map(\.checked), [false, false, false, false])
    }

    func testMutedHereOffersTalkAndShowsMuteChecked() {
        let rows = StatusItemController.rows(for: MenuBarState(state: listening), loginItemEnabled: false, voice: VoiceMenuState(heldHere: true, muted: true))
        let talk = items(rows).first { $0.title == "Talk" }
        let mute = items(rows).first { $0.title == "Mute" }
        XCTAssertEqual(talk?.enabled, true)
        XCTAssertEqual(mute?.checked, true)
    }

    func testADeniedMicrophoneSaysSoAndOffersTheSettings() {
        let voice = VoiceMenuState(heldHere: true, micDenied: true, problem: MacVoiceHost.microphoneOff)
        let rows = StatusItemController.rows(for: MenuBarState(state: listening), loginItemEnabled: false, voice: voice)
        XCTAssertTrue(rows.contains(.status("Microphone off: open System Settings")))
        XCTAssertEqual(items(rows).first { $0.title == "Open Microphone Settings…" }?.enabled, true)
        let without = StatusItemController.rows(for: MenuBarState(state: listening), loginItemEnabled: false)
        XCTAssertNil(items(without).first { $0.title == "Open Microphone Settings…" })
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

    func testQuitEndsTheVoiceStopsTheSourceAndQuits() {
        var quit = 0
        let voice = FakeVoice()
        let (c, source) = make(voice: voice, quit: { quit += 1 })
        source.start()
        c.perform(.quit)
        XCTAssertEqual(voice.routes, [.end])
        XCTAssertFalse(source.running)
        XCTAssertEqual(quit, 1)
    }

    func testTheVoiceItemsDriveTheVoice() {
        let voice = FakeVoice()
        let (c, _) = make(voice: voice)
        c.perform(.talk)
        c.perform(.toggleMute)
        c.perform(.stop)
        c.perform(.useHere)
        c.perform(.openMicrophoneSettings)
        XCTAssertEqual(voice.routes, [.talk, .toggleMute, .stop])
        XCTAssertEqual(voice.useHeres, 1)
        XCTAssertEqual(voice.settingsOpened, 1)
    }

    func testUpdateFollowsTheDaemon() {
        let (c, _) = make()
        XCTAssertEqual(c.state.kind, .daemonDown)
        c.update(DaemonState(agents: [], voice: DaemonState.VoiceInfo(mode: "off", status: "off")))
        XCTAssertEqual(c.state.kind, .off)
    }
}
