import Foundation
import XCTest
@testable import SheepdogKit

/// Failing unit tests for app-factory PR 11 (`docs/dev/design/app-factory.md` §8, D11, §10 row 11;
/// `internal/web/factory.go`, `internal/factory/settings.go`, `internal/factory/flow/settings.go`).
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

    func testSettingRowsDecodeAndModelCandidateParseFormat() async throws {
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
        XCTAssertEqual(rows.first { $0.name == "ci_stalled_minutes" }?.problem, "ci stalled minutes 0: allowed 10–600")
        XCTAssertEqual(rows.first { $0.name == "second_reviewer" }?.note, "not used while reviewer models are set")
        XCTAssertEqual(rows.first { $0.name == "design_gate" }?.allowed, ["", "owner", "review"])
        XCTAssertThrowsError(try DaemonClient.decoder.decode(FactorySettingRow.self, from: Data(#"{"label":"x","value":"1","default":"1","min":0,"max":1}"#.utf8)))

        let parsed = FactoryModelCandidate.parse("agy/gemini-4-neon/high, claude/sonnet/medium")
        XCTAssertEqual(parsed, [FactoryModelCandidate(cli: "agy", model: "gemini-4-neon", effort: "high"), FactoryModelCandidate(cli: "claude", model: "sonnet", effort: "medium")])
        XCTAssertEqual(FactoryModelCandidate.format(parsed), "agy/gemini-4-neon/high, claude/sonnet/medium")
        XCTAssertEqual(FactoryModelCandidate.parse("   "), [])
    }

    func testFactorySettingsGetAndProposeClientRoutes() async throws {
        StubProtocol.seen = []
        defer { StubProtocol.seen = []; StubProtocol.handler = nil }

        StubProtocol.handler = { _ in (200, Data(Self.sampleRowsJSON.utf8)) }
        _ = try await StubProtocol.client().factorySettings()
        XCTAssertEqual(StubProtocol.seen.last?.url?.path, "/api/factory/settings")
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

        let refusals: [(Int, String, DaemonError)] = [
            (400, #"{"code":"not_launchable","error":"the reviewer must differ from the worker: the worker models are claude/sonnet/medium"}"#,
             .rejected(status: 400, message: "the reviewer must differ from the worker: the worker models are claude/sonnet/medium")),
            (403, #"{"code":"proof_required","error":"proof_required"}"#, .rejected(status: 403, message: "proof_required")),
            (404, #"{"code":"not_found","error":"No project \"proj-1\" exists."}"#, .rejected(status: 404, message: "No project \"proj-1\" exists.")),
        ]
        for (status, body, expected) in refusals {
            StubProtocol.handler = { _ in (status, Data(body.utf8)) }
            do { _ = try await StubProtocol.client().proposeFactorySetting(projectID: "proj-1", name: "models.reviewer", value: "claude/sonnet/medium"); XCTFail("expected \(expected)") }
            catch { XCTAssertEqual(error as? DaemonError, expected) }
        }
    }
}
