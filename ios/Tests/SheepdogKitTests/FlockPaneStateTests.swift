import XCTest
@testable import SheepdogKit

final class FlockPaneStateTests: XCTestCase {
    func testAgentsAreNeverHiddenByAConnectionProblem() {
        for status in [ConnectionChip.Status.connecting, .reconnecting, .connected, .notConnected, .offline, .tokenRefused, .updateNeeded] {
            XCTAssertEqual(FlockPaneState.resolve(chip: status, hasAgents: true), .content, "\(status)")
        }
    }

    func testAnEmptyFlockSaysWhyByConnection() {
        XCTAssertEqual(FlockPaneState.resolve(chip: .connecting, hasAgents: false), .loading)
        XCTAssertEqual(FlockPaneState.resolve(chip: .reconnecting, hasAgents: false), .reconnecting)
        XCTAssertEqual(FlockPaneState.resolve(chip: .connected, hasAgents: false), .empty)
        XCTAssertEqual(FlockPaneState.resolve(chip: .notConnected, hasAgents: false), .notConnected)
        XCTAssertEqual(FlockPaneState.resolve(chip: .offline, hasAgents: false), .unreachable)
        XCTAssertEqual(FlockPaneState.resolve(chip: .tokenRefused, hasAgents: false), .tokenRefused)
        XCTAssertEqual(FlockPaneState.resolve(chip: .updateNeeded, hasAgents: false), .updateNeeded)
    }
}
