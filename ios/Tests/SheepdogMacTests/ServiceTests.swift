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

@MainActor
final class PanelOpenerTests: XCTestCase {
    func testRunsHerdrAppDoModOpenThroughEnvWithAnInstallSearchPath() throws {
        let launcher = FakeLauncher()
        try PanelOpener(launcher: launcher, environment: ["PATH": "/usr/bin:/bin"], reporter: PanelProblemAlertNever()).openPanel()
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
        XCTAssertThrowsError(try PanelOpener(launcher: launcher, environment: [:], reporter: PanelProblemAlertNever()).openPanel())
    }

    // What herdr-app answers, as herdr-app and env print it.

    func testSuccessIsNoProblem() {
        XCTAssertNil(PanelOpenProblem(status: 0, output: "{\n  \"ok\": true\n}\n"))
    }

    func testAMissingModIsPanelMissing() {
        XCTAssertEqual(PanelOpenProblem(status: 1, output: "{\n  \"error\": \"no mod folder \\\"sheepdog\\\"\"\n}\n"), .panelMissing)
    }

    func testEnvNotFindingHerdrAppIsHerdrAppMissing() {
        XCTAssertEqual(PanelOpenProblem(status: 127, output: "env: herdr-app: No such file or directory\n"), .herdrAppMissing)
    }

    func testAnyOtherErrorIsShownInHerdrAppsWords() {
        XCTAssertEqual(PanelOpenProblem(status: 1, output: "{\"error\": \"the window is not open\"}"), .failed("the window is not open"))
        XCTAssertEqual(PanelOpenProblem(status: 2, output: "  boom\n"), .failed("boom"))
        XCTAssertEqual(PanelOpenProblem(status: 3, output: ""), .failed("herdr-app stopped with status 3."))
    }

    func testEveryProblemHasWordsAndAGuide() {
        for problem in [PanelOpenProblem.herdrAppMissing, .panelMissing, .failed("x")] {
            XCTAssertFalse(problem.title.isEmpty)
            XCTAssertFalse(problem.message.isEmpty)
            XCTAssertTrue(problem.guide.absoluteString.contains("docs/desktop.md#"))
        }
        XCTAssertTrue(PanelOpenProblem.panelMissing.message.contains("cp -R mods/sheepdog ~/.config/herdr-app/mods/"))
    }

    func testAFailedOpenIsReportedOnTheMainActorAndLogged() async throws {
        let launcher = FakeLauncher()
        launcher.exit = (1, #"{"error": "no mod folder \"sheepdog\""}"#)
        let reporter = FakePanelReporter()
        let log = FakeLog()
        try PanelOpener(launcher: launcher, environment: [:], reporter: reporter, log: log).openPanel()
        for _ in 0..<50 where reporter.reported.isEmpty {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTAssertEqual(reporter.reported, [.panelMissing])
        XCTAssertEqual(log.lines.count, 1)
    }

    func testASuccessfulOpenReportsNothing() async throws {
        let reporter = FakePanelReporter()
        try PanelOpener(launcher: FakeLauncher(), environment: [:], reporter: reporter).openPanel()
        try await Task.sleep(nanoseconds: 50_000_000)
        XCTAssertTrue(reporter.reported.isEmpty)
    }
}

/// For tests that never get as far as a report: fails loudly if one comes.
private struct PanelProblemAlertNever: PanelProblemReporting {
    func report(_ problem: PanelOpenProblem) { XCTFail("unexpected report \(problem)") }
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
    private func make(
        voice: FakeVoice? = nil,
        hotkey: FakeHotkey? = nil,
        login: FakeLoginItem = FakeLoginItem(),
        panel: FakePanel = FakePanel(),
        log: FakeLog = FakeLog(),
        quit: @escaping () -> Void = {}
    ) -> (StatusItemController, FakeStatusSource) {
        let source = FakeStatusSource()
        return (StatusItemController(source: source, voice: voice ?? FakeVoice(), panel: panel, hotkey: hotkey ?? FakeHotkey(), loginItem: login, log: log, quit: quit), source)
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
        XCTAssertEqual(items.map(\.title), ["Open panel", "Talk", "Mute", "Stop", "Use here", "⌥ Space", "⌃ Space", "⌃⌥ Space", "Launch at login", "Quit Sheepdog"])
        XCTAssertEqual(items.map(\.enabled), [true, false, false, false, false, true, true, true, true, true])
        XCTAssertEqual(items.first { $0.title == "Launch at login" }?.checked, true)
        XCTAssertEqual(items.first { $0.title == "⌥ Space" }?.checked, true)
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

    func testTheHotkeyMenuChoiceAndActionsDriveTheVoice() {
        let voice = FakeVoice()
        let hotkey = FakeHotkey()
        let (c, _) = make(voice: voice, hotkey: hotkey)
        c.perform(.selectHotkey(.controlSpace))
        XCTAssertEqual(hotkey.selections, [.controlSpace])
        hotkey.onAction?(.pushToTalk(true))
        hotkey.onAction?(.pushToTalk(false))
        hotkey.onAction?(.toggleMute)
        XCTAssertEqual(voice.pushToTalkCalls, [true, false])
        XCTAssertEqual(voice.routes, [.toggleMute])
    }

    func testUpdateFollowsTheDaemon() {
        let (c, _) = make()
        XCTAssertEqual(c.state.kind, .daemonDown)
        c.update(DaemonState(agents: [], voice: DaemonState.VoiceInfo(mode: "off", status: "off")))
        XCTAssertEqual(c.state.kind, .off)
    }
}
