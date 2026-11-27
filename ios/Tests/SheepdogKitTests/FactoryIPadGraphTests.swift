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
}
#endif
