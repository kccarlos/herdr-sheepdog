@testable import SheepdogKit
import XCTest

private final class FakeLauncher: UpdateDetachedLaunching, @unchecked Sendable {
    var scripts: [String] = []
    func launch(script: String) throws { scripts.append(script) }
}

private final class Opened: @unchecked Sendable { var urls: [URL] = [] }

/// The Mac menu's Update: the daemon updates itself (`POST /api/update`);
/// the app follows `/api/state` and relaunches when its bundle was replaced.
@MainActor
final class MacUpdaterTests: XCTestCase {
    private func offer(_ latest: String = "v0.2.6", available: Bool = true) -> MacUpdateOffer? {
        MacUpdateOffer(info: UpdateInfo(current: "v0.2.5", latest: latest, available: available))
    }

    private func daemon(_ version: String, _ update: UpdateInfo? = nil) -> DaemonState {
        DaemonState(agents: [], voice: DaemonState.VoiceInfo(mode: "off", status: "off"), version: version, update: update)
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

    func testOfferOnlyWhileTheDaemonIsBehind() {
        XCTAssertEqual(offer()?.latest, "v0.2.6")
        XCTAssertNil(offer(available: false), "the daemon runs the newest release: no offer")
        XCTAssertEqual(MacUpdateOffer(info: UpdateInfo(restartPending: true, restartTo: "v0.2.6"))?.latest, "v0.2.6")
        XCTAssertNil(MacUpdateOffer(info: nil))
        XCTAssertNil(MacUpdateOffer(info: UpdateInfo(available: true)), "no latest, nothing to name")
        XCTAssertNil(offer("dev"))
        XCTAssertEqual(offer("0.3.0")?.latest, "v0.3.0")
        XCTAssertEqual(offer()?.releaseURL?.absoluteString, "https://github.com/kccarlos/herdr-sheepdog/releases/tag/v0.2.6")
    }

    func testStartAsksTheDaemonAndFollowsItsProgress() async throws {
        var installs = 0
        let updater = MacUpdater(install: { installs += 1; return UpdateInfo(state: "installing") }, launcher: FakeLauncher(), openURL: { _ in })
        await updater.start(offer: try XCTUnwrap(offer()))
        XCTAssertEqual(installs, 1)
        XCTAssertEqual(updater.state, .upgrading)
        updater.follow(nil) // restarting
        XCTAssertEqual(updater.state, .upgrading)
        updater.follow(daemon("v0.2.5", UpdateInfo(available: true, state: "failed", error: "brew upgrade: exit status 1")))
        XCTAssertEqual(updater.state, .failed("brew upgrade: exit status 1"))
        updater.follow(daemon("v0.2.5", UpdateInfo(available: true, state: "installing")))
        XCTAssertEqual(updater.state, .upgrading)
        updater.follow(daemon("v0.2.6"))
        XCTAssertEqual(updater.state, .idle)
    }

    func testARefusalShowsTheDaemonsWordsAndUnreachableOpensTheReleasePage() async throws {
        let opened = Opened()
        let refused = MacUpdater(install: { throw DaemonError.rejected(status: 409, message: "sheepdog v0.2.6 is up to date") }, launcher: FakeLauncher(), openURL: { opened.urls.append($0) })
        await refused.start(offer: try XCTUnwrap(offer()))
        XCTAssertEqual(refused.state, .failed("The daemon refused: sheepdog v0.2.6 is up to date"))
        XCTAssertEqual(MacUpdatePresentation(offer: offer(), state: refused.state).itemTitle, "Try the update again — v0.2.6")

        let down = MacUpdater(install: { throw DaemonError.unreachable(endpoint: "127.0.0.1:7777", detail: "") }, launcher: FakeLauncher(), openURL: { opened.urls.append($0) })
        await down.start(offer: try XCTUnwrap(offer()))
        XCTAssertEqual(down.state, .idle)
        XCTAssertEqual(opened.urls.map(\.absoluteString), ["https://github.com/kccarlos/herdr-sheepdog/releases/tag/v0.2.6"])
    }

    func testRelaunchesWhenTheDaemonChangedVersionAndTheBundleWasReplaced() throws {
        let bundle = try makeBundle(version: "0.2.5")
        let launcher = FakeLauncher()
        var quits = 0
        let updater = MacUpdater(install: { nil }, launcher: launcher, openURL: { _ in }, bundleURL: bundle, quit: { quits += 1 })
        updater.follow(daemon("v0.2.5"))
        updater.follow(daemon("v0.2.6"))
        XCTAssertEqual(quits, 0, "a daemon update that left the app alone: no relaunch")

        try writeBundleVersion("0.2.6", to: bundle)
        updater.follow(nil)
        updater.follow(daemon("v0.2.6"))
        XCTAssertEqual(quits, 0, "same daemon version: nothing to do")
        updater.follow(daemon("v0.2.7"))
        XCTAssertEqual(quits, 1)
        let relaunch = MacUpdater.relaunchScript(bundlePath: bundle.path)
        XCTAssertEqual(launcher.scripts, [relaunch])
        XCTAssertTrue(relaunch.contains("kill -0 \(ProcessInfo.processInfo.processIdentifier)"))
        XCTAssertTrue(relaunch.contains("/usr/bin/open '\(bundle.path)'"))
    }
}
