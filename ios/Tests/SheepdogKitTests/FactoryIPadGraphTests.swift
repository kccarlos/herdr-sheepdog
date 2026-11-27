import Foundation
import XCTest
@testable import SheepdogKit

/// Pure `SheepdogKit` unit tests for app-factory PR 13 (`docs/dev/design/app-factory.md`
/// §3.1, §4, D12, §10 row 13; `FactoryIPad*`, `FactoryWebGraph*`; `slice14-wording.md`).
final class FactoryIPadGraphTests: XCTestCase {
    private func decodeGraph(_ json: String) throws -> FactoryGraphView {
        try DaemonClient.decoder.decode(FactoryGraphView.self, from: Data(json.utf8))
    }

    private func decodeHeads(_ json: String) throws -> [FactoryProjectHead] {
        try DaemonClient.decoder.decode([FactoryProjectHead].self, from: Data(json.utf8))
    }

    /// Layered graph ordering (ascending layers, one-pass barycenter within later layers,
    /// input-order tie-breaking, parentless nodes last), forward deduplicated edges,
    /// summary label, empty state, and focus highlighting.
    func testGraphLayersBarycenterOrderEdgesAndHighlight() throws {
        XCTAssertTrue(FactoryTaskGraph(tasks: []).isEmpty)
        XCTAssertEqual(FactoryTaskGraph(tasks: []).layers.count, 0)
        XCTAssertEqual(FactoryTaskGraph(tasks: []).edges, [])

        let single = try decodeGraph(#"{"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[{"ref":"T1","title":"Notes page","stage":"working","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[{"name":"implementing","state":"now"}]}]}"#)
        let singleGraph = FactoryTaskGraph(tasks: single.tasks)
        XCTAssertFalse(singleGraph.isEmpty)
        XCTAssertEqual(singleGraph.accessibilityLabel, "task graph: 1 task in 1 layer")

        // Crossed children (T3 -> right root T2; T4, T5 -> left root T1), orphan T6, duplicate/missing/self/backward refs, skipped layer 5.
        let view = try decodeGraph(#"""
        {"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[
          {"ref":"T1","title":"Notes table","stage":"done","after":["T999","T1"],"dependents":["T4","T5","T7"],"layer":0,"rounds":0,"steps":[{"name":"merged","state":"done"}]},
          {"ref":"T2","title":"Search index","stage":"done","after":["T4"],"dependents":["T3"],"layer":0,"rounds":0,"steps":[{"name":"merged","state":"done"}]},
          {"ref":"T6","title":"Standalone docs","stage":"queued","after":[],"dependents":[],"layer":2,"rounds":0,"steps":[]},
          {"ref":"T3","title":"Search API","stage":"working","after":["T2","T2"],"dependents":["T7"],"layer":2,"rounds":0,"steps":[{"name":"implementing","state":"now"}]},
          {"ref":"T4","title":"Notes list","stage":"done","after":["T1"],"dependents":["T7"],"layer":2,"rounds":0,"steps":[{"name":"merged","state":"done"}]},
          {"ref":"T5","title":"Note editor","stage":"ci","after":["T1"],"dependents":[],"layer":2,"rounds":0,"steps":[{"name":"CI","state":"now"}]},
          {"ref":"T7","title":"Search box","stage":"owner_answer","after":["T1","T4","T3"],"dependents":[],"layer":5,"rounds":0,"steps":[{"name":"implementing","state":"now"}]}
        ]}
        """#)

        let graph = FactoryTaskGraph(tasks: view.tasks)
        XCTAssertEqual(graph.accessibilityLabel, "task graph: 7 tasks in 3 layers")
        XCTAssertEqual(graph.layers.count, 3)
        XCTAssertEqual(graph.layers[0].map(\.ref), ["T1", "T2"], "root layer preserves input order")
        XCTAssertEqual(graph.layers[1].map(\.ref), ["T4", "T5", "T3", "T6"], "barycenter uncrosses children, keeps input tie order, puts parentless T6 last")
        XCTAssertEqual(graph.layers[2].map(\.ref), ["T7"])
        XCTAssertEqual(graph.nodes.map(\.ref), ["T1", "T2", "T4", "T5", "T3", "T6", "T7"])
        for (layerIdx, row) in graph.layers.enumerated() {
            for (slotIdx, node) in row.enumerated() {
                XCTAssertEqual(node.layerIndex, layerIdx)
                XCTAssertEqual(node.slotIndex, slotIdx)
            }
        }
        XCTAssertEqual(graph.edges, [
            .init(from: "T1", to: "T4"), .init(from: "T1", to: "T5"), .init(from: "T2", to: "T3"),
            .init(from: "T1", to: "T7"), .init(from: "T4", to: "T7"), .init(from: "T3", to: "T7"),
        ])

        let focus = graph.highlighted(focusing: "T3")
        XCTAssertEqual(focus.nodes, ["T2", "T3", "T7"])
        XCTAssertEqual(focus.edges, [.init(from: "T2", to: "T3"), .init(from: "T3", to: "T7")])
        XCTAssertTrue(graph.highlighted(focusing: nil).nodes.isEmpty)
        XCTAssertTrue(graph.highlighted(focusing: "UNKNOWN").edges.isEmpty)
    }

    /// Node status words (including unmerged `ended` steps `split`, `dropped`, `closed`
    /// instead of `done`), tones, dashed outlines, and VoiceOver labels.
    func testGraphNodeStatusWordsTonesDashedAndVoiceOverLabels() throws {
        let view = try decodeGraph(#"""
        {"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[
          {"ref":"T1","title":"Notes page","stage":"done","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[{"name":"planned","state":"done"},{"name":"merged","state":"done"}]},
          {"ref":"T6","title":"Storage","stage":"done","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[{"name":"planned","state":"done"},{"name":"split","state":"ended","text":"ended without merging"}]},
          {"ref":"T9","title":"Export to PDF","stage":"done","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[{"name":"implementing","state":"done"},{"name":"dropped","state":"ended","text":"ended without merging"}]},
          {"ref":"T15","title":"Old spike","stage":"done","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[{"name":"CI","state":"done"},{"name":"closed","state":"ended","text":"ended without merging: its PR was closed"}]},
          {"ref":"T7","title":"Search","stage":"working","after":["T1","T6"],"dependents":[],"layer":1,"rounds":1,"steps":[{"name":"review","state":"now"}]},
          {"ref":"T5","title":"Editor","stage":"ci","after":["T1"],"dependents":[],"layer":1,"rounds":0,"steps":[{"name":"CI","state":"now"}]},
          {"ref":"T10","title":"Import","stage":"owner_answer","after":["T6"],"dependents":[],"layer":1,"rounds":0,"steps":[{"name":"implementing","state":"now"}]},
          {"ref":"T11","title":"Deploy","stage":"owner_gate","after":[],"dependents":[],"layer":1,"rounds":0,"steps":[{"name":"CI","state":"now"}]},
          {"ref":"T12","title":"Broken","stage":"blocked_or_failed","after":[],"dependents":[],"layer":1,"rounds":0,"steps":[{"name":"implementing","state":"now"}]},
          {"ref":"T13","title":"Sync","stage":"waiting_on_dep","after":["T7"],"dependents":[],"layer":2,"rounds":0,"steps":[{"name":"planned","state":"now"}]},
          {"ref":"T14","title":"Filters","stage":"queued","after":["T7"],"dependents":[],"layer":2,"rounds":0,"steps":[]},
          {"ref":"T16","title":"Custom","stage":"plan_review","after":[],"dependents":[],"layer":2,"rounds":0,"steps":[]}
        ]}
        """#)

        let byRef = Dictionary(uniqueKeysWithValues: FactoryTaskGraph(tasks: view.tasks).nodes.map { ($0.ref, $0) })
        let expected: [(String, String, FactoryTaskGraph.Tone, Bool, String)] = [
            ("T1", "done", .done, false, "T1, Notes page, done"),
            ("T6", "split", .idle, true, "T6, Storage, split"),
            ("T9", "dropped", .idle, true, "T9, Export to PDF, dropped"),
            ("T15", "closed", .idle, true, "T15, Old spike, closed"),
            ("T7", "working", .working, false, "T7, Search, working, after T1 and T6"),
            ("T5", "waiting on CI", .working, false, "T5, Editor, waiting on CI, after T1"),
            ("T10", "waiting on you", .blocked, false, "T10, Import, waiting on you, after T6"),
            ("T11", "needs your approval", .blocked, false, "T11, Deploy, needs your approval"),
            ("T12", "blocked", .blocked, false, "T12, Broken, blocked"),
            ("T13", "waiting on others", .idle, false, "T13, Sync, waiting on others, after T7"),
            ("T14", "queued", .idle, true, "T14, Filters, queued, after T7"),
            ("T16", "plan review", .idle, false, "T16, Custom, plan review"),
        ]
        for (ref, word, tone, dashed, aria) in expected {
            let node = try XCTUnwrap(byRef[ref], ref)
            XCTAssertEqual(node.statusWord, word, ref)
            XCTAssertEqual(node.tone, tone, ref)
            XCTAssertEqual(node.isDashed, dashed, ref)
            XCTAssertEqual(node.accessibilityLabel, aria, ref)
        }
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
