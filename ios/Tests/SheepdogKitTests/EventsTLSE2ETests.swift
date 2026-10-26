import XCTest
@testable import SheepdogKit

/// `/events` the way a paired phone opens it: the app's own `DaemonClient`
/// stream session, pinned TLS and a session ticket, against the pair
/// fixture daemon (ios/scripts/fixture-daemons.sh). Skips when none runs.
///
/// The fixture UI tests talk plain HTTP to loopback, so they never saw
/// that `URLSession.bytes(for:)` skipped the session-level pin check: on a
/// real phone `/events` failed with -1202 on every try and the chip said
/// "Reconnecting…" forever while the voice worked.
final class EventsTLSE2ETests: XCTestCase {
    private var port: String { ProcessInfo.processInfo.environment["SHEEPDOG_FIXTURE_PAIR_PORT"] ?? "7798" }

    func testEventsOpenAndDeliverOverPinnedTLS() async throws {
        let data: Data
        do {
            (data, _) = try await URLSession.shared.data(from: URL(string: "http://127.0.0.1:\(port)/api/pair?new=1")!)
        } catch {
            throw XCTSkip("no pair fixture daemon on 127.0.0.1:\(port)")
        }
        let object = try XCTUnwrap(try JSONSerialization.jsonObject(with: data) as? [String: Any])
        let code = try XCTUnwrap(PairingCode(try XCTUnwrap(object["link"] as? String)))
        let address = "127.0.0.1:\(port)"
        let base = URL(string: "https://\(address)/")!

        DeviceKey.delete(daemonID: code.daemonID)
        let key = try DeviceKey.loadOrCreate(daemonID: code.daemonID)
        defer { DeviceKey.delete(daemonID: code.daemonID) }
        let trust = PinnedTrustDelegate { [code.pin] }
        let pinned = URLSession(configuration: .ephemeral, delegate: trust, delegateQueue: nil)
        defer { pinned.invalidateAndCancel() }
        let auth = DeviceAuth(base: base, session: pinned)
        let enrolled = try await auth.enroll(code: code.code, publicKey: try XCTUnwrap(DeviceKey.publicKey(key)), name: "Events iPhone", kind: "ios", hardware: false)
        let credentials = DeviceCredentials(daemonID: code.daemonID, secrets: InMemorySecretStore())
        credentials.computer = PairedComputer(daemonID: code.daemonID, name: code.name, addresses: [address], pin: code.pin, deviceID: enrolled.deviceID, pairedAt: Date())
        let endpoint = try XCTUnwrap(DaemonEndpoint(text: address, secure: true, credentials: credentials))
        let client = DaemonClient(endpoint: endpoint)

        // Opens: onOpen runs once the daemon answered 200.
        let opened = expectation(description: "/events opened over pinned TLS")
        let delivered = expectation(description: "an event arrived over /events")
        delivered.assertForOverFulfill = false
        let failure = ErrorBox()
        let reader = Task {
            do {
                for try await output in client.events(since: 0, onOpen: { opened.fulfill() }) {
                    if case .event = output { delivered.fulfill() }
                }
            } catch {
                failure.set(error)
            }
        }
        defer { reader.cancel() }
        await fulfillment(of: [opened], timeout: 10)
        XCTAssertNil(failure.get(), "the stream failed: \(String(describing: failure.get()))")

        // Delivers: something the daemon logs reaches the open stream.
        var say = URLRequest(url: URL(string: "http://127.0.0.1:\(port)/api/utterance")!)
        say.httpMethod = "POST"
        say.setValue("application/json", forHTTPHeaderField: "Content-Type")
        say.httpBody = Data(#"{"text":"what is going on"}"#.utf8)
        _ = try await URLSession.shared.data(for: say)
        await fulfillment(of: [delivered], timeout: 20)

        let ticket = try await credentials.ticket(base: base)
        await auth.unpair(ticket: ticket)
    }

    /// Two addresses, the preferred one dead (nothing listens on port 9):
    /// the computer's runtime races both over pinned TLS, goes online on the
    /// live one, and opens `/events` there (docs/dev/design/ios-state-architecture.md 3.3).
    @MainActor
    func testTheRaceSkipsADeadAddressAndStreamsFromTheLiveOne() async throws {
        let data: Data
        do {
            (data, _) = try await URLSession.shared.data(from: URL(string: "http://127.0.0.1:\(port)/api/pair?new=1")!)
        } catch {
            throw XCTSkip("no pair fixture daemon on 127.0.0.1:\(port)")
        }
        let object = try XCTUnwrap(try JSONSerialization.jsonObject(with: data) as? [String: Any])
        let code = try XCTUnwrap(PairingCode(try XCTUnwrap(object["link"] as? String)))
        let live = "127.0.0.1:\(port)"
        let dead = "127.0.0.1:9"
        let base = URL(string: "https://\(live)/")!

        DeviceKey.delete(daemonID: code.daemonID)
        let key = try DeviceKey.loadOrCreate(daemonID: code.daemonID)
        defer { DeviceKey.delete(daemonID: code.daemonID) }
        let trust = PinnedTrustDelegate { [code.pin] }
        let pinned = URLSession(configuration: .ephemeral, delegate: trust, delegateQueue: nil)
        defer { pinned.invalidateAndCancel() }
        let auth = DeviceAuth(base: base, session: pinned)
        let enrolled = try await auth.enroll(code: code.code, publicKey: try XCTUnwrap(DeviceKey.publicKey(key)), name: "Race iPhone", kind: "ios", hardware: false)
        let credentials = DeviceCredentials(daemonID: code.daemonID, secrets: InMemorySecretStore())
        credentials.computer = PairedComputer(daemonID: code.daemonID, name: code.name, addresses: [dead, live], pin: code.pin, deviceID: enrolled.deviceID, pairedAt: Date())

        let connection = ComputerConnection(computerID: code.daemonID, address: dead) { address in
            DaemonEndpoint(text: address, secure: true, credentials: credentials).map { DaemonClient(endpoint: $0) }
        }
        let model = ComputerModel(id: code.daemonID, name: code.name, addresses: [dead, live], certExpires: nil, connection: connection,
                                  scheduler: LinkClock(), demand: LinkDemand(isCurrent: true),
                                  path: NetworkPath(satisfied: true, interface: .wifi))
        model.start()
        defer { model.stop() }
        await eventually("online on the live address", timeout: 15) { model.link.onlineAddress == live }
        XCTAssertEqual(model.link.winners, [.wifi: live])
        await eventually("/events open there", timeout: 15) { model.liveUpdates == .open }

        let ticket = try await credentials.ticket(base: base)
        await auth.unpair(ticket: ticket)
    }
}

private final class ErrorBox: @unchecked Sendable {
    private let lock = NSLock()
    private var error: Error?
    func set(_ e: Error) { lock.lock(); error = e; lock.unlock() }
    func get() -> Error? { lock.lock(); defer { lock.unlock() }; return error }
}
