import XCTest
@testable import SheepdogKit

final class ConnectionChipTests: XCTestCase {
    typealias State = SocketLife

    func testNeverConnectedReadsAsNotConnectedNotOffline() {
        let chip = ConnectionChip(.init())
        XCTAssertEqual(chip.status, .notConnected)
        XCTAssertEqual(chip.word, "Not connected")
        XCTAssertEqual(chip.tint, .grey)
    }

    func testBothUpIsConnected() {
        let chip = ConnectionChip(.init(socket: .open, events: .open))
        XCTAssertEqual(chip.status, .connected)
        XCTAssertEqual(chip.word, "Connected")
        XCTAssertEqual(chip.tint, .green)
    }

    func testEitherSideConnectingOrReconnectingReadsAsReconnecting() {
        let combos: [(State, State)] = [
            (.connecting, .idle), (.idle, .connecting),
            (.reconnecting, .open), (.open, .reconnecting),
            (.connecting, .connecting), (.reconnecting, .reconnecting),
        ]
        for (socket, events) in combos {
            let chip = ConnectionChip(.init(socket: socket, events: events))
            XCTAssertEqual(chip.status, .connecting, "socket \(socket) events \(events)")
            XCTAssertEqual(chip.word, "Reconnecting…")
            XCTAssertEqual(chip.tint, .amber)
        }
    }

    func testSocketOpenWithEventsStillIdleIsNotYetConnected() {
        // The socket can open a beat before the events stream has said
        // anything; that is not "connected" until both are up, but it is
        // not "connecting" either unless something is actually in flight —
        // it settles to not-connected rather than falsely claiming green.
        let chip = ConnectionChip(.init(socket: .open, events: .idle))
        XCTAssertEqual(chip.status, .notConnected)
    }

    func testAFailedAttemptIsOfflineEvenThoughNothingIsRetrying() {
        let chip = ConnectionChip(.init(socket: .idle, events: .idle, failed: true))
        XCTAssertEqual(chip.status, .offline)
        XCTAssertEqual(chip.word, "Offline")
        XCTAssertEqual(chip.tint, .red)
    }

    func testTokenRefusedOutranksAPlainFailure() {
        let chip = ConnectionChip(.init(socket: .idle, events: .idle, failed: true, tokenRefused: true))
        XCTAssertEqual(chip.status, .tokenRefused)
        XCTAssertEqual(chip.word, "Token refused")
        XCTAssertEqual(chip.tint, .red)
    }

    func testTokenRefusedOutranksConnecting() {
        let chip = ConnectionChip(.init(socket: .connecting, events: .connecting, tokenRefused: true))
        XCTAssertEqual(chip.status, .tokenRefused)
    }

    func testFailedOutranksConnecting() {
        // A stale "connecting" snapshot never survives a failure in
        // practice, but the priority order still has to be unambiguous.
        let chip = ConnectionChip(.init(socket: .connecting, events: .idle, failed: true))
        XCTAssertEqual(chip.status, .offline)
    }

    func testEveryCombinationOfSocketAndEventsStateIsHandled() {
        let states: [State] = [.idle, .connecting, .open, .reconnecting]
        for socket in states {
            for events in states {
                let chip = ConnectionChip(.init(socket: socket, events: events))
                let anyReconnecting = [socket, events].contains(.connecting) || [socket, events].contains(.reconnecting)
                if anyReconnecting {
                    XCTAssertEqual(chip.status, .connecting, "socket \(socket) events \(events)")
                } else if socket == .open, events == .open {
                    XCTAssertEqual(chip.status, .connected, "socket \(socket) events \(events)")
                } else {
                    XCTAssertEqual(chip.status, .notConnected, "socket \(socket) events \(events)")
                }
            }
        }
    }
}
