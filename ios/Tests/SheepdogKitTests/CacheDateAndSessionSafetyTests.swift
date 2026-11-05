#if os(iOS)
import Foundation
import XCTest
@testable import Sheepdog
@testable import SheepdogKit

/// Audit coverage for Group 4:
/// - Finding 3: Thread-safe RFC 3339 parsing and formatting across fractional
///   and whole-second timestamps (`SSEParser`, `DaemonClient`, `ConversationCache`,
///   `TimelineRow.rawJSON`).
/// - Finding 2: Cancelling debounced `saveTask` on `clearConversation()`,
///   `removeComputer()`, and `switchComputer(to:)`, and serializing cache I/O.
/// - Finding 4: Invalidating one-shot `URLSession` and `DaemonClient` instances
///   in `DeviceCredentials.ticket`, `SessionViewModel.testConnection`,
///   `SessionViewModel.addAddress`, and `SessionViewModel.removeComputer`.
@MainActor
final class CacheDateAndSessionSafetyTests: XCTestCase {
    private var suiteName: String!
    private var defaults: UserDefaults!
    private var cacheDir: URL!

    override func setUp() async throws {
        try await super.setUp()
        suiteName = "sheepdog.tests.group4-\(UUID().uuidString)"
        defaults = UserDefaults(suiteName: suiteName)!
        cacheDir = FileManager.default.temporaryDirectory.appendingPathComponent("sheepdog-g4-\(UUID().uuidString)", isDirectory: true)
    }

    override func tearDown() async throws {
        defaults.removePersistentDomain(forName: suiteName)
        try? FileManager.default.removeItem(at: cacheDir)
        try await super.tearDown()
    }

    private func makeItem(_ id: Int64) -> ConversationItem {
        ConversationItem(id: id, ts: Date(timeIntervalSince1970: 1_790_000_000.125), kind: .shepherd, text: "item \(id)")
    }

    private func makePairedComputer(_ id: String, name: String = "Work Mac", address: String = "127.0.0.1:1") -> PairedComputer {
        PairedComputer(
            daemonID: id,
            name: name,
            addresses: [address],
            pin: "pin_\(id)",
            deviceID: "dev-\(id)",
            pairedAt: Date(timeIntervalSince1970: 1_700_000_000)
        )
    }

    // MARK: - Finding 3: Thread-safe whole-second & fractional RFC 3339 dates

    func testSSEParserDecodesWholeSecondAndFractionalRFC3339Timestamps() throws {
        var calendar = Calendar(identifier: .iso8601)
        calendar.timeZone = TimeZone(secondsFromGMT: 0)!
        let expectedBase = try XCTUnwrap(
            calendar.date(from: DateComponents(year: 2026, month: 9, day: 22, hour: 0, minute: 12, second: 0))
        )

        let wholeUTC = try XCTUnwrap(SSEParser.decode(#"{"type":"daemon.start","ts":"2026-09-22T00:12:00Z"}"#, seq: 1))
        let wholeUTCTS = try XCTUnwrap(wholeUTC.ts, "SSEParser dropped a whole-second RFC 3339 UTC timestamp")
        XCTAssertEqual(wholeUTCTS.timeIntervalSince1970, expectedBase.timeIntervalSince1970, accuracy: 0.001)

        let wholeOffset = try XCTUnwrap(SSEParser.decode(#"{"type":"daemon.start","ts":"2026-09-21T17:12:00-07:00"}"#, seq: 2))
        let wholeOffsetTS = try XCTUnwrap(wholeOffset.ts, "SSEParser dropped a whole-second RFC 3339 offset timestamp")
        XCTAssertEqual(wholeOffsetTS.timeIntervalSince1970, expectedBase.timeIntervalSince1970, accuracy: 0.001)

        let fractional = try XCTUnwrap(SSEParser.decode(#"{"type":"daemon.start","ts":"2026-09-22T00:12:00.125Z"}"#, seq: 3))
        let fractionalTS = try XCTUnwrap(fractional.ts, "SSEParser dropped a fractional RFC 3339 timestamp")
        XCTAssertEqual(fractionalTS.timeIntervalSince1970, expectedBase.timeIntervalSince1970 + 0.125, accuracy: 0.0005)
    }

    func testConcurrentRFC3339ParsingAndFormattingProducesExactTimestamps() async throws {
        let baseEpoch: TimeInterval = 1_790_000_000
        try await withThrowingTaskGroup(of: Void.self) { group in
            for worker in 0 ..< 24 {
                group.addTask {
                    let endpoint = DaemonEndpoint(host: "127.0.0.1", port: 7777)
                    for step in 0 ..< 20 {
                        let offset = TimeInterval(worker * 100 + step)
                        let wholeJSON = #"{"type":"voice.turn","ts":"2026-09-22T00:12:00Z"}"#
                        let fracJSON = #"{"type":"voice.turn","ts":"2026-09-22T00:12:00.250Z"}"#
                        guard let wholeEvent = SSEParser.decode(wholeJSON, seq: 1), wholeEvent.ts != nil else {
                            throw NSError(domain: "RFC3339Test", code: 1, userInfo: [NSLocalizedDescriptionKey: "missing whole-second SSE ts"])
                        }
                        guard let fracEvent = SSEParser.decode(fracJSON, seq: 2), fracEvent.ts != nil else {
                            throw NSError(domain: "RFC3339Test", code: 2, userInfo: [NSLocalizedDescriptionKey: "missing fractional SSE ts"])
                        }

                        let exactDate = Date(timeIntervalSince1970: baseEpoch + offset + 0.375)
                        let item = ConversationItem(id: Int64(step + 1), ts: exactDate, kind: .shepherd, text: "turn")
                        let snapshot = ConversationSnapshot(endpoint: endpoint, history: ConversationHistory(items: [item]), lastEventID: 1, savedAt: exactDate)
                        let encoded = try ConversationCache.encoder.encode(snapshot)
                        let decoded = try DaemonClient.decoder.decode(ConversationSnapshot.self, from: encoded)
                        guard let roundTripped = decoded.items.first?.ts,
                              abs(roundTripped.timeIntervalSince1970 - exactDate.timeIntervalSince1970) < 0.002
                        else {
                            throw NSError(domain: "RFC3339Test", code: 3, userInfo: [NSLocalizedDescriptionKey: "corrupted round-trip timestamp"])
                        }

                        let raw = TimelineRow.day(exactDate).rawJSON
                        guard raw.contains("2026-") else {
                            throw NSError(domain: "RFC3339Test", code: 4, userInfo: [NSLocalizedDescriptionKey: "unexpected rawJSON \(raw)"])
                        }
                    }
                }
            }
            try await group.waitForAll()
        }
    }

    // MARK: - Finding 2: Debounced cache task cancellation & serialization

    func testClearConversationCancelsDebouncedSaveTaskSoCacheStaysDeleted() async throws {
        let cache = ConversationCache(directory: cacheDir)
        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let settings = SettingsStore(defaults: defaults, computers: store)
        settings.address = "127.0.0.1:7777"
        settings.hasBeenConfigured = true
        let endpoint = try XCTUnwrap(settings.endpoint)

        try cache.save(
            ConversationSnapshot(endpoint: endpoint, history: ConversationHistory(items: [makeItem(1)]), lastEventID: 1),
            for: endpoint
        )

        let model = SessionViewModel(settings: settings, cache: cache)
        defer { model.fleet.set([]) }

        // Trigger a debounced 1s save capturing item 1, then clear immediately.
        model.receiveCard(ScreenCard(cardID: "card-1", kind: .card, title: "Summary", markdown: "Done"))
        model.clearConversation()
        XCTAssertNil(cache.load(for: endpoint))

        try await Task.sleep(nanoseconds: 1_150_000_000)
        XCTAssertNil(cache.load(for: endpoint), "debounced saveTask resurrected the cleared conversation cache")
    }

    func testRemoveComputerCancelsDebouncedSaveTaskSoCacheStaysDeleted() async throws {
        let cache = ConversationCache(directory: cacheDir)
        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let settings = SettingsStore(defaults: defaults, computers: store)
        store.save(makePairedComputer("work-id"), address: "127.0.0.1:1")
        settings.hasBeenConfigured = true
        let workEndpoint = try XCTUnwrap(store.endpoint(for: "work-id"))

        try cache.save(
            ConversationSnapshot(endpoint: workEndpoint, history: ConversationHistory(items: [makeItem(1)]), lastEventID: 1),
            for: workEndpoint
        )

        let model = SessionViewModel(settings: settings, cache: cache)
        defer { model.fleet.set([]) }

        model.receiveCard(ScreenCard(cardID: "card-rm", kind: .card, title: "Summary", markdown: "Done"))
        await model.removeComputer()

        try await Task.sleep(nanoseconds: 1_150_000_000)
        XCTAssertNil(cache.load(for: workEndpoint), "debounced saveTask resurrected a removed computer's cache")
    }

    func testSwitchingComputersCancelsPreviousDebouncedSaveSoNewerSnapshotWins() async throws {
        let cache = ConversationCache(directory: cacheDir)
        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let settings = SettingsStore(defaults: defaults, computers: store)
        store.save(makePairedComputer("work-id", name: "Work Mac"), address: "127.0.0.1:1")
        store.save(makePairedComputer("home-id", name: "Home Mac"), address: "127.0.0.1:2")
        store.currentID = "work-id"
        settings.hasBeenConfigured = true
        let workEndpoint = try XCTUnwrap(store.endpoint(for: "work-id"))

        try cache.save(
            ConversationSnapshot(endpoint: workEndpoint, history: ConversationHistory(items: [makeItem(1)]), lastEventID: 1),
            for: workEndpoint
        )

        let model = SessionViewModel(settings: settings, cache: cache)
        defer { model.fleet.set([]) }

        // Start a debounced save on work-id with [1], then advance history to [1, 2] and switch.
        model.receiveCard(ScreenCard(cardID: "card-sw", kind: .card, title: "Summary", markdown: "Done"))
        model.fleet.current?.conversation.history.applyNewest(ConversationPage(items: [makeItem(1), makeItem(2)], nextBefore: nil))
        model.switchComputer(to: "home-id")

        try await Task.sleep(nanoseconds: 1_150_000_000)
        let saved = try XCTUnwrap(cache.load(for: workEndpoint))
        XCTAssertEqual(saved.items.map(\.id), [1, 2], "stale debounced saveTask overwrote the switch-time snapshot")
    }

    // MARK: - Finding 4: Invalidating one-shot URLSession and DaemonClient instances

    func testDeviceCredentialsTicketInvalidatesTemporaryURLSession() async throws {
        let daemonID = "test-ticket-leak-\(UUID().uuidString)"
        _ = try DeviceKey.loadOrCreate(daemonID: daemonID)
        defer { DeviceKey.delete(daemonID: daemonID) }

        weak var weakTrust: PinnedTrustDelegate?
        do {
            let creds = DeviceCredentials(daemonID: daemonID, secrets: InMemorySecretStore())
            creds.computer = makePairedComputer(daemonID)
            weakTrust = creds.trust
            _ = try? await creds.ticket(base: URL(string: "https://127.0.0.1:1/")!)
        }

        try await Task.sleep(nanoseconds: 150_000_000)
        XCTAssertNil(weakTrust, "DeviceCredentials.ticket(base:) leaked its temporary URLSession and trust delegate")
    }

    func testTestConnectionInvalidatesProbeClient() async throws {
        let daemonID = "test-probe-leak-\(UUID().uuidString)"
        defer { DeviceKey.delete(daemonID: daemonID) }

        weak var weakCreds: DeviceCredentials?
        do {
            let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
            let settings = SettingsStore(defaults: defaults, computers: store)
            let model = SessionViewModel(settings: settings, cache: ConversationCache(directory: cacheDir))

            store.save(makePairedComputer(daemonID), address: "127.0.0.1:1")
            settings.hasBeenConfigured = true
            weakCreds = store.credentials(for: daemonID)

            _ = await model.testConnection()
            model.fleet.set([])
        }

        try await Task.sleep(nanoseconds: 150_000_000)
        XCTAssertNil(weakCreds, "SessionViewModel.testConnection() leaked its one-shot DaemonClient and URLSessions")
    }

    func testAddAddressInvalidatesCheckAddressClient() async throws {
        let daemonID = "test-addr-leak-\(UUID().uuidString)"
        defer { DeviceKey.delete(daemonID: daemonID) }

        weak var weakCreds: DeviceCredentials?
        do {
            let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
            let settings = SettingsStore(defaults: defaults, computers: store)
            let model = SessionViewModel(settings: settings, cache: ConversationCache(directory: cacheDir))

            store.save(makePairedComputer(daemonID), address: "127.0.0.1:1")
            settings.hasBeenConfigured = true
            weakCreds = store.credentials(for: daemonID)

            _ = await model.addAddress("127.0.0.1:2")
            model.fleet.set([])
        }

        try await Task.sleep(nanoseconds: 150_000_000)
        XCTAssertNil(weakCreds, "SessionViewModel.addAddress(_:) leaked its one-shot DaemonClient and URLSessions")
    }

    func testRemoveComputerFallbackUnpairInvalidatesTemporarySession() async throws {
        let daemonID = "test-unpair-leak-\(UUID().uuidString)"
        defer { DeviceKey.delete(daemonID: daemonID) }

        weak var weakTrust: PinnedTrustDelegate?
        do {
            let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
            let settings = SettingsStore(defaults: defaults, computers: store)
            // Create the model before saving so fleet.model(daemonID) is nil and
            // removeComputer(_:) takes the fallback credentials.session(timeout: 4) path.
            let model = SessionViewModel(settings: settings, cache: ConversationCache(directory: cacheDir))

            store.save(makePairedComputer(daemonID), address: "127.0.0.1:1")
            let creds = store.credentials(for: daemonID)
            creds.adopt(SessionTicket(ticket: "tok-test", expires: Date().addingTimeInterval(3600), nextPin: nil, certExpires: nil))
            weakTrust = creds.trust

            await model.removeComputer(daemonID)
            model.fleet.set([])
        }

        try await Task.sleep(nanoseconds: 150_000_000)
        XCTAssertNil(weakTrust, "SessionViewModel.removeComputer(_:) leaked its fallback unpair URLSession")
    }
}
#endif
