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

    func testOverlappingStalledTimeoutAndFreshPairSerializesAndPreservesEnrolledKey() async throws {
        let daemonID = "test-overlap-timeout-\(UUID().uuidString)"
        DeviceKey.delete(daemonID: daemonID)
        defer { DeviceKey.delete(daemonID: daemonID) }

        let model = makeModel()
        defer { model.fleet.set([]) }
        PairStubProtocol.enrolledDaemonID = daemonID

        let firstReachedEnroll = expectation(description: "first attempt reached /api/devices/enroll")
        PairStubProtocol.setBehavior(
            forCode: "stalled-code",
            .init(
                delay: 0.15,
                result: .failure(URLError(.timedOut)),
                onStart: { firstReachedEnroll.fulfill() }
            )
        )
        PairStubProtocol.setBehavior(
            forCode: "fresh-code",
            .init(delay: 0, result: .status(200, nil))
        )

        let stalledCode = try makeCode(daemonID: daemonID, code: "stalled-code")
        let freshCode = try makeCode(daemonID: daemonID, code: "fresh-code")

        let attempt1 = Task { await model.pair(stalledCode, protocolClasses: [PairStubProtocol.self]) }
        await fulfillment(of: [firstReachedEnroll], timeout: 2.0)

        let attempt2 = Task { await model.pair(freshCode, protocolClasses: [PairStubProtocol.self]) }
        let outcome2 = await attempt2.value
        _ = await attempt1.value

        guard case let .paired(computer) = outcome2 else {
            return XCTFail("expected second pairing attempt to succeed, got \(outcome2)")
        }
        XCTAssertEqual(computer.daemonID, daemonID)
        XCTAssertEqual(PairStubProtocol.maxConcurrentEnrolls, 1, "enrollments for the same daemon must be serialised")

        let survivingKey = try XCTUnwrap(
            DeviceKey.load(daemonID: daemonID),
            "timed-out first attempt must not delete the key adopted by the successful attempt"
        )
        let enrolledPub = try XCTUnwrap(PairStubProtocol.enrolledPublicKey(forCode: "fresh-code"))
        XCTAssertEqual(DeviceKey.publicKey(survivingKey), enrolledPub)
    }

    func testOverlappingDefiniteFailureAndFreshPairNeverLeavesPairedComputerKeyless() async throws {
        let daemonID = "test-overlap-410-\(UUID().uuidString)"
        DeviceKey.delete(daemonID: daemonID)
        defer { DeviceKey.delete(daemonID: daemonID) }

        let model = makeModel()
        defer { model.fleet.set([]) }
        PairStubProtocol.enrolledDaemonID = daemonID

        let firstReachedEnroll = expectation(description: "first attempt reached /api/devices/enroll")
        PairStubProtocol.setBehavior(
            forCode: "stalled-expired",
            .init(
                delay: 0.15,
                result: .status(410, nil),
                onStart: { firstReachedEnroll.fulfill() }
            )
        )
        PairStubProtocol.setBehavior(
            forCode: "fresh-valid",
            .init(delay: 0, result: .status(200, nil))
        )

        let stalledCode = try makeCode(daemonID: daemonID, code: "stalled-expired")
        let freshCode = try makeCode(daemonID: daemonID, code: "fresh-valid")

        let attempt1 = Task { await model.pair(stalledCode, protocolClasses: [PairStubProtocol.self]) }
        await fulfillment(of: [firstReachedEnroll], timeout: 2.0)

        let attempt2 = Task { await model.pair(freshCode, protocolClasses: [PairStubProtocol.self]) }
        let outcome2 = await attempt2.value
        _ = await attempt1.value

        guard case .paired = outcome2 else {
            return XCTFail("expected fresh code to pair, got \(outcome2)")
        }
        XCTAssertEqual(PairStubProtocol.maxConcurrentEnrolls, 1, "enrollments for the same daemon must be serialised")

        let survivingKey = try XCTUnwrap(
            DeviceKey.load(daemonID: daemonID),
            "earlier failed attempt must never delete the key used by the paired computer"
        )
        let enrolledPub = try XCTUnwrap(PairStubProtocol.enrolledPublicKey(forCode: "fresh-valid"))
        XCTAssertEqual(DeviceKey.publicKey(survivingKey), enrolledPub)
    }

    func testAmbiguousEnrollmentFailuresPreserveNewDeviceKey() async throws {
        // 1. Network timeout after sending POST /api/devices/enroll is ambiguous: keep the key.
        let timeoutDaemon = "test-ambig-timeout-\(UUID().uuidString)"
        DeviceKey.delete(daemonID: timeoutDaemon)
        defer { DeviceKey.delete(daemonID: timeoutDaemon) }

        let model = makeModel()
        defer { model.fleet.set([]) }
        PairStubProtocol.enrolledDaemonID = timeoutDaemon
        PairStubProtocol.setBehavior(
            forCode: "timeout-once",
            .init(delay: 0, result: .failure(URLError(.timedOut)))
        )

        let timeoutCode = try makeCode(daemonID: timeoutDaemon, code: "timeout-once")
        let timeoutOutcome = await model.pair(timeoutCode, protocolClasses: [PairStubProtocol.self])
        if case .paired = timeoutOutcome {
            XCTFail("expected timeout to fail pairing, got \(timeoutOutcome)")
        }
        let preservedTimeoutKey = try XCTUnwrap(
            DeviceKey.load(daemonID: timeoutDaemon),
            "ambiguous transport failure during enroll must preserve the key"
        )
        let timeoutSentPub = try XCTUnwrap(PairStubProtocol.enrolledPublicKey(forCode: "timeout-once"))
        XCTAssertEqual(DeviceKey.publicKey(preservedTimeoutKey), timeoutSentPub)

        // 2. HTTP 502 from a reverse proxy or daemon crash is ambiguous: keep the key.
        let serverErrDaemon = "test-ambig-502-\(UUID().uuidString)"
        DeviceKey.delete(daemonID: serverErrDaemon)
        defer { DeviceKey.delete(daemonID: serverErrDaemon) }

        PairStubProtocol.setBehavior(
            forCode: "proxy-502",
            .init(delay: 0, result: .status(502, nil))
        )
        let serverErrCode = try makeCode(daemonID: serverErrDaemon, code: "proxy-502")
        _ = await model.pair(serverErrCode, protocolClasses: [PairStubProtocol.self])
        XCTAssertNotNil(
            DeviceKey.load(daemonID: serverErrDaemon),
            "HTTP 5xx during enroll is ambiguous and must preserve the key"
        )

        // 3. Truncated HTTP 200 body (JSON decode failure) is ambiguous: keep the key.
        let truncatedDaemon = "test-ambig-200-\(UUID().uuidString)"
        DeviceKey.delete(daemonID: truncatedDaemon)
        defer { DeviceKey.delete(daemonID: truncatedDaemon) }

        PairStubProtocol.setBehavior(
            forCode: "truncated-200",
            .init(delay: 0, result: .status(200, Data(#"{"device_id":"#.utf8)))
        )
        let truncatedCode = try makeCode(daemonID: truncatedDaemon, code: "truncated-200")
        _ = await model.pair(truncatedCode, protocolClasses: [PairStubProtocol.self])
        XCTAssertNotNil(
            DeviceKey.load(daemonID: truncatedDaemon),
            "truncated 200 enroll reply is ambiguous and must preserve the key"
        )
    }

    func testKeyLeaseNeverDeletesKeyAdoptedByAnotherAttemptAndDifferentDaemonsRunConcurrently() async throws {
        // 1. Direct KeyLease adoption invariant: if attempt 1 creates a key and
        //    attempt 2 adopts it, attempt 1 failing definitely must never delete it.
        let daemonID = "test-lease-adopt-\(UUID().uuidString)"
        DeviceKey.delete(daemonID: daemonID)
        defer { DeviceKey.delete(daemonID: daemonID) }

        let lease1 = try PairingEnrollment.KeyLease.acquire(daemonID: daemonID)
        XCTAssertTrue(lease1.createdNew)
        let createdPub = try XCTUnwrap(lease1.publicKey)

        let lease2 = try PairingEnrollment.KeyLease.acquire(daemonID: daemonID)
        XCTAssertFalse(lease2.createdNew)
        XCTAssertEqual(lease2.publicKey, createdPub)
        lease2.markCommitted()
        lease2.release(definiteNonEnrollment: false, isComputerPaired: true)

        // Even if lease1 unwinds with definiteNonEnrollment == true and isComputerPaired == false,
        // the registry knows another attempt adopted and committed this key.
        lease1.release(definiteNonEnrollment: true, isComputerPaired: false)
        let surviving = try XCTUnwrap(DeviceKey.load(daemonID: daemonID), "adopted key must never be deleted by an earlier failed lease")
        XCTAssertEqual(DeviceKey.publicKey(surviving), createdPub)

        // 2. Per-daemon turns do not block distinct daemons.
        let turnA = await PairingEnrollment.beginTurn(daemonID: "daemon-a-\(UUID().uuidString)")
        let turnB = await PairingEnrollment.beginTurn(daemonID: "daemon-b-\(UUID().uuidString)")
        turnB.end()
        turnA.end()
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

private final class PairStubProtocol: URLProtocol, @unchecked Sendable {
    struct CodeBehavior {
        enum Outcome {
            case status(Int, Data?)
            case failure(Error)
        }
        var delay: TimeInterval = 0
        var result: Outcome
        var onStart: (() -> Void)?
    }

    private static let lock = NSLock()
    nonisolated(unsafe) private static var state = (
        enrollStatus: 200,
        enrolledDaemonID: "daemon-1",
        lastEnrolledPublicKey: String?.none,
        enrolledByCode: [String: String](),
        behaviors: [String: CodeBehavior](),
        activeEnrolls: 0,
        maxConcurrentEnrolls: 0
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

    static var maxConcurrentEnrolls: Int {
        lock.withLock { state.maxConcurrentEnrolls }
    }

    static func enrolledPublicKey(forCode code: String) -> String? {
        lock.withLock { state.enrolledByCode[code] }
    }

    static func setBehavior(forCode code: String, _ behavior: CodeBehavior) {
        lock.withLock { state.behaviors[code] = behavior }
    }

    static func reset() {
        lock.withLock {
            state = (
                enrollStatus: 200,
                enrolledDaemonID: "daemon-1",
                lastEnrolledPublicKey: nil,
                enrolledByCode: [:],
                behaviors: [:],
                activeEnrolls: 0,
                maxConcurrentEnrolls: 0
            )
        }
    }

    private var finished = false
    private var countedEnroll = false

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func stopLoading() {
        Self.lock.withLock {
            finished = true
            if countedEnroll {
                countedEnroll = false
                Self.state.activeEnrolls -= 1
            }
        }
    }

    override func startLoading() {
        let path = request.url?.path ?? ""
        switch path {
        case "/api/hello":
            respond(200, ["version": "test"])
        case "/api/devices/enroll":
            var codeText = ""
            if let body = Self.body(request),
               let obj = try? JSONSerialization.jsonObject(with: body) as? [String: Any] {
                codeText = obj["code"] as? String ?? ""
                if let pub = obj["public_key"] as? String {
                    Self.lock.withLock {
                        Self.state.lastEnrolledPublicKey = pub
                        if !codeText.isEmpty { Self.state.enrolledByCode[codeText] = pub }
                    }
                }
            }
            let behavior = Self.lock.withLock { () -> CodeBehavior? in
                countedEnroll = true
                Self.state.activeEnrolls += 1
                Self.state.maxConcurrentEnrolls = max(Self.state.maxConcurrentEnrolls, Self.state.activeEnrolls)
                return Self.state.behaviors[codeText]
            }
            behavior?.onStart?()

            let complete = { [weak self] in
                guard let self else { return }
                let shouldDeliver = Self.lock.withLock { () -> Bool in
                    if self.countedEnroll {
                        self.countedEnroll = false
                        Self.state.activeEnrolls -= 1
                    }
                    guard !self.finished else { return false }
                    self.finished = true
                    return true
                }
                guard shouldDeliver else { return }
                if let behavior {
                    switch behavior.result {
                    case let .failure(error):
                        self.client?.urlProtocol(self, didFailWithError: error)
                    case let .status(status, rawData):
                        if let rawData {
                            self.respondRaw(status, rawData)
                        } else {
                            self.deliverEnrollStatus(status)
                        }
                    }
                } else {
                    self.deliverEnrollStatus(Self.enrollStatus)
                }
            }

            if let delay = behavior?.delay, delay > 0 {
                DispatchQueue.global().asyncAfter(deadline: .now() + delay, execute: complete)
            } else {
                complete()
            }
        default:
            respond(404, ["error": "not found"])
        }
    }

    private func deliverEnrollStatus(_ status: Int) {
        if status == 200 {
            respondRaw(200, Self.jsonData([
                "device_id": "dev-enrolled",
                "daemon_id": Self.enrolledDaemonID,
                "name": "Studio Mac",
            ]))
        } else if status == 410 {
            respondRaw(410, Self.jsonData(["error": "gone", "reason": "code_used"]))
        } else {
            respondRaw(status, Self.jsonData(["error": "enroll failed"]))
        }
    }

    private func respond(_ status: Int, _ object: [String: Any]) {
        let shouldDeliver = Self.lock.withLock { () -> Bool in
            guard !finished else { return false }
            finished = true
            return true
        }
        guard shouldDeliver else { return }
        respondRaw(status, Self.jsonData(object))
    }

    private func respondRaw(_ status: Int, _ data: Data) {
        let response = HTTPURLResponse(
            url: request.url!,
            statusCode: status,
            httpVersion: "HTTP/1.1",
            headerFields: ["Content-Type": "application/json"]
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }

    private static func jsonData(_ object: [String: Any]) -> Data {
        (try? JSONSerialization.data(withJSONObject: object)) ?? Data()
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
