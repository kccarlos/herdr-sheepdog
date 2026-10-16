import XCTest
@testable import SheepdogKit

/// The Health pane's model: the problem lines in plain words from the
/// daemon's payloads (as `internal/orchestrator`, `internal/voice`,
/// `internal/supervisor` and `internal/web` log them), uptime, GPT-Live's
/// billed seconds, and the orchestrator-model and settings calls.
final class DiagnosticsTests: XCTestCase {
    private func event(_ id: UInt64, _ type: String, _ payload: [String: JSONValue]) -> DaemonEvent {
        DaemonEvent(seq: id, ts: Date(timeIntervalSince1970: TimeInterval(id)), source: "x", type: type, payload: payload)
    }

    private func line(_ type: String, _ payload: [String: JSONValue]) -> DaemonHealth.Problem? {
        var health = DaemonHealth()
        health.fold(event(1, type, payload))
        return health.problems.first
    }

    func testEveryProblemTypeReadsAsOneLine() {
        var p = line("brain.fallback", ["reason": .string("timeout"), "kind": .string("utterance"), "trigger": .string("ceo"), "turn": .number(4), "acted": .bool(false), "error": .string("context deadline exceeded\nmore")])
        XCTAssertEqual(p?.title, "Orchestrator fell back: timeout")
        XCTAssertEqual(p?.detail, "context deadline exceeded")
        XCTAssertEqual(p?.severity, .error)

        p = line("brain.rate_limited", ["model": .string("gemini-3.5-flash"), "key_index": .number(1), "keys": .number(4), "retry_after_ms": .number(31_000)])
        XCTAssertEqual(p?.title, "Orchestrator rate-limited (gemini-3.5-flash)")
        XCTAssertEqual(p?.detail, "key 2 of 4 · retry in 31.0 s")

        p = line("voice.connect_error", ["key_index": .number(0), "resumed": .bool(false), "dur_ms": .number(812), "error": .string("websocket: bad handshake")])
        XCTAssertEqual(p?.title, "Voice could not connect")
        XCTAssertEqual(p?.detail, "websocket: bad handshake")

        p = line("voice.push_held", ["text": .string("billing finished."), "urgency": .string("idle"), "reason": .string("no_listener")])
        XCTAssertEqual(p?.title, "Push held — nobody listening")
        XCTAssertEqual(p?.detail, "billing finished.")
        XCTAssertEqual(p?.severity, .info)

        p = line("voice.push_dropped", ["text": .string("billing finished."), "age_ms": .number(300_000)])
        XCTAssertEqual(p?.title, "Push dropped — nobody listened for 5 min")

        p = line("supervisor.echo_dropped", ["heard": .string("No change since I last checked"), "said": .string("No change since I last checked.")])
        XCTAssertEqual(p?.title, "Echo dropped — the voice heard itself")
        XCTAssertEqual(p?.detail, "heard “No change since I last checked”")

        p = line("web.audio_unheard", ["bytes": .number(96_000), "provider": .string("gemini")])
        XCTAssertEqual(p?.title, "Audio played to nobody (2.0 s)")
        XCTAssertEqual(p?.detail, "gemini, no client attached")

        p = line("voice.playback", ["state": .string("flushed"), "played_ms": .number(1_850), "run_ms": .number(4_200), "cut": .bool(true), "heard": .string("auth is waiting on")])
        XCTAssertEqual(p?.title, "Reply cut off after 1.9 s of 4.2 s")
        XCTAssertEqual(p?.detail, "heard “auth is waiting on”")

        p = line("worker.session_missing", ["agent": .string("auth"), "reason": .string("no transcript path")])
        XCTAssertEqual(p?.title, "Worker transcript not found for auth")
        XCTAssertEqual(p?.detail, "no transcript path")
        XCTAssertTrue(p?.raw.contains(#""reason" : "no transcript path""#) ?? false, "the raw JSON is kept")
    }

    func testOnlyACutPlaybackAndTheListedTypesCount() {
        XCTAssertNil(line("voice.playback", ["state": .string("drained"), "played_ms": .number(4_200), "run_ms": .number(4_200)]))
        XCTAssertNil(line("voice.push", ["text": .string("x")]))
        XCTAssertNil(line("brain.turn", [:]))
    }

    func testFoldingIsIdempotentOrderedAndCapped() {
        var health = DaemonHealth()
        XCTAssertTrue(health.fold(event(20, "voice.push_dropped", ["text": .string("b")])))
        XCTAssertTrue(health.fold(event(10, "voice.push_held", ["text": .string("a")])))
        XCTAssertFalse(health.fold(event(20, "voice.push_dropped", ["text": .string("b")])), "a replay changes nothing")
        XCTAssertEqual(health.problems.map(\.id), [10, 20], "oldest first")
        XCTAssertEqual(health.problems(since: Date(timeIntervalSince1970: 15)).map(\.id), [20], "newest first, since a date")
        for i in 100 ..< 300 { health.fold(event(UInt64(i), "voice.push_held", ["text": .string("x")])) }
        XCTAssertEqual(health.problems.count, DaemonHealth.capacity)
        XCTAssertEqual(health.problems.last?.id, 299)
    }

    func testUptimeAndBilledSeconds() {
        var health = DaemonHealth()
        XCTAssertNil(health.uptime(at: Date()))
        health.fold(event(1_000, "daemon.start", ["version": .string("dev")]))
        XCTAssertEqual(health.uptime(at: Date(timeIntervalSince1970: 1_000 + 2 * 3600 + 5 * 60)), "2 h 5 min")
        XCTAssertFalse(health.fold(event(900, "daemon.start", [:])), "an older start is not the current run")

        health.fold(event(1_010, "web.client.stats", ["voice_billed_seconds": .number(42.5), "voice_session_open": .bool(true)]))
        XCTAssertEqual(health.billedSeconds, 42.5)
        health.fold(event(1_020, "voice.billed_seconds", ["total_s": .number(61), "provider": .string("openai")]))
        XCTAssertEqual(health.billedSeconds, 61)
        health.fold(event(1_030, "web.client.stats", ["buffer_ms": .number(0)]))
        XCTAssertEqual(health.billedSeconds, 61, "a stats line without the field keeps it")
        health.fold(event(2_000, "daemon.start", [:]))
        XCTAssertNil(health.billedSeconds, "a restarted daemon bills from zero")
    }

    // MARK: - Orchestrator models and settings

    /// Captured from the fixture daemon (`--brain=fake`) at `0be70e6`.
    private let brainModelsJSON = """
    {"provider":"fake","providers":[{"id":"fake","label":"Fake","available":true},{"id":"deepseek","label":"DeepSeek","available":false,"reason":"no DEEPSEEK_API_KEY"}],
     "model":"fake-reflex","models_provider":"fake",
     "models":[{"name":"gemini-3.8-flash","display_name":"Gemini 3.8 Flash","input_token_limit":1048576,"methods":["generateContent"],"recommended":true},
               {"name":"gemma-4","display_name":"Gemma 4"}],
     "stale":false}
    """

    func testBrainModelsDecodeAndOfferTheCurrentModelEvenWhenUnlisted() throws {
        let models = try DaemonClient.decoder.decode(BrainModels.self, from: Data(brainModelsJSON.utf8))
        XCTAssertEqual(models.provider, "fake")
        XCTAssertEqual(models.providers[1].reason, "no DEEPSEEK_API_KEY")
        XCTAssertFalse(models.providers[1].available)
        XCTAssertEqual(models.choices.map(\.name), ["fake-reflex", "gemini-3.8-flash", "gemma-4"])
    }

    func testSwitchingProviderPicksFromTheNewCatalogNeverTheOldModel() throws {
        // GET /api/brain/models?provider=deepseek while the orchestrator runs on
        // Gemini: provider/model are still the current ones, models are
        // DeepSeek's, and none is "recommended" (a gemini/gemma notion).
        let json = #"{"provider":"gemini","providers":[],"model":"gemini-3.5-flash","models_provider":"deepseek","models":[{"name":"deepseek-chat"},{"name":"deepseek-reasoner"}],"stale":false}"#
        let catalog = try DaemonClient.decoder.decode(BrainModels.self, from: Data(json.utf8))
        XCTAssertEqual(catalog.choices.first?.name, "gemini-3.5-flash", "the picker still offers the current model")
        XCTAssertEqual(catalog.defaultModelForSwitch, "deepseek-chat", "but a switch takes the new provider's model")

        let empty = try DaemonClient.decoder.decode(BrainModels.self, from: Data(#"{"provider":"gemini","providers":[],"model":"gemini-3.5-flash","models_provider":"deepseek","models":[]}"#.utf8))
        XCTAssertNil(empty.defaultModelForSwitch, "no catalog: leave the orchestrator alone")
        let failed = try DaemonClient.decoder.decode(BrainModels.self, from: Data(#"{"provider":"gemini","providers":[],"model":"x","models_provider":"deepseek","models":[{"name":"deepseek-chat"}],"error":"401 from DeepSeek"}"#.utf8))
        XCTAssertNil(failed.defaultModelForSwitch, "a list that could not be fetched is not trusted")
        let gemini = try DaemonClient.decoder.decode(BrainModels.self, from: Data(brainModelsJSON.utf8))
        XCTAssertEqual(gemini.defaultModelForSwitch, "gemini-3.8-flash", "recommended first")
    }

    func testTheBrainAndSettingsCalls() async throws {
        StubProtocol.handler = { request in
            switch (request.httpMethod, request.url?.path) {
            case ("GET", "/api/brain/models"):
                XCTAssertEqual(request.url?.query, "provider=deepseek")
                return (400, Data(#"{"error":"provider \"deepseek\": no DEEPSEEK_API_KEY"}"#.utf8))
            case ("PUT", "/api/brain/model"):
                let body = try? JSONSerialization.jsonObject(with: request.httpBody ?? Data()) as? [String: String]
                XCTAssertEqual(body, ["provider": "fake", "model": "gemini-3.8-flash"])
                return (200, Data(self.brainModelsJSON.utf8))
            case ("GET", "/api/settings"):
                return (200, Data(#"{"chattiness":"normal","levels":["quiet","normal","chatty"]}"#.utf8))
            case ("PUT", "/api/settings"):
                let body = try? JSONSerialization.jsonObject(with: request.httpBody ?? Data()) as? [String: String]
                XCTAssertEqual(body, ["chattiness": "quiet"])
                return (200, Data(#"{"chattiness":"quiet","levels":["quiet","normal","chatty"]}"#.utf8))
            default:
                return (404, Data())
            }
        }
        let client = StubProtocol.client()
        do {
            _ = try await client.brainModels(provider: "deepseek")
            XCTFail("expected the daemon's refusal")
        } catch let error as DaemonError {
            XCTAssertEqual(error, .rejected(status: 400, message: #"provider "deepseek": no DEEPSEEK_API_KEY"#))
        }
        let switched = try await client.setBrainModel(provider: "fake", model: "gemini-3.8-flash")
        XCTAssertEqual(switched.provider, "fake")
        let settings = try await client.settings()
        XCTAssertEqual(settings, DaemonSettings(chattiness: "normal", levels: ["quiet", "normal", "chatty"]))
        let quiet = try await client.setChattiness("quiet")
        XCTAssertEqual(quiet.chattiness, "quiet")

        StubProtocol.handler = { _ in (404, Data()) }
        let none = try await client.brainModels()
        XCTAssertNil(none, "an older daemon without the route hides the picker")
    }
}
