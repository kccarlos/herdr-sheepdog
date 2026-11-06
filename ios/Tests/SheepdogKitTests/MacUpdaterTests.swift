@testable import SheepdogKit
import XCTest

private final class FakeRunner: UpdateCommandRunning, @unchecked Sendable {
    private let lock = NSLock()
    private var recorded: [[String]] = []
    var results: [String: UpdateRunResult] = [:]
    var calls: [[String]] { lock.lock(); defer { lock.unlock() }; return recorded }

    func run(_ executable: String, arguments: [String], timeout: TimeInterval) async -> UpdateRunResult {
        lock.lock(); recorded.append([executable] + arguments); lock.unlock()
        return results[arguments.joined(separator: " ")] ?? UpdateRunResult(status: 1)
    }
}

private struct FakeFiles: UpdateFileChecking {
    var present: Set<String> = []
    func exists(_ path: String) -> Bool { present.contains(path) }
}

private final class FakeLog: UpdateLogStoring, @unchecked Sendable {
    private let lock = NSLock()
    private var _text: String?
    private var reads = 0
    var modifiedAt: Date?
    /// After this many reads, the text becomes the second value (the script finished).
    var finishAfterReads: (Int, String)?
    var text: String? {
        get { lock.lock(); defer { lock.unlock() }; return _text }
        set { lock.lock(); _text = newValue; lock.unlock() }
    }

    func read() -> String? {
        lock.lock(); defer { lock.unlock() }
        reads += 1
        if let (n, tail) = finishAfterReads, reads >= n { _text = (_text ?? "") + tail }
        return _text
    }

    func modified() -> Date? { modifiedAt ?? (text == nil ? nil : Date()) }
    func clear() { text = nil }
}

private final class FakeLauncher: UpdateDetachedLaunching, @unchecked Sendable {
    var scripts: [String] = []
    var failure = false
    /// What the detached script writes to the log at once.
    var output: String?
    var log: FakeLog?
    var onLaunch: ((String) -> Void)?

    struct Boom: Error {}

    func launch(script: String) throws {
        if failure { throw Boom() }
        scripts.append(script)
        if let output { log?.text = output }
        onLaunch?(script)
    }
}

private final class Opened: @unchecked Sendable { var urls: [URL] = [] }

@MainActor
final class MacUpdaterTests: XCTestCase {
    private let brew = "/opt/homebrew/bin/brew"
    private let ok = UpdateRunResult(status: 0)

    private func offer(_ latest: String = "v0.2.6", available: Bool = true, app: String? = "0.2.5") -> MacUpdateOffer? {
        MacUpdateOffer(info: UpdateInfo(current: "v0.2.5", latest: latest, available: available), appVersion: app)
    }

    func testOfferFromDaemonOrOlderApp() {
        XCTAssertEqual(offer()?.latest, "v0.2.6")
        XCTAssertNil(offer(available: false, app: "0.2.6"))
        XCTAssertEqual(offer(available: false, app: "0.2.5")?.latest, "v0.2.6", "the app is older even if the daemon is current")
        XCTAssertNil(MacUpdateOffer(info: nil, appVersion: "0.2.5"))
        XCTAssertNil(MacUpdateOffer(info: UpdateInfo(available: true), appVersion: "0.2.5"), "no latest, nothing to name")
        XCTAssertNil(offer("dev"))
        XCTAssertEqual(offer("0.3.0")?.latest, "v0.3.0")
        XCTAssertEqual(offer()?.releaseURL?.absoluteString, "https://github.com/kccarlos/herdr-sheepdog/releases/tag/v0.2.6")
    }

    func testDetectionNeedsBrewAndAPackage() async {
        let runner = FakeRunner()
        let none = MacInstallDetector(runner: runner, files: FakeFiles())
        let noBrew = await none.detect()
        XCTAssertEqual(noBrew, .manual)
        XCTAssertTrue(runner.calls.isEmpty, "no brew, nothing is run")

        runner.results["list --cask sheepdog-app"] = ok
        let d = MacInstallDetector(runner: runner, files: FakeFiles(present: [brew]))
        let caskOnly = await d.detect()
        XCTAssertEqual(caskOnly, .homebrew(brew: brew, formula: false, cask: true))

        runner.results["list --formula sheepdog"] = ok
        let both = await d.detect()
        XCTAssertEqual(both, .homebrew(brew: brew, formula: true, cask: true))

        let notBrewed = MacInstallDetector(runner: FakeRunner(), files: FakeFiles(present: [brew]))
        let manual = await notBrewed.detect()
        XCTAssertEqual(manual, .manual, "brew exists but owns neither")
    }

    func testCaskroomEntryCountsWhenListFails() async {
        let d = MacInstallDetector(runner: FakeRunner(), files: FakeFiles(present: ["/usr/local/bin/brew", "/usr/local/Caskroom/sheepdog-app"]))
        let method = await d.detect()
        XCTAssertEqual(method, .homebrew(brew: "/usr/local/bin/brew", formula: false, cask: true))
    }

    func testUpgradeArgumentsNeverUseSudo() {
        XCTAssertEqual(MacUpdater.upgradeArguments(formula: true, cask: true), ["upgrade", "sheepdog", "sheepdog-app"])
        XCTAssertEqual(MacUpdater.upgradeArguments(formula: false, cask: true), ["upgrade", "--cask", "sheepdog-app"])
        XCTAssertEqual(MacUpdater.upgradeArguments(formula: true, cask: false), ["upgrade", "--formula", "sheepdog"])
    }

    private func makeUpdater(
        _ runner: FakeRunner = FakeRunner(), present: Set<String> = [], launcher: FakeLauncher = FakeLauncher(), log: FakeLog = FakeLog(),
        opened: Opened = Opened(), timeout: TimeInterval = 5
    ) -> MacUpdater {
        MacUpdater(runner: runner, files: FakeFiles(present: present), launcher: launcher, log: log,
                   openURL: { opened.urls.append($0) }, timeout: timeout, pollInterval: 0.005)
    }

    private func brewRunner(formula: Bool = true) -> FakeRunner {
        let runner = FakeRunner()
        runner.results["list --cask sheepdog-app"] = ok
        if formula { runner.results["list --formula sheepdog"] = ok }
        return runner
    }

    func testScriptQuotesAndRecordsTheExitStatus() {
        XCTAssertEqual(MacUpdater.script(brew: "/opt/homebrew/bin/brew", arguments: ["upgrade", "sheepdog", "sheepdog-app"]),
                       "'/opt/homebrew/bin/brew' 'upgrade' 'sheepdog' 'sheepdog-app'; echo \"exit=$?\"")
        XCTAssertFalse(MacUpdater.script(brew: brew, arguments: ["upgrade"]).contains("sudo"))
    }

    func testHomebrewUpgradeRunsDetachedAndSucceeds() async throws {
        let launcher = FakeLauncher()
        launcher.output = "==> Upgrading\nexit=0\n"
        let log = FakeLog()
        launcher.log = log
        let opened = Opened()
        let updater = makeUpdater(brewRunner(), present: [brew], launcher: launcher, log: log, opened: opened)
        var changes: [MacUpdateState] = []
        updater.onChange = { changes.append(updater.state) }
        await updater.start(offer: try XCTUnwrap(offer()))
        XCTAssertEqual(changes, [.upgrading, .idle])
        XCTAssertEqual(launcher.scripts, ["'\(brew)' 'upgrade' 'sheepdog' 'sheepdog-app'; echo \"exit=$?\""])
        XCTAssertTrue(opened.urls.isEmpty)
    }

    func testFailureShowsThePlainErrorAndAllowsRetry() async throws {
        let launcher = FakeLauncher()
        launcher.output = "==> Upgrading\nError: Cask 'sheepdog-app' is unavailable\nexit=1\n"
        let log = FakeLog()
        launcher.log = log
        let updater = makeUpdater(brewRunner(formula: false), present: [brew], launcher: launcher, log: log)
        let o = try XCTUnwrap(offer())
        await updater.start(offer: o)
        XCTAssertEqual(updater.state, .failed("Error: Cask 'sheepdog-app' is unavailable"))
        let p = MacUpdatePresentation(offer: o, state: updater.state)
        XCTAssertEqual(p.status, "Update failed: Error: Cask 'sheepdog-app' is unavailable")
        XCTAssertEqual(p.itemTitle, "Try the update again — v0.2.6")
        launcher.output = "exit=0\n"
        await updater.start(offer: o)
        XCTAssertEqual(updater.state, .idle)
    }

    func testLaunchFailureAndTimeout() async throws {
        let launcher = FakeLauncher()
        launcher.failure = true
        let updater = makeUpdater(brewRunner(), present: [brew], launcher: launcher)
        await updater.start(offer: try XCTUnwrap(offer()))
        guard case .failed = updater.state else { return XCTFail("expected failure") }

        let hung = makeUpdater(brewRunner(), present: [brew], timeout: 0.05)
        await hung.start(offer: try XCTUnwrap(offer()))
        XCTAssertEqual(hung.state, .failed("Homebrew took longer than 10 minutes. Run brew upgrade in Terminal."))
        XCTAssertEqual(MacUpdater.timeout, 600)
        XCTAssertEqual(MacUpdater.outcome(UpdateRunResult(status: 2)), .failed("Homebrew stopped with code 2."))
    }

    func testRelaunchAfterTheCaskQuitShowsAFailureOnce() async {
        let log = FakeLog()
        log.text = "Error: download failed\nexit=1\n"
        let updater = makeUpdater(log: log)
        await updater.resume()
        XCTAssertEqual(updater.state, .failed("Error: download failed"))
        XCTAssertNil(log.text, "shown once")
    }

    func testRelaunchAfterSuccessClearsTheLog() async {
        let log = FakeLog()
        log.text = "==> Upgrading\nexit=0\n"
        let updater = makeUpdater(log: log)
        await updater.resume()
        XCTAssertEqual(updater.state, .idle)
        XCTAssertNil(log.text)
        await updater.resume()
        XCTAssertEqual(updater.state, .idle, "no log, nothing to say")
    }

    func testRelaunchWhileBrewStillRunsFollowsItAndAStaleLogFails() async {
        let log = FakeLog()
        log.text = "==> Upgrading\n"
        log.modifiedAt = Date()
        let updater = makeUpdater(log: log)
        log.finishAfterReads = (3, "exit=0\n")
        await updater.resume()
        XCTAssertEqual(updater.state, .idle)

        let stale = FakeLog()
        stale.text = "==> Upgrading\n"
        stale.modifiedAt = Date(timeIntervalSinceNow: -3600)
        let other = makeUpdater(log: stale)
        await other.resume()
        XCTAssertEqual(other.state, .failed("The last update did not finish. Run brew upgrade in Terminal."))
    }

    func testManualInstallOpensTheReleasePage() async throws {
        let opened = Opened()
        let updater = makeUpdater(opened: opened)
        await updater.start(offer: try XCTUnwrap(offer()))
        XCTAssertEqual(opened.urls.map(\.absoluteString), ["https://github.com/kccarlos/herdr-sheepdog/releases/tag/v0.2.6"])
        XCTAssertEqual(updater.state, .idle)
    }

    func testPresentationAndVoiceOverLabels() throws {
        let o = try XCTUnwrap(offer())
        let idle = MacUpdatePresentation(offer: o, state: .idle)
        XCTAssertEqual(idle.itemTitle, "Update available — v0.2.6")
        XCTAssertNil(idle.status)
        XCTAssertEqual(idle.accessibilityLabel, "Update available, version 0.2.6. Choose to install it.")
        let busy = MacUpdatePresentation(offer: o, state: .upgrading)
        XCTAssertNil(busy.itemTitle)
        XCTAssertEqual(busy.status, "Updating Sheepdog to v0.2.6…")
        let none = MacUpdatePresentation(offer: nil, state: .idle)
        XCTAssertNil(none.itemTitle)
        XCTAssertNil(none.status)
    }

    private func makeBundle(version: String) throws -> URL {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("Sheepdog-\(UUID().uuidString).app")
        try FileManager.default.createDirectory(at: dir.appendingPathComponent("Contents"), withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
        try writeBundleVersion(version, to: dir)
        return dir
    }

    private func writeBundleVersion(_ version: String, to bundle: URL) throws {
        let data = try PropertyListSerialization.data(fromPropertyList: ["CFBundleShortVersionString": version], format: .xml, options: 0)
        try data.write(to: bundle.appendingPathComponent("Contents/Info.plist"))
    }

    func testSuccessfulCaskUpgradeRelaunchesWhenBundleVersionChanged() async throws {
        let bundle = try makeBundle(version: "0.2.5")
        let log = FakeLog()
        let launcher = FakeLauncher()
        launcher.onLaunch = { [weak self] script in
            guard script.contains("brew") else { return }
            try? self?.writeBundleVersion("0.2.6", to: bundle)
            log.text = "==> Upgrading\nSkipping quitting application as brew is running inside it\nexit=0\n"
        }
        var quits = 0
        let updater = MacUpdater(
            runner: brewRunner(), files: FakeFiles(present: [brew]), launcher: launcher, log: log,
            openURL: { _ in }, timeout: 5, pollInterval: 0.005, bundleURL: bundle, quit: { quits += 1 }
        )
        await updater.start(offer: try XCTUnwrap(offer()))
        XCTAssertEqual(quits, 1)
        XCTAssertEqual(launcher.scripts.count, 2)
        let relaunch = MacUpdater.relaunchScript(bundlePath: bundle.path)
        XCTAssertEqual(launcher.scripts.last, relaunch)
        XCTAssertTrue(relaunch.contains("kill -0 \(ProcessInfo.processInfo.processIdentifier)"))
        XCTAssertTrue(relaunch.contains("/usr/bin/open '\(bundle.path)'"))
        XCTAssertTrue(relaunch.contains("exit=0"))

        // When the new 0.2.6 bundle launches and resumes, it clears the log and does not loop.
        log.text = "exit=0\n"
        var secondQuits = 0
        let reopened = MacUpdater(
            runner: FakeRunner(), files: FakeFiles(), launcher: FakeLauncher(), log: log,
            openURL: { _ in }, bundleURL: bundle, quit: { secondQuits += 1 }
        )
        await reopened.resume()
        XCTAssertEqual(reopened.state, .idle)
        XCTAssertNil(log.text)
        XCTAssertEqual(secondQuits, 0)
    }

    func testNoRelaunchWhenBundleVersionUnchangedOrUpgradeFailed() async throws {
        let bundle = try makeBundle(version: "0.2.5")
        let log = FakeLog()
        let launcher = FakeLauncher()
        launcher.output = "==> Upgrading sheepdog\nexit=0\n"
        launcher.log = log
        var quits = 0
        let updater = MacUpdater(
            runner: brewRunner(), files: FakeFiles(present: [brew]), launcher: launcher, log: log,
            openURL: { _ in }, timeout: 5, pollInterval: 0.005, bundleURL: bundle, quit: { quits += 1 }
        )
        await updater.start(offer: try XCTUnwrap(offer()))
        XCTAssertEqual(updater.state, .idle)
        XCTAssertEqual(quits, 0)
        XCTAssertEqual(launcher.scripts.count, 1)

        launcher.output = nil
        launcher.onLaunch = { [weak self] _ in
            try? self?.writeBundleVersion("0.2.6", to: bundle)
            log.text = "Error: sha256 mismatch\nexit=1\n"
        }
        await updater.start(offer: try XCTUnwrap(offer()))
        XCTAssertEqual(updater.state, .failed("Error: sha256 mismatch"))
        XCTAssertEqual(quits, 0)
        XCTAssertEqual(launcher.scripts.count, 2)
    }

    func testVoiceSessionDefersRelaunchUntilRestartChosen() async throws {
        let bundle = try makeBundle(version: "0.2.5")
        let log = FakeLog()
        let launcher = FakeLauncher()
        launcher.onLaunch = { [weak self] script in
            guard script.contains("brew") else { return }
            try? self?.writeBundleVersion("0.2.6", to: bundle)
            log.text = "==> Upgrading\nexit=0\n"
        }
        var quits = 0
        let updater = MacUpdater(
            runner: brewRunner(), files: FakeFiles(present: [brew]), launcher: launcher, log: log,
            openURL: { _ in }, timeout: 5, pollInterval: 0.005, bundleURL: bundle,
            voiceActive: { true }, quit: { quits += 1 }
        )
        let o = try XCTUnwrap(offer())
        await updater.start(offer: o)
        XCTAssertEqual(updater.state, .restartNeeded)
        XCTAssertEqual(quits, 0)
        XCTAssertEqual(launcher.scripts.count, 1)

        let withOffer = MacUpdatePresentation(offer: o, state: updater.state)
        XCTAssertEqual(withOffer.itemTitle, "Restart to finish updating")
        XCTAssertNil(withOffer.status)
        XCTAssertEqual(MacUpdatePresentation(offer: nil, state: .restartNeeded).itemTitle, "Restart to finish updating")

        updater.relaunch()
        XCTAssertEqual(quits, 1)
        XCTAssertEqual(launcher.scripts.count, 2)
        XCTAssertEqual(updater.state, .idle)
    }
}
