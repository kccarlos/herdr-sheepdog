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

    /// Top-aligned barycenter coordinates match `FactoryTaskGraphView`'s `.top` column alignment
    /// on skip-layer edges across uneven columns; empty titles omit blank segments and empty
    /// stages fall back to `"unknown"`.
    func testTopAlignedBarycenterAndEmptyFieldFallbacks() throws {
        let skipLayer = try decodeGraph(#"""
        {"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[
          {"ref":"T1","title":"One","stage":"done","after":[],"dependents":["T5"],"layer":0,"rounds":0,"steps":[]},
          {"ref":"T2","title":"Two","stage":"done","after":[],"dependents":["T7"],"layer":0,"rounds":0,"steps":[]},
          {"ref":"T3","title":"Three","stage":"done","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[]},
          {"ref":"T4","title":"Four","stage":"done","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[]},
          {"ref":"T5","title":"Five","stage":"done","after":["T1"],"dependents":["T6"],"layer":1,"rounds":0,"steps":[]},
          {"ref":"T6","title":"Six","stage":"working","after":["T5"],"dependents":[],"layer":2,"rounds":0,"steps":[]},
          {"ref":"T7","title":"Seven","stage":"working","after":["T2"],"dependents":[],"layer":2,"rounds":0,"steps":[]}
        ]}
        """#)
        let skipGraph = FactoryTaskGraph(tasks: skipLayer.tasks)
        XCTAssertEqual(
            skipGraph.layers.map { $0.map(\.ref) },
            [["T1", "T2", "T3", "T4"], ["T5"], ["T6", "T7"]],
            "top-aligned barycenter keeps T5 (slot 0) above T2 (slot 1) so T5->T6 does not cross T2->T7"
        )

        let edgeCases = try decodeGraph(#"""
        {"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[
          {"ref":"T1","title":"","stage":"done","after":[],"dependents":["T2"],"layer":0,"rounds":0,"steps":[]},
          {"ref":"T2","title":"   ","stage":"","after":["T1"],"dependents":[],"layer":1,"rounds":0,"steps":[]},
          {"ref":"T3","title":"Custom","stage":"   ","after":[],"dependents":[],"layer":1,"rounds":0,"steps":[]}
        ]}
        """#)
        let byRef = Dictionary(uniqueKeysWithValues: FactoryTaskGraph(tasks: edgeCases.tasks).nodes.map { ($0.ref, $0) })
        let t1 = try XCTUnwrap(byRef["T1"])
        XCTAssertEqual(t1.statusWord, "done")
        XCTAssertEqual(t1.accessibilityLabel, "T1, done")

        let t2 = try XCTUnwrap(byRef["T2"])
        XCTAssertEqual(t2.statusWord, "unknown")
        XCTAssertEqual(t2.tone, .idle)
        XCTAssertFalse(t2.isDashed)
        XCTAssertEqual(t2.accessibilityLabel, "T2, unknown, after T1")

        let t3 = try XCTUnwrap(byRef["T3"])
        XCTAssertEqual(t3.statusWord, "unknown")
        XCTAssertEqual(t3.accessibilityLabel, "T3, Custom, unknown")
    }

    /// Board layout stacks in compact, at accessibility text sizes, and in narrow regular-width
    /// Stage Manager / Split View windows instead of crushing the graph beside a fixed 340 pt list;
    /// when side by side, list width scales down in narrower regular widths so 3 board-sized layers
    /// fit without clipping.
    func testBoardLayoutThresholdAndAdaptiveListWidth() {
        let compact = FactoryTaskGraph.BoardLayout(
            isRegular: false, isAccessibilitySize: false, availableWidth: 900
        )
        XCTAssertFalse(compact.sideBySide)
        XCTAssertNil(compact.listWidth)

        let ax5 = FactoryTaskGraph.BoardLayout(
            isRegular: true, isAccessibilitySize: true, availableWidth: 900
        )
        XCTAssertFalse(ax5.sideBySide)
        XCTAssertNil(ax5.listWidth)

        for narrow: CGFloat in [0, 360, 520, 679] {
            let layout = FactoryTaskGraph.BoardLayout(
                isRegular: true, isAccessibilitySize: false, availableWidth: narrow
            )
            XCTAssertFalse(layout.sideBySide, "width \(narrow) must stack vertically")
            XCTAssertNil(layout.listWidth, "width \(narrow)")
        }

        let minRegular = FactoryTaskGraph.BoardLayout(
            isRegular: true, isAccessibilitySize: false, availableWidth: 680
        )
        XCTAssertTrue(minRegular.sideBySide)
        XCTAssertEqual(minRegular.listWidth, 280)
        XCTAssertGreaterThanOrEqual(680 - 32 - 16 - (minRegular.listWidth ?? 0), 350)

        let narrowSidebarLandscape = FactoryTaskGraph.BoardLayout(
            isRegular: true, isAccessibilitySize: false, availableWidth: 790
        )
        XCTAssertTrue(narrowSidebarLandscape.sideBySide)
        let threeLayerBoardWidth: CGFloat = 3 * 118 + 2 * 20 + 2 * 12
        XCTAssertGreaterThanOrEqual(
            790 - 32 - 16 - (narrowSidebarLandscape.listWidth ?? 0), threeLayerBoardWidth,
            "3 board-sized layers (418 pt) fit unclipped beside the list at 790 pt detail width"
        )

        let wideLandscape = FactoryTaskGraph.BoardLayout(
            isRegular: true, isAccessibilitySize: false, availableWidth: 860
        )
        XCTAssertTrue(wideLandscape.sideBySide)
        XCTAssertEqual(wideLandscape.listWidth, 340)
        XCTAssertGreaterThanOrEqual(860 - 32 - 16 - (wideLandscape.listWidth ?? 0), threeLayerBoardWidth)
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
            ("T13", "waiting on others", .idle, true, "T13, Sync, waiting on others, after T7"),
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

    /// Every stage emitted by the daemon (`internal/factory/flow/rows.go`, `ladder.go`,
    /// `view.go`) plus unmerged `ended` steps must map to the right `isDashed` border style:
    /// queued-looking stages (`queued`, `waiting_on_dep`) and ended steps (`split`, `dropped`,
    /// `closed`) are dashed; active, blocked, and merged-done stages are solid.
    func testDaemonStageDashedBorders() throws {
        let cases: [(stage: String, endedStep: String?, wantDashed: Bool)] = [
            ("done", nil, false),
            ("done", "split", true),
            ("done", "dropped", true),
            ("done", "closed", true),
            ("working", nil, false),
            ("ci", nil, false),
            ("owner_gate", nil, false),
            ("owner_answer", nil, false),
            ("blocked_or_failed", nil, false),
            ("waiting_on_dep", nil, true),
            ("queued", nil, true),
        ]
        let tasksJSON = cases.enumerated().map { index, item in
            let steps = item.endedStep.map { #"[{"name":"\#($0)","state":"ended"}]"# } ?? "[]"
            return #"{"ref":"T\#(index + 1)","title":"Task \#(index + 1)","stage":"\#(item.stage)","after":[],"dependents":[],"layer":0,"rounds":0,"steps":\#(steps)}"#
        }.joined(separator: ",")
        let view = try decodeGraph(#"{"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[\#(tasksJSON)]}"#)
        let byRef = Dictionary(uniqueKeysWithValues: FactoryTaskGraph(tasks: view.tasks).nodes.map { ($0.ref, $0) })
        for (index, item) in cases.enumerated() {
            let ref = "T\(index + 1)"
            let node = try XCTUnwrap(byRef[ref], ref)
            let label = item.endedStep.map { "\(item.stage)/\($0)" } ?? item.stage
            XCTAssertEqual(node.isDashed, item.wantDashed, "stage \(label) isDashed")
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

    /// Multi-computer summary line: filters factory worker/coordinator panes out of ordinary
    /// agent counts, counts factory projects instead of saying "No agents", counts open factory
    /// asks and stuck coordinators without counting stuck workers, and never leaks counts offline.
    func testComputerSnapshotSummaryCountsFactoryProjectsAndFiltersFactoryPanes() throws {
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        let now = ISO8601DateFormatter().date(from: "2026-10-23T12:00:00Z")!
        let since = ISO8601DateFormatter().date(from: "2026-10-23T09:12:00Z")!

        let heads = try decodeHeads(#"""
        [{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"running",
          "counts":{"working":2,"owner_answer":1,"done":4},"folder":"~/code/notes",
          "coordinator_pane":"coord-notes-1"},
         {"id":"proj-2","name":"photos","goal":"Organise albums","state":"stopped",
          "counts":{"done":1,"queued":1},"folder":"~/code/photos"},
         {"id":"proj-3","name":"recipes","goal":"Import recipes","state":"finished",
          "counts":{"done":4},"folder":"~/code/recipes"},
         {"id":"proj-4","name":"docs","goal":"Docs site","state":"running",
          "counts":{"working":1},"folder":"~/code/docs","coordinator_pane":"coord-docs-1"}]
        """#)
        let ask = PendingAction(
            id: "fask-1", paneID: "factory:proj-1", kind: "answer_prompt", question: "Delete table?",
            options: [PendingOption(index: 1, label: "Keep it")], agent: "notes",
            factory: FactoryAskMeta(project: "proj-1", task: "T10", kind: "delete")
        )
        let coordDialog = PendingAction(
            id: "ap-coord", paneID: "coord-docs-1", kind: "answer_prompt", question: "Allow gh pr list?",
            options: [PendingOption(index: 1, label: "Yes")], agent: "docs"
        )

        let homeOnlyFactory = ComputerSnapshot(
            id: "home", name: "Home Mac", isCurrent: false, reachability: .online,
            agents: [], questions: [], factoryHeads: [heads[1]]
        )
        XCTAssertEqual(homeOnlyFactory.summary(now: now, calendar: utc), "1 factory project")

        let twoProjectsWaiting = ComputerSnapshot(
            id: "home", name: "Home Mac", isCurrent: false, reachability: .online,
            agents: [], questions: [ask], factoryHeads: [heads[0], heads[2]]
        )
        XCTAssertEqual(twoProjectsWaiting.summary(now: now, calendar: utc), "2 factory projects · 1 waiting")

        let mixedAgents = [
            Agent(paneID: "p-pay", alias: "payments", workspaceLabel: "webapp", state: .working),
            Agent(paneID: "p-w8", alias: "worker-t8", workspaceLabel: "notes", state: .working,
                  labels: ["sheepdog.project": "proj-1", "sheepdog.role": "worker"]),
            Agent(paneID: "p-w9", alias: "worker-t9", workspaceLabel: "notes", state: .blocked,
                  labels: ["sheepdog.project": "proj-1", "sheepdog.role": "worker"]),
            Agent(paneID: "coord-docs-1", alias: "docs-coord", workspaceLabel: "docs", state: .blocked),
            Agent(paneID: "p-gone", alias: "gone", workspaceLabel: "webapp", state: .exited),
        ]
        var mixed = ComputerSnapshot(
            id: "work", name: "Work Mac", isCurrent: false, reachability: .online,
            agents: mixedAgents, questions: [ask, coordDialog], factoryHeads: [heads[0], heads[3]]
        )
        XCTAssertEqual(mixed.summary(now: now, calendar: utc), "1 agent · 2 factory projects · 2 waiting")

        mixed.reachability = .connecting
        XCTAssertEqual(mixed.summary(now: now, calendar: utc), "Connecting…")
        mixed.reachability = .offline(since: since)
        XCTAssertEqual(mixed.summary(now: now, calendar: utc), "Offline since 9:12")
        mixed.reachability = .offline(since: nil)
        XCTAssertEqual(mixed.summary(now: now, calendar: utc), "Offline")

        let empty = ComputerSnapshot(id: "e", name: "Empty Mac", isCurrent: false, reachability: .online)
        XCTAssertEqual(empty.summary(now: now, calendar: utc), "No agents")
    }
}

#if os(iOS)
import SwiftUI
import UIKit
@testable import Sheepdog

@MainActor
final class FactoryIPadAppModelTests: XCTestCase {
    /// AllAgentsView focus/scroll helpers must filter factory worker and coordinator panes
    /// on both the current computer and secondary computers instead of inserting nonexistent
    /// card keys into `expandedAgents`, while still expanding ordinary agents.
    func testAllAgentsFocusIgnoresFilteredFactoryPanesAndExpandsOrdinaryAgents() throws {
        let suite = "FactoryIPadAppModelTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        let cacheDir = FileManager.default.temporaryDirectory.appendingPathComponent(suite, isDirectory: true)
        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let model = SessionViewModel(
            settings: SettingsStore(defaults: defaults, computers: store),
            cache: ConversationCache(directory: cacheDir)
        )
        defer {
            model.fleet.set([])
            defaults.removePersistentDomain(forName: suite)
            try? FileManager.default.removeItem(at: cacheDir)
        }

        let work = PairedComputer(
            daemonID: "work-id", name: "Work Mac", addresses: ["192.0.2.10:7777"],
            pin: "pw", deviceID: "dw", pairedAt: Date()
        )
        let home = PairedComputer(
            daemonID: "home-id", name: "Home Mac", addresses: ["198.51.100.20:7777"],
            pin: "ph", deviceID: "dh", pairedAt: Date()
        )
        model.settings.computers.save(work, address: "192.0.2.10:7777")
        model.settings.computers.save(home, address: "198.51.100.20:7777")
        model.settings.computers.currentID = "work-id"
        model.settings.hasBeenConfigured = true
        model.computersChanged()

        let workState = #"""
        {"agents":[
          {"pane_id":"p-pay","alias":"payments","state":"idle"},
          {"pane_id":"p-w8","alias":"worker-t8","state":"working",
           "labels":{"sheepdog.project":"proj-1","sheepdog.role":"worker"}},
          {"pane_id":"coord-notes-1","alias":"coord-notes","state":"blocked"}
        ],"questions":[],"factory":[
          {"id":"proj-1","name":"notes","goal":"Notes","state":"running",
           "counts":{"working":1},"coordinator_pane":"coord-notes-1"}
        ],"voice":{"mode":"off","status":"closed","muted":false,"stalled":false,"client_connected":false}}
        """#
        let homeState = #"""
        {"agents":[
          {"pane_id":"p-ed","alias":"editor","state":"working"},
          {"pane_id":"p-hw","alias":"worker-home","state":"working",
           "labels":{"sheepdog.project":"proj-2","sheepdog.role":"worker"}},
          {"pane_id":"coord-home-1","alias":"coord-home","state":"blocked"}
        ],"questions":[],"factory":[
          {"id":"proj-2","name":"photos","goal":"Photos","state":"running",
           "counts":{"working":1},"coordinator_pane":"coord-home-1"}
        ],"voice":{"mode":"off","status":"closed","muted":false,"stalled":false,"client_connected":false}}
        """#
        model.fleet.current?.accept(try DaemonClient.decoder.decode(DaemonState.self, from: Data(workState.utf8)))
        model.session("home-id")?.accept(try DaemonClient.decoder.decode(DaemonState.self, from: Data(homeState.utf8)))

        let ui = SceneUI()
        let host = UIHostingController(
            rootView: AllAgentsView(leading: nil).environmentObject(model).environment(ui)
        )
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 1024, height: 768))
        window.rootViewController = host
        window.makeKeyAndVisible()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        defer {
            window.isHidden = true
            window.rootViewController = nil
            RunLoop.main.run(until: Date().addingTimeInterval(0.02))
        }

        let filteredTargets: [(focus: AgentFocus, forbiddenKey: String)] = [
            (AgentFocus(paneID: "coord-notes-1"), "coord-notes-1"),
            (AgentFocus(paneID: "p-w8"), "p-w8"),
            (AgentFocus(computerID: "home-id", paneID: "p-hw"), "home-id|p-hw"),
            (AgentFocus(computerID: "home-id", paneID: "coord-home-1"), "home-id|coord-home-1"),
        ]
        for entry in filteredTargets {
            model.agentsModel.expandedAgents.removeAll()
            model.navigation.focusedAgent = entry.focus
            host.view.setNeedsLayout()
            host.view.layoutIfNeeded()
            RunLoop.main.run(until: Date().addingTimeInterval(0.05))
            XCTAssertFalse(
                model.agentsModel.expandedAgents.contains(entry.forbiddenKey),
                "filtered factory pane \(entry.forbiddenKey) must not be inserted into expandedAgents"
            )
        }

        model.agentsModel.expandedAgents.removeAll()
        model.navigation.focusedAgent = AgentFocus(paneID: "p-pay")
        host.view.setNeedsLayout()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        XCTAssertTrue(model.agentsModel.expandedAgents.contains("p-pay"))
        XCTAssertNil(model.navigation.focusedAgent)

        model.navigation.focusedAgent = AgentFocus(computerID: "home-id", paneID: "p-ed")
        host.view.setNeedsLayout()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        XCTAssertTrue(model.agentsModel.expandedAgents.contains("home-id|p-ed"))
        XCTAssertNil(model.navigation.focusedAgent)
    }

    /// `FactoryTaskGraphView` must scroll horizontally only (never a nested vertical `ScrollView`
    /// competing with the outer vertical `boardScroll`).
    func testGraphViewUsesHorizontalOnlyScrollView() throws {
        let json = #"""
        {"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[
          {"ref":"T1","title":"One","stage":"done","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[]},
          {"ref":"T2","title":"Two","stage":"done","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[]},
          {"ref":"T3","title":"Three","stage":"done","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[]},
          {"ref":"T4","title":"Four","stage":"working","after":["T1"],"dependents":[],"layer":1,"rounds":0,"steps":[]}
        ]}
        """#
        let view = try DaemonClient.decoder.decode(FactoryGraphView.self, from: Data(json.utf8))
        let host = UIHostingController(
            rootView: FactoryTaskGraphView(graph: FactoryTaskGraph(tasks: view.tasks)) { _ in }
                .frame(width: 420, height: 110)
        )
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 420, height: 110))
        window.rootViewController = host
        window.makeKeyAndVisible()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        defer {
            window.isHidden = true
            window.rootViewController = nil
            RunLoop.main.run(until: Date().addingTimeInterval(0.02))
        }

        func findScrollViews(in root: UIView) -> [UIScrollView] {
            var found: [UIScrollView] = []
            if let sv = root as? UIScrollView { found.append(sv) }
            for sub in root.subviews { found.append(contentsOf: findScrollViews(in: sub)) }
            return found
        }
        let scrollViews = findScrollViews(in: host.view)
        XCTAssertFalse(scrollViews.isEmpty, "graph view contains a horizontal scroll view")
        for sv in scrollViews {
            XCTAssertLessThanOrEqual(
                sv.contentSize.height, sv.bounds.height + 1,
                "graph scroll view must not scroll vertically inside boardScroll"
            )
        }

        // At AX5 inside a 420 pt container, a 3-layer wrapped graph (3 × 250 pt + gaps > 420 pt)
        // must activate horizontal scrolling while still never scrolling vertically.
        let ax5JSON = #"""
        {"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[
          {"ref":"T1","title":"Approve gate","stage":"owner_gate","after":[],"dependents":["T2","T3"],"layer":0,"rounds":1,"steps":[]},
          {"ref":"T2","title":"Wait on dep","stage":"waiting_on_dep","after":["T1"],"dependents":["T3"],"layer":1,"rounds":0,"steps":[]},
          {"ref":"T3","title":"Ship it","stage":"done","after":["T1","T2"],"dependents":[],"layer":2,"rounds":0,"steps":[]}
        ]}
        """#
        let ax5View = try DaemonClient.decoder.decode(FactoryGraphView.self, from: Data(ax5JSON.utf8))
        let ax5Host = UIHostingController(
            rootView: VStack(spacing: 0) {
                FactoryTaskGraphView(graph: FactoryTaskGraph(tasks: ax5View.tasks)) { _ in }
                Spacer(minLength: 0)
            }
            .environment(\.dynamicTypeSize, .accessibility5)
            .frame(width: 420, height: 500)
        )
        ax5Host.traitOverrides.preferredContentSizeCategory = .accessibilityExtraExtraExtraLarge
        let ax5Window = UIWindow(frame: CGRect(x: 0, y: 0, width: 420, height: 500))
        ax5Window.traitOverrides.preferredContentSizeCategory = .accessibilityExtraExtraExtraLarge
        ax5Window.rootViewController = ax5Host
        ax5Window.makeKeyAndVisible()
        ax5Host.view.setNeedsLayout()
        ax5Host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        defer {
            ax5Window.isHidden = true
            ax5Window.rootViewController = nil
            RunLoop.main.run(until: Date().addingTimeInterval(0.02))
        }

        let ax5Scrolls = findScrollViews(in: ax5Host.view)
        XCTAssertFalse(ax5Scrolls.isEmpty, "AX5 graph view contains a horizontal scroll view")
        for sv in ax5Scrolls {
            XCTAssertGreaterThan(
                sv.contentSize.width, sv.bounds.width + 100,
                "AX5 3-layer graph (3 × 250 pt) must scroll horizontally inside a 420 pt container"
            )
            XCTAssertLessThanOrEqual(
                sv.contentSize.height, sv.bounds.height + 1,
                "AX5 graph scroll view must not scroll vertically inside boardScroll"
            )
        }
    }

    private func sampledPoints(of path: Path, stepsPerSegment: Int = 24) -> [CGPoint] {
        var points: [CGPoint] = []
        var current = CGPoint.zero
        var subpathStart = CGPoint.zero
        path.cgPath.applyWithBlock { elementPointer in
            let el = elementPointer.pointee
            switch el.type {
            case .moveToPoint:
                current = el.points[0]
                subpathStart = current
                points.append(current)
            case .addLineToPoint:
                let end = el.points[0]
                for step in 1 ... stepsPerSegment {
                    let t = CGFloat(step) / CGFloat(stepsPerSegment)
                    points.append(CGPoint(
                        x: current.x + (end.x - current.x) * t,
                        y: current.y + (end.y - current.y) * t
                    ))
                }
                current = end
            case .addQuadCurveToPoint:
                let c = el.points[0], end = el.points[1]
                for step in 1 ... stepsPerSegment {
                    let t = CGFloat(step) / CGFloat(stepsPerSegment)
                    let u = 1 - t
                    points.append(CGPoint(
                        x: u * u * current.x + 2 * u * t * c.x + t * t * end.x,
                        y: u * u * current.y + 2 * u * t * c.y + t * t * end.y
                    ))
                }
                current = end
            case .addCurveToPoint:
                let c1 = el.points[0], c2 = el.points[1], end = el.points[2]
                for step in 1 ... stepsPerSegment {
                    let t = CGFloat(step) / CGFloat(stepsPerSegment)
                    let u = 1 - t
                    let x = u * u * u * current.x + 3 * u * u * t * c1.x + 3 * u * t * t * c2.x + t * t * t * end.x
                    let y = u * u * u * current.y + 3 * u * u * t * c1.y + 3 * u * t * t * c2.y + t * t * t * end.y
                    points.append(CGPoint(x: x, y: y))
                }
                current = end
            case .closeSubpath:
                current = subpathStart
                points.append(current)
            @unknown default:
                break
            }
        }
        return points
    }

    private func layoutRects(
        for graph: FactoryTaskGraph,
        nodeWidth: CGFloat = 118,
        heights: [String: CGFloat] = [:]
    ) -> (rects: [String: CGRect], canvasHeight: CGFloat, verticalPadding: CGFloat) {
        let layerOf = Dictionary(graph.nodes.map { ($0.ref, $0.layerIndex) }, uniquingKeysWith: { _, new in new })
        let skips = graph.edges.filter { (layerOf[$0.to] ?? 0) - (layerOf[$0.from] ?? 0) > 1 }.count
        let vPad: CGFloat = skips > 0 ? 14 + CGFloat(min(skips, 5)) * 8 : 12
        var rects: [String: CGRect] = [:]
        var maxBottom: CGFloat = vPad
        for (layerIndex, layer) in graph.layers.enumerated() {
            let x: CGFloat = 12 + CGFloat(layerIndex) * (nodeWidth + 20)
            var y = vPad
            for node in layer {
                let h = heights[node.ref] ?? 46
                rects[node.ref] = CGRect(x: x, y: y, width: nodeWidth, height: h)
                y += h + 14
                maxBottom = max(maxBottom, y - 14)
            }
        }
        return (rects, maxBottom + vPad, vPad)
    }

    /// Regression test for skip-edge routing (`FactoryTaskGraphView.edgePaths(graph:rects:)`):
    /// covers a 3-layer obstructed skip, >3-layer obstructed skips (top and bottom bypass lanes),
    /// multiple staggered skip lanes within canvas bounds, and unobstructed skip edges that stay
    /// direct without detouring. Fails if `edgePaths` reverts to straight direct cubic Beziers.
    func testSkipEdgeRoutingAvoidsIntermediateNodesAcrossThreeAndMoreLayers() throws {
        func decode(_ json: String) throws -> FactoryTaskGraph {
            let view = try DaemonClient.decoder.decode(FactoryGraphView.self, from: Data(json.utf8))
            return FactoryTaskGraph(tasks: view.tasks)
        }

        // 1. 3-layer obstructed skip: T1 -> T3 must arch above T2 instead of crossing through T2.
        let threeLayer = try decode(#"""
        {"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[
          {"ref":"T1","title":"Approve gate","stage":"owner_gate","after":[],"dependents":["T2","T3"],"layer":0,"rounds":1,"steps":[]},
          {"ref":"T2","title":"Wait on dep","stage":"waiting_on_dep","after":["T1"],"dependents":["T3"],"layer":1,"rounds":0,"steps":[]},
          {"ref":"T3","title":"Ship it","stage":"done","after":["T1","T2"],"dependents":[],"layer":2,"rounds":0,"steps":[]}
        ]}
        """#)
        let threeLayout = layoutRects(for: threeLayer, heights: ["T1": 62, "T2": 62, "T3": 46])
        let threePaths = FactoryTaskGraphView.edgePaths(graph: threeLayer, rects: threeLayout.rects)
        let t1 = try XCTUnwrap(threeLayout.rects["T1"])
        let t2 = try XCTUnwrap(threeLayout.rects["T2"])
        let t3 = try XCTUnwrap(threeLayout.rects["T3"])
        let skip3Edge = FactoryTaskGraph.Edge(from: "T1", to: "T3")
        let skip3Points = sampledPoints(of: try XCTUnwrap(threePaths[skip3Edge]))
        XCTAssertEqual(skip3Points.first?.x ?? -1, t1.maxX, accuracy: 0.5)
        XCTAssertEqual(skip3Points.first?.y ?? -1, t1.midY, accuracy: 0.5)
        XCTAssertEqual(skip3Points.last?.x ?? -1, t3.minX, accuracy: 0.5)
        XCTAssertEqual(skip3Points.last?.y ?? -1, t3.midY, accuracy: 0.5)
        XCTAssertFalse(
            skip3Points.contains(where: { t2.insetBy(dx: -1, dy: -1).contains($0) }),
            "3-layer skip edge T1->T3 must not intersect intermediate node T2"
        )
        let overT2 = skip3Points.filter { $0.x >= t2.minX && $0.x <= t2.maxX }
        XCTAssertFalse(overT2.isEmpty, "T1->T3 spans across T2's horizontal band")
        for pt in overT2 {
            XCTAssertLessThanOrEqual(pt.y, t2.minY - 10, "T1->T3 bypass lane must clear above T2")
            XCTAssertGreaterThanOrEqual(pt.y, 0, "T1->T3 bypass lane must stay inside top canvas padding")
        }

        // 2. >3 layers (4 layers, uneven intermediate heights): T1->T7 detours above layers 1 & 2,
        //    and T2->T8 detours below layers 1 & 2 without intersecting any intermediate node.
        let fourLayer = try decode(#"""
        {"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[
          {"ref":"T1","title":"L0 top","stage":"done","after":[],"dependents":["T3","T7"],"layer":0,"rounds":0,"steps":[]},
          {"ref":"T2","title":"L0 bot","stage":"done","after":[],"dependents":["T4","T8"],"layer":0,"rounds":0,"steps":[]},
          {"ref":"T3","title":"L1 top","stage":"working","after":["T1"],"dependents":["T5"],"layer":1,"rounds":0,"steps":[]},
          {"ref":"T4","title":"L1 bot","stage":"working","after":["T2"],"dependents":["T6"],"layer":1,"rounds":0,"steps":[]},
          {"ref":"T5","title":"L2 top","stage":"working","after":["T3"],"dependents":["T7"],"layer":2,"rounds":0,"steps":[]},
          {"ref":"T6","title":"L2 bot","stage":"working","after":["T4"],"dependents":["T8"],"layer":2,"rounds":0,"steps":[]},
          {"ref":"T7","title":"L3 top","stage":"queued","after":["T1","T5"],"dependents":[],"layer":3,"rounds":0,"steps":[]},
          {"ref":"T8","title":"L3 bot","stage":"queued","after":["T2","T6"],"dependents":[],"layer":3,"rounds":0,"steps":[]}
        ]}
        """#)
        let fourLayout = layoutRects(
            for: fourLayer,
            heights: ["T1": 64, "T2": 46, "T3": 46, "T4": 60, "T5": 46, "T6": 72, "T7": 46, "T8": 60]
        )
        let fourPaths = FactoryTaskGraphView.edgePaths(graph: fourLayer, rects: fourLayout.rects)
        let inter4 = try ["T3", "T4", "T5", "T6"].map { try XCTUnwrap(fourLayout.rects[$0]) }
        let inter4MinY = try XCTUnwrap(inter4.map(\.minY).min())
        let inter4MaxY = try XCTUnwrap(inter4.map(\.maxY).max())

        let topSkip4 = sampledPoints(of: try XCTUnwrap(fourPaths[.init(from: "T1", to: "T7")]))
        for rect in inter4 {
            XCTAssertFalse(
                topSkip4.contains(where: { rect.insetBy(dx: -1, dy: -1).contains($0) }),
                "4-layer top skip T1->T7 must clear every intermediate node across layers 1 and 2"
            )
        }
        let topSpan4 = topSkip4.filter { $0.x >= inter4[0].minX && $0.x <= inter4[2].maxX }
        XCTAssertFalse(topSpan4.isEmpty)
        for pt in topSpan4 {
            XCTAssertLessThanOrEqual(pt.y, inter4MinY - 10)
            XCTAssertGreaterThanOrEqual(pt.y, 0)
        }

        let botSkip4 = sampledPoints(of: try XCTUnwrap(fourPaths[.init(from: "T2", to: "T8")]))
        for rect in inter4 {
            XCTAssertFalse(
                botSkip4.contains(where: { rect.insetBy(dx: -1, dy: -1).contains($0) }),
                "4-layer bottom skip T2->T8 must clear every intermediate node across layers 1 and 2"
            )
        }
        let botSpan4 = botSkip4.filter { $0.x >= inter4[1].minX && $0.x <= inter4[3].maxX }
        XCTAssertFalse(botSpan4.isEmpty)
        for pt in botSpan4 {
            XCTAssertGreaterThanOrEqual(pt.y, inter4MaxY + 10)
            XCTAssertLessThanOrEqual(pt.y, fourLayout.canvasHeight)
        }

        // 3. Multiple skips staggered (5 layers: 3 top lanes + 2 bottom lanes).
        let fiveLayer = try decode(#"""
        {"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[
          {"ref":"A0","title":"A0","stage":"done","after":[],"dependents":["A1","A2","A3"],"layer":0,"rounds":0,"steps":[]},
          {"ref":"B0","title":"B0","stage":"done","after":[],"dependents":["B1","B2","B3"],"layer":0,"rounds":0,"steps":[]},
          {"ref":"A1","title":"A1","stage":"done","after":["A0"],"dependents":["A2","A4"],"layer":1,"rounds":0,"steps":[]},
          {"ref":"B1","title":"B1","stage":"done","after":["B0"],"dependents":["B2"],"layer":1,"rounds":0,"steps":[]},
          {"ref":"A2","title":"A2","stage":"working","after":["A0","A1"],"dependents":["A3"],"layer":2,"rounds":0,"steps":[]},
          {"ref":"B2","title":"B2","stage":"working","after":["B0","B1"],"dependents":["B3"],"layer":2,"rounds":0,"steps":[]},
          {"ref":"A3","title":"A3","stage":"queued","after":["A0","A2"],"dependents":["A4"],"layer":3,"rounds":0,"steps":[]},
          {"ref":"B3","title":"B3","stage":"queued","after":["B0","B2"],"dependents":["B4"],"layer":3,"rounds":0,"steps":[]},
          {"ref":"A4","title":"A4","stage":"queued","after":["A1","A3"],"dependents":[],"layer":4,"rounds":0,"steps":[]},
          {"ref":"B4","title":"B4","stage":"queued","after":["B3"],"dependents":[],"layer":4,"rounds":0,"steps":[]}
        ]}
        """#)
        let fiveLayout = layoutRects(for: fiveLayer)
        let fivePaths = FactoryTaskGraphView.edgePaths(graph: fiveLayer, rects: fiveLayout.rects)
        let nodeByRef5 = Dictionary(fiveLayer.nodes.map { ($0.ref, $0) }, uniquingKeysWith: { _, new in new })

        let topEdges: [FactoryTaskGraph.Edge] = [
            .init(from: "A0", to: "A2"),
            .init(from: "A0", to: "A3"),
            .init(from: "A1", to: "A4"),
        ]
        var topLaneYs: [CGFloat] = []
        for edge in topEdges {
            let pts = sampledPoints(of: try XCTUnwrap(fivePaths[edge]))
            let fromL = try XCTUnwrap(nodeByRef5[edge.from]?.layerIndex)
            let toL = try XCTUnwrap(nodeByRef5[edge.to]?.layerIndex)
            let interRects = fiveLayer.nodes
                .filter { $0.layerIndex > fromL && $0.layerIndex < toL }
                .compactMap { fiveLayout.rects[$0.ref] }
            for rect in interRects {
                XCTAssertFalse(
                    pts.contains(where: { rect.insetBy(dx: -1, dy: -1).contains($0) }),
                    "staggered top skip \(edge.from)->\(edge.to) must not intersect intermediate nodes"
                )
            }
            let minX = try XCTUnwrap(interRects.map(\.minX).min())
            let maxX = try XCTUnwrap(interRects.map(\.maxX).max())
            let spanYs = pts.filter { $0.x >= minX && $0.x <= maxX }.map(\.y)
            let laneY = try XCTUnwrap(spanYs.min())
            XCTAssertGreaterThanOrEqual(laneY, 0, "top lane stays inside canvas top padding")
            topLaneYs.append(laneY)
        }
        XCTAssertGreaterThan(topLaneYs[0] - topLaneYs[1], 7, "top skip lanes 0 and 1 must stagger by ~8 pt")
        XCTAssertGreaterThan(topLaneYs[1] - topLaneYs[2], 7, "top skip lanes 1 and 2 must stagger by ~8 pt")

        let botEdges: [FactoryTaskGraph.Edge] = [
            .init(from: "B0", to: "B2"),
            .init(from: "B0", to: "B3"),
        ]
        var botLaneYs: [CGFloat] = []
        for edge in botEdges {
            let pts = sampledPoints(of: try XCTUnwrap(fivePaths[edge]))
            let fromL = try XCTUnwrap(nodeByRef5[edge.from]?.layerIndex)
            let toL = try XCTUnwrap(nodeByRef5[edge.to]?.layerIndex)
            let interRects = fiveLayer.nodes
                .filter { $0.layerIndex > fromL && $0.layerIndex < toL }
                .compactMap { fiveLayout.rects[$0.ref] }
            for rect in interRects {
                XCTAssertFalse(
                    pts.contains(where: { rect.insetBy(dx: -1, dy: -1).contains($0) }),
                    "staggered bottom skip \(edge.from)->\(edge.to) must not intersect intermediate nodes"
                )
            }
            let minX = try XCTUnwrap(interRects.map(\.minX).min())
            let maxX = try XCTUnwrap(interRects.map(\.maxX).max())
            let spanYs = pts.filter { $0.x >= minX && $0.x <= maxX }.map(\.y)
            let laneY = try XCTUnwrap(spanYs.max())
            XCTAssertLessThanOrEqual(laneY, fiveLayout.canvasHeight, "bottom lane stays inside canvas bottom padding")
            botLaneYs.append(laneY)
        }
        XCTAssertGreaterThan(botLaneYs[1] - botLaneYs[0], 7, "bottom skip lanes 0 and 1 must stagger by ~8 pt")

        // 4. Unobstructed skip edges (T6->T8 horizontal and T6->T11 diagonal below single-node T4)
        //    keep direct curves within [min(start.y, end.y), max(start.y, end.y)] while obstructed
        //    T1->T7 still detours above T4.
        let sampleStyle = try decode(#"""
        {"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[
          {"ref":"T1","title":"Notes page","stage":"done","after":[],"dependents":["T4","T7"],"layer":0,"rounds":0,"steps":[]},
          {"ref":"T6","title":"Storage","stage":"done","after":[],"dependents":["T8","T11"],"layer":0,"rounds":0,"steps":[]},
          {"ref":"T9","title":"Export","stage":"done","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[]},
          {"ref":"T4","title":"Search box","stage":"done","after":["T1"],"dependents":["T7"],"layer":1,"rounds":0,"steps":[]},
          {"ref":"T7","title":"Search","stage":"working","after":["T1","T4"],"dependents":[],"layer":2,"rounds":0,"steps":[]},
          {"ref":"T8","title":"Tags","stage":"working","after":["T6"],"dependents":[],"layer":2,"rounds":0,"steps":[]},
          {"ref":"T11","title":"Archive","stage":"queued","after":["T6"],"dependents":[],"layer":2,"rounds":0,"steps":[]}
        ]}
        """#)
        let sampleLayout = layoutRects(for: sampleStyle)
        let samplePaths = FactoryTaskGraphView.edgePaths(graph: sampleStyle, rects: sampleLayout.rects)
        let rectT4 = try XCTUnwrap(sampleLayout.rects["T4"])
        let rectT6 = try XCTUnwrap(sampleLayout.rects["T6"])
        let rectT8 = try XCTUnwrap(sampleLayout.rects["T8"])
        let rectT11 = try XCTUnwrap(sampleLayout.rects["T11"])

        let obstructedPts = sampledPoints(of: try XCTUnwrap(samplePaths[.init(from: "T1", to: "T7")]))
        XCTAssertFalse(obstructedPts.contains(where: { rectT4.insetBy(dx: -1, dy: -1).contains($0) }))
        XCTAssertLessThanOrEqual(
            try XCTUnwrap(obstructedPts.filter { $0.x >= rectT4.minX && $0.x <= rectT4.maxX }.map(\.y).max()),
            rectT4.minY - 10,
            "obstructed T1->T7 detours above T4"
        )

        for (toRef, toRect) in [("T8", rectT8), ("T11", rectT11)] {
            let pts = sampledPoints(of: try XCTUnwrap(samplePaths[.init(from: "T6", to: toRef)]))
            let minY = min(rectT6.midY, toRect.midY)
            let maxY = max(rectT6.midY, toRect.midY)
            XCTAssertFalse(pts.contains(where: { rectT4.insetBy(dx: -1, dy: -1).contains($0) }))
            for pt in pts {
                XCTAssertGreaterThanOrEqual(pt.y, minY - 0.5, "unobstructed T6->\(toRef) must not detour above endpoints")
                XCTAssertLessThanOrEqual(pt.y, maxY + 0.5, "unobstructed T6->\(toRef) must not detour below endpoints")
            }
        }
        // 5. Vertical padding expands when skip-layer edges exist so bypass lanes stay inside canvas bounds.
        let noSkipThreeLayer = try decode(#"""
        {"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[
          {"ref":"T1","title":"Approve gate","stage":"owner_gate","after":[],"dependents":["T2"],"layer":0,"rounds":1,"steps":[]},
          {"ref":"T2","title":"Wait on dep","stage":"waiting_on_dep","after":["T1"],"dependents":["T3"],"layer":1,"rounds":0,"steps":[]},
          {"ref":"T3","title":"Ship it","stage":"done","after":["T2"],"dependents":[],"layer":2,"rounds":0,"steps":[]}
        ]}
        """#)
        func hostedGraphContentHeight(_ graph: FactoryTaskGraph) throws -> CGFloat {
            let host = UIHostingController(
                rootView: VStack(spacing: 0) {
                    FactoryTaskGraphView(graph: graph) { _ in }
                    Spacer(minLength: 0)
                }
                .frame(width: 500, height: 300)
            )
            let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 500, height: 300))
            window.rootViewController = host
            window.makeKeyAndVisible()
            host.view.setNeedsLayout()
            host.view.layoutIfNeeded()
            RunLoop.main.run(until: Date().addingTimeInterval(0.05))
            defer {
                window.isHidden = true
                window.rootViewController = nil
                RunLoop.main.run(until: Date().addingTimeInterval(0.02))
            }
            func firstScrollView(in root: UIView) -> UIScrollView? {
                if let sv = root as? UIScrollView { return sv }
                for sub in root.subviews {
                    if let found = firstScrollView(in: sub) { return found }
                }
                return nil
            }
            return try XCTUnwrap(firstScrollView(in: host.view)).contentSize.height
        }
        let noSkipHeight = try hostedGraphContentHeight(noSkipThreeLayer)
        let withSkipHeight = try hostedGraphContentHeight(threeLayer)
        XCTAssertGreaterThanOrEqual(
            withSkipHeight, noSkipHeight + 16,
            "graph with skip-layer edge must expand vertical padding for bypass lanes"
        )
    }

    /// `FactoryTaskGraph(tasks:)` does not deduplicate task refs, so `FactoryTaskGraphView`'s
    /// padding and `edgePaths` lookups must not trap with `Dictionary(uniqueKeysWithValues:)`
    /// if a payload ever contains duplicate refs.
    func testEdgePathsAndGraphViewTolerateDuplicateTaskRefs() throws {
        let json = #"""
        {"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[
          {"ref":"T1","title":"First","stage":"done","after":[],"dependents":["T2","T3"],"layer":0,"rounds":0,"steps":[]},
          {"ref":"T1","title":"Duplicate","stage":"done","after":[],"dependents":["T2","T3"],"layer":0,"rounds":0,"steps":[]},
          {"ref":"T2","title":"Middle","stage":"working","after":["T1"],"dependents":["T3"],"layer":1,"rounds":0,"steps":[]},
          {"ref":"T3","title":"End","stage":"queued","after":["T1","T2"],"dependents":[],"layer":2,"rounds":0,"steps":[]}
        ]}
        """#
        let view = try DaemonClient.decoder.decode(FactoryGraphView.self, from: Data(json.utf8))
        let graph = FactoryTaskGraph(tasks: view.tasks)
        let layout = layoutRects(for: graph)
        let paths = FactoryTaskGraphView.edgePaths(graph: graph, rects: layout.rects)
        XCTAssertNotNil(paths[.init(from: "T1", to: "T3")])

        let host = UIHostingController(
            rootView: FactoryTaskGraphView(graph: graph) { _ in }
                .frame(width: 420, height: 200)
        )
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 420, height: 200))
        window.rootViewController = host
        window.makeKeyAndVisible()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        defer {
            window.isHidden = true
            window.rootViewController = nil
            RunLoop.main.run(until: Date().addingTimeInterval(0.02))
        }
    }

    /// In a narrow regular-width iPad window (e.g. a 760 pt Stage Manager window with sidebar
    /// leaving ~440 pt for the detail), `FactoryProjectView` must stack the graph below tasks at
    /// full detail width instead of crushing it beside a fixed 340 pt task column.
    func testNarrowRegularWidthWindowStacksInsteadOfCrushingGraph() async throws {
        let graphJSON = #"""
        {"protocol":1,"project":"proj-1","folder":"~/code/notes","tasks":[
          {"ref":"T1","title":"Notes page","stage":"done","after":[],"dependents":["T4","T7"],"layer":0,"rounds":0,"steps":[{"name":"merged","state":"done"}]},
          {"ref":"T6","title":"Storage","stage":"done","after":[],"dependents":["T8"],"layer":0,"rounds":0,"steps":[{"name":"split","state":"ended"}]},
          {"ref":"T9","title":"Export to PDF","stage":"done","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[{"name":"dropped","state":"ended"}]},
          {"ref":"T4","title":"Search box","stage":"done","after":["T1"],"dependents":["T7","T10"],"layer":1,"rounds":0,"steps":[{"name":"merged","state":"done"}]},
          {"ref":"T7","title":"Search","stage":"working","why_waiting":"In review round 2","after":["T1","T4"],"dependents":[],"layer":2,"rounds":1,"steps":[{"name":"review","state":"now"}]},
          {"ref":"T8","title":"Tags","stage":"working","why_waiting":"Worker is working","after":["T6"],"dependents":[],"layer":2,"rounds":1,"steps":[{"name":"implementing","state":"now"}]},
          {"ref":"T10","title":"Import","stage":"owner_answer","why_waiting":"Delete the old notes table?","after":["T4"],"dependents":[],"layer":2,"rounds":0,"steps":[{"name":"implementing","state":"now"}]}
        ]}
        """#
        let stateJSON = #"""
        {"agents":[{"pane_id":"p-pay","alias":"payments","kind":"claude","state":"working","workspace_label":"webapp"}],
         "questions":[{"id":"fask-del-1","pane_id":"factory:proj-1","kind":"answer_prompt","agent":"notes",
           "question":"Delete the old notes table?","summary":"Delete the old notes table?",
           "factory":{"project":"proj-1","task":"T10","kind":"delete"},
           "options":[{"index":1,"label":"Keep it","default":true,"recommended":true},{"index":2,"label":"Delete it"}]}],
         "factory":[{"id":"proj-1","name":"notes","goal":"A notes page with search","state":"running",
           "counts":{"working":2,"owner_answer":1,"done":4},"folder":"~/code/notes","coordinator_pane":"coord-notes-1"},
          {"id":"proj-2","name":"photos","goal":"Faster thumbnails","state":"stopped",
           "counts":{"done":2,"queued":4},"folder":"~/code/photos"}],
         "voice":{"mode":"off","status":"closed","muted":false,"stalled":false,"client_connected":false}}
        """#
        NarrowGraphURLStub.graphJSON = graphJSON
        NarrowGraphURLStub.stateJSON = stateJSON

        let suite = "FactoryIPadNarrowTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        let cacheDir = FileManager.default.temporaryDirectory.appendingPathComponent(suite, isDirectory: true)
        let store = ComputerStore(defaults: defaults, secrets: InMemorySecretStore())
        let settings = SettingsStore(defaults: defaults, computers: store)
        settings.address = "127.0.0.1:7799"
        settings.hasBeenConfigured = true
        let model = SessionViewModel(settings: settings, cache: ConversationCache(directory: cacheDir))

        let endpoint = DaemonEndpoint(host: "127.0.0.1", port: 7799)
        let connection = ComputerConnection(computerID: "fixture-1", address: endpoint.display) { _ in
            let config = URLSessionConfiguration.ephemeral
            config.protocolClasses = [NarrowGraphURLStub.self]
            return DaemonClient(endpoint: endpoint, session: URLSession(configuration: config))
        }
        let workMac = ComputerModel(
            id: "fixture-1", name: "Work Mac", addresses: [endpoint.display], certExpires: nil,
            connection: connection, scheduler: ManualLinkClock(), demand: LinkDemand(isCurrent: true)
        )
        let decodedState = try DaemonClient.decoder.decode(DaemonState.self, from: Data(stateJSON.utf8))
        workMac.accept(decodedState)
        model.fleet.set([workMac])
        let head = try XCTUnwrap(decodedState.factory?.first)
        defer {
            workMac.stop()
            model.fleet.set([])
            defaults.removePersistentDomain(forName: suite)
            try? FileManager.default.removeItem(at: cacheDir)
        }

        let host = UIHostingController(
            rootView: NavigationStack {
                FactoryProjectView(initialHead: head, computerID: "fixture-1", onDismiss: {})
                    .environment(\.horizontalSizeClass, .regular)
            }
            .environmentObject(model)
            .frame(width: 440, height: 1600)
        )
        host.traitOverrides.horizontalSizeClass = .regular
        let scene = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first
        let window = scene.map { UIWindow(windowScene: $0) } ?? UIWindow(frame: .zero)
        window.frame = CGRect(x: 0, y: 0, width: 440, height: 1600)
        window.traitOverrides.horizontalSizeClass = .regular
        window.rootViewController = host
        window.makeKeyAndVisible()
        defer {
            window.isHidden = true
            window.rootViewController = nil
        }

        func findScrollViews(in root: UIView) -> [UIScrollView] {
            var found: [UIScrollView] = []
            if let sv = root as? UIScrollView, !(sv is UITextView) { found.append(sv) }
            for sub in root.subviews { found.append(contentsOf: findScrollViews(in: sub)) }
            return found
        }

        let deadline = Date().addingTimeInterval(5)
        var innerScroll: UIScrollView?
        while Date() < deadline {
            host.view.setNeedsLayout()
            host.view.layoutIfNeeded()
            RunLoop.main.run(until: Date().addingTimeInterval(0.01))
            try await Task.sleep(nanoseconds: 20_000_000)
            let scrolls = findScrollViews(in: host.view)
            if scrolls.count >= 2 {
                innerScroll = scrolls.dropFirst().first
                break
            }
        }
        let graphScroll = try XCTUnwrap(
            innerScroll,
            "graph scroll view must render in narrow regular-width board (scrolls: \(findScrollViews(in: host.view).count))"
        )
        XCTAssertGreaterThanOrEqual(
            graphScroll.bounds.width, 350,
            "narrow regular-width board must stack the graph at full width instead of crushing it beside 340 pt tasks"
        )
        XCTAssertGreaterThanOrEqual(
            graphScroll.convert(graphScroll.bounds, to: window).minY, 600,
            "narrow regular-width board must stack the graph below the task list"
        )

        if let shotPath = ProcessInfo.processInfo.environment["SHEEPDOG_NARROW_SHOT"] {
            let ui = SceneUI()
            ui.detail = .factoryProject(computerID: "fixture-1", projectID: "proj-1")
            let shotSize = CGSize(width: 760, height: 740)
            let shotHost = UIHostingController(
                rootView: IPadRootView()
                    .environmentObject(model)
                    .environment(ui)
                    .environment(\.horizontalSizeClass, .regular)
                    .frame(width: shotSize.width, height: shotSize.height)
                    .clipped()
            )
            shotHost.traitOverrides.horizontalSizeClass = .regular
            let shotWindow = scene.map { UIWindow(windowScene: $0) } ?? UIWindow(frame: .zero)
            shotWindow.frame = CGRect(origin: .zero, size: shotSize)
            shotWindow.traitOverrides.horizontalSizeClass = .regular
            shotWindow.rootViewController = shotHost
            shotWindow.makeKeyAndVisible()
            defer {
                shotWindow.isHidden = true
                shotWindow.rootViewController = nil
            }
            let shotDeadline = Date().addingTimeInterval(5)
            var detailScroll: UIScrollView?
            while Date() < shotDeadline {
                shotHost.view.setNeedsLayout()
                shotHost.view.layoutIfNeeded()
                RunLoop.main.run(until: Date().addingTimeInterval(0.01))
                try await Task.sleep(nanoseconds: 20_000_000)
                if let sv = findScrollViews(in: shotHost.view)
                    .filter({ $0.contentSize.height > $0.bounds.height + 100 })
                    .max(by: { $0.contentSize.height < $1.contentSize.height }) {
                    detailScroll = sv
                    break
                }
            }
            if let boardSV = detailScroll {
                let targetY = max(0, boardSV.contentSize.height - boardSV.bounds.height + 60)
                boardSV.setContentOffset(CGPoint(x: 0, y: targetY), animated: false)
                shotHost.view.setNeedsLayout()
                shotHost.view.layoutIfNeeded()
                RunLoop.main.run(until: Date().addingTimeInterval(0.05))
                try await Task.sleep(nanoseconds: 100_000_000)
            }
            let bounds = shotHost.view.bounds
            let origin = CGPoint(
                x: max(0, (bounds.width - shotSize.width) / 2),
                y: max(0, (bounds.height - shotSize.height) / 2)
            )
            let renderer = UIGraphicsImageRenderer(size: shotSize)
            let data = renderer.pngData { ctx in
                ctx.cgContext.translateBy(x: -origin.x, y: -origin.y)
                shotWindow.drawHierarchy(in: shotWindow.bounds, afterScreenUpdates: true)
            }
            try data.write(to: URL(fileURLWithPath: shotPath))
        }
    }
}

private final class NarrowGraphURLStub: URLProtocol {
    static var graphJSON = ""
    static var stateJSON = ""

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let url = request.url ?? URL(string: "http://127.0.0.1:7799/")!
        let body = url.path.contains("/graph") ? Self.graphJSON : Self.stateJSON
        let data = Data(body.utf8)
        let response = HTTPURLResponse(
            url: url, statusCode: 200, httpVersion: "HTTP/1.1",
            headerFields: ["Content-Type": "application/json"]
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}
#endif
