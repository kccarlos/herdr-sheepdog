import SheepdogKit
import XCTest

final class SidebarTests: XCTestCase {
    private func agent(_ alias: String, _ space: String?, _ state: Agent.State = .working) -> Agent {
        Agent(paneID: "p-" + alias, alias: alias, workspaceLabel: space, state: state)
    }

    func testOneComputerHasNoComputerRow() {
        let one = SidebarSections(computers: [.computer(id: "a", name: "Work Mac", agents: [agent("x", "web")])])
        XCTAssertFalse(one.showsComputerHeaders)
        let two = SidebarSections(computers: [
            .computer(id: "a", name: "Work Mac", agents: [agent("x", "web")]),
            .computer(id: "b", name: "Home Mac", agents: []),
        ])
        XCTAssertTrue(two.showsComputerHeaders)
    }

    func testSpacesAndAgentsKeepAStableOrder() {
        let sections = SidebarSections(computers: [.computer(id: "a", name: "Mac", agents: [
            agent("payments", "webapp"),
            agent("auth-fix", "webapp", .blocked),
            agent("loose", nil, .idle),
            agent("api-docs", "docs-site"),
            agent("gone", "webapp", .exited),
        ])])
        let spaces = sections.computers[0].spaces
        XCTAssertEqual(spaces.map(\.name), ["docs-site", "webapp", ""], "by name, no space last")
        XCTAssertEqual(spaces[1].agents.map(\.alias), ["auth-fix", "payments"], "by alias; a waiting agent does not jump")
        XCTAssertEqual(sections.agents.map(\.alias), ["api-docs", "auth-fix", "payments", "loose"], "running only, in sidebar order")
    }

    // MARK: - Several computers keep the store's order

    private func snapshot(_ id: String, current: Bool, online: Bool = true) -> ComputerSnapshot {
        ComputerSnapshot(id: id, name: id.uppercased(), isCurrent: current, reachability: online ? .online : .offline(since: nil),
                         agents: [agent("agent-" + id, "w")])
    }

    private func order(current: String) -> [String] {
        let snapshots = ["a", "b", "c"].map { snapshot($0, current: $0 == current) }
        return SidebarSections(snapshots: snapshots, currentID: current, currentName: current, currentAgents: []).computers.map(\.id)
    }

    func testTheOrderDoesNotDependOnTheCurrentComputer() {
        XCTAssertEqual(order(current: "a"), ["a", "b", "c"])
        XCTAssertEqual(order(current: "b"), ["a", "b", "c"], "Talk to does not move a section")
        XCTAssertEqual(order(current: "c"), ["a", "b", "c"])
    }

    func testTheCurrentComputerShowsItsOwnAgentsAndAnOfflineOneNone() {
        let sections = SidebarSections(
            snapshots: [snapshot("a", current: false, online: false), snapshot("b", current: true)],
            currentID: "b", currentName: "B", currentAgents: [agent("own", "w")])
        XCTAssertEqual(sections.computers[0].spaces.flatMap(\.agents).count, 0, "an offline computer shows no stale agents")
        XCTAssertEqual(sections.computers[1].spaces.flatMap(\.agents).map(\.alias), ["own"])
    }

    func testACurrentComputerWithoutASnapshotGoesFirst() {
        let sections = SidebarSections(snapshots: [snapshot("a", current: false)], currentID: "x", currentName: "X", currentAgents: [])
        XCTAssertEqual(sections.computers.map(\.id), ["x", "a"])
    }
}
