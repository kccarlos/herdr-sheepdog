import XCTest
@testable import SheepdogKit

/// Factory wire models, `DaemonClient` routes, and event refresh rules for
/// app-factory PR 7 (`docs/dev/design/app-factory.md` §3, §4, §5, D10–D12,
/// §10 row 7; `docs/dev/ws-contract.md` "Factory").
final class FactoryStateDecodeTests: XCTestCase {
    private final class BundleToken {}

    private func loadFactoryStateFixture() throws -> Data {
        let url = try XCTUnwrap(
            Bundle(for: BundleToken.self).url(forResource: "factory-state", withExtension: "json"),
            "missing fixture factory-state.json in the test bundle"
        )
        return try Data(contentsOf: url)
    }

    /// Pins ws-contract.md "Factory" & app-factory.md §4: older daemons and
    /// daemons with zero projects omit `"factory"`; an empty array also decodes.
    func testStateWithoutFactoryKeyDecodesAsNilAndEmptyArrayDecodesEmpty() throws {
        let goldenURL = ProtocolGoldenTests.root.appendingPathComponent("rest/state.json")
        let oldState = try DaemonClient.decoder.decode(DaemonState.self, from: Data(contentsOf: goldenURL))
        XCTAssertNil(oldState.factory)
        XCTAssertNil(oldState.pendingAction?.factory)
        XCTAssertNil(oldState.questions?.first?.factory)
        XCTAssertEqual(oldState.agents.map(\.alias), ["auth", "docs"])
        XCTAssertEqual(oldState.pendingAction?.id, "ap-golden-2")
        XCTAssertEqual(oldState.questions?.count, 1)
        XCTAssertEqual(oldState.voice.mode, "gemini")
        XCTAssertEqual(oldState.brain?.provider, "gemini")
        XCTAssertEqual(oldState.herdr, "mock")
        XCTAssertEqual(oldState.version, "test")
        XCTAssertNotNil(oldState.conversationStartedAt)

        let emptyJSON = #"{"agents":[],"factory":[],"voice":{"mode":"off","status":"closed","muted":false,"stalled":false,"client_connected":false}}"#
        let emptyState = try DaemonClient.decoder.decode(DaemonState.self, from: Data(emptyJSON.utf8))
        XCTAssertEqual(emptyState.factory, [])
    }

    private static let minimalAgentJSON = #"{"pane_id":"fake:1","alias":"auth","kind":"codex","workspace":"synthetic","spoken":"auth","state":"working","state_since":"2026-11-25T11:00:00Z","last_interaction":"2026-11-25T11:00:00Z"}"#
    private static let minimalVoiceJSON = #"{"mode":"off","status":"closed","muted":false,"stalled":false,"client_connected":false}"#

    /// Boundary isolation: one malformed factory head (missing required `id`)
    /// must be dropped without failing `/api/state`; good sibling heads and the
    /// rest of the state survive intact.
    func testStateDropsMalformedFactoryHeadMissingIDAndKeepsRestOfState() throws {
        let json = """
        {
          "agents": [\(Self.minimalAgentJSON)],
          "factory": [
            {"id":"proj-good-1","name":"notes","goal":"A notes page","state":"running","counts":{"done":2}},
            {"name":"broken-no-id","goal":"missing id","state":"running","counts":{}},
            {"id":"proj-good-2","name":"recipes","goal":"Recipe index","state":"stopped","counts":{"queued":1}}
          ],
          "voice": \(Self.minimalVoiceJSON),
          "version": "test"
        }
        """
        let state = try DaemonClient.decoder.decode(DaemonState.self, from: Data(json.utf8))
        XCTAssertEqual(state.factory?.map(\.id), ["proj-good-1", "proj-good-2"])
        XCTAssertEqual(state.agents.map(\.alias), ["auth"])
        XCTAssertEqual(state.version, "test")
        XCTAssertEqual(state.voice.mode, "off")
    }

    /// Boundary isolation + strict inner decoding: a head with a malformed
    /// nested field (`alert`, `counts`, non-string `state`) fails its own decode
    /// and is dropped from `/api/state` rather than kept with silently zeroed fields.
    func testStateDropsFactoryHeadWithMalformedNestedFields() throws {
        let json = """
        {
          "agents": [\(Self.minimalAgentJSON)],
          "factory": [
            {"id":"proj-good-1","name":"notes","goal":"g","state":"running","counts":{"done":2}},
            {"id":"proj-bad-alert","name":"bad-alert","goal":"g","state":"running","counts":{},"alert":{"kind":"disk"}},
            {"id":"proj-bad-counts","name":"bad-counts","goal":"g","state":"running","counts":{"done":"nine"}},
            {"id":"proj-bad-state","name":"bad-state","goal":"g","state":123,"counts":{}},
            {"id":"proj-good-2","name":"recipes","goal":"g","state":"stopped","counts":{"queued":1}}
          ],
          "voice": \(Self.minimalVoiceJSON),
          "version": "test"
        }
        """
        let state = try DaemonClient.decoder.decode(DaemonState.self, from: Data(json.utf8))
        XCTAssertEqual(state.factory?.map(\.id), ["proj-good-1", "proj-good-2"])
        XCTAssertEqual(state.agents.map(\.alias), ["auth"])
    }

    /// Boundary isolation: a non-array `"factory"` value on `/api/state` decodes
    /// as `nil` factory while leaving the rest of the state intact.
    func testStateNonArrayFactoryDecodesAsNilAndKeepsRestOfState() throws {
        let cases: [(label: String, factoryJSON: String)] = [
            ("object", #"{"id":"proj-1"}"#),
            ("string", #""not-an-array""#),
            ("number", "42"),
            ("boolean", "true"),
            ("null", "null"),
        ]
        for (label, factoryJSON) in cases {
            let json = """
            {
              "agents": [\(Self.minimalAgentJSON)],
              "factory": \(factoryJSON),
              "voice": \(Self.minimalVoiceJSON),
              "version": "test"
            }
            """
            let state = try DaemonClient.decoder.decode(DaemonState.self, from: Data(json.utf8))
            XCTAssertNil(state.factory, "non-array factory (\(label)) should decode as nil")
            XCTAssertEqual(state.agents.map(\.alias), ["auth"], "agents intact for \(label)")
            XCTAssertEqual(state.version, "test", "version intact for \(label)")
            XCTAssertEqual(state.voice.mode, "off", "voice intact for \(label)")
        }
    }

    /// Boundary isolation: a `PendingAction` with a malformed `"factory"` object
    /// (missing `project`, wrong field type, or non-object) still decodes the card
    /// and `/api/state` with `factory == nil`.
    func testStateWithMalformedPendingActionFactoryDecodesCardWithNilFactory() throws {
        let json = """
        {
          "agents": [\(Self.minimalAgentJSON)],
          "pending_action": {
            "id": "fask-missing-project",
            "pane_id": "factory:proj-1",
            "kind": "answer_prompt",
            "agent": "notes",
            "question": "Delete?",
            "factory": {"kind": "delete"}
          },
          "questions": [
            {
              "id": "fask-missing-project",
              "pane_id": "factory:proj-1",
              "kind": "answer_prompt",
              "agent": "notes",
              "question": "Delete?",
              "factory": {"kind": "delete"}
            },
            {
              "id": "fask-wrong-type",
              "pane_id": "factory:proj-1",
              "kind": "answer_prompt",
              "agent": "notes",
              "question": "Save?",
              "factory": {"project": 123, "kind": "rule"}
            },
            {
              "id": "fask-non-object",
              "pane_id": "factory:proj-1",
              "kind": "answer_prompt",
              "agent": "notes",
              "question": "Merge?",
              "factory": "bad"
            }
          ],
          "factory": [
            {"id":"proj-1","name":"notes","goal":"g","state":"running","counts":{}}
          ],
          "voice": \(Self.minimalVoiceJSON)
        }
        """
        let state = try DaemonClient.decoder.decode(DaemonState.self, from: Data(json.utf8))
        let pinned = try XCTUnwrap(state.pendingAction)
        XCTAssertEqual(pinned.id, "fask-missing-project")
        XCTAssertEqual(pinned.agent, "notes")
        XCTAssertNil(pinned.factory)

        let questions = try XCTUnwrap(state.questions)
        XCTAssertEqual(questions.map(\.id), ["fask-missing-project", "fask-wrong-type", "fask-non-object"])
        for q in questions {
            XCTAssertNil(q.factory, "malformed factory on \(q.id) should decode as nil")
        }
        XCTAssertEqual(state.factory?.map(\.id), ["proj-1"])
        XCTAssertEqual(state.agents.map(\.alias), ["auth"])
    }

    /// Strict inner decoding: malformed `alert`, `counts`, or non-string `state`
    /// on `FactoryProjectHead` must fail decoding rather than silently zeroing the field.
    func testFactoryProjectHeadRejectsMalformedNestedValues() throws {
        let badCases: [(label: String, json: String)] = [
            ("alert missing cause", #"{"id":"proj-1","name":"notes","goal":"g","state":"running","counts":{},"alert":{"kind":"disk"}}"#),
            ("alert non-object", #"{"id":"proj-1","name":"notes","goal":"g","state":"running","counts":{},"alert":"disk"}"#),
            ("counts wrong value type", #"{"id":"proj-1","name":"notes","goal":"g","state":"running","counts":{"done":"nine"}}"#),
            ("counts non-object", #"{"id":"proj-1","name":"notes","goal":"g","state":"running","counts":"bad"}"#),
            ("state non-string", #"{"id":"proj-1","name":"notes","goal":"g","state":123,"counts":{}}"#),
        ]
        for (label, json) in badCases {
            XCTAssertThrowsError(
                try DaemonClient.decoder.decode(FactoryProjectHead.self, from: Data(json.utf8)),
                "FactoryProjectHead must fail on \(label), not silently zero the field"
            )
        }

        let validNulls = #"{"id":"proj-1","name":"notes","goal":"g","state":"paused","counts":null,"alert":null}"#
        let head = try DaemonClient.decoder.decode(FactoryProjectHead.self, from: Data(validNulls.utf8))
        XCTAssertEqual(head.state, .unknown)
        XCTAssertEqual(head.counts, [:])
        XCTAssertNil(head.alert)
    }

    func testFactoryHeadAndProjectDecodeGoalUndeliveredAndRejectMalformedType() throws {
        let omittedHead = try DaemonClient.decoder.decode(
            FactoryProjectHead.self,
            from: Data(#"{"id":"proj-1","name":"notes","goal":"g","state":"running","counts":{}}"#.utf8)
        )
        XCTAssertFalse(omittedHead.goalUndelivered)

        let undeliveredHead = try DaemonClient.decoder.decode(
            FactoryProjectHead.self,
            from: Data(#"{"id":"proj-1","name":"notes","goal":"g","state":"running","counts":{},"goal_undelivered":true}"#.utf8)
        )
        XCTAssertTrue(undeliveredHead.goalUndelivered)

        XCTAssertThrowsError(
            try DaemonClient.decoder.decode(
                FactoryProjectHead.self,
                from: Data(#"{"id":"proj-1","name":"notes","goal":"g","state":"running","counts":{},"goal_undelivered":"yes"}"#.utf8)
            )
        )

        let undeliveredProject = try DaemonClient.decoder.decode(
            FactoryProject.self,
            from: Data(#"{"id":"proj-1","name":"notes","goal":"g","state":"running","running":true,"counts":{},"goal_undelivered":true}"#.utf8)
        )
        XCTAssertTrue(undeliveredProject.goalUndelivered)

        let normalProject = try DaemonClient.decoder.decode(
            FactoryProject.self,
            from: Data(#"{"id":"proj-1","name":"notes","goal":"g","state":"running","running":true,"counts":{}}"#.utf8)
        )
        XCTAssertFalse(normalProject.goalUndelivered)
    }

    /// Strict inner decoding: malformed `alert`, `counts`, or non-string `state`
    /// on `FactoryProject` must fail decoding rather than silently zeroing the field.
    func testFactoryProjectRejectsMalformedNestedValues() throws {
        let badCases: [(label: String, json: String)] = [
            ("alert wrong cause type", #"{"id":"proj-1","name":"notes","running":true,"state":"running","counts":{},"alert":{"kind":"disk","cause":42}}"#),
            ("alert missing cause", #"{"id":"proj-1","name":"notes","running":true,"state":"running","counts":{},"alert":{"kind":"disk"}}"#),
            ("counts non-object", #"{"id":"proj-1","name":"notes","running":true,"state":"running","counts":"bad"}"#),
            ("counts wrong value type", #"{"id":"proj-1","name":"notes","running":true,"state":"running","counts":{"done":"nine"}}"#),
            ("state non-string", #"{"id":"proj-1","name":"notes","running":true,"state":999,"counts":{}}"#),
        ]
        for (label, json) in badCases {
            XCTAssertThrowsError(
                try DaemonClient.decoder.decode(FactoryProject.self, from: Data(json.utf8)),
                "FactoryProject must fail on \(label), not silently zero the field"
            )
        }

        // Go's Engine.Projects() LoadState-error shape omits state/counts/goal/alert.
        let loadErrorJSON = #"{"id":"proj-bad","name":"proj-bad","running":false,"error":"unexpected EOF"}"#
        let errored = try DaemonClient.decoder.decode(FactoryProject.self, from: Data(loadErrorJSON.utf8))
        XCTAssertEqual(errored.state, .unknown)
        XCTAssertEqual(errored.counts, [:])
        XCTAssertNil(errored.alert)
        XCTAssertEqual(errored.error, "unexpected EOF")
    }

    /// Strict inner decoding: malformed `run_started_at` date or malformed
    /// `last_actor_decision` on `FactoryTask` must fail decoding, while Go's
    /// nil-slice `null` arrays still decode as empty arrays.
    func testFactoryTaskRejectsMalformedDateAndLastActorDecision() throws {
        let badCases: [(label: String, json: String)] = [
            ("bad run_started_at", #"{"ref":"T8","title":"t","stage":"working","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[],"run_role":"worker","run_cli":"codex","run_started_at":"not-a-date"}"#),
            ("bad last_actor_decision date", #"{"ref":"T8","title":"t","stage":"working","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[],"last_actor_decision":{"at":"bad-date","by":"coordinator","decision":"sent back"}}"#),
            ("last_actor_decision missing by", #"{"ref":"T8","title":"t","stage":"working","after":[],"dependents":[],"layer":0,"rounds":0,"steps":[],"last_actor_decision":{"at":"2026-11-25T11:03:00Z","decision":"sent back"}}"#),
        ]
        for (label, json) in badCases {
            XCTAssertThrowsError(
                try DaemonClient.decoder.decode(FactoryTask.self, from: Data(json.utf8)),
                "FactoryTask must fail on \(label), not silently nil the field"
            )
        }

        let nilSlicesJSON = #"{"ref":"T11","title":"sync","stage":"queued","after":null,"dependents":null,"layer":0,"rounds":0,"steps":null}"#
        let task = try DaemonClient.decoder.decode(FactoryTask.self, from: Data(nilSlicesJSON.utf8))
        XCTAssertEqual(task.after, [])
        XCTAssertEqual(task.dependents, [])
        XCTAssertEqual(task.steps, [])
        XCTAssertNil(task.runStartedAt)
        XCTAssertNil(task.lastActorDecision)
    }

    /// Strict inner decoding: malformed `at` date or malformed `set` object on
    /// `FactoryAsk` must fail decoding, while Go's nil-slice `null` options still decode.
    func testFactoryAskRejectsMalformedDateAndSettingChange() throws {
        let badCases: [(label: String, json: String)] = [
            ("bad at date", #"{"id":"ask-1","project":"proj-1","kind":"rule","gated":true,"question":"Save?","options":["Save"],"at":"not-a-date"}"#),
            ("set missing value", #"{"id":"ask-1","project":"proj-1","kind":"rule","gated":true,"question":"Save?","options":["Save"],"set":{"name":"max_running"}}"#),
            ("set non-object", #"{"id":"ask-1","project":"proj-1","kind":"rule","gated":true,"question":"Save?","options":["Save"],"set":"bad"}"#),
        ]
        for (label, json) in badCases {
            XCTAssertThrowsError(
                try DaemonClient.decoder.decode(FactoryAsk.self, from: Data(json.utf8)),
                "FactoryAsk must fail on \(label), not silently nil the field"
            )
        }

        let nilOptionsJSON = #"{"id":"ask-2","project":"proj-1","kind":"rule","gated":true,"question":"Save?","options":null}"#
        let ask = try DaemonClient.decoder.decode(FactoryAsk.self, from: Data(nilOptionsJSON.utf8))
        XCTAssertEqual(ask.options, [])
        XCTAssertNil(ask.set)
        XCTAssertNil(ask.at)
    }

    /// Pins app-factory.md §3.2, §4 (`factory` head keys incl. optional
    /// `folder`, `coordinator_pane`, `alert` `{kind, cause, fix?}`) &
    /// ws-contract.md "Factory" against Go's `factory.ProjectHead`.
    func testStateWithFactoryHeadsDecodesPinnedAndOptionalKeys() throws {
        let state = try DaemonClient.decoder.decode(DaemonState.self, from: try loadFactoryStateFixture())
        let heads = try XCTUnwrap(state.factory)
        XCTAssertEqual(heads.count, 3)

        let notes = heads[0]
        XCTAssertEqual(notes.id, "proj-1a2b3c4d")
        XCTAssertEqual(notes.name, "notes")
        XCTAssertEqual(notes.goal, "A notes page with search")
        XCTAssertEqual(notes.state, .running)
        XCTAssertEqual(notes.counts, ["done": 9, "owner_answer": 1, "waiting_on_dep": 1, "working": 3])
        XCTAssertEqual(notes.folder, "/work/notes")
        XCTAssertEqual(notes.coordinatorPane, "coord-notes-1")
        XCTAssertEqual(
            notes.alert,
            FactoryProjectAlert(kind: "agents_down", cause: "claude coordinator is signed out", fix: "sign in to claude at this Mac")
        )

        let recipes = heads[1]
        XCTAssertEqual(recipes.id, "proj-2b3c4d5e")
        XCTAssertEqual(recipes.name, "recipes")
        XCTAssertEqual(recipes.state, .stopped)
        XCTAssertNil(recipes.coordinatorPane)
        XCTAssertEqual(recipes.alert, FactoryProjectAlert(kind: "ci_stalled", cause: "CI pending", fix: nil))

        let finished = heads[2]
        XCTAssertEqual(finished.id, "proj-3c4d5e6f")
        XCTAssertEqual(finished.name, "proj-3c4d5e6f")
        XCTAssertEqual(finished.state, .finished)
        XCTAssertNil(finished.folder)
        XCTAssertNil(finished.coordinatorPane)
        XCTAssertNil(finished.alert)
    }

    /// Pins lenient decoding (mirroring `Agent.State`): an unknown project state
    /// must not break `/api/state`.
    func testUnknownProjectStateDecodesLeniently() throws {
        let json = #"{"agents":[],"factory":[{"id":"proj-9","name":"beta","goal":"g","state":"paused","counts":{}}],"voice":{"mode":"off","status":"closed","muted":false,"stalled":false,"client_connected":false}}"#
        let state = try DaemonClient.decoder.decode(DaemonState.self, from: Data(json.utf8))
        XCTAssertEqual(state.factory?.first?.state, .unknown)
    }

    /// Pins app-factory.md §4 ("The kind and task on a factory ask card") &
    /// ws-contract.md "Factory": `fask-` cards carry `factory {project, task?, kind}`
    /// and the project name in `agent`; ordinary cards omit `factory`.
    func testPendingActionDecodesFactoryMetaForTaskRuleAndOrdinaryCards() throws {
        let state = try DaemonClient.decoder.decode(DaemonState.self, from: try loadFactoryStateFixture())
        let pinned = try XCTUnwrap(state.pendingAction)
        XCTAssertEqual(pinned.id, "fask-ask-delete-1")
        XCTAssertEqual(pinned.paneID, "factory:proj-1a2b3c4d")
        XCTAssertEqual(pinned.agent, "notes")
        XCTAssertEqual(pinned.factory?.project, "proj-1a2b3c4d")
        XCTAssertEqual(pinned.factory?.task, "T10")
        XCTAssertEqual(pinned.factory?.kind, "delete")

        let questions = try XCTUnwrap(state.questions)
        let rule = try XCTUnwrap(questions.first { $0.id == "fask-ask-rule-1" })
        XCTAssertEqual(rule.factory?.project, "proj-1a2b3c4d")
        XCTAssertNil(rule.factory?.task)
        XCTAssertEqual(rule.factory?.kind, "rule")

        XCTAssertNil(try XCTUnwrap(questions.first { $0.id == "ap-ordinary-1" }).factory)

        let event = DaemonEvent(
            seq: 42, source: "supervisor", type: "pending.created", paneID: "factory:proj-1a2b3c4d",
            payload: [
                "id": .string("fask-ask-delete-1"), "kind": .string("answer_prompt"), "agent": .string("notes"),
                "question": .string("Delete?"),
                "factory": .object(["project": .string("proj-1a2b3c4d"), "task": .string("T10"), "kind": .string("delete")]),
            ]
        )
        let fromEvent = try XCTUnwrap(PendingAction(event: event))
        XCTAssertEqual(fromEvent.factory?.project, "proj-1a2b3c4d")
        XCTAssertEqual(fromEvent.factory?.task, "T10")
        XCTAssertEqual(fromEvent.factory?.kind, "delete")
    }
}

final class FactoryGraphDecodeTests: XCTestCase {
    /// Matches Go's `GraphView`/`TaskView`/`StepView`/`LogEntry` in
    /// `internal/factory/view.go` and `TestTaskViewRunMetadataAndLastActorDecision`.
    static let sampleGraphJSON = #"""
    {"protocol":1,"project":"proj-1a2b3c4d","folder":"/work/notes","tasks":[
      {"run_role":"worker","run_cli":"codex","run_started_at":"2026-11-25T11:01:00Z",
       "last_actor_decision":{"at":"2026-11-25T11:03:00Z","by":"coordinator","decision":"sent back","why":"the tag index must be case-insensitive"},
       "ref":"T8","title":"feat: tags","brief":"Add case-insensitive tag filtering.","stage":"working","why_waiting":"Worker is working",
       "after":["T6"],"dependents":["T11"],"layer":1,"rounds":1,"model":"gpt-5.4","terminal":"w-8","last_rule":"N29","last_decision":"started reviewer",
       "steps":[{"name":"planned","state":"done"},{"name":"implementing","state":"now","text":"sent back from review round 1; 1 round done"},{"name":"review","state":"todo","text":"needs 2 passing, at most 4"}]},
      {"run_role":"reviewer","run_cli":"claude","run_started_at":"2026-11-25T11:02:00Z",
       "last_actor_decision":{"at":"2026-11-25T11:05:00Z","by":"owner","decision":"answered ask-1"},
       "ref":"T7","title":"feat: search","stage":"working","why_waiting":"In review round 2","after":[],"dependents":[],"layer":0,"rounds":1,"model":"sonnet","terminal":"rev-1","steps":[]},
      {"ref":"T11","title":"feat: sync","stage":"waiting_on_dep","why_waiting":"Waiting on T8","after":["T8"],"dependents":[],"layer":2,"rounds":0,"steps":[]}
    ]}
    """#

    /// Pins app-factory.md §4 ("The new `TaskView` keys") & §5.3 & ws-contract.md
    /// "Factory": `run_role`, `run_cli`, `run_started_at`, `last_actor_decision`,
    /// alongside existing `model`, `terminal`, `last_rule`, `last_decision`, `steps`.
    func testGraphDecodesTaskViewRunMetadataAndLastActorDecision() throws {
        let graph = try DaemonClient.decoder.decode(FactoryGraphView.self, from: Data(Self.sampleGraphJSON.utf8))
        XCTAssertEqual(graph.protocol, 1)
        XCTAssertEqual(graph.project, "proj-1a2b3c4d")
        XCTAssertEqual(graph.folder, "/work/notes")
        XCTAssertEqual(graph.tasks.count, 3)

        let t8 = graph.tasks[0]
        XCTAssertEqual(t8.ref, "T8")
        XCTAssertEqual(t8.title, "feat: tags")
        XCTAssertEqual(t8.stage, "working")
        XCTAssertEqual(t8.model, "gpt-5.4")
        XCTAssertEqual(t8.terminal, "w-8")
        XCTAssertEqual(t8.lastRule, "N29")
        XCTAssertEqual(t8.lastDecision, "started reviewer")
        XCTAssertEqual(t8.runRole, "worker")
        XCTAssertEqual(t8.runCLI, "codex")
        XCTAssertEqual(t8.runStartedAt, RFC3339Date.date(from: "2026-11-25T11:01:00Z"))
        XCTAssertEqual(t8.lastActorDecision?.by, "coordinator")
        XCTAssertEqual(t8.lastActorDecision?.decision, "sent back")
        XCTAssertEqual(t8.lastActorDecision?.why, "the tag index must be case-insensitive")
        XCTAssertEqual(t8.lastActorDecision?.at, RFC3339Date.date(from: "2026-11-25T11:03:00Z"))
        XCTAssertEqual(t8.steps.count, 3)
        XCTAssertEqual(t8.steps[1].text, "sent back from review round 1; 1 round done")

        let t7 = graph.tasks[1]
        XCTAssertEqual(t7.runRole, "reviewer")
        XCTAssertEqual(t7.runCLI, "claude")
        XCTAssertEqual(t7.lastActorDecision?.by, "owner")
        XCTAssertNil(t7.lastActorDecision?.why)

        let t11 = graph.tasks[2]
        XCTAssertNil(t11.runRole)
        XCTAssertNil(t11.runCLI)
        XCTAssertNil(t11.runStartedAt)
        XCTAssertNil(t11.lastActorDecision)
        XCTAssertEqual(t11.after, ["T8"])
    }
}

final class FactoryClientRouteTests: XCTestCase {
    override func setUp() {
        StubProtocol.seen = []
        StubProtocol.handler = nil
    }

    override func tearDown() {
        StubProtocol.seen = []
        StubProtocol.handler = nil
    }

    /// Pins app-factory.md §4 (`GET /api/factory/projects`) & ws-contract.md.
    func testFactoryProjectsRouteFetchesAndDecodesProjects() async throws {
        let json = #"""
        {"protocol":1,"projects":[
          {"id":"proj-1a2b3c4d","name":"notes","folder":"/work/notes","repo_url":"https://github.com/example/notes",
           "goal":"A notes page with search","state":"running","running":true,"live":true,"status":"Plan review",
           "counts":{"done":9,"working":3},"coordinator_pane":"coord-notes-1",
           "alert":{"kind":"disk","cause":"volume full","fix":"free space"}}
        ]}
        """#
        StubProtocol.handler = { _ in (200, Data(json.utf8)) }
        let projects = try await StubProtocol.client().factoryProjects()
        let request = try XCTUnwrap(StubProtocol.seen.first)
        XCTAssertEqual(request.httpMethod, "GET")
        XCTAssertEqual(request.url?.path, "/api/factory/projects")
        XCTAssertEqual(projects.count, 1)
        XCTAssertEqual(projects[0].id, "proj-1a2b3c4d")
        XCTAssertEqual(projects[0].name, "notes")
        XCTAssertEqual(projects[0].state, .running)
        XCTAssertTrue(projects[0].running)
        XCTAssertEqual(projects[0].live, true)
        XCTAssertEqual(projects[0].status, "Plan review")
        XCTAssertEqual(projects[0].coordinatorPane, "coord-notes-1")
        XCTAssertEqual(projects[0].alert?.kind, "disk")
    }

    /// Pins app-factory.md §4 (`GET /api/factory/projects/{id}/graph`) &
    /// path-segment escaping for project ids.
    func testFactoryGraphRouteEscapesProjectIDAndDecodesGraph() async throws {
        StubProtocol.handler = { _ in (200, Data(FactoryGraphDecodeTests.sampleGraphJSON.utf8)) }
        let client = StubProtocol.client()
        let graph = try await client.factoryGraph(projectID: "proj-1a2b3c4d")
        XCTAssertEqual(StubProtocol.seen.first?.httpMethod, "GET")
        XCTAssertEqual(StubProtocol.seen.first?.url?.path, "/api/factory/projects/proj-1a2b3c4d/graph")
        XCTAssertEqual(graph.tasks.map(\.ref), ["T8", "T7", "T11"])

        _ = try await client.factoryGraph(projectID: "a/b%1")
        XCTAssertEqual(StubProtocol.seen.last?.url?.absoluteString.contains("/api/factory/projects/a%2Fb%251/graph"), true)
    }

    /// Pins ws-contract.md "Factory" (`GET /api/factory/asks`) against Go's
    /// `factoryAskView` (`flow.Ask` + `command`).
    func testFactoryAsksRouteFetchesAndDecodesOpenAsks() async throws {
        let json = #"""
        {"protocol":1,"asks":[
          {"id":"ask-1","project":"proj-1a2b3c4d","folder":"/work/notes","kind":"override","gated":true,
           "question":"Merge T8 now?","options":["Yes, merge now","No, wait"],"recommend":"Yes, merge now",
           "task":"T8","at":"2026-11-25T11:05:00Z","status":"open","command":"PR #42 · checks green"},
          {"id":"ask-2","project":"proj-1a2b3c4d","kind":"rule","gated":true,
           "question":"This project: max running 3 (now 2). Save?","options":["Save","Keep current"],"recommend":"Save",
           "set":{"name":"max_running","value":"3","was":"2"},"at":"2026-11-25T11:06:00Z","status":"open"}
        ]}
        """#
        StubProtocol.handler = { _ in (200, Data(json.utf8)) }
        let asks = try await StubProtocol.client().factoryAsks()
        XCTAssertEqual(StubProtocol.seen.first?.httpMethod, "GET")
        XCTAssertEqual(StubProtocol.seen.first?.url?.path, "/api/factory/asks")
        XCTAssertEqual(asks.count, 2)
        XCTAssertEqual(asks[0].id, "ask-1")
        XCTAssertEqual(asks[0].kind, "override")
        XCTAssertEqual(asks[0].task, "T8")
        XCTAssertEqual(asks[0].command, "PR #42 · checks green")
        XCTAssertEqual(asks[1].set, FactorySettingChange(name: "max_running", value: "3", was: "2"))
    }

    /// Pins internal/web/factory.go & app-factory.md §8: 503 unavailable,
    /// 404 project not found, and 401 unauthorized surface cleanly as `DaemonError`.
    func testFactoryRoutesMapUnavailableNotFoundAndUnauthorized() async {
        let cases: [(Int, String, DaemonError)] = [
            (503, #"{"error":"factory is not available"}"#, .rejected(status: 503, message: "factory is not available")),
            (404, #"{"error":"factory project not found"}"#, .rejected(status: 404, message: "factory project not found")),
            (401, #"{"error":"unauthorized"}"#, .unauthorized(endpoint: "127.0.0.1:7799")),
        ]
        for (status, body, expected) in cases {
            StubProtocol.handler = { _ in (status, Data(body.utf8)) }
            do {
                _ = try await StubProtocol.client().factoryGraph(projectID: "proj-missing")
                XCTFail("expected \(expected) for HTTP \(status)")
            } catch {
                XCTAssertEqual(error as? DaemonError, expected, "HTTP \(status)")
            }
        }
    }

    /// Pins app-factory.md §8 & internal/web/factory.go (`POST /api/factory/projects/start`):
    /// `resumeFactory` posts `{folder, goal}`, propagates `ActionRunner`'s
    /// `Idempotency-Key`, and maps `403 proof_required` and `400` refusals cleanly.
    func testFactoryResumeRoutePostsFolderAndGoalHonoursIdempotencyKeyAndMapsRefusals() async throws {
        StubProtocol.handler = { _ in (200, Data(#"{"ok":true,"device_id":"dev-1"}"#.utf8)) }
        try await DaemonClient.$idempotencyKey.withValue("resume-key-1") {
            try await StubProtocol.client().resumeFactory(folder: "/work/notes", goal: "A notes page with search")
        }
        let request = try XCTUnwrap(StubProtocol.seen.first)
        XCTAssertEqual(request.httpMethod, "POST")
        XCTAssertEqual(request.url?.path, "/api/factory/projects/start")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Idempotency-Key"), "resume-key-1")
        let payload = try JSONDecoder().decode([String: String].self, from: try XCTUnwrap(request.httpBody))
        XCTAssertEqual(payload, ["folder": "/work/notes", "goal": "A notes page with search"])

        let refusals: [(Int, String, DaemonError)] = [
            (403, #"{"code":"proof_required","error":"proof_required"}"#, .rejected(status: 403, message: "proof_required")),
            (400, #"{"error":"Not started: a setting is not allowed. Change it in settings below."}"#,
             .rejected(status: 400, message: "Not started: a setting is not allowed. Change it in settings below.")),
        ]
        for (status, body, expected) in refusals {
            StubProtocol.handler = { _ in (status, Data(body.utf8)) }
            do {
                try await StubProtocol.client().resumeFactory(folder: "/work/notes", goal: "A notes page with search")
                XCTFail("expected \(expected) for HTTP \(status)")
            } catch {
                XCTAssertEqual(error as? DaemonError, expected, "HTTP \(status)")
            }
        }
    }
}

final class FactoryEventRefreshTests: XCTestCase {
    /// Pins app-factory.md §4 & §9 and ws-contract.md "Factory":
    /// `factory.changed` `{project}` triggers `/api/state` refetch via
    /// `AttentionEvents.refreshesState`, and other `factory.*` types do not.
    func testFactoryChangedEventRefreshesState() throws {
        XCTAssertTrue(AttentionEvents.refreshesState("factory.changed"))
        XCTAssertFalse(AttentionEvents.refreshesState("factory.other"))

        var parser = SSEParser()
        _ = parser.push(line: "id: 1795550000000001")
        _ = parser.push(line: "event: log")
        _ = parser.push(line: #"data: {"id":1795550000000001,"ts":"2026-11-25T11:06:00Z","source":"factory","type":"factory.changed","payload":{"project":"proj-1a2b3c4d"}}"#)
        guard case let .event(event)? = parser.push(line: "") else {
            return XCTFail("expected parsed factory.changed SSE event")
        }
        XCTAssertEqual(event.type, "factory.changed")
        XCTAssertEqual(event.string("project"), "proj-1a2b3c4d")
        XCTAssertTrue(AttentionEvents.refreshesState(event.type))
    }
}
