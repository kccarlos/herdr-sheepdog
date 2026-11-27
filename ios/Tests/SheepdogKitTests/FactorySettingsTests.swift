import Foundation
import XCTest
@testable import SheepdogKit

/// Unit tests for app-factory PR 11a (`docs/dev/design/app-factory.md` §8, D11, §10 row 11;
/// `docs/dev/design/canvas/slice14-wording.md`; `internal/web/factory.go`,
/// `internal/factory/settings.go`, `internal/factory/flow/settings.go`).
final class FactorySettingsTests: XCTestCase {
    static let sampleRowsJSON = #"""
    {"protocol":1,"rows":[
      {"name":"models.coordinator","label":"coordinator models","value":"claude/opus/medium","default":"claude/opus/medium","min":0,"max":0,"clis":["claude","codex","agy"],"top":true},
      {"name":"models.worker","label":"worker models","value":"claude/sonnet/medium","default":"claude/sonnet/medium","min":0,"max":0,"clis":["claude","codex","agy"],"models":{"agy":["gemini-3.8-flash","gemini-4-carbon","gemini-4-hydrogen","gemini-4-neon"]},"top":true},
      {"name":"models.reviewer","label":"reviewer models","value":"claude/opus/medium, claude/sonnet/medium","default":"claude/opus/medium, claude/sonnet/medium","min":0,"max":0,"clis":["claude","codex","agy"],"top":true},
      {"name":"models.retro","label":"retro models","value":"claude/opus/high","default":"claude/opus/high","min":0,"max":0,"clis":["claude","agy"],"top":true},
      {"name":"models.plan_reviewer","label":"plan reviewer models","value":"","default":"","min":0,"max":0,"clis":["claude","agy"],"top":true},
      {"name":"models.integrator","label":"integrator models","value":"codex/gpt-6.2-sol/high","default":"codex/gpt-6.2-sol/high","min":0,"max":0,"clis":["claude","codex"],"top":true},
      {"name":"max_running","label":"max running","value":"3","default":"3","min":1,"max":20,"top":true},
      {"name":"review_rounds","label":"review rounds","value":"2","default":"2","min":1,"max":5},
      {"name":"ci_stalled_minutes","label":"ci stalled minutes","value":"0","default":"90","min":10,"max":600,"problem":"ci stalled minutes 0: allowed 10–600"},
      {"name":"second_reviewer","label":"second reviewer","value":"opus","default":"opus","min":0,"max":0,"allowed":["opus","codex"],"note":"not used while reviewer models are set"},
      {"name":"design_gate","label":"design gate","value":"","default":"","min":0,"max":0,"allowed":["","owner","review"]},
      {"name":"dispatch_workflow","label":"dispatch workflow","value":"","default":"","min":0,"max":0}
    ]}
    """#

    func testSettingRowsDecode() async throws {
        StubProtocol.seen = []
        defer { StubProtocol.seen = []; StubProtocol.handler = nil }
        StubProtocol.handler = { _ in (200, Data(Self.sampleRowsJSON.utf8)) }

        let rows = try await StubProtocol.client().factorySettings(projectID: "proj-1")
        XCTAssertEqual(rows.filter(\.top).map(\.name), [
            "models.coordinator", "models.worker", "models.reviewer", "models.retro",
            "models.plan_reviewer", "models.integrator", "max_running",
        ])
        let worker = try XCTUnwrap(rows.first { $0.name == "models.worker" })
        XCTAssertEqual(worker.id, "models.worker")
        XCTAssertEqual(worker.label, "worker models")
        XCTAssertEqual(worker.defaultValue, "claude/sonnet/medium")
        XCTAssertEqual(worker.clis, ["claude", "codex", "agy"])
        XCTAssertEqual(worker.models["agy"], ["gemini-3.8-flash", "gemini-4-carbon", "gemini-4-hydrogen", "gemini-4-neon"])
        XCTAssertNil(worker.models["claude"])
        XCTAssertEqual(rows.first { $0.name == "models.retro" }?.clis, ["claude", "agy"])
        XCTAssertEqual(rows.first { $0.name == "models.plan_reviewer" }?.value, "")
        XCTAssertEqual(rows.first { $0.name == "models.plan_reviewer" }?.displayValue, "not set")
        XCTAssertEqual(rows.first { $0.name == "models.plan_reviewer" }?.metaLine, "default not set")
        let stalled = try XCTUnwrap(rows.first { $0.name == "ci_stalled_minutes" })
        XCTAssertEqual(stalled.problem, "ci stalled minutes 0: allowed 10–600")
        XCTAssertEqual(stalled.storedProblemMessage, "The stored value 0 is not allowed (allowed 10–600). Change it before the next start.")
        XCTAssertEqual(rows.first { $0.name == "second_reviewer" }?.note, "not used while reviewer models are set")
        XCTAssertEqual(rows.first { $0.name == "design_gate" }?.allowed, ["", "owner", "review"])
        XCTAssertThrowsError(try DaemonClient.decoder.decode(FactorySettingRow.self, from: Data(#"{"label":"x","value":"1","default":"1","min":0,"max":1}"#.utf8)))
    }

    func testSettingRowKindsMetaLineAndChangedState() async throws {
        StubProtocol.seen = []
        defer { StubProtocol.seen = []; StubProtocol.handler = nil }
        StubProtocol.handler = { _ in (200, Data(Self.sampleRowsJSON.utf8)) }

        let rows = try await StubProtocol.client().factorySettings(projectID: "proj-1")
        let worker = try XCTUnwrap(rows.first { $0.name == "models.worker" })
        XCTAssertTrue(worker.isModels)
        XCTAssertFalse(worker.isChoice || worker.isNumber)

        var maxRunning = try XCTUnwrap(rows.first { $0.name == "max_running" })
        XCTAssertTrue(maxRunning.isNumber)
        XCTAssertFalse(maxRunning.isModels || maxRunning.isChoice || maxRunning.isChanged)
        XCTAssertEqual(maxRunning.metaLine, "default 3 · allowed 1–20")
        XCTAssertNil(maxRunning.storedProblemMessage)
        maxRunning.value = "5"
        XCTAssertTrue(maxRunning.isChanged)

        let stalled = try XCTUnwrap(rows.first { $0.name == "ci_stalled_minutes" })
        XCTAssertFalse(stalled.isChanged, "a row with a stored problem must show not allowed rather than changed")

        var gate = try XCTUnwrap(rows.first { $0.name == "design_gate" })
        XCTAssertTrue(gate.isChoice)
        XCTAssertEqual(gate.metaLine, "default not set · allowed not set, owner, review")
        gate.problem = "design gate bad is not allowed"
        XCTAssertEqual(gate.storedProblemMessage, "design gate bad is not allowed. Change it before the next start.")

        let workflow = try XCTUnwrap(rows.first { $0.name == "dispatch_workflow" })
        XCTAssertFalse(workflow.isModels || workflow.isChoice || workflow.isNumber)
        XCTAssertEqual(workflow.metaLine, "default not set")
    }

    func testFactorySettingsGetAndProposeClientRoutes() async throws {
        StubProtocol.seen = []
        defer { StubProtocol.seen = []; StubProtocol.handler = nil }

        StubProtocol.handler = { _ in (200, Data(Self.sampleRowsJSON.utf8)) }
        _ = try await StubProtocol.client().factorySettings(projectID: "a/b%1")
        XCTAssertEqual(StubProtocol.seen.last?.url?.absoluteString.contains("/api/factory/projects/a%2Fb%251/settings"), true)

        let proposeReply = #"{"ok":true,"ask":{"id":"ask-set-1","project":"proj-1","kind":"rule","gated":true,"question":"This project: max running 4 (now 3). Save?","options":["Save","Keep current"],"recommend":"Save","set":{"name":"max_running","value":"4","was":"3"},"asked_by":"engine","at":"2026-11-26T09:00:00Z","status":"open"}}"#
        StubProtocol.handler = { _ in (200, Data(proposeReply.utf8)) }
        let ask = try await DaemonClient.$idempotencyKey.withValue("set-key-1") {
            try await StubProtocol.client().proposeFactorySetting(projectID: "proj-1", name: "max_running", value: "4")
        }
        let req = try XCTUnwrap(StubProtocol.seen.last)
        XCTAssertEqual(req.httpMethod, "POST")
        XCTAssertEqual(req.url?.path, "/api/factory/projects/proj-1/settings")
        XCTAssertEqual(req.value(forHTTPHeaderField: "Idempotency-Key"), "set-key-1")
        XCTAssertEqual(try JSONDecoder().decode([String: String].self, from: try XCTUnwrap(req.httpBody)), ["name": "max_running", "value": "4"])
        XCTAssertEqual(ask.id, "ask-set-1")
        XCTAssertEqual(ask.kind, "rule")
        XCTAssertEqual(ask.options, ["Save", "Keep current"])
        XCTAssertEqual(ask.set, FactorySettingChange(name: "max_running", value: "4", was: "3"))

        // Real refusals emitted by internal/factory/settings.go & internal/web/factory.go (Opus #5, Carbon §4.1).
        let realRefusals: [(status: Int, name: String, value: String, body: String, expectedText: String)] = [
            (400, "models.retro", "codex/gpt-6.2-sol/high",
             #"{"code":"not_launchable","error":"the engine cannot launch codex as retro yet"}"#,
             "the engine cannot launch codex as retro yet"),
            (400, "max_running", "3",
             #"{"code":"unchanged","error":"max running is already 3"}"#,
             "max running is already 3"),
            (400, "review_rounds", "4",
             #"{"code":"out_of_range","error":"review rounds 4 does not fit: max rounds is 3 (allowed 4–10). Raise max rounds first."}"#,
             "review rounds 4 does not fit: max rounds is 3 (allowed 4–10). Raise max rounds first."),
        ]
        for item in realRefusals {
            StubProtocol.handler = { _ in (item.status, Data(item.body.utf8)) }
            do {
                _ = try await StubProtocol.client().proposeFactorySetting(projectID: "proj-1", name: item.name, value: item.value)
                XCTFail("expected refusal \(item.expectedText)")
            } catch {
                let msg = FactorySettingRow.errorMessage(error, computerName: "Work Mac")
                XCTAssertEqual(msg, item.expectedText)
                XCTAssertFalse(msg.contains("The daemon refused") || msg.contains("not_launchable") || msg.contains("out_of_range") || msg.contains("unchanged"))
            }
        }

        StubProtocol.handler = { _ in (404, Data(#"{"code":"not_found","error":"No project \"proj-1\" exists."}"#.utf8)) }
        do {
            _ = try await StubProtocol.client().proposeFactorySetting(projectID: "proj-1", name: "max_running", value: "4")
            XCTFail("expected 404 project gone")
        } catch {
            XCTAssertTrue(FactoryProjectBoard.isProjectGone(error), "404 not_found on proposeFactorySetting must be recognized by isProjectGone")
        }
    }

    /// Fix-round (Opus #2, Carbon §2.3): `writeFactoryRefusal` includes `"fix"` on `not_launchable`
    /// CLI refusals (`mo.r.signin`, `mo.r.install`); `proposeFactorySetting` must not discard it.
    func testProposeSettingPreservesDaemonRefusalFix() async throws {
        StubProtocol.seen = []
        defer { StubProtocol.seen = []; StubProtocol.handler = nil }

        let cases: [(body: String, expectedWhy: String, expectedFixFragment: String)] = [
            (#"{"code":"not_launchable","error":"codex is not signed in","fix":"run: codex login"}"#,
             "codex is not signed in",
             "codex login"),
            (#"{"code":"not_launchable","error":"agy is not installed","fix":"install Antigravity CLI (agy), then run agy without arguments and sign in"}"#,
             "agy is not installed",
             "install Antigravity CLI (agy)"),
        ]

        for item in cases {
            StubProtocol.handler = { _ in (400, Data(item.body.utf8)) }
            do {
                _ = try await StubProtocol.client().proposeFactorySetting(projectID: "proj-1", name: "models.worker", value: "codex/gpt-6.2-sol/medium")
                XCTFail("expected 400 not_launchable with fix")
            } catch {
                let msg = FactorySettingRow.errorMessage(error, computerName: "Work Mac")
                XCTAssertTrue(msg.contains(item.expectedWhy), "error message should contain why \(item.expectedWhy.debugDescription), got \(msg.debugDescription)")
                XCTAssertFalse(msg.contains("The daemon refused") || msg.contains("not_launchable"), "got \(msg)")
                let reflected = "\(String(reflecting: error)) | \(msg)"
                XCTAssertTrue(
                    reflected.contains(item.expectedFixFragment),
                    "proposeFactorySetting threw \(String(reflecting: error)), dropping daemon fix \(item.expectedFixFragment.debugDescription)"
                )
            }
        }
    }

    /// Fix-round (Carbon §2.2, Opus What-can-be-deleted #1): all real `403 proof_required` wire shapes
    /// (`handleFactorySettings`, `Engine.ProposeSetting`, and `Supervisor.answerFactoryTap`) must map
    /// to the plain paired-phone recovery sentence without leaking `"proof_required"` or `"The daemon refused"`.
    func testErrorMessageFormatsAllProofRequiredDaemonShapes() async throws {
        StubProtocol.seen = []
        defer { StubProtocol.seen = []; StubProtocol.handler = nil }

        let wireBodies = [
            #"{"code":"proof_required","error":"proof_required"}"#,
            #"{"code":"proof_required","error":"Changing a setting requires a paired device or browser session."}"#,
            #"{"error":"proof_required: owner asks require a paired device session"}"#,
        ]

        for body in wireBodies {
            StubProtocol.handler = { _ in (403, Data(body.utf8)) }
            do {
                _ = try await StubProtocol.client().proposeFactorySetting(projectID: "proj-1", name: "max_running", value: "4")
                XCTFail("expected 403 proof_required for \(body)")
            } catch {
                let formatted = FactorySettingRow.errorMessage(error, computerName: "Work Mac")
                XCTAssertTrue(formatted.contains("Work Mac no longer knows this phone"), "for \(body), got: \(formatted)")
                XCTAssertTrue(formatted.contains("Pair this phone again in Settings ▸ Work Mac"), "for \(body), got: \(formatted)")
                XCTAssertFalse(formatted.contains("proof_required") || formatted.contains("The daemon refused"), "for \(body), leaked: \(formatted)")
            }
        }
    }

    func testAnswerErrorKeptNoticeAndParseFix() throws {
        let row = try DaemonClient.decoder.decode(
            FactorySettingRow.self,
            from: Data(#"{"name":"max_running","label":"max running","value":"3","default":"3","min":1,"max":20}"#.utf8)
        )
        XCTAssertEqual(row.keptNotice(was: "3"), "Kept at 3. Nothing changed.")
        XCTAssertEqual(row.keptNotice(was: "  "), "Kept at not set. Nothing changed.")
        XCTAssertEqual(row.keptNotice(was: nil), "Kept at not set. Nothing changed.")

        XCTAssertEqual(
            FactorySettingRow.answerError(
                "The daemon refused: out_of_range: max running 4 does not fit",
                computerName: "Work Mac"
            ),
            "Not saved: another setting changed after you asked. max running 4 does not fit"
        )
        let expectedPair = "Not saved: Work Mac no longer knows this phone."
            + " Pair this phone again in Settings ▸ Work Mac, then try again."
        XCTAssertEqual(
            FactorySettingRow.answerError(
                "The daemon refused: proof_required: owner asks require a paired device session",
                computerName: "Work Mac"
            ),
            expectedPair
        )
        for rejected in [
            "The daemon refused: proof_rejected: AnswerProof is unknown, expired, or does not match this ask and option.",
            "The daemon refused: proof_rejected: Owner answer proof must come from a paired device tap or live voice.",
            "The daemon refused: proof_rejected: Owner answer proof requires tap or voice input from a paired device or live voice session.",
        ] {
            let msg = FactorySettingRow.answerError(rejected, computerName: "Work Mac")
            XCTAssertEqual(msg, expectedPair)
            XCTAssertFalse(msg.contains("AnswerProof") || msg.contains("proof_rejected"))
        }
        XCTAssertEqual(
            FactorySettingRow.answerError(
                "The daemon refused: unknown_setting: Unknown setting \"foo\".",
                computerName: "Work Mac"
            ),
            "Unknown setting \"foo\"."
        )
        XCTAssertEqual(
            FactorySettingRow.answerError("Not saved: plain message", computerName: "Work Mac"),
            "Not saved: plain message"
        )

        let simple = FactorySettingRow.parseFix("run: codex login")
        XCTAssertEqual(simple.command, "codex login")
        XCTAssertEqual(simple.rest, "")

        let withRest = FactorySettingRow.parseFix("run: claude auth login, then retry")
        XCTAssertEqual(withRest.command, "claude auth login")
        XCTAssertEqual(withRest.rest, ", then retry")

        let plain = FactorySettingRow.parseFix("install Antigravity CLI (agy)")
        XCTAssertNil(plain.command)
        XCTAssertEqual(plain.rest, "install Antigravity CLI (agy)")
    }
}
