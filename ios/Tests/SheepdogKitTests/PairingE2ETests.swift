import XCTest
@testable import SheepdogKit

/// The whole pairing path against the pair fixture daemon (port
/// `SHEEPDOG_FIXTURE_PAIR_PORT`, passed as
/// `TEST_RUNNER_SHEEPDOG_FIXTURE_PAIR_PORT`; ios/scripts/fixture-daemons.sh):
/// a fresh one-time code from loopback, the pinned TLS handshake (and a
/// wrong pin refused), enroll, a used code refused, a signed session, the
/// ticket on `/api/state` and the `/ws` upgrade, and revocation. Skips
/// when no pair daemon runs.
final class PairingE2ETests: XCTestCase {
    private var port: String { ProcessInfo.processInfo.environment["SHEEPDOG_FIXTURE_PAIR_PORT"] ?? "7798" }

    private func freshCode() async throws -> PairingCode {
        let url = URL(string: "http://127.0.0.1:\(port)/api/pair?new=1")!
        let data: Data
        do {
            (data, _) = try await URLSession.shared.data(from: url)
        } catch {
            throw XCTSkip("no pair fixture daemon on 127.0.0.1:\(port)")
        }
        let object = try XCTUnwrap(try JSONSerialization.jsonObject(with: data) as? [String: Any])
        let link = try XCTUnwrap(object["link"] as? String, "no link in \(object)")
        return try XCTUnwrap(PairingCode(link), "not a pairing code: \(link)")
    }

    private func pinnedSession(_ pins: [String]) -> (URLSession, PinnedTrustDelegate) {
        let trust = PinnedTrustDelegate { pins }
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 8
        return (URLSession(configuration: config, delegate: trust, delegateQueue: nil), trust)
    }

    func testScanEnrollAndAPinnedSession() async throws {
        let code = try await freshCode()
        XCTAssertTrue(code.addresses.contains("127.0.0.1:\(port)"), "\(code.addresses)")
        let address = "127.0.0.1:\(port)"
        let base = URL(string: "https://\(address)/")!

        // A key that matches no pin is refused, and that request's own
        // delegate says so.
        let (wrong, wrongTrust) = pinnedSession(["AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"])
        let wrongAttempt = wrongTrust.attempt()
        do {
            _ = try await wrong.data(for: URLRequest(url: base.appendingPathComponent("api/hello")), delegate: wrongAttempt)
            XCTFail("a wrong pin was accepted")
        } catch {
            XCTAssertTrue(wrongAttempt.sawMismatch)
        }
        // The next request on the same session starts with a clean verdict:
        // nothing sticky survives from the last one.
        XCTAssertFalse(wrongTrust.attempt().sawMismatch)

        // The code's pin: TLS to a self-signed certificate, no ATS exception.
        let (session, trust) = pinnedSession([code.pin])
        let attempt = trust.attempt()
        let (_, hello) = try await session.data(for: URLRequest(url: base.appendingPathComponent("api/hello")), delegate: attempt)
        XCTAssertEqual((hello as? HTTPURLResponse)?.statusCode, 200)
        XCTAssertFalse(attempt.sawMismatch)
        // Without a per-request delegate the session's own (task-level)
        // check still pins.
        let (_, plain) = try await session.data(from: base.appendingPathComponent("api/hello"))
        XCTAssertEqual((plain as? HTTPURLResponse)?.statusCode, 200)

        // Enroll once; the same code again is refused.
        DeviceKey.delete(daemonID: code.daemonID)
        let key = try DeviceKey.loadOrCreate(daemonID: code.daemonID)
        defer { DeviceKey.delete(daemonID: code.daemonID) }
        let auth = DeviceAuth(base: base, session: session, trust: trust)
        let enrolled = try await auth.enroll(code: code.code, publicKey: try XCTUnwrap(DeviceKey.publicKey(key)), name: "Test iPhone", kind: "ios", hardware: false)
        XCTAssertFalse(enrolled.deviceID.isEmpty)
        do {
            _ = try await auth.enroll(code: code.code, publicKey: try XCTUnwrap(DeviceKey.publicKey(key)), name: "Again", kind: "ios", hardware: false)
            XCTFail("a used code was accepted")
        } catch let problem as PairingProblem {
            XCTAssertEqual(problem, .codeUsed)
        }

        // The paired computer: every request is pinned TLS with a ticket.
        let credentials = DeviceCredentials(daemonID: code.daemonID, secrets: InMemorySecretStore())
        credentials.computer = PairedComputer(daemonID: code.daemonID, name: code.name, addresses: [address], pin: code.pin, deviceID: enrolled.deviceID, pairedAt: Date())
        let endpoint = try XCTUnwrap(DaemonEndpoint(text: address, secure: true, credentials: credentials))
        let client = DaemonClient(endpoint: endpoint)
        _ = try await client.state()

        // A paired client whose pin does not match (the phone's key exists,
        // so the sign-in reaches TLS): the request is `untrusted`, and the
        // link decides between the warning and a rescan, never this
        // request by itself.
        let stranger = DeviceCredentials(daemonID: code.daemonID, secrets: InMemorySecretStore())
        stranger.computer = PairedComputer(daemonID: code.daemonID, name: code.name, addresses: [address], pin: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", deviceID: enrolled.deviceID, pairedAt: Date())
        do {
            _ = try await DaemonClient(endpoint: try XCTUnwrap(DaemonEndpoint(text: address, secure: true, credentials: stranger))).hello()
            XCTFail("a wrong pin was accepted")
        } catch {
            XCTAssertEqual(error as? DaemonError, .untrusted(endpoint: address))
            XCTAssertEqual(LinkError(error), .tlsUntrusted)
        }

        // The app's own cancellation is never an error for the link, even
        // right after a mismatch on another client (the old sticky flag
        // turned it into "scan again").
        let slow = Task { try await client.conversation(limit: 200) }
        slow.cancel()
        do {
            _ = try await slow.value
        } catch {
            XCTAssertEqual(LinkError(error), .cancelled, "\(error)")
        }
        let header = try XCTUnwrap(credentials.header())
        XCTAssertTrue(header.hasPrefix("Bearer sds_"))

        // A ticket the daemon does not know: signed again, and retried.
        credentials.adopt(SessionTicket(ticket: "bogus", expires: Date().addingTimeInterval(3600)))
        _ = try await client.state()
        XCTAssertNotEqual(credentials.header(), "Bearer sds_bogus")

        // The /ws upgrade over the pinned session, with the ticket: the
        // daemon's first frame is a status (docs/ws-contract.md). A
        // WebSocket-level ping is no proof: URLSessionWebSocketTask reads
        // the pong only while a receive is pending, so it waits forever
        // (the app's own liveness ping is the contract's JSON one).
        var upgrade = URLRequest(url: try XCTUnwrap(endpoint.webSocketURL))
        endpoint.authorize(&upgrade)
        let ws = credentials.session(timeout: 8).webSocketTask(with: upgrade)
        ws.resume()
        guard case .string(let first) = try await ws.receive() else {
            return XCTFail("the first /ws frame is not text")
        }
        XCTAssertTrue(first.contains("\"status\""), first)
        ws.cancel(with: .goingAway, reason: nil)

        // Remove: the computer forgets this phone; its ticket stops working.
        let ticket = try await credentials.ticket(base: base)
        await auth.unpair(ticket: ticket)
        do {
            _ = try await client.state()
            XCTFail("a revoked phone was let in")
        } catch let DaemonError.pairing(problem) {
            XCTAssertEqual(problem, .revoked)
        }
    }
}
