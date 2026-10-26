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
        XCTAssertEqual(demo.handle(method: "POST", path: "/api/spaces", body: ["dir": "/Users/demo/code/website"], busy: false)?.status, 200)
        XCTAssertEqual(demo.spaces.map(\.name), ["store", "website"])
        XCTAssertNil(demo.handle(method: "GET", path: "/api/state", body: [:], busy: false), "not a space route")
        // The view decodes as the app reads it.
        let data = try JSONSerialization.data(withJSONObject: demo.view(agents: []))
        let view = try JSONDecoder().decode(SpacesView.self, from: data)
        XCTAssertEqual(view.spaces.map(\.name), ["store", "website"])
        XCTAssertEqual(view.kinds.map(\.kind), ["claude", "codex", "agy"])
    }

    func testEditingACommandAndRunningALineInTheDemo() {
        var demo = DemoSpaces()
        XCTAssertEqual(demo.handle(method: "PUT", path: "/api/spaces/w1/commands/c1", body: ["name": "Tests", "command": "npm test -- --ci"], busy: false)?.status, 200)
        XCTAssertEqual(demo.spaces[0].commands.first?.name, "Tests")
        XCTAssertEqual(demo.spaces[0].commands.first?.command, "npm test -- --ci")
        XCTAssertEqual(demo.handle(method: "PUT", path: "/api/spaces/w1/commands/nope", body: ["name": "x", "command": "y"], busy: false)?.status, 404)
        XCTAssertEqual(demo.handle(method: "POST", path: "/api/spaces/w1/run", body: ["command": "make lint"], busy: false)?.status, 200)
        XCTAssertEqual(demo.spaces[0].commands.count, 2, "a one-off line is not saved")
    }

    // MARK: - needs_access

    private func decodeSpaces(extra: String) throws -> SpacesView {
        let json = #"{"spaces":[],"folders":[],"kinds":[{"kind":"claude","name":"Claude"}],"worktrees":true"# + extra + "}"
        return try DaemonClient.decoder.decode(SpacesView.self, from: Data(json.utf8))
    }

    func testNeedsAccessIsEmptyFromAnOlderDaemon() throws {
        XCTAssertEqual(try decodeSpaces(extra: "").needsAccess, [])
        XCTAssertEqual(try decodeSpaces(extra: #","needs_access":[]"#).needsAccess, [])
    }

    func testNeedsAccessDecodesBothReasonsAndNamesTheFolder() throws {
        let view = try decodeSpaces(extra: #","needs_access":[{"path":"/Users/x/Documents","reason":"denied"},{"path":"/Users/x/Desktop","reason":"slow"}]"#)
        XCTAssertEqual(view.needsAccess.map(\.reason), [.denied, .slow])
        XCTAssertEqual(view.needsAccess.map(\.folder), ["Documents", "Desktop"])
        XCTAssertEqual(view.needsAccess[0].message, "Sheepdog needs access to Documents. On your computer, allow it in System Settings ▸ Privacy & Security ▸ Files and Folders.")
        XCTAssertEqual(view.needsAccess[1].message, "Your computer is waiting for you to allow access to Desktop.")
    }

    func testAnUnknownNeedsAccessReasonReadsAsDenied() throws {
        let view = try decodeSpaces(extra: #","needs_access":[{"path":"/Users/x/Downloads","reason":"later"}]"#)
        XCTAssertEqual(view.needsAccess, [.init(path: "/Users/x/Downloads", reason: .denied)])
    }

    // MARK: - Choose another folder…

    private func folderView() throws -> SpacesView {
        let json: [String: Any] = [
            "spaces": [["id": "w1", "name": "shop", "path": "/Users/you/code/shop", "git": true, "worktree": false, "agents": [], "shells": [], "commands": []]],
            "folders": [["name": "website", "path": "/Users/you/code/website", "git": true],
                        ["name": "notes", "path": "/Users/you/code/notes", "git": false],
                        ["name": "lab", "path": "/Volumes/work/lab", "git": true]],
            "kinds": [["kind": "claude", "name": "Claude"]],
            "worktrees": true,
        ]
        return try JSONDecoder().decode(SpacesView.self, from: JSONSerialization.data(withJSONObject: json))
    }

    func testTheRootsAreTheFoldersParents() throws {
        XCTAssertEqual(NewAgentOptions.roots(try folderView()), ["/Users/you/code", "/Volumes/work"])
    }

    func testATypedFolder() throws {
        let view = try folderView()
        let typed = NewAgentOptions.typedFolder(" /Users/you/code/webapp/packages/api/ ", in: view)
        XCTAssertEqual(typed?.name, "api")
        XCTAssertEqual(typed?.path, "/Users/you/code/webapp/packages/api")
        XCTAssertNil(typed?.space)
        XCTAssertTrue(NewAgentOptions.isTyped(typed!, in: view), "no git claim either way")
        XCTAssertEqual(NewAgentOptions.typedFolder("~/code/x", in: view)?.path, "~/code/x")
        // A listed folder or space is that one.
        XCTAssertEqual(NewAgentOptions.typedFolder("/Users/you/code/notes", in: view)?.git, false)
        XCTAssertFalse(NewAgentOptions.isTyped(NewAgentOptions.typedFolder("/Users/you/code/notes", in: view)!, in: view))
        XCTAssertEqual(NewAgentOptions.typedFolder("/Users/you/code/shop", in: view)?.space, "w1")
        // Not a full path, or climbing out.
        for bad in ["", "webapp", "code/webapp", "/Users/you/code/../../etc", "/", "~", "~/"] {
            XCTAssertNil(NewAgentOptions.typedFolder(bad, in: view), bad)
        }
    }
}
