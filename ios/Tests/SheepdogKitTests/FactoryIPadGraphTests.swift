import Foundation
import XCTest
@testable import SheepdogKit

/// Pure `SheepdogKit` unit tests for app-factory PR 13a (iPad sidebar factory
/// projects, worker/coordinator filtering, sidebar meta, and offline rule).
final class FactoryIPadGraphTests: XCTestCase {
    private func decodeHeads(_ json: String) throws -> [FactoryProjectHead] {
        try DaemonClient.decoder.decode([FactoryProjectHead].self, from: Data(json.utf8))
    }

    /// iPad sidebar model: running & stopped projects in head order, finished counts in
    /// `factorySectionDetail`, filtered worker/coordinator panes, and offline rule.
    func testSidebarSectionsFactoryProjectsFilteringMetaAndOfflineRule() throws {
        let heads = try decodeHeads(#"""
        [{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"running","counts":{"working":3,"owner_answer":1,"done":9},"folder":"~/code/notes","coordinator_pane":"coord-notes-1"},
         {"id":"proj-2","name":"photos","goal":"Faster thumbnails","state":"stopped","counts":{"done":2,"queued":4},"folder":"~/code/photos"},
         {"id":"proj-3","name":"recipes","goal":"Import recipes","state":"finished","counts":{"done":8},"folder":"~/code/recipes"},
         {"id":"proj-4","name":"docs","goal":"Docs site","state":"running","counts":{"working":1},"folder":"~/code/docs","coordinator_pane":"coord-docs-1"}]
        """#)
        let ask = PendingAction(id: "fask-1", paneID: "factory:proj-1", kind: "answer_prompt", question: "Delete table?",
                                options: [PendingOption(index: 1, label: "Keep it")], agent: "notes",
                                factory: FactoryAskMeta(project: "proj-1", task: "T10", kind: "delete"))
        let agents = [
            Agent(paneID: "p-pay", alias: "payments", workspaceLabel: "webapp", state: .working),
            Agent(paneID: "p-w8", alias: "worker-t8", workspaceLabel: "notes", state: .working, labels: ["sheepdog.project": "proj-1", "sheepdog.role": "worker"]),
            Agent(paneID: "coord-docs-1", alias: "docs-coord", workspaceLabel: "docs", state: .blocked),
        ]

        let comp = SidebarSections.Computer.computer(id: "work", name: "Work Mac", agents: agents, factoryHeads: heads, questions: [ask])
        XCTAssertTrue(comp.hasContent)
        XCTAssertEqual(comp.factoryProjects.map(\.id), ["proj-1", "proj-2", "proj-4"])
        XCTAssertEqual(comp.finishedFactoryCount, 1)
        XCTAssertEqual(comp.factorySectionDetail, "· 3, and 1 done")
        XCTAssertEqual(comp.spaces.flatMap(\.agents).map(\.alias), ["payments"])
        XCTAssertEqual(comp.factoryProjects[0].sidebarMeta, "1 waiting")
        XCTAssertTrue(comp.factoryProjects[0].isSidebarWaiting)
        XCTAssertEqual(comp.factoryProjects[0].sidebarAccessibilityLabel, "notes factory, running, 1 waiting on you")
        XCTAssertEqual(comp.factoryProjects[1].sidebarMeta, "stopped")
        XCTAssertFalse(comp.factoryProjects[1].isSidebarWaiting)
        XCTAssertEqual(comp.factoryProjects[1].sidebarAccessibilityLabel, "photos factory, stopped")
        XCTAssertEqual(comp.factoryProjects[2].sidebarMeta, "1 waiting")
        XCTAssertTrue(comp.factoryProjects[2].isSidebarWaiting)
        XCTAssertEqual(comp.factoryProjects[2].sidebarAccessibilityLabel, "docs factory, running, 1 waiting on you")

        let plain = SidebarSections.Computer.computer(id: "w", name: "W", agents: [], factoryHeads: try decodeHeads(#"[{"id":"p","name":"api","goal":"API","state":"running","counts":{"working":2}}]"#))
        XCTAssertEqual(plain.factorySectionDetail, "· 1")
        XCTAssertEqual(plain.factoryProjects[0].sidebarMeta, "running")
        XCTAssertFalse(plain.factoryProjects[0].isSidebarWaiting)
        XCTAssertEqual(plain.factoryProjects[0].sidebarAccessibilityLabel, "api factory, running")

        let doneOnly = SidebarSections.Computer.computer(id: "w", name: "W", agents: [], factoryHeads: [heads[2]])
        XCTAssertTrue(doneOnly.hasContent && doneOnly.factoryProjects.isEmpty)
        XCTAssertEqual(doneOnly.factorySectionDetail, "· 1 done")

        let emptyComp = SidebarSections.Computer.computer(id: "w", name: "W", agents: [])
        XCTAssertTrue(!emptyComp.hasContent && emptyComp.factorySectionDetail == nil)

        let snapshots = [
            ComputerSnapshot(id: "work", name: "Work Mac", isCurrent: true, reachability: .online, agents: agents, questions: [ask], factoryHeads: [heads[0], heads[3]]),
            ComputerSnapshot(id: "home", name: "Home Mac", isCurrent: false, reachability: .online, agents: [], questions: [], factoryHeads: [heads[1]]),
            ComputerSnapshot(id: "off", name: "Lab Mac", isCurrent: false, reachability: .offline(since: nil), agents: [], questions: [], factoryHeads: [heads[0]]),
        ]
        let multi = SidebarSections(snapshots: snapshots, currentID: "work", currentName: "Work Mac", currentAgents: agents)
        XCTAssertEqual(multi.computers.map(\.id), ["work", "home", "off"])
        XCTAssertEqual(multi.computers[0].factoryProjects.map(\.id), ["proj-1", "proj-4"])
        XCTAssertEqual(multi.computers[1].factoryProjects.map(\.id), ["proj-2"])
        XCTAssertTrue(multi.computers[2].factoryProjects.isEmpty)
        XCTAssertEqual(multi.agents.map(\.alias), ["payments"])
    }
}
