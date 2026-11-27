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
