#if os(iOS)
import Foundation
import SwiftUI
import UIKit
import XCTest
@testable import Sheepdog
@testable import SheepdogKit

/// Pairing safety and recovery:
/// - Scanning an expired/used code for an already-paired computer must never
///   destroy its enrolled Keychain key (`DeviceKey`).
/// - Re-pairing a known computer re-enrolls its existing key rather than
///   replacing it before the daemon accepts.
/// - A failed first-time scan leaves no orphan key behind.
/// - Once `POST /api/devices/enroll` burns the one-time code, a transient
///   sign-in failure during eager ticket warm-up does not fail pairing or
///   leave the fleet unconfigured.
/// - Dismissing the `.paired` sheet by swipe applies the suggested/typed
///   name and runs `onPaired` once.
@MainActor
final class PairingSafetyTests: XCTestCase {
    private let suite = "sheepdog.tests.pairing-safety"
    private var defaults: UserDefaults!
    private var cacheDir: URL!

    override func setUp() async throws {
        try await super.setUp()
        defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        cacheDir = FileManager.default.temporaryDirectory.appendingPathComponent("sheepdog-pair-\(UUID().uuidString)", isDirectory: true)
        PairStubProtocol.reset()
    }

    override func tearDown() async throws {
        PairStubProtocol.reset()
        defaults.removePersistentDomain(forName: suite)
        try? FileManager.default.removeItem(at: cacheDir)
        try await super.tearDown()
    }

    private func makeModel() -> SessionViewModel {
        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let settings = SettingsStore(defaults: defaults, computers: store)
        return SessionViewModel(settings: settings, cache: ConversationCache(directory: cacheDir))
    }

    private func makeCode(daemonID: String, name: String = "Studio Mac", code: String = "one-time", address: String = "127.0.0.1:1") throws -> PairingCode {
        let encodedName = name.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? name
        let encodedAddr = address.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? address
        let link = "https://\(address)/voice#pair=1&d=\(daemonID)&e=\(code)&k=pin_test_123&n=\(encodedName)&u=\(encodedAddr)"
        return try XCTUnwrap(PairingCode(link))
    }

    // MARK: - Finding 1: Key preservation on failed or repeated pairing

    func testFailedRePairPreservesExistingDeviceKey() async throws {
        let daemonID = "test-preserve-\(UUID().uuidString)"
        defer { DeviceKey.delete(daemonID: daemonID) }

        let originalKey = try DeviceKey.loadOrCreate(daemonID: daemonID)
        let originalPub = try XCTUnwrap(DeviceKey.publicKey(originalKey))

        let model = makeModel()
        let existing = PairedComputer(
            daemonID: daemonID,
            name: "Studio Mac",
            addresses: ["127.0.0.1:1"],
            pin: "pin_test_123",
            deviceID: "dev-existing",
            pairedAt: Date(timeIntervalSince1970: 1_700_000_000)
        )
        model.settings.computers.save(existing, address: "127.0.0.1:1")
        model.settings.hasBeenConfigured = true

        // Simulate scanning an already-used or expired QR code (HTTP 410).
        PairStubProtocol.enrollStatus = 410
        let expiredCode = try makeCode(daemonID: daemonID, code: "expired-code")
        let outcome = await model.pair(expiredCode, protocolClasses: [PairStubProtocol.self])
        guard case .problem(.codeUsed) = outcome else {
            return XCTFail("expected .problem(.codeUsed), got \(outcome)")
        }

        let survivingKey = try XCTUnwrap(DeviceKey.load(daemonID: daemonID), "existing key was deleted on failed re-pair")
        XCTAssertEqual(DeviceKey.publicKey(survivingKey), originalPub, "existing enrolled key was overwritten before enroll succeeded")
    }

    func testRePairEnrollsExistingDeviceKey() async throws {
        let daemonID = "test-reuse-\(UUID().uuidString)"
        defer { DeviceKey.delete(daemonID: daemonID) }

        let originalKey = try DeviceKey.loadOrCreate(daemonID: daemonID)
        let originalPub = try XCTUnwrap(DeviceKey.publicKey(originalKey))

        let model = makeModel()
        PairStubProtocol.enrollStatus = 200
        PairStubProtocol.enrolledDaemonID = daemonID
        let freshCode = try makeCode(daemonID: daemonID, code: "fresh-code")
        _ = await model.pair(freshCode, protocolClasses: [PairStubProtocol.self])

        XCTAssertEqual(PairStubProtocol.lastEnrolledPublicKey, originalPub, "re-pair should enroll the device's existing key")
        let afterKey = try XCTUnwrap(DeviceKey.load(daemonID: daemonID))
        XCTAssertEqual(DeviceKey.publicKey(afterKey), originalPub)
    }

    func testFailedFirstPairCleansUpNewDeviceKey() async throws {
        let daemonID = "test-cleanup-\(UUID().uuidString)"
        DeviceKey.delete(daemonID: daemonID)
        defer { DeviceKey.delete(daemonID: daemonID) }

        let model = makeModel()
        PairStubProtocol.enrollStatus = 410
        let expiredCode = try makeCode(daemonID: daemonID, code: "expired-first")
        let outcome = await model.pair(expiredCode, protocolClasses: [PairStubProtocol.self])
        guard case .problem(.codeUsed) = outcome else {
            return XCTFail("expected .problem(.codeUsed), got \(outcome)")
        }

        XCTAssertNil(DeviceKey.load(daemonID: daemonID), "failed first-time pairing should not leave an un-enrolled key behind")
    }

    // MARK: - Finding 11: Post-enroll ticket warm-up failure does not fail pairing

    func testPairingSucceedsAndUpdatesFleetWhenInitialTicketWarmupFails() async throws {
        let daemonID = "test-warmup-\(UUID().uuidString)"
        DeviceKey.delete(daemonID: daemonID)
        defer { DeviceKey.delete(daemonID: daemonID) }

        let model = makeModel()
        model.notices.setPairingProblem(.revoked)
        PairStubProtocol.enrollStatus = 200
        PairStubProtocol.enrolledDaemonID = daemonID
        // Port 1 on loopback refuses connections immediately when
        // `DeviceCredentials.ticket(base:)` attempts eager sign-in outside
        // `pair`'s session.
        let code = try makeCode(daemonID: daemonID, name: "Studio Mac", code: "valid-once", address: "127.0.0.1:1")
        let outcome = await model.pair(code, protocolClasses: [PairStubProtocol.self])

        guard case let .paired(computer) = outcome else {
            return XCTFail("expected .paired after enroll succeeded, got \(outcome)")
        }
        XCTAssertEqual(computer.daemonID, daemonID)
        XCTAssertTrue(model.isConfigured)
        XCTAssertEqual(model.computerCount, 1)
        XCTAssertEqual(model.fleet.current?.id, daemonID)
        XCTAssertNil(model.notices.pairingProblem)
    }

    // MARK: - Finding 12: Swipe-dismissing the `.paired` sheet applies name & notifies once

    func testSwipingDownPairedSheetAppliesSuggestedNameAndNotifiesOnPaired() async throws {
        let daemonID = "test-swipe-\(UUID().uuidString)"
        let model = makeModel()
        let paired = PairedComputer(
            daemonID: daemonID,
            name: "M944QD4VY",
            addresses: ["127.0.0.1:7777"],
            pin: "pin_test_123",
            deviceID: "dev-1",
            pairedAt: Date()
        )
        model.settings.computers.save(paired, address: "127.0.0.1:7777")
        model.settings.hasBeenConfigured = true
        model.computersChanged()

        var onPairedCalls = 0
        let view = PairingFlowView(
            stage: .paired("M944QD4VY"),
            onPaired: { onPairedCalls += 1 },
            pairedID: daemonID,
            typedName: "Office Mac"
        )
        .environmentObject(model)

        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 390, height: 844))
        let host = UIHostingController(rootView: view)
        window.rootViewController = host
        window.makeKeyAndVisible()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        await settle(2)

        // Simulate interactive swipe-down dismissal without tapping "Done".
        window.rootViewController = UIViewController()
        window.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        await settle(2)
        window.isHidden = true

        XCTAssertEqual(onPairedCalls, 1, "onPaired should run once when the .paired sheet disappears")
        XCTAssertEqual(model.settings.computers.computer(daemonID)?.localName, "Office Mac")
    }

    func testPairingCompletionIsIdempotentAndSkipsNonPairedStages() {
        var completion = PairingCompletion()
        completion.begin(daemonID: "d1")

        // Non-paired stage never fires onPaired or renames.
        let problemResult = completion.finish(stage: .problem(.codeUsed))
        XCTAssertFalse(problemResult.shouldNotifyPaired)
        XCTAssertNil(problemResult.rename)
        XCTAssertFalse(completion.didFinish)

        // Default suggestion applies once, then subsequent calls (e.g. .onDisappear after Done) are no-ops.
        let first = completion.finish(stage: .paired("M944QD4VY"))
        XCTAssertTrue(first.shouldNotifyPaired)
        XCTAssertEqual(first.rename?.id, "d1")
        XCTAssertEqual(first.rename?.name, "Work Mac")

        let second = completion.finish(stage: .paired("M944QD4VY"))
        XCTAssertFalse(second.shouldNotifyPaired)
        XCTAssertNil(second.rename)

        // Clearing the field keeps the computer's own name while still notifying onPaired once.
        completion.begin(daemonID: "d2")
        completion.typedName = "   "
        let cleared = completion.finish(stage: .paired("M944QD4VY"))
        XCTAssertTrue(cleared.shouldNotifyPaired)
        XCTAssertNil(cleared.rename)
    }
}

private final class PairStubProtocol: URLProtocol {
    private static let lock = NSLock()
    nonisolated(unsafe) private static var state = (
        enrollStatus: 200,
        enrolledDaemonID: "daemon-1",
        lastEnrolledPublicKey: String?.none
    )

    static var enrollStatus: Int {
        get { lock.withLock { state.enrollStatus } }
        set { lock.withLock { state.enrollStatus = newValue } }
    }

    static var enrolledDaemonID: String {
        get { lock.withLock { state.enrolledDaemonID } }
        set { lock.withLock { state.enrolledDaemonID = newValue } }
    }

    static var lastEnrolledPublicKey: String? {
        lock.withLock { state.lastEnrolledPublicKey }
    }

    static func reset() {
        lock.withLock {
            state = (enrollStatus: 200, enrolledDaemonID: "daemon-1", lastEnrolledPublicKey: nil)
        }
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func stopLoading() {}

    override func startLoading() {
        let path = request.url?.path ?? ""
        switch path {
        case "/api/hello":
            respond(200, ["version": "test"])
        case "/api/devices/enroll":
            if let body = Self.body(request),
               let obj = try? JSONSerialization.jsonObject(with: body) as? [String: Any],
               let pub = obj["public_key"] as? String {
                Self.lock.withLock { Self.state.lastEnrolledPublicKey = pub }
            }
            let status = Self.enrollStatus
            if status == 200 {
                respond(200, [
                    "device_id": "dev-enrolled",
                    "daemon_id": Self.enrolledDaemonID,
                    "name": "Studio Mac",
                ])
            } else if status == 410 {
                respond(410, ["error": "gone", "reason": "code_used"])
            } else {
                respond(status, ["error": "enroll failed"])
            }
        default:
            respond(404, ["error": "not found"])
        }
    }

    private func respond(_ status: Int, _ object: [String: Any]) {
        let response = HTTPURLResponse(
            url: request.url!,
            statusCode: status,
            httpVersion: "HTTP/1.1",
            headerFields: ["Content-Type": "application/json"]
        )!
        let data = (try? JSONSerialization.data(withJSONObject: object)) ?? Data()
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }

    private static func body(_ request: URLRequest) -> Data? {
        if let body = request.httpBody { return body }
        guard let stream = request.httpBodyStream else { return nil }
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 4096)
        while stream.hasBytesAvailable {
            let count = stream.read(&buffer, maxLength: buffer.count)
            if count <= 0 { break }
            data.append(buffer, count: count)
        }
        return data
    }
}
#endif
