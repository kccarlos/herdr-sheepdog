#if os(iOS)
import Darwin
import Foundation
import SwiftUI
import UIKit
import XCTest
@testable import Sheepdog
@testable import SheepdogKit

/// Audit coverage for Group 5:
/// - Finding 17: Multi-window `appPhase` lifecycle when only `AgentWindowScene`
///   is open, clearing screen visibility when closing the last main window, and
///   immediately reporting `setScreen` when claiming audio ("Use here").
/// - Finding 18: Dynamic Island `isStale` consistency (title, look, waiting
///   badge, and compact/minimal VoiceOver labels) and `VoiceLiveActivity`
///   refresh update serialization behind `updating`.
/// - Finding 15: `PermissionPrimerView` scrollability at `.accessibility5`
///   Dynamic Type and `.isHeader` trait on its title.
/// - Finding 16: `FlockUnavailableView` `.notConnected` pairing copy and action
///   when `!model.isConfigured`.
/// - Finding 19: `Attachments.loadFile` checking `fileSize` metadata against
///   `Attachments.maxBytes` before reading file bytes, off `@MainActor`.
@MainActor
final class UIAndLifecycleAuditTests: XCTestCase {
    private var suiteName: String!
    private var defaults: UserDefaults!
    private var cacheDir: URL!

    override func setUp() async throws {
        try await super.setUp()
        suiteName = "sheepdog.tests.group5-\(UUID().uuidString)"
        defaults = UserDefaults(suiteName: suiteName)!
        cacheDir = FileManager.default.temporaryDirectory.appendingPathComponent("sheepdog-g5-\(UUID().uuidString)", isDirectory: true)
        SceneCoordinator.shared.scenes = []
        SceneCoordinator.shared.agentScenes = []
        SceneCoordinator.shared.requester = nil
    }

    override func tearDown() async throws {
        SceneCoordinator.shared.scenes = []
        SceneCoordinator.shared.agentScenes = []
        SceneCoordinator.shared.requester = nil
        defaults.removePersistentDomain(forName: suiteName)
        try? FileManager.default.removeItem(at: cacheDir)
        try await super.tearDown()
    }

    private func makeModel() -> SessionViewModel {
        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let settings = SettingsStore(defaults: defaults, computers: store)
        return SessionViewModel(settings: settings, cache: ConversationCache(directory: cacheDir))
    }

    private func findSubview<T: UIView>(ofType type: T.Type, in view: UIView) -> T? {
        if let matched = view as? T { return matched }
        for subview in view.subviews {
            if let found = findSubview(ofType: type, in: subview) { return found }
        }
        return nil
    }

    // MARK: - Finding 17: Multi-window ownership & AgentWindowScene appPhase

    func testClaimingAudioInSecondWindowImmediatelyReportsNewOwnersScreen() {
        let model = makeModel()
        model.settings.address = "127.0.0.1:7777"
        model.settings.hasBeenConfigured = true
        defer { model.fleet.set([]) }

        let idA = UUID()
        let idB = UUID()
        let uiA = SceneUI()
        let uiB = SceneUI()
        SceneCoordinator.shared.register(idA)
        SceneCoordinator.shared.register(idB)

        let root = HStack {
            Color.clear
                .modifier(SessionChrome(ui: uiA, voiceScreen: true, agentsScreen: false))
                .environment(\.sceneID, idA)
            Color.clear
                .modifier(SessionChrome(ui: uiB, voiceScreen: false, agentsScreen: true))
                .environment(\.sceneID, idB)
        }
        .environmentObject(model)
        .environment(\.appPhase, .active)

        let host = UIHostingController(rootView: root)
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 1024, height: 768))
        window.rootViewController = host
        window.makeKeyAndVisible()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        defer {
            window.isHidden = true
            window.rootViewController = nil
        }

        XCTAssertFalse(model.viewingAgentsScreen, "initial owner idA is on Conversation")

        SceneCoordinator.shared.claimAudio(for: idB)
        host.view.setNeedsLayout()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.08))

        XCTAssertTrue(model.viewingAgentsScreen, "claiming audio in window B should immediately report window B's agentsScreen")
    }

    func testAgentWindowDrivesAppBackgroundAndActiveWhenNoMainWindowIsOpen() {
        struct Harness: View {
            let box: PhaseBox
            let model: SessionViewModel
            var body: some View {
                AgentWindowScene(route: nil)
                    .environmentObject(model)
                    .environment(\.appPhase, box.phase)
            }
        }

        let model = makeModel()
        defer { model.fleet.set([]) }
        let box = PhaseBox()

        let host = UIHostingController(rootView: Harness(box: box, model: model))
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 800, height: 600))
        window.rootViewController = host
        window.makeKeyAndVisible()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        defer {
            window.isHidden = true
            window.rootViewController = nil
            RunLoop.main.run(until: Date().addingTimeInterval(0.02))
        }

        XCTAssertEqual(model.appPhase, .active)

        box.phase = .background
        host.view.setNeedsLayout()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.08))
        XCTAssertEqual(model.appPhase, .background, "AgentWindowScene should drive enteredBackground() when no main window is open")

        box.phase = .active
        host.view.setNeedsLayout()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.08))
        XCTAssertEqual(model.appPhase, .active, "AgentWindowScene should drive becameActive() when no main window is open")
    }

    func testClosingLastMainWindowClearsVoiceAndAgentsScreenVisibility() {
        struct Harness: View {
            let box: MainOpenBox
            let model: SessionViewModel
            let ui = SceneUI()
            var body: some View {
                ZStack {
                    if box.showMain {
                        MainSceneRoot {
                            Color.clear
                                .modifier(SessionChrome(ui: ui, voiceScreen: false, agentsScreen: true))
                                .environmentObject(model)
                                .environment(\.appPhase, .active)
                        }
                    }
                    AgentWindowScene(route: nil)
                        .environmentObject(model)
                        .environment(\.appPhase, .active)
                }
            }
        }

        let model = makeModel()
        model.settings.address = "127.0.0.1:7777"
        model.settings.hasBeenConfigured = true
        defer { model.fleet.set([]) }
        let box = MainOpenBox()

        let host = UIHostingController(rootView: Harness(box: box, model: model))
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 1024, height: 768))
        window.rootViewController = host
        window.makeKeyAndVisible()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        defer {
            window.isHidden = true
            window.rootViewController = nil
            RunLoop.main.run(until: Date().addingTimeInterval(0.02))
        }

        XCTAssertTrue(model.viewingAgentsScreen)

        box.showMain = false
        host.view.setNeedsLayout()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.08))

        XCTAssertFalse(model.viewingAgentsScreen, "closing the last main window should clear agentsScreen visibility")
    }

    // MARK: - Finding 18: Dynamic Island isStale & VoiceLiveActivity refresh serialization

    func testDynamicIslandRespectsIsStaleInTitleLookWaitingAndCompactVoiceOver() {
        let row = LiveActivityContent.Row(pane: "%1", agent: "auth-fix", question: "Run tests?")
        let active = LiveActivityContent(phase: .listening, muted: false, rows: [row], waiting: 2, working: 1, done: 0)

        XCTAssertEqual(active.islandCenterTitle(isStale: false), "Listening")
        XCTAssertEqual(active.islandCenterTitle(isStale: true), LiveActivityContent.staleTitle)

        XCTAssertEqual(active.islandWaitingCount(isStale: false), 2)
        XCTAssertEqual(active.islandWaitingCount(isStale: true), 0, "stale Dynamic Island should not advertise live waiting counts")

        XCTAssertEqual(active.compactAccessibilityLabel(isStale: false), "Listening, 2 waiting")
        XCTAssertEqual(active.compactAccessibilityLabel(isStale: true), LiveActivityContent.staleAccessibilityLabel)

        let freshLook = PhaseLook(active, isStale: false)
        let staleLook = PhaseLook(active, isStale: true)
        XCTAssertEqual(freshLook.symbol, "waveform")
        XCTAssertNotEqual(staleLook.symbol, "waveform", "stale Dynamic Island should not show an active listening waveform")
    }

    func testLiveActivityRefreshSerializesBehindInFlightSyncUpdate() async throws {
        let driver = VoiceLiveActivity()
        var events: [String] = []

        driver.enqueueSyncUpdate {
            events.append("sync-start")
            try? await Task.sleep(nanoseconds: 120_000_000)
            events.append("sync-end")
        }
        driver.enqueueRefreshUpdate {
            events.append("refresh-start")
            events.append("refresh-end")
        }
        driver.enqueueSyncUpdate {
            events.append("next-sync")
        }

        try await Task.sleep(nanoseconds: 250_000_000)
        XCTAssertEqual(events, ["sync-start", "sync-end", "refresh-start", "refresh-end", "next-sync"])
    }

    // MARK: - Finding 15: PermissionPrimerView ScrollView at Accessibility Dynamic Type

    func testPermissionPrimerScrollsAtAccessibility5AndDoesNotOverflowViewport() throws {
        let view = PermissionPrimerView(primer: .notifications, answer: { _ in })
            .environment(\.dynamicTypeSize, .accessibility5)
        let host = UIHostingController(rootView: view)
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 320, height: 568))
        window.rootViewController = host
        window.makeKeyAndVisible()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        defer {
            window.isHidden = true
            window.rootViewController = nil
        }

        let scrollView = try XCTUnwrap(
            findSubview(ofType: UIScrollView.self, in: host.view),
            "PermissionPrimerView must wrap its content in a ScrollView for Accessibility Dynamic Type"
        )
        XCTAssertGreaterThan(scrollView.contentSize.height, scrollView.bounds.height)
    }

    // MARK: - Finding 16: FlockUnavailableView unpaired copy & action

    func testFlockUnavailableNotConnectedWhenUnpairedOffersPairActionInsteadOfConnect() {
        let unpairedCopy = FlockUnavailableView.notConnectedCopy(isConfigured: false)
        XCTAssertEqual(unpairedCopy.title, "Pair your computer")
        XCTAssertEqual(unpairedCopy.description, "Pair your computer to see its agents.")
        XCTAssertEqual(unpairedCopy.actionTitle, "Pair")

        let configuredCopy = FlockUnavailableView.notConnectedCopy(isConfigured: true)
        XCTAssertEqual(configuredCopy.title, "Not connected")
        XCTAssertEqual(configuredCopy.description, "Connect to see your agents.")
        XCTAssertEqual(configuredCopy.actionTitle, "Connect")

        let unpairedModel = makeModel()
        defer { unpairedModel.fleet.set([]) }
        XCTAssertFalse(unpairedModel.isConfigured)

        let id = UUID()
        SceneCoordinator.shared.register(id)
        FlockUnavailableView.performNotConnectedAction(model: unpairedModel, scenes: .shared, sceneID: id)
        XCTAssertEqual(unpairedModel.presentation.current, .settings)
    }

    // MARK: - Finding 19: Pre-read fileSize check & off-MainActor file read

    func testAttachmentsLoadFileRejectsOversizedFileByMetadataWithoutReadingContents() async throws {
        let oversizedURL = cacheDir.appendingPathComponent("oversized.bin")
        try FileManager.default.createDirectory(at: cacheDir, withIntermediateDirectories: true)
        FileManager.default.createFile(atPath: oversizedURL.path, contents: Data())
        XCTAssertEqual(truncate(oversizedURL.path, off_t(Attachments.maxBytes + 1)), 0)
        // Remove read permission: stat() still reports fileSize = 20 MB + 1, whereas
        // attempting Data(contentsOf:) before checking fileSize fails with EACCES (.unreadable).
        XCTAssertEqual(chmod(oversizedURL.path, 0), 0)
        defer {
            _ = chmod(oversizedURL.path, 0o600)
            try? FileManager.default.removeItem(at: oversizedURL)
        }

        let oversizedResult = await Attachments.loadFile(at: oversizedURL)
        XCTAssertEqual(oversizedResult, .failure(.tooLarge), "loadFile should reject >20 MB files via resourceValues before reading file bytes")

        let smallURL = cacheDir.appendingPathComponent("note.json")
        let payload = Data(#"{"ok":true}"#.utf8)
        try payload.write(to: smallURL)
        let smallResult = await Attachments.loadFile(at: smallURL)
        let loaded = try smallResult.get()
        XCTAssertEqual(loaded.data, payload)
        XCTAssertEqual(loaded.name, "note.json")
        XCTAssertEqual(loaded.mimeType, "application/json")
    }
}

@Observable
private final class PhaseBox {
    var phase: ScenePhase = .active
}

@Observable
private final class MainOpenBox {
    var showMain = true
}
#endif
