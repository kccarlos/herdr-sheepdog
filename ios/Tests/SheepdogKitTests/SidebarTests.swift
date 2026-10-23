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
}
