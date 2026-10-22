import XCTest
@testable import SheepdogKit

final class SpacesTests: XCTestCase {
    private func space(_ states: [String], shells: Int = 0) -> SpacesView.Space {
        SpacesView.Space(
            id: "w1", name: "webapp", path: "/code/webapp", git: true, repo: "webapp", branch: "main", worktree: false,
            agents: states.enumerated().map { i, state in
                SpacesView.SpaceAgent(paneID: "p\(i)", alias: ["auth-fix", "payments", "docs"][i], name: ["auth-fix", "payments", "docs"][i], kind: "claude", state: state)
            },
            shells: (0 ..< shells).map { "s\($0)" }, commands: []
        )
    }

    func testTheSummaryLine() {
        XCTAssertEqual(SpaceSummary.line(space([])), "No agents")
        XCTAssertEqual(SpaceSummary.line(space(["blocked", "working"])), "2 agents · 1 waiting")
        XCTAssertEqual(SpaceSummary.line(space(["working"])), "1 agent · working")
        XCTAssertEqual(SpaceSummary.line(space(["done"])), "1 agent · done")
    }

    func testCloseIsRefusedWhileAnAgentWorksOrWaits() {
        XCTAssertNil(SpaceSummary.closeBlocker(space(["done", "idle"])))
        XCTAssertEqual(SpaceSummary.closeBlocker(space(["working", "blocked"])), "You can close it once payments has its answer.")
        XCTAssertEqual(SpaceSummary.closeBlocker(space(["working", "working"])), "You can close it once auth-fix and payments are done working.")
    }

    func testTheCloseConfirmationNamesWhatEnds() {
        XCTAssertEqual(SpaceSummary.closeMessage(space(["done", "idle"], shells: 1)),
                       "Closing it stops auth-fix and payments and closes its shell. The folder and its files stay on your computer.")
        XCTAssertEqual(SpaceSummary.closeMessage(space([])), "Closing it ends the space. The folder and its files stay on your computer.")
    }

    func testTheDemoSpaces() throws {
        var demo = DemoSpaces()
        // Busy agents keep "shop" open.
        XCTAssertEqual(demo.handle(method: "POST", path: "/api/spaces/w1/close", body: [:], busy: true)?.status, 409)
        // A dirty worktree needs force.
        XCTAssertEqual(demo.handle(method: "POST", path: "/api/spaces/w2/worktree/remove", body: [:], busy: false)?.status, 409)
        XCTAssertEqual(demo.handle(method: "POST", path: "/api/spaces/w2/worktree/remove", body: ["force": true], busy: false)?.status, 200)
        XCTAssertEqual(demo.spaces.map(\.name), ["shop"])
        // Rename, a command, and a new space from a free folder.
        XCTAssertEqual(demo.handle(method: "POST", path: "/api/spaces/w1/rename", body: ["name": "store"], busy: false)?.status, 200)
        XCTAssertEqual(demo.handle(method: "POST", path: "/api/spaces/w1/commands", body: ["name": "Build", "command": "make"], busy: false)?.status, 200)
        XCTAssertEqual(demo.spaces[0].commands.map(\.name), ["Run tests", "Lint", "Build"])
        XCTAssertEqual(demo.handle(method: "POST", path: "/api/spaces", body: ["dir": "/Users/you/code/website"], busy: false)?.status, 200)
        XCTAssertEqual(demo.spaces.map(\.name), ["store", "website"])
        XCTAssertNil(demo.handle(method: "GET", path: "/api/state", body: [:], busy: false), "not a space route")
        // The view decodes as the app reads it.
        let data = try JSONSerialization.data(withJSONObject: demo.view(agents: []))
        let view = try JSONDecoder().decode(SpacesView.self, from: data)
        XCTAssertEqual(view.spaces.map(\.name), ["store", "website"])
        XCTAssertEqual(view.kinds.map(\.kind), ["claude", "codex", "agy"])
    }
}
