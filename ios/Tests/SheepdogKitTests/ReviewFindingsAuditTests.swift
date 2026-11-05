#if os(iOS)
import Foundation
import Observation
import SwiftUI
import UIKit
import XCTest
@testable import Sheepdog
@testable import SheepdogKit

/// Regression tests for the independent iOS audit review (`run-r-ios.jsonl.md`),
/// ordered worst-first and written to fail before each fix.
@MainActor
final class ReviewFindingsAuditTests: XCTestCase {
    private var suiteName: String!
    private var defaults: UserDefaults!
    private var cacheDir: URL!
    private var keyService: String!

    override func setUp() async throws {
        try await super.setUp()
        suiteName = "sheepdog.tests.review-audit-\(UUID().uuidString)"
        defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)
        cacheDir = FileManager.default.temporaryDirectory.appendingPathComponent("sheepdog-ra-\(UUID().uuidString)", isDirectory: true)
        keyService = "sheepdog.tests.pushkeys.\(UUID().uuidString)"
        ReviewAuditStub.reset()
    }

    override func tearDown() async throws {
        ReviewAuditStub.reset()
        defaults.removePersistentDomain(forName: suiteName)
        try? FileManager.default.removeItem(at: cacheDir)
        try await super.tearDown()
    }

    private func makeModel() -> SessionViewModel {
        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let settings = SettingsStore(defaults: defaults, computers: store)
        return SessionViewModel(settings: settings, cache: ConversationCache(directory: cacheDir))
    }

    private func makeCoordinator(
        store: ComputerStore,
        settings: SettingsStore,
        token: String? = "deadbeef",
        onOpenInstead: @escaping (PushPayload, String) -> Void = { _, _ in }
    ) -> PushCoordinator {
        let service = keyService!
        let names = suiteName!
        return PushCoordinator(
            defaults: defaults,
            store: store,
            makeSettings: { settings },
            makeKeyStore: { id in
                let account = id.map(PushContentKeyStore.account(computer:)) ?? "content-key"
                return PushContentKeyStore(service: service, account: account, accessGroup: nil)
            },
            namesSuite: names,
            makeClient: { endpoint in
                let config = URLSessionConfiguration.ephemeral
                config.protocolClasses = [ReviewAuditStub.self]
                return DaemonClient(endpoint: endpoint, session: URLSession(configuration: config))
            },
            notifyOpenHandler: { payload, reason in onOpenInstead(payload, reason) },
            removeDeliveredHandler: { _ in },
            initialDeviceToken: token
        )
    }

    private func makeCode(daemonID: String, name: String = "Studio Mac", pin: String = "pin_test_123", code: String = "one-time", address: String = "127.0.0.1:1") throws -> PairingCode {
        let encodedName = name.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? name
        let encodedAddr = address.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? address
        let link = "https://\(address)/voice#pair=1&d=\(daemonID)&e=\(code)&k=\(pin)&n=\(encodedName)&u=\(encodedAddr)"
        return try XCTUnwrap(PairingCode(link))
    }

    private func makeStubbedComputerModel(id: String, name: String, endpoint: DaemonEndpoint) -> ComputerModel {
        let connection = ComputerConnection(computerID: id, address: endpoint.display) { _ in
            let config = URLSessionConfiguration.ephemeral
            config.protocolClasses = [ReviewAuditStub.self]
            return DaemonClient(endpoint: endpoint, session: URLSession(configuration: config))
        }
        return ComputerModel(
            id: id,
            name: name,
            addresses: [endpoint.display],
            certExpires: nil,
            connection: connection,
            scheduler: ManualLinkClock(),
            demand: LinkDemand(isCurrent: false)
        )
    }

    // MARK: - Finding 2 (High / Supreme Rule 1): Never route an answer to another computer

    func testNotificationActionForRemovedComputerNeverAnswersCurrentComputer() async throws {
        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let settings = SettingsStore(defaults: defaults, computers: store)
        let compB = PairedComputer(
            daemonID: "computer-b",
            name: "Work Mac",
            addresses: ["198.51.100.20:7777"],
            pin: "pin_b",
            deviceID: "dev-b",
            pairedAt: Date()
        )
        store.save(compB, address: "198.51.100.20:7777")
        store.currentID = "computer-b"
        settings.hasBeenConfigured = true
        store.credentials(for: "computer-b").adopt(SessionTicket(ticket: "tk-b", expires: Date().addingTimeInterval(3600)))

        var openInsteadReasons: [String] = []
        let push = makeCoordinator(store: store, settings: settings) { _, reason in
            openInsteadReasons.append(reason)
        }

        // Notification belongs to removed "computer-a", which happens to share pendingID "ap-1".
        let payload = PushPayload(
            kind: "approval",
            paneID: "%1",
            agent: "deploy",
            pendingID: "ap-1",
            computer: "computer-a",
            options: [PendingOption(index: 1, label: "Yes"), PendingOption(index: 2, label: "No")]
        )
        await push.handle(action: PushActions.approve, payload: payload, notificationID: "notif-1")

        XCTAssertTrue(ReviewAuditStub.answeredHosts.isEmpty, "Approve for removed computer-a must NEVER reach computer-b")
        XCTAssertEqual(openInsteadReasons.count, 1, "User must be told via follow-up notification that the answer was not sent")
    }

    func testNotificationActionWithoutComputerIDRefusesWhenMultipleComputersPaired() async throws {
        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let settings = SettingsStore(defaults: defaults, computers: store)
        let compA = PairedComputer(daemonID: "computer-a", name: "Home Mac", addresses: ["192.0.2.10:7777"], pin: "pin_a", deviceID: "dev-a", pairedAt: Date())
        let compB = PairedComputer(daemonID: "computer-b", name: "Work Mac", addresses: ["198.51.100.20:7777"], pin: "pin_b", deviceID: "dev-b", pairedAt: Date())
        store.save(compA, address: "192.0.2.10:7777")
        store.save(compB, address: "198.51.100.20:7777")
        store.currentID = "computer-b"
        settings.hasBeenConfigured = true
        store.credentials(for: "computer-a").adopt(SessionTicket(ticket: "tk-a", expires: Date().addingTimeInterval(3600)))
        store.credentials(for: "computer-b").adopt(SessionTicket(ticket: "tk-b", expires: Date().addingTimeInterval(3600)))

        var openInsteadReasons: [String] = []
        let push = makeCoordinator(store: store, settings: settings) { _, reason in
            openInsteadReasons.append(reason)
        }

        // Legacy notification without sheepdog.computer while 2 computers are paired.
        let payload = PushPayload(
            kind: "approval",
            paneID: "%1",
            agent: "deploy",
            pendingID: "ap-1",
            computer: nil,
            options: [PendingOption(index: 1, label: "Yes"), PendingOption(index: 2, label: "No")]
        )
        await push.handle(action: PushActions.approve, payload: payload, notificationID: "notif-2")

        XCTAssertTrue(ReviewAuditStub.answeredHosts.isEmpty, "Ambiguous notification without computer id must never guess currentID")
        XCTAssertEqual(openInsteadReasons.count, 1)
    }

    func testTappingCardForRemovedRemoteComputerDropsAnswerAndWarnsUser() async throws {
        let model = makeModel()
        let compB = PairedComputer(daemonID: "computer-b", name: "Work Mac", addresses: ["198.51.100.20:7777"], pin: "pin_b", deviceID: "dev-b", pairedAt: Date())
        model.settings.computers.save(compB, address: "198.51.100.20:7777")
        model.settings.computers.currentID = "computer-b"
        model.settings.hasBeenConfigured = true
        model.computersChanged()
        model.endpointChanged()
        defer { model.fleet.set([]) }

        let colliding = PendingAction(id: "ap-1", paneID: "%1", kind: "answer_prompt", question: "Drop prod DB?", options: [PendingOption(index: 1, label: "Yes")])
        model.fleet.current?.accept(DaemonState(agents: [], pendingAction: colliding, questions: [colliding], voice: .init(mode: "gemini", status: "ready")))

        model.answer(question: "remote:computer-a|ap-1", option: 1)

        XCTAssertEqual(model.fleet.current?.answersPending, 0, "Colliding question on computer-b must not be answered")
        XCTAssertTrue(ReviewAuditStub.answeredHosts.isEmpty)
        XCTAssertNotNil(model.notices.banner, "Dropping an answer for a removed computer must tell the user via banner")

        // Also verify same-frame local-card tap after switching computers drops & warns instead of answering computer-b.
        model.notices.banner = nil
        model.answer(question: "ap-1", option: 1, on: "computer-a")
        XCTAssertEqual(model.fleet.current?.answersPending, 0, "Local card bound to computer-a must never answer computer-b")
        XCTAssertNotNil(model.notices.banner, "Dropping a mismatched local-card answer must warn the user")
    }

    // MARK: - Finding 1 (High / Supreme Rule 2): Pairing daemonID mismatch & key preservation

    func testPairDaemonIDMismatchRejectsWithoutLosingKeysOrCorruptingExistingComputer() async throws {
        let daemonA = "test-mismatch-new-\(UUID().uuidString)"
        let daemonB = "test-mismatch-existing-\(UUID().uuidString)"
        let daemonC = "test-mismatch-repair-\(UUID().uuidString)"
        DeviceKey.delete(daemonID: daemonA)
        DeviceKey.delete(daemonID: daemonB)
        DeviceKey.delete(daemonID: daemonC)
        defer {
            DeviceKey.delete(daemonID: daemonA)
            DeviceKey.delete(daemonID: daemonB)
            DeviceKey.delete(daemonID: daemonC)
        }

        let keyB = try DeviceKey.loadOrCreate(daemonID: daemonB)
        let pubB = try XCTUnwrap(DeviceKey.publicKey(keyB))
        let keyC = try DeviceKey.loadOrCreate(daemonID: daemonC)
        let pubC = try XCTUnwrap(DeviceKey.publicKey(keyC))

        let model = makeModel()
        defer { model.fleet.set([]) }
        let existingB = PairedComputer(
            daemonID: daemonB,
            name: "Home Mac",
            addresses: ["192.0.2.10:7777"],
            pin: "pin_existing_b",
            deviceID: "dev-existing-b",
            pairedAt: Date(timeIntervalSince1970: 1_700_000_000)
        )
        model.settings.computers.save(existingB, address: "192.0.2.10:7777")
        model.settings.computers.currentID = daemonB
        model.settings.hasBeenConfigured = true

        // 1. First-time scan of daemonA where server replies with daemonB's ID.
        ReviewAuditStub.enrollDaemonID = daemonB
        ReviewAuditStub.enrollDeviceID = "dev-rogue"
        let codeA = try makeCode(daemonID: daemonA, pin: "pin_rogue_a")
        let outcomeA = await model.pair(codeA, protocolClasses: [ReviewAuditStub.self])
        if case .paired = outcomeA {
            XCTFail("Mismatched enrolled.daemonID must not succeed as .paired, got \(outcomeA)")
        }
        XCTAssertNil(DeviceKey.load(daemonID: daemonA), "Un-enrolled key for daemonA should be cleaned up")
        XCTAssertNil(model.settings.computers.computer(daemonA))
        let survivingB = try XCTUnwrap(model.settings.computers.computer(daemonB))
        XCTAssertEqual(survivingB.pin, "pin_existing_b", "Existing computer B's pin must not be overwritten")
        XCTAssertEqual(survivingB.deviceID, "dev-existing-b", "Existing computer B's deviceID must not be overwritten")
        XCTAssertEqual(DeviceKey.publicKey(try XCTUnwrap(DeviceKey.load(daemonID: daemonB))), pubB)

        // 2. Re-pair scan of daemonC (which already has a working key!) where server replies with daemonB's ID.
        let codeC = try makeCode(daemonID: daemonC, pin: "pin_repair_c")
        let outcomeC = await model.pair(codeC, protocolClasses: [ReviewAuditStub.self])
        if case .paired = outcomeC {
            XCTFail("Mismatched enrolled.daemonID on re-pair must not succeed as .paired")
        }
        let survivingKeyC = try XCTUnwrap(DeviceKey.load(daemonID: daemonC), "Existing working key for daemonC must never be deleted")
        XCTAssertEqual(DeviceKey.publicKey(survivingKeyC), pubC)
    }

    func testPairEmptyDeviceIDRejectsAndCleansUpNewKey() async throws {
        let daemonID = "test-empty-dev-\(UUID().uuidString)"
        DeviceKey.delete(daemonID: daemonID)
        defer { DeviceKey.delete(daemonID: daemonID) }

        let model = makeModel()
        defer { model.fleet.set([]) }
        ReviewAuditStub.enrollDaemonID = daemonID
        ReviewAuditStub.enrollDeviceID = "   "

        let code = try makeCode(daemonID: daemonID)
        let outcome = await model.pair(code, protocolClasses: [ReviewAuditStub.self])
        if case .paired = outcome {
            XCTFail("Empty device_id in enroll response must fail pairing")
        }
        XCTAssertNil(DeviceKey.load(daemonID: daemonID))
        XCTAssertNil(model.settings.computers.computer(daemonID))
    }

    // MARK: - Finding 3 (High): Superseded computer errors must throw Moved & never clobber new computer

    func testComputerFleetOnCurrentThrowsMovedWhenSupersededRequestFails() async throws {
        let credsA = DeviceCredentials(daemonID: "comp-a", secrets: InMemorySecretStore())
        credsA.computer = PairedComputer(daemonID: "comp-a", name: "Mac A", addresses: ["192.0.2.10:7777"], pin: "pa", deviceID: "da", pairedAt: Date())
        credsA.adopt(SessionTicket(ticket: "ta", expires: Date().addingTimeInterval(3600)))
        let credsB = DeviceCredentials(daemonID: "comp-b", secrets: InMemorySecretStore())
        credsB.computer = PairedComputer(daemonID: "comp-b", name: "Mac B", addresses: ["198.51.100.20:7777"], pin: "pb", deviceID: "db", pairedAt: Date())
        credsB.adopt(SessionTicket(ticket: "tb", expires: Date().addingTimeInterval(3600)))

        let endA = try XCTUnwrap(DaemonEndpoint(text: "192.0.2.10:7777", secure: true, credentials: credsA))
        let endB = try XCTUnwrap(DaemonEndpoint(text: "198.51.100.20:7777", secure: true, credentials: credsB))
        let modelA = makeStubbedComputerModel(id: "comp-a", name: "Mac A", endpoint: endA)
        let modelB = makeStubbedComputerModel(id: "comp-b", name: "Mac B", endpoint: endB)

        let fleet = ComputerFleet()
        fleet.set([modelA, modelB])
        fleet.makeCurrent("comp-a")
        defer { fleet.set([]) }

        ReviewAuditStub.mode = .hang
        ReviewAuditStub.statusByHost["192.0.2.10"] = (401, ["error": "revoked", "reason": "device_revoked"])

        let inFlight = Task {
            try await fleet.onCurrent(as: DaemonClient.self) { try await $0.state() }
        }
        try await ReviewAuditStub.waitForHangingRequests(count: 1)

        // Switch current computer to B before A's failing request returns.
        fleet.makeCurrent("comp-b")
        ReviewAuditStub.releaseAll()

        do {
            _ = try await inFlight.value
            XCTFail("Expected ComputerFleet.Moved to be thrown")
        } catch is ComputerFleet.Moved {
            // Expected: superseded error is converted to Moved.
        } catch {
            XCTFail("Expected ComputerFleet.Moved, but got \(error)")
        }
    }

    func testLoudRefreshFailureFromPreviousComputerDoesNotTearDownNewComputer() async throws {
        let model = makeModel()
        let work = PairedComputer(daemonID: "work-id", name: "Work Mac", addresses: ["192.0.2.10:7777"], pin: "pw", deviceID: "dw", pairedAt: Date())
        let home = PairedComputer(daemonID: "home-id", name: "Home Mac", addresses: ["198.51.100.20:7777"], pin: "ph", deviceID: "dh", pairedAt: Date())
        model.settings.computers.save(work, address: "192.0.2.10:7777")
        model.settings.computers.save(home, address: "198.51.100.20:7777")
        model.settings.computers.currentID = "work-id"
        model.settings.hasBeenConfigured = true
        model.settings.computers.credentials(for: "work-id").adopt(SessionTicket(ticket: "tw", expires: Date().addingTimeInterval(3600)))
        model.settings.computers.credentials(for: "home-id").adopt(SessionTicket(ticket: "th", expires: Date().addingTimeInterval(3600)))

        let workEnd = try XCTUnwrap(model.settings.computers.endpoint(for: "work-id"))
        let homeEnd = try XCTUnwrap(model.settings.computers.endpoint(for: "home-id"))
        model.fleet.set([
            makeStubbedComputerModel(id: "work-id", name: "Work Mac", endpoint: workEnd),
            makeStubbedComputerModel(id: "home-id", name: "Home Mac", endpoint: homeEnd),
        ])
        model.fleet.makeCurrent("work-id")
        defer {
            model.disconnect()
            model.fleet.set([])
        }

        ReviewAuditStub.mode = .hang
        ReviewAuditStub.statusByHost["192.0.2.10"] = (401, ["error": "revoked", "reason": "device_revoked"])

        let loudRefresh = Task { await model.refreshState(quiet: false) }
        try await ReviewAuditStub.waitForHangingRequests(count: 1)

        // User switches to Home Mac and begins probing/connecting there while Work Mac's loud refresh is in flight.
        model.settings.computers.currentID = "home-id"
        model.fleet.makeCurrent("home-id")
        model.consent.agree()
        ReviewAuditStub.hangHosts = ["198.51.100.20"]
        ReviewAuditStub.mode = .immediate
        model.voice.send(.connectTapped)
        XCTAssertTrue(model.voice.core.connection.wantsConnection)

        ReviewAuditStub.release(host: "192.0.2.10")
        let ok = await loudRefresh.value

        XCTAssertFalse(ok)
        XCTAssertTrue(model.voice.core.connection.wantsConnection, "Work Mac's loud refresh failure must not end Home Mac's voice session")
        XCTAssertNil(model.notices.pairingProblem, "Work Mac's 401 device_revoked must not mark Home Mac as needing re-scan")
        XCTAssertNil(model.notices.banner, "Work Mac's failure must not raise an error banner on Home Mac")
        model.disconnect()
        ReviewAuditStub.releaseAll()
    }

    func testSettingsModelClearResetsSwitchingFlagsAndIgnoresLateErrorsFromPreviousComputer() async throws {
        let model = makeModel()
        let work = PairedComputer(daemonID: "work-id", name: "Work Mac", addresses: ["192.0.2.10:7777"], pin: "pw", deviceID: "dw", pairedAt: Date())
        let home = PairedComputer(daemonID: "home-id", name: "Home Mac", addresses: ["198.51.100.20:7777"], pin: "ph", deviceID: "dh", pairedAt: Date())
        model.settings.computers.save(work, address: "192.0.2.10:7777")
        model.settings.computers.save(home, address: "198.51.100.20:7777")
        model.settings.computers.currentID = "work-id"
        model.settings.hasBeenConfigured = true
        model.settings.computers.credentials(for: "work-id").adopt(SessionTicket(ticket: "tw", expires: Date().addingTimeInterval(3600)))
        model.settings.computers.credentials(for: "home-id").adopt(SessionTicket(ticket: "th", expires: Date().addingTimeInterval(3600)))

        let workEnd = try XCTUnwrap(model.settings.computers.endpoint(for: "work-id"))
        let homeEnd = try XCTUnwrap(model.settings.computers.endpoint(for: "home-id"))
        let workRuntime = makeStubbedComputerModel(id: "work-id", name: "Work Mac", endpoint: workEnd)
        let homeRuntime = makeStubbedComputerModel(id: "home-id", name: "Home Mac", endpoint: homeEnd)
        model.fleet.set([workRuntime, homeRuntime])
        model.fleet.makeCurrent("work-id")
        defer {
            model.disconnect()
            model.fleet.set([])
        }

        // Seed Work Mac's voice providers so selectVoiceProvider("gemini") starts a switch.
        await model.settingsModel.loadVoiceProviders()
        XCTAssertEqual(model.settingsModel.voiceProviders?.provider, "openai")

        // Hang Work Mac's requests and make them fail with HTTP 500 when released.
        ReviewAuditStub.mode = .hang
        ReviewAuditStub.statusByHost["192.0.2.10"] = (500, ["error": "work daemon exploded"])

        model.settingsModel.selectVoiceProvider("gemini")
        model.settingsModel.selectBrain(provider: "anthropic", model: "claude-opus-4-6")
        let staleLoads = Task {
            await model.settingsModel.loadVoiceProviders()
            await model.settingsModel.loadDaemonSettings()
        }
        try await ReviewAuditStub.waitForHangingRequests(count: 3)
        XCTAssertTrue(model.settingsModel.switchingProvider)
        XCTAssertTrue(model.settingsModel.switchingBrain)

        // Switch to Home Mac and clear SettingsModel as endpointChanged() does.
        model.settings.computers.currentID = "home-id"
        model.fleet.makeCurrent("home-id")
        model.settingsModel.clear()

        XCTAssertFalse(model.settingsModel.switchingProvider, "clear() must reset switchingProvider immediately")
        XCTAssertFalse(model.settingsModel.switchingBrain, "clear() must reset switchingBrain immediately")

        // Home Mac responds immediately and populates SettingsModel.
        ReviewAuditStub.mode = .immediate
        await model.settingsModel.loadVoiceProviders()
        await model.settingsModel.loadDaemonSettings()
        XCTAssertNotNil(model.settingsModel.voiceProviders)
        XCTAssertNotNil(model.settingsModel.daemonSettings)

        // Now release Work Mac's hanging 500 errors.
        ReviewAuditStub.releaseAll()
        await staleLoads.value
        try await Task.sleep(nanoseconds: 50_000_000)

        XCTAssertNotNil(model.settingsModel.voiceProviders, "Work Mac's late error must not wipe Home Mac's voiceProviders")
        XCTAssertNotNil(model.settingsModel.daemonSettings, "Work Mac's late error must not wipe Home Mac's daemonSettings")
        XCTAssertNil(model.settingsModel.providerError, "Work Mac's late error must not set providerError on Home Mac")
        XCTAssertNil(model.settingsModel.brainError, "Work Mac's late error must not set brainError on Home Mac")
        XCTAssertNil(model.settingsModel.settingsError, "Work Mac's late error must not set settingsError on Home Mac")
    }

    // MARK: - Finding 4 (Medium): Removing last computer clears all in-memory UI/model state

    func testRemovingLastComputerClearsInMemorySessionAndModelState() async throws {
        let soloID = "test-solo-\(UUID().uuidString)"
        defer { DeviceKey.delete(daemonID: soloID) }

        let model = makeModel()
        let solo = PairedComputer(daemonID: soloID, name: "Solo Mac", addresses: ["192.0.2.10:7777"], pin: "p1", deviceID: "d1", pairedAt: Date())
        model.settings.computers.save(solo, address: "192.0.2.10:7777")
        model.settings.computers.currentID = soloID
        model.settings.hasBeenConfigured = true
        model.settings.computers.credentials(for: soloID).adopt(SessionTicket(ticket: "t1", expires: Date().addingTimeInterval(3600)))

        let soloEnd = try XCTUnwrap(model.settings.computers.endpoint(for: soloID))
        let soloRuntime = makeStubbedComputerModel(id: soloID, name: "Solo Mac", endpoint: soloEnd)
        model.fleet.set([soloRuntime])
        model.fleet.makeCurrent(soloID)
        model.attachCurrent()
        defer {
            model.disconnect()
            model.fleet.set([])
        }

        // Populate conversation cache, agents, hello, settingsModel, and notices.
        let page = try DaemonClient.decoder.decode(ConversationPage.self, from: Data(Fixtures.conversationPage.utf8))
        var populatedHistory = ConversationHistory()
        populatedHistory.applyNewest(page, requestedAt: Date())
        try ConversationCache(directory: cacheDir).save(
            ConversationSnapshot(endpoint: soloEnd, history: populatedHistory, lastEventID: 42),
            for: soloEnd
        )
        let agent = try DaemonClient.decoder.decode(
            Agent.self,
            from: Data(#"{"pane_id":"%1","alias":"auth","state":"working"}"#.utf8)
        )
        let hello = Hello(
            name: "sheepdog",
            version: "1.2.3",
            voiceName: "Rex",
            auth: "token",
            ws: "/ws",
            audio: .init(inHz: 16000, outHz: 24000, format: "pcm16")
        )
        await model.settingsModel.loadVoiceProviders()
        soloRuntime.accept(DaemonState(agents: [agent], pendingAction: nil, questions: [], voice: .init(mode: "gemini", status: "ready")))
        model.receiveCard(ScreenCard(cardID: "card-solo", kind: .card, title: "Summary", markdown: "Hello"))
        model.agentsModel.expandedAgents.insert("%1")
        model.linkModel.setHello(hello)
        model.notices.banner = .init(tone: .warning, message: "Old warning", hint: nil, retryTitle: nil)

        XCTAssertFalse(model.conversationModel.rows.isEmpty)
        XCTAssertFalse(model.agentsModel.agents.isEmpty)
        XCTAssertNotNil(model.settingsModel.voiceProviders)
        XCTAssertNotNil(model.linkModel.hello)
        XCTAssertNotNil(model.notices.banner)

        await model.removeComputer(soloID)

        XCTAssertTrue(model.conversationModel.rows.isEmpty, "Removing last computer must clear visible conversation rows")
        XCTAssertTrue(model.agentsModel.agents.isEmpty, "Removing last computer must clear agentsModel.agents")
        XCTAssertTrue(model.agentsModel.expandedAgents.isEmpty, "Removing last computer must clear expandedAgents")
        XCTAssertNil(model.settingsModel.voiceProviders, "Removing last computer must clear settingsModel")
        XCTAssertNil(model.linkModel.hello, "Removing last computer must clear linkModel.hello")
        XCTAssertNil(model.notices.banner, "Removing last computer must clear active banner")
    }

    // MARK: - Finding 5 (Medium): PushCoordinator unregister vs in-flight register race & daemonStatus isolation

    func testUnregisterComputerCancelsInFlightRegisterAndPreventsStaleKeyOrDaemonStatusLeak() async throws {
        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let settings = SettingsStore(defaults: defaults, computers: store)
        let compA = PairedComputer(daemonID: "comp-a", name: "Mac A", addresses: ["192.0.2.10:7777"], pin: "pa", deviceID: "da", pairedAt: Date())
        let compB = PairedComputer(daemonID: "comp-b", name: "Mac B", addresses: ["198.51.100.20:7777"], pin: "pb", deviceID: "db", pairedAt: Date())
        store.save(compA, address: "192.0.2.10:7777")
        store.save(compB, address: "198.51.100.20:7777")
        store.currentID = "comp-a"
        settings.hasBeenConfigured = true
        store.credentials(for: "comp-a").adopt(SessionTicket(ticket: "ta", expires: Date().addingTimeInterval(3600)))
        store.credentials(for: "comp-b").adopt(SessionTicket(ticket: "tb", expires: Date().addingTimeInterval(3600)))

        let service = keyService!
        let keyStoreA = PushContentKeyStore(service: service, account: PushContentKeyStore.account(computer: "comp-a"), accessGroup: nil)
        let keyStoreB = PushContentKeyStore(service: service, account: PushContentKeyStore.account(computer: "comp-b"), accessGroup: nil)
        defer {
            keyStoreA.remove()
            keyStoreB.remove()
        }

        // Hang Mac A's HTTP requests; Mac B returns 404 on /api/push/status (no push service).
        ReviewAuditStub.hangHosts = ["192.0.2.10"]
        ReviewAuditStub.pushStatusByHost["192.0.2.10"] = (200, ["enabled": true, "devices": 2, "mode": "relay"])
        ReviewAuditStub.pushStatusByHost["198.51.100.20"] = (404, ["error": "not found"])

        let push = makeCoordinator(store: store, settings: settings)
        let regTask = Task { await push.register() }
        let statusTask = Task { await push.loadDaemonStatus() }
        try await ReviewAuditStub.waitForHangingRequests(count: 2)

        // While Mac A's register & status are in flight, remove Mac A and switch to Mac B!
        ReviewAuditStub.hangHosts = []
        let unregisterTask = Task { await push.unregister(computer: "comp-a") }
        store.remove("comp-a")
        store.currentID = "comp-b"
        await unregisterTask.value

        // Release Mac A's in-flight responses after Mac A was already unregistered & removed.
        ReviewAuditStub.releaseAll()
        await regTask.value
        await statusTask.value

        XCTAssertNil(keyStoreA.key(), "In-flight register for removed comp-a must NOT write comp-a's contentKey back to Keychain")
        let savedTargets = Set(defaults.stringArray(forKey: "push.registeredTargets") ?? [])
        XCTAssertFalse(savedTargets.contains("computer:comp-a"), "Removed comp-a must NOT be resurrected in registeredTargets")
        XCTAssertNil(push.daemonStatus, "Stale /api/push/status from comp-a must not leak onto comp-b")
    }

    // MARK: - Finding 6 (Medium): Cold-wake notification answer preserves Preferred address

    func testColdBackgroundAnswerRecordsLinkWinnerWithoutOverwritingPreferredAddress() async throws {
        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let settings = SettingsStore(defaults: defaults, computers: store)
        let home = PairedComputer(
            daemonID: "home",
            name: "Home Mac",
            addresses: ["192.0.2.10:7777", "198.51.100.20:7777"],
            pin: "pin_home",
            deviceID: "dev-home",
            pairedAt: Date()
        )
        store.save(home, address: "192.0.2.10:7777")
        store.currentID = "home"
        settings.hasBeenConfigured = true
        store.credentials(for: "home").adopt(SessionTicket(ticket: "th", expires: Date().addingTimeInterval(3600)))

        // LAN address is unreachable while away from home; Tailscale address answers.
        ReviewAuditStub.unreachableHosts = ["192.0.2.10"]

        let push = makeCoordinator(store: store, settings: settings)
        let payload = PushPayload(
            kind: "approval",
            paneID: "%1",
            agent: "auth",
            pendingID: "ap-1",
            computer: "home",
            options: [PendingOption(index: 1, label: "Yes"), PendingOption(index: 2, label: "No")]
        )
        await push.handle(action: PushActions.approve, payload: payload, notificationID: "notif-home")

        XCTAssertEqual(ReviewAuditStub.answeredHosts, ["198.51.100.20"])
        XCTAssertEqual(store.address(for: "home"), "192.0.2.10:7777", "Cold-wake race winner must not overwrite the user's Preferred address")
    }

    // MARK: - Finding 7 (Medium): ConversationCache load/clear never block @MainActor behind enqueueSave

    func testConversationCacheLoadAndClearDoNotBlockOnQueuedDiskWrite() async throws {
        // Create a file where a cache expects a directory so its enqueueSave triggers onError ON ioQueue,
        // letting us pause ioQueue deterministically without test-only hooks.
        let blockerFile = cacheDir.appendingPathComponent("blocker-file")
        try FileManager.default.createDirectory(at: cacheDir, withIntermediateDirectories: true)
        try Data("x".utf8).write(to: blockerFile)

        let blockingCache = ConversationCache(directory: blockerFile)
        let fastDir = cacheDir.appendingPathComponent("fast", isDirectory: true)
        let fastCache = ConversationCache(directory: fastDir)

        let epBlock = DaemonEndpoint(host: "127.0.0.1", port: 7001)
        let epFast = DaemonEndpoint(host: "127.0.0.1", port: 7002)
        let snap = ConversationSnapshot(endpoint: epFast.display, items: [], nextBefore: nil, lastEventID: 99)

        let ioPaused = DispatchSemaphore(value: 0)
        let releaseIO = DispatchSemaphore(value: 0)
        var released = false
        defer {
            if !released { releaseIO.signal() }
        }

        blockingCache.enqueueSave(snap, for: epBlock) { _ in
            ioPaused.signal()
            releaseIO.wait()
        }
        XCTAssertEqual(ioPaused.wait(timeout: .now() + 1.0), .success, "ioQueue should enter the blocking callback")

        // While ioQueue is stalled, enqueueSave + load + clear on fastCache must finish immediately without blocking!
        let probeDone = DispatchSemaphore(value: 0)
        nonisolated(unsafe) var loadedBeforeClear: ConversationSnapshot?
        nonisolated(unsafe) var loadedAfterClear: ConversationSnapshot?
        DispatchQueue.global(qos: .userInitiated).async {
            fastCache.enqueueSave(snap, for: epFast)
            loadedBeforeClear = fastCache.load(for: epFast)
            fastCache.clear(for: epFast)
            loadedAfterClear = fastCache.load(for: epFast)
            probeDone.signal()
        }

        let finishedWhileStalled = probeDone.wait(timeout: .now() + 0.15) == .success
        released = true
        releaseIO.signal()
        _ = probeDone.wait(timeout: .now() + 1.0)

        XCTAssertTrue(finishedWhileStalled, "ConversationCache.load and clear must not block behind queued disk I/O")
        XCTAssertEqual(loadedBeforeClear?.lastEventID, 99)
        XCTAssertNil(loadedAfterClear, "clear(for:) must immediately hide an enqueued snapshot")

        // Give ioQueue a moment to drain and ensure the superseded enqueueSave did NOT recreate the cleared file.
        try await Task.sleep(nanoseconds: 50_000_000)
        XCTAssertNil(fastCache.load(for: epFast), "Draining enqueueSave after clear(for:) must not resurrect cleared file")
    }

    // MARK: - Finding 8 (Medium): Foreground approval banner NOT suppressed when covered by Settings, demo, or terminal

    func testForegroundPushBannerNotSuppressedWhenCoveredBySettingsDemoOrTerminal() async throws {
        let model = makeModel()
        let work = PairedComputer(daemonID: "work-id", name: "Work Mac", addresses: ["192.0.2.10:7777"], pin: "pw", deviceID: "dw", pairedAt: Date())
        model.settings.computers.save(work, address: "192.0.2.10:7777")
        model.settings.computers.currentID = "work-id"
        model.settings.hasBeenConfigured = true
        model.settings.computers.credentials(for: "work-id").adopt(SessionTicket(ticket: "tw", expires: Date().addingTimeInterval(3600)))

        let workEnd = try XCTUnwrap(model.settings.computers.endpoint(for: "work-id"))
        let workRuntime = makeStubbedComputerModel(id: "work-id", name: "Work Mac", endpoint: workEnd)
        model.fleet.set([workRuntime])
        model.fleet.makeCurrent("work-id")
        model.attachCurrent()
        defer { model.fleet.set([]) }

        // Simulate current computer online with /events open, voice off.
        workRuntime.setDemand(LinkDemand(isCurrent: true, phase: .active))
        workRuntime.start()
        for _ in 0..<20 where !workRuntime.link.isUsable {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTAssertTrue(workRuntime.link.isUsable)
        model.linkModel.setLiveUpdates(.open)

        let approval = PushPayload(
            kind: "approval",
            paneID: "%1",
            agent: "auth",
            pendingID: "ap-1",
            computer: "work-id",
            options: [PendingOption(index: 1, label: "Yes"), PendingOption(index: 2, label: "No")]
        )

        // 1. Uncovered Conversation screen -> floating dock shows approval card -> suppress banner.
        model.setScreen(visible: true, voiceScreen: true, agentsScreen: false)
        XCTAssertTrue(model.shouldSuppressForegroundPush(approval))

        // 2. Covered by Settings sheet -> approval card is covered -> DO NOT suppress banner!
        model.presentation.present(.settings)
        XCTAssertFalse(model.shouldSuppressForegroundPush(approval), "Settings sheet covers floating cards, so banner must not be suppressed")
        model.presentation.dismiss(.settings)

        // 3. Covered by offline demo fullScreenCover -> DO NOT suppress real daemon's banner!
        model.openDemo()
        XCTAssertFalse(model.shouldSuppressForegroundPush(approval), "Offline demo covers real session, so real daemon banner must not be suppressed")
        model.demoClosed()

        // 4. Pushed into an agent's terminal screen (voiceScreen == false, agentsScreen == false, voice off) -> DO NOT suppress!
        model.setScreen(visible: true, voiceScreen: false, agentsScreen: false)
        XCTAssertFalse(model.shouldSuppressForegroundPush(approval), "Terminal screen hides other agents' floating cards when voice is off")
    }

    // MARK: - Finding 9 (Medium): Multi-computer focusedAgent waits until target agent card loads

    func testMultiComputerFocusedAgentRemainsUntilTargetAgentLoads() throws {
        let model = makeModel()
        let work = PairedComputer(daemonID: "work-id", name: "Work Mac", addresses: ["192.0.2.10:7777"], pin: "pw", deviceID: "dw", pairedAt: Date())
        let home = PairedComputer(daemonID: "home-id", name: "Home Mac", addresses: ["198.51.100.20:7777"], pin: "ph", deviceID: "dh", pairedAt: Date())
        model.settings.computers.save(work, address: "192.0.2.10:7777")
        model.settings.computers.save(home, address: "198.51.100.20:7777")
        model.settings.computers.currentID = "work-id"
        model.settings.hasBeenConfigured = true
        model.settings.computers.credentials(for: "work-id").adopt(SessionTicket(ticket: "tw", expires: Date().addingTimeInterval(3600)))
        model.settings.computers.credentials(for: "home-id").adopt(SessionTicket(ticket: "th", expires: Date().addingTimeInterval(3600)))
        model.computersChanged()

        let workEnd = try XCTUnwrap(model.settings.computers.endpoint(for: "work-id"))
        let homeEnd = try XCTUnwrap(model.settings.computers.endpoint(for: "home-id"))
        let workRuntime = makeStubbedComputerModel(id: "work-id", name: "Work Mac", endpoint: workEnd)
        let homeRuntime = makeStubbedComputerModel(id: "home-id", name: "Home Mac", endpoint: homeEnd)
        model.fleet.set([workRuntime, homeRuntime])
        model.fleet.makeCurrent("work-id")
        model.attachCurrent()
        defer { model.fleet.set([]) }

        // Notification tap arrives before Home Mac's /api/state has populated agent %9.
        let target = AgentFocus(computerID: "home-id", paneID: "%9")
        model.focusAgent(target)

        let box = ReviewFlockBox(computers: model.computerSnapshots)
        let host = UIHostingController(rootView: ReviewFlockHarness(box: box, model: model))
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 390, height: 844))
        window.rootViewController = host
        window.makeKeyAndVisible()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        defer {
            window.isHidden = true
            window.rootViewController = nil
            RunLoop.main.run(until: Date().addingTimeInterval(0.02))
        }

        XCTAssertEqual(model.navigation.focusedAgent, target, "focusedAgent must stay non-nil until Home Mac's %9 card exists to scroll to")

        // Now Home Mac's /api/state arrives with agent %9!
        let homeAgent = try DaemonClient.decoder.decode(
            Agent.self,
            from: Data(#"{"pane_id":"%9","alias":"deploy","state":"blocked"}"#.utf8)
        )
        homeRuntime.accept(DaemonState(agents: [homeAgent], pendingAction: nil, questions: [], voice: .init(mode: "gemini", status: "ready")))
        box.computers = model.computerSnapshots
        host.view.setNeedsLayout()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.1))

        XCTAssertNil(model.navigation.focusedAgent, "focusedAgent should clear once %9 has rendered and scrolled")
    }

    // MARK: - Finding 10 (Medium): Reopening window when model.appPhase == .background restores .active

    func testReopeningWindowWhileModelBackgroundedRestoresActiveAppPhase() {
        SceneCoordinator.shared.scenes = []
        SceneCoordinator.shared.agentScenes = []
        SceneCoordinator.shared.requester = nil
        defer {
            SceneCoordinator.shared.scenes = []
            SceneCoordinator.shared.agentScenes = []
            SceneCoordinator.shared.requester = nil
        }

        let model = makeModel()
        model.settings.address = "127.0.0.1:7777"
        model.settings.hasBeenConfigured = true
        model.start()
        model.enteredBackground()
        defer { model.fleet.set([]) }
        XCTAssertEqual(model.appPhase, .background)

        // 1. Reopen a MainSceneRoot while SwiftUI's appPhase is already .active.
        let mainRoot = MainSceneRoot {
            Color.clear
                .modifier(SessionChrome(ui: SceneUI(), voiceScreen: true, agentsScreen: false))
                .environmentObject(model)
                .environment(\.appPhase, .active)
        }
        let mainHost = UIHostingController(rootView: mainRoot)
        let window1 = UIWindow(frame: CGRect(x: 0, y: 0, width: 800, height: 600))
        window1.rootViewController = mainHost
        window1.makeKeyAndVisible()
        mainHost.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        XCTAssertEqual(model.appPhase, .active, "Mounting SessionChrome while appPhase is .active must transition model out of .background")

        window1.isHidden = true
        window1.rootViewController = nil
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))

        // 2. Same for reopening an AgentWindowScene with no main window open.
        SceneCoordinator.shared.scenes = []
        SceneCoordinator.shared.agentScenes = []
        model.enteredBackground()
        XCTAssertEqual(model.appPhase, .background)

        let agentRoot = AgentWindowScene(route: nil)
            .environmentObject(model)
            .environment(\.appPhase, .active)
        let agentHost = UIHostingController(rootView: agentRoot)
        let window2 = UIWindow(frame: CGRect(x: 0, y: 0, width: 800, height: 600))
        window2.rootViewController = agentHost
        window2.makeKeyAndVisible()
        agentHost.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        defer {
            window2.isHidden = true
            window2.rootViewController = nil
        }
        XCTAssertEqual(model.appPhase, .active, "Mounting AgentWindowScene while appPhase is .active must transition model out of .background")
    }

    // MARK: - Finding 11 (Low): PairingFlowView buttons scroll inside ScrollView at Accessibility Dynamic Type

    func testPairingFlowButtonsScrollInsideScrollViewAtAccessibilityDynamicType() throws {
        let model = makeModel()
        defer { model.fleet.set([]) }

        let standardView = PairingFlowView(stage: .problem(.pinMismatch))
            .environmentObject(model)
            .environment(\.dynamicTypeSize, .large)
        let standardHost = UIHostingController(rootView: standardView)
        let window1 = UIWindow(frame: CGRect(x: 0, y: 0, width: 320, height: 568))
        window1.rootViewController = standardHost
        window1.makeKeyAndVisible()
        standardHost.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        let standardScroll = try XCTUnwrap(findSubview(ofType: UIScrollView.self, in: standardHost.view))
        let standardBottomInset = standardScroll.adjustedContentInset.bottom
        window1.isHidden = true
        window1.rootViewController = nil

        let axView = PairingFlowView(stage: .problem(.pinMismatch))
            .environmentObject(model)
            .environment(\.dynamicTypeSize, .accessibility5)
        let axHost = UIHostingController(rootView: axView)
        let window2 = UIWindow(frame: CGRect(x: 0, y: 0, width: 320, height: 568))
        window2.rootViewController = axHost
        window2.makeKeyAndVisible()
        axHost.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        defer {
            window2.isHidden = true
            window2.rootViewController = nil
        }

        let axScroll = try XCTUnwrap(findSubview(ofType: UIScrollView.self, in: axHost.view))
        XCTAssertGreaterThan(standardBottomInset, 60, "Standard Dynamic Type pins buttons in a bottom safeAreaInset")
        XCTAssertLessThan(
            axScroll.adjustedContentInset.bottom,
            standardBottomInset,
            "At Accessibility Dynamic Type sizes, PairingFlowView must move buttons into the ScrollView instead of reserving a large fixed bottom inset"
        )
        XCTAssertGreaterThan(axScroll.contentSize.height, axScroll.bounds.height)
    }

    // MARK: - Finding 12 (Low): Stale Live Activity lock screen orb & stale buttons

    func testStaleLiveActivityLockScreenUsesStaleOrbAndHidesLiveMicToggle() {
        let staleView = VoiceSessionLockScreenView(state: LiveActivitySamples.one, voiceName: "Sheepdog", isStale: true)
        XCTAssertEqual(staleView.orbLook.symbol, "questionmark.circle", "Lock screen orb must use stale PhaseLook when isStale is true")
        XCTAssertFalse(SessionButtons(state: LiveActivitySamples.one, isStale: true).showsLiveControl)
        XCTAssertTrue(SessionButtons(state: LiveActivitySamples.one, isStale: false).showsLiveControl)

        let host = UIHostingController(rootView: staleView)
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 390, height: 200))
        window.rootViewController = host
        window.makeKeyAndVisible()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        defer {
            window.isHidden = true
            window.rootViewController = nil
        }

        let labels = collectAccessibilityLabels(in: host.view)
        XCTAssertFalse(labels.contains("Mute") || labels.contains("Unmute") || labels.contains("Talk"), "Stale Live Activity must not offer an active Mute/Talk toggle")
    }

    // MARK: - View hierarchy helpers

    private func findSubview<T: UIView>(ofType type: T.Type, in view: UIView) -> T? {
        if let matched = view as? T { return matched }
        for subview in view.subviews {
            if let found = findSubview(ofType: type, in: subview) { return found }
        }
        return nil
    }

    private func findLabel(text: String, in view: UIView) -> UILabel? {
        if let label = view as? UILabel, label.text == text { return label }
        for subview in view.subviews {
            if let found = findLabel(text: text, in: subview) { return found }
        }
        return nil
    }

    private func collectAccessibilityLabels(in view: UIView) -> [String] {
        var out: [String] = []
        if let label = view.accessibilityLabel, !label.isEmpty { out.append(label) }
        if let elements = view.accessibilityElements {
            for element in elements {
                if let obj = element as? NSObject, let label = obj.accessibilityLabel, !label.isEmpty {
                    out.append(label)
                }
            }
        }
        for subview in view.subviews {
            out.append(contentsOf: collectAccessibilityLabels(in: subview))
        }
        return out
    }
}

private final class ReviewAuditStub: URLProtocol {
    enum Mode { case immediate, hang }

    private static let lock = NSLock()
    nonisolated(unsafe) private static var state = (
        mode: Mode.immediate,
        hangHosts: Set<String>(),
        unreachableHosts: Set<String>(),
        enrollStatus: 200,
        enrollDaemonID: "daemon-1",
        enrollDeviceID: "dev-enrolled",
        answeredHosts: [String](),
        statusByHost: [String: (Int, [String: Any])](),
        pushStatusByHost: [String: (Int, [String: Any])](),
        waiting: [(host: String, resume: () -> Void)]()
    )

    static var mode: Mode {
        get { lock.withLock { state.mode } }
        set { lock.withLock { state.mode = newValue } }
    }

    static var hangHosts: Set<String> {
        get { lock.withLock { state.hangHosts } }
        set { lock.withLock { state.hangHosts = newValue } }
    }

    static var unreachableHosts: Set<String> {
        get { lock.withLock { state.unreachableHosts } }
        set { lock.withLock { state.unreachableHosts = newValue } }
    }

    static var pushStatusByHost: [String: (Int, [String: Any])] {
        get { lock.withLock { state.pushStatusByHost } }
        set { lock.withLock { state.pushStatusByHost = newValue } }
    }

    static var enrollStatus: Int {
        get { lock.withLock { state.enrollStatus } }
        set { lock.withLock { state.enrollStatus = newValue } }
    }

    static var enrollDaemonID: String {
        get { lock.withLock { state.enrollDaemonID } }
        set { lock.withLock { state.enrollDaemonID = newValue } }
    }

    static var enrollDeviceID: String {
        get { lock.withLock { state.enrollDeviceID } }
        set { lock.withLock { state.enrollDeviceID = newValue } }
    }

    static var answeredHosts: [String] {
        lock.withLock { state.answeredHosts }
    }

    static var statusByHost: [String: (Int, [String: Any])] {
        get { lock.withLock { state.statusByHost } }
        set { lock.withLock { state.statusByHost = newValue } }
    }

    static func release(host: String) {
        let callbacks = lock.withLock { () -> [() -> Void] in
            let matching = state.waiting.filter { $0.host == host }.map(\.resume)
            state.waiting.removeAll { $0.host == host }
            return matching
        }
        for cb in callbacks { cb() }
    }

    static func releaseAll() {
        let callbacks = lock.withLock { () -> [() -> Void] in
            let cbs = state.waiting.map(\.resume)
            state.waiting.removeAll()
            return cbs
        }
        for cb in callbacks { cb() }
    }

    static func waitForHangingRequests(count: Int) async throws {
        let deadline = Date().addingTimeInterval(2.0)
        while lock.withLock({ state.waiting.count }) < count {
            if Date() > deadline {
                XCTFail("Timed out waiting for \(count) hanging requests")
                return
            }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    static func reset() {
        releaseAll()
        lock.withLock {
            state = (
                mode: .immediate,
                hangHosts: [],
                unreachableHosts: [],
                enrollStatus: 200,
                enrollDaemonID: "daemon-1",
                enrollDeviceID: "dev-enrolled",
                answeredHosts: [],
                statusByHost: [:],
                pushStatusByHost: [:],
                waiting: []
            )
        }
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func stopLoading() {}

    override func startLoading() {
        let host = request.url?.host ?? ""
        if Self.lock.withLock({ Self.state.unreachableHosts.contains(host) }) {
            client?.urlProtocol(self, didFailWithError: URLError(.cannotConnectToHost))
            return
        }
        let shouldHang = Self.lock.withLock { Self.state.mode == .hang || Self.state.hangHosts.contains(host) }
        if shouldHang {
            Self.lock.withLock {
                Self.state.waiting.append((host: host, resume: { [weak self] in self?.finish() }))
            }
            return
        }
        finish()
    }

    private func finish() {
        guard let url = request.url else { return }
        let host = url.host ?? ""
        let path = url.path

        if let custom = Self.lock.withLock({ Self.state.statusByHost[host] }), path != "/api/hello" {
            respond(custom.0, custom.1)
            return
        }

        switch path {
        case "/api/hello":
            respond(200, [
                "name": "sheepdog",
                "version": "1.2.3",
                "auth": "token",
                "ws": "/ws",
                "audio": ["in_hz": 16000, "out_hz": 24000, "format": "pcm16"],
            ])
        case "/api/push/register":
            let key = Data(repeating: 0x42, count: 32).base64EncodedString()
            respond(200, ["content_key": key, "encryption": "aes-256-gcm"])
        case "/api/push/unregister":
            respond(200, ["ok": true])
        case "/api/push/status":
            if let ps = Self.lock.withLock({ Self.state.pushStatusByHost[host] }) {
                respond(ps.0, ps.1)
            } else {
                respond(200, ["enabled": true, "devices": 1, "mode": "relay"])
            }
        case "/api/devices/enroll":
            let status = Self.enrollStatus
            if status == 200 {
                respond(200, [
                    "device_id": Self.enrollDeviceID,
                    "daemon_id": Self.enrollDaemonID,
                    "name": "Studio Mac",
                ])
            } else {
                respond(status, ["error": "enroll failed"])
            }
        case "/api/pending/answer":
            Self.lock.withLock { Self.state.answeredHosts.append(host) }
            respond(200, ["ok": true, "spoken": "Approved"])
        case "/api/state":
            respond(200, [
                "agents": [],
                "voice": ["mode": "gemini", "status": "ready", "muted": false, "stalled": false, "client_connected": false],
            ])
        case "/api/voice/providers":
            respond(200, [
                "provider": "openai",
                "model": "gpt-live-1",
                "providers": [
                    ["id": "openai", "label": "GPT-Live", "model": "gpt-live-1", "available": true],
                    ["id": "gemini", "label": "Gemini Live", "model": "gemini-3.8-live", "available": true],
                ],
            ])
        case "/api/voice/provider":
            respond(200, [
                "provider": "gemini",
                "model": "gemini-3.8-live",
                "providers": [
                    ["id": "openai", "label": "GPT-Live", "model": "gpt-live-1", "available": true],
                    ["id": "gemini", "label": "Gemini Live", "model": "gemini-3.8-live", "available": true],
                ],
            ])
        case "/api/brain/models", "/api/brain/model":
            respond(200, ["provider": "anthropic", "providers": [], "model": "claude-sonnet-4-6", "models": []])
        case "/api/settings":
            respond(200, ["chattiness": "normal", "levels": ["quiet", "normal", "chatty"], "languages": ["en"]])
        default:
            respond(200, ["ok": true])
        }
    }

    private func respond(_ status: Int, _ object: [String: Any]) {
        guard let url = request.url else { return }
        let response = HTTPURLResponse(
            url: url,
            statusCode: status,
            httpVersion: "HTTP/1.1",
            headerFields: ["Content-Type": "application/json"]
        )!
        let data = (try? JSONSerialization.data(withJSONObject: object)) ?? Data()
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }
}

@Observable
private final class ReviewFlockBox {
    var computers: [ComputerSnapshot]

    init(computers: [ComputerSnapshot]) {
        self.computers = computers
    }
}

@MainActor
private struct ReviewFlockHarness: View {
    let box: ReviewFlockBox
    let model: SessionViewModel

    var body: some View {
        FlockView(
            flock: model.flock,
            question: { model.questionCard(for: $0) },
            answer: { model.answer(question: $0, option: $1, on: model.currentComputerID) },
            expanded: Bindable(model.agentsModel).expandedAgents,
            focusedAgent: Bindable(model.navigation).focusedAgent,
            computers: box.computers,
            currentName: model.currentComputerName,
            remoteCard: { model.remoteCard(computer: $0, paneID: $1) },
            collapsedComputers: model.agentsModel.collapsedComputers,
            toggleComputer: { model.agentsModel.toggleComputer($0) },
            expandComputer: { model.agentsModel.expandComputer($0) }
        )
    }
}
#endif
