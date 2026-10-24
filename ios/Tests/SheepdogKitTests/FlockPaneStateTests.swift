import XCTest
@testable import SheepdogKit

/// The Agents page reads the same link as the chip.
final class FlockPaneStateTests: XCTestCase {
    /// One link in each state the page tells apart.
    private func links() -> [(String, ComputerLink, FlockPaneState)] {
        var out: [(String, ComputerLink, FlockPaneState)] = []
        var d = LinkDriver(demand: LinkDemand(phase: .background))
        out.append(("idle", d.link, .notConnected))
        d.send(.appPhase(.active))
        out.append(("first connect", d.link, .loading))
        d.fail("lan:7777")
        out.append(("waiting", d.link, .reconnecting))
        d.fire(.backoff)
        out.append(("retrying in the quiet period", d.link, .reconnecting))
        d.advance(31)
        d.fail("lan:7777")
        out.append(("offline", d.link, .unreachable))
        d.fire(.backoff)
        out.append(("retrying past the quiet period", d.link, .unreachable))
        d.succeed("lan:7777")
        out.append(("online", d.link, .empty))
        d.send(.pathChanged(NetworkPath(satisfied: false)))
        out.append(("no network", d.link, .noNetwork))

        var untrusted = LinkDriver()
        untrusted.send(.demandChanged(untrusted.link.demand))
        untrusted.fail("lan:7777", .tlsUntrusted)
        out.append(("untrusted", untrusted.link, .untrusted))

        var refused = LinkDriver()
        refused.send(.demandChanged(refused.link.demand))
        refused.fail("lan:7777", .unauthorized(.revoked))
        out.append(("refused", refused.link, .tokenRefused))

        var incompatible = LinkDriver()
        incompatible.online()
        incompatible.send(.requestFailed(.incompatible("Update the app.")))
        out.append(("incompatible", incompatible.link, .updateNeeded))
        return out
    }

    func testAgentsAreNeverHiddenByAConnectionProblem() {
        for (name, link, _) in links() {
            XCTAssertEqual(FlockPaneState.resolve(link: link, hasAgents: true), .content, name)
        }
    }

    func testAnEmptyFlockSaysWhyFromTheLink() {
        for (name, link, expected) in links() {
            XCTAssertEqual(FlockPaneState.resolve(link: link, hasAgents: false), expected, name)
        }
    }
}
