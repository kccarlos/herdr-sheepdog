import Foundation

/// The factory's wire shapes and `DaemonClient` routes: the heads in
/// `/api/state`'s `factory`, the `factory` object on a `fask-` question,
/// and the read-only views the project page reads. Go's `internal/factory`
/// (`ProjectHead`, `ProjectView`, `GraphView`) and `internal/web/factory.go`
/// are the source of truth (docs/dev/ws-contract.md "Factory").
/// At the state boundary, malformed factory heads and card metadata are dropped.

/// A project's state word. Anything else reads as `.unknown`, as `Agent.State`.
public enum FactoryProjectState: String, Codable, Equatable, Sendable {
    case running, stopped, finished, unknown

    public init(from decoder: Decoder) throws {
        let raw = try decoder.singleValueContainer().decode(String.self)
        self = FactoryProjectState(rawValue: raw) ?? .unknown
    }
}

/// The open alert of a project: shown as the daemon wrote it.
public struct FactoryProjectAlert: Codable, Equatable, Sendable {
    public var kind: String
    public var cause: String
    public var fix: String?

    public init(kind: String, cause: String, fix: String? = nil) {
        self.kind = kind
        self.cause = cause
        self.fix = fix
    }
}

/// One entry of `/api/state`'s `factory` (Go `ProjectHead`).
public struct FactoryProjectHead: Codable, Equatable, Identifiable, Sendable {
    public var id: String
    public var name: String
    public var goal: String
    public var state: FactoryProjectState
    @FactoryDefaultEmpty public var counts: [String: Int]
    public var folder: String?
    /// Left out by the daemon when no coordinator runs.
    public var coordinatorPane: String?
    public var alert: FactoryProjectAlert?

    enum CodingKeys: String, CodingKey {
        case id, name, goal, state, counts, folder, alert
        case coordinatorPane = "coordinator_pane"
    }
}

/// The `factory` object on a `fask-` question card: its project (the id),
/// the task when the ask is about one, and the ask's own kind word.
public struct FactoryAskMeta: Codable, Equatable, Sendable {
    public var project: String
    public var task: String?
    public var kind: String

    public init(project: String, task: String? = nil, kind: String) {
        self.project = project
        self.task = task
        self.kind = kind
    }
}

/// An entry of `GET /api/factory/projects` (Go `ProjectView`).
public struct FactoryProject: Decodable, Equatable, Identifiable, Sendable {
    public var id: String
    public var name: String
    public var folder: String?
    public var repoURL: String?
    public var goal: String
    public var state: FactoryProjectState
    public var running: Bool
    public var live: Bool?
    public var status: String?
    public var counts: [String: Int]
    public var coordinatorPane: String?
    public var alert: FactoryProjectAlert?
    /// Set instead of the rest when the project's state file did not load.
    public var error: String?

    enum CodingKeys: String, CodingKey {
        case id, name, folder, goal, state, running, live, status, counts, alert, error
        case repoURL = "repo_url"
        case coordinatorPane = "coordinator_pane"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? id
        folder = try c.decodeIfPresent(String.self, forKey: .folder)
        repoURL = try c.decodeIfPresent(String.self, forKey: .repoURL)
        goal = try c.decodeIfPresent(String.self, forKey: .goal) ?? ""
        state = try c.decodeIfPresent(FactoryProjectState.self, forKey: .state) ?? .unknown
        running = try c.decodeIfPresent(Bool.self, forKey: .running) ?? false
        live = try c.decodeIfPresent(Bool.self, forKey: .live)
        status = try c.decodeIfPresent(String.self, forKey: .status)
        counts = try c.decodeIfPresent([String: Int].self, forKey: .counts) ?? [:]
        coordinatorPane = try c.decodeIfPresent(String.self, forKey: .coordinatorPane)
        alert = try c.decodeIfPresent(FactoryProjectAlert.self, forKey: .alert)
        error = try c.decodeIfPresent(String.self, forKey: .error)
    }
}

/// One step of a task (`done`, `now`, `todo`, `skipped`, `ended`).
public struct FactoryStep: Decodable, Equatable, Sendable {
    public var name: String
    public var state: String
    public var text: String?
}

/// A log line (Go `LogEntry`): `last_actor_decision` on a task.
public struct FactoryLogEntry: Decodable, Equatable, Sendable {
    public var at: Date?
    public var by: String
    public var task: String?
    public var decision: String?
    public var why: String?
    public var kind: String?
}

/// A task of the graph (Go `TaskView`).
public struct FactoryTask: Decodable, Equatable, Identifiable, Sendable {
    public var ref: String
    public var title: String
    public var brief: String?
    public var stage: String
    public var whyWaiting: String?
    @FactoryDefaultEmpty public var after: [String]
    @FactoryDefaultEmpty public var dependents: [String]
    public var layer: Int
    public var rounds: Int
    public var model: String?
    public var terminal: String?
    public var lastRule: String?
    public var lastDecision: String?
    @FactoryDefaultEmpty public var steps: [FactoryStep]
    /// The live run's role, CLI and start; nil when no run is live.
    public var runRole: String?
    public var runCLI: String?
    public var runStartedAt: Date?
    /// The newest decision by the coordinator or the owner on this task.
    public var lastActorDecision: FactoryLogEntry?

    public var id: String { ref }

    enum CodingKeys: String, CodingKey {
        case ref, title, brief, stage, after, dependents, layer, rounds, model, terminal, steps
        case whyWaiting = "why_waiting"
        case lastRule = "last_rule"
        case lastDecision = "last_decision"
        case runRole = "run_role"
        case runCLI = "run_cli"
        case runStartedAt = "run_started_at"
        case lastActorDecision = "last_actor_decision"
    }
}

/// `GET /api/factory/projects/{id}/graph` (Go `GraphView`).
public struct FactoryGraphView: Decodable, Equatable, Sendable {
    public var `protocol`: Int?
    public var project: String
    public var folder: String?
    public var tasks: [FactoryTask]
}

/// The change a rule ask proposes (`set` on a `flow.Ask`).
public struct FactorySettingChange: Codable, Equatable, Sendable {
    public var name: String
    public var value: String
    public var was: String?

    public init(name: String, value: String, was: String? = nil) {
        self.name = name
        self.value = value
        self.was = was
    }
}

/// An open ask of `GET /api/factory/asks` (`flow.Ask` plus `command`).
public struct FactoryAsk: Decodable, Equatable, Identifiable, Sendable {
    public var id: String
    @FactoryDefaultEmpty public var project: String
    public var folder: String?
    public var kind: String
    public var gated: Bool
    public var question: String
    @FactoryDefaultEmpty public var options: [String]
    public var recommend: String?
    public var task: String?
    public var set: FactorySettingChange?
    public var at: Date?
    public var status: String?
    public var command: String?
}

/// A structured refusal from a factory write route (`code`, `error`, optional `fix`).
public struct FactoryRefusal: Error, Equatable, Sendable { public var status: Int; public var code, message: String; public var fix: String? }

/// One row of `GET /api/factory/projects/{id}/settings` (Go `flow.SettingRow`).
public struct FactorySettingRow: Decodable, Equatable, Identifiable, Sendable {
    public var name, label, value, defaultValue: String
    public var min, max: Int
    @FactoryDefaultEmpty public var allowed: [String]
    @FactoryDefaultEmpty public var clis: [String]
    @FactoryDefaultEmpty public var models: [String: [String]]
    public var problem, note: String?
    @FactoryDefaultEmpty public var top: Bool
    public var id: String { name }
    public var isModels: Bool { !clis.isEmpty }
    public var isChoice: Bool { !isModels && !allowed.isEmpty }
    public var isNumber: Bool { !isModels && !isChoice && max >= min && (min != 0 || max != 0) }
    public var isChanged: Bool { value != defaultValue && problem == nil }
    public var displayDefault: String { defaultValue.nonEmpty ?? "not set" }
    public var displayValue: String { isModels ? FactoryModelCandidate.parse(value).map { "\($0.cli) · \($0.model) · \($0.effort)" }.joined(separator: ", ").nonEmpty ?? "not set" : value.nonEmpty ?? "not set" }
    public var metaLine: String {
        isModels ? "default \(FactoryModelCandidate.format(FactoryModelCandidate.parse(defaultValue), separator: " then ").nonEmpty ?? "not set")"
            : isNumber ? "default \(displayDefault) · allowed \(min)–\(max)" : isChoice ? "default \(displayDefault) · allowed \(allowed.map { $0.nonEmpty ?? "not set" }.joined(separator: ", "))" : "default \(displayDefault)"
    }
    public var storedProblemMessage: String? { problem?.nonEmpty.map { isNumber ? "The stored value \(value.nonEmpty ?? "not set") is not allowed (allowed \(min)–\(max)). Change it before the next start." : "\($0). Change it before the next start." } }
    public func keptNotice(was: String?) -> String { "Kept at \(isModels ? (FactoryModelCandidate.format(FactoryModelCandidate.parse(was ?? ""), separator: " then ").nonEmpty ?? "not set") : (was?.trimmed.nonEmpty ?? "not set")). Nothing changed." }
    public static func errorMessage(_ error: Error, computerName: String, linkUsable: Bool = true) -> String {
        if let r = error as? FactoryRefusal, r.code != "proof_required", !r.message.isEmpty { return r.message }
        if case let .rejected(status, msg) = error as? DaemonError, !(status == 403 && msg.hasPrefix("proof_required")), !msg.isEmpty { return msg }
        return factoryActionError(error, computerName: computerName, linkUsable: linkUsable, prefix: "Not changed: ", retry: "try again")
    }
    public static func answerError(_ raw: String, computerName: String) -> String {
        let text = raw.hasPrefix("The daemon refused: ") ? String(raw.dropFirst(20)) : raw
        if text.hasPrefix("proof_required") { return factoryActionError(DaemonError.rejected(status: 403, message: "proof_required"), computerName: computerName, linkUsable: true, prefix: "Not saved: ", retry: "try again") }
        return text.hasPrefix("out_of_range:") ? "Not saved: another setting changed after you asked. \(String(text.dropFirst(13)).trimmed)" : text
    }
    public static func parseFix(_ fix: String) -> (command: String?, rest: String) {
        let s = fix.trimmed; guard s.lowercased().hasPrefix("run:") else { return (nil, s) }
        let body = String(s.dropFirst(4)).trimmed, cut = [body.range(of: " ("), body.range(of: ", then ")].compactMap { $0?.lowerBound }.min()
        return cut.map { (String(body[..<$0]).trimmed, String(body[$0...]).trimmed) } ?? (body.nonEmpty, "")
    }
    enum CodingKeys: String, CodingKey { case name, label, value, min, max, allowed, clis, models, problem, note, top; case defaultValue = "default" }
}

/// One CLI/model/effort candidate in a `models.<role>` setting value.
public struct FactoryModelCandidate: Equatable, Sendable {
    public var cli, model, effort: String
    public static let efforts = ["low", "medium", "high"]
    public init(cli: String, model: String, effort: String = "medium") { (self.cli, self.model, self.effort) = (cli, model, effort) }
    public static func parse(_ raw: String) -> [FactoryModelCandidate] {
        raw.split(separator: ",").compactMap { let s = $0.trimmingCharacters(in: .whitespacesAndNewlines), p = s.split(separator: "/", omittingEmptySubsequences: false).map { $0.trimmingCharacters(in: .whitespaces) }; return s.isEmpty ? nil : p.count >= 3 ? .init(cli: p[0], model: p.dropFirst().dropLast().joined(separator: "/"), effort: p.last!.nonEmpty ?? "medium") : .init(cli: "", model: s) }
    }
    public static func format(_ candidates: [FactoryModelCandidate], separator: String = ", ") -> String { candidates.map { "\($0.cli.trimmed)/\($0.model.trimmed)/\($0.effort.trimmed)" }.joined(separator: separator) }
    public static func ordinal(_ i: Int) -> String { i < 9 ? "\(["first", "second", "third", "fourth", "fifth", "sixth", "seventh", "eighth", "ninth"][i]) choice" : "#\(i + 1) choice" }
}

// Go can omit an ask's project and send null for nil collections.
private protocol FactoryEmptyValue {
    init()
}

extension Array: FactoryEmptyValue {}
extension Dictionary: FactoryEmptyValue {}
extension String: FactoryEmptyValue {}
extension Bool: FactoryEmptyValue {}

/// Supports synthesized decoding for fields where Go can send a nil collection or omit a value.
@propertyWrapper
public struct FactoryDefaultEmpty<Value: Decodable & Equatable & Sendable>: Decodable, Equatable, Sendable {
    public var wrappedValue: Value

    public init(from decoder: Decoder) throws {
        wrappedValue = try decoder.singleValueContainer().decode(Value.self)
    }

    fileprivate init(_ value: Value) { wrappedValue = value }
}

extension FactoryDefaultEmpty: Encodable where Value: Encodable {
    public func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        try container.encode(wrappedValue)
    }
}

private extension KeyedDecodingContainer {
    func decode<Value: FactoryEmptyValue>(_ type: FactoryDefaultEmpty<Value>.Type, forKey key: Key) throws -> FactoryDefaultEmpty<Value> {
        try decodeIfPresent(type, forKey: key) ?? FactoryDefaultEmpty(Value())
    }
}

extension DaemonClient {
    /// `GET /api/factory/projects`.
    public func factoryProjects() async throws -> [FactoryProject] {
        struct Reply: Decodable { let projects: [FactoryProject] }
        return try await factoryGet(Reply.self, encodedPath: "/api/factory/projects", what: "the project list").projects
    }

    /// `GET /api/factory/projects/{id}/graph`; the id is escaped as one path segment.
    public func factoryGraph(projectID: String) async throws -> FactoryGraphView {
        try await factoryGet(FactoryGraphView.self, encodedPath: "/api/factory/projects/\(factoryPathSegment(projectID))/graph", what: "the project graph")
    }

    /// `GET /api/factory/asks` — the open asks.
    public func factoryAsks() async throws -> [FactoryAsk] {
        struct Reply: Decodable { let asks: [FactoryAsk] }
        return try await factoryGet(Reply.self, encodedPath: "/api/factory/asks", what: "the ask list").asks
    }

    /// `GET /api/factory/projects/{id}/settings`.
    public func factorySettings(projectID: String) async throws -> [FactorySettingRow] {
        struct Reply: Decodable { let rows: [FactorySettingRow] }; return try await factoryGet(Reply.self, encodedPath: "/api/factory/projects/\(factoryPathSegment(projectID))/settings", what: "the settings").rows
    }

    /// `POST /api/factory/projects/{id}/settings`: proposes a change as a read-back `rule` ask.
    public func proposeFactorySetting(projectID: String, name: String, value: String) async throws -> FactoryAsk {
        struct Reply: Decodable { let ok: Bool; let ask: FactoryAsk }
        let r: Reply = try await factoryPost("/api/factory/projects/\(factoryPathSegment(projectID))/settings", ["name": name, "value": value], what: "the setting proposal", structuredRefusal: true)
        guard r.ok else { throw DaemonError.malformed("the setting proposal did not complete") }; return r.ask
    }

    /// Resume with the project's existing goal using the paired-device start route.
    public func resumeFactory(folder: String, goal: String) async throws { try await factoryPost("/api/factory/projects/start", ["folder": folder, "goal": goal]) }

    public func sayFactory(projectID: String, text: String) async throws {
        try await factoryPost("/api/factory/projects/\(factoryPathSegment(projectID))/say", ["text": text])
    }

    public func stopFactory(projectID: String) async throws { try await factoryPost("/api/factory/projects/stop", ["project": projectID]) }

    private func factoryPathSegment(_ id: String) -> String {
        let allowed = CharacterSet.urlPathAllowed.subtracting(CharacterSet(charactersIn: "/%?#"))
        return id.addingPercentEncoding(withAllowedCharacters: allowed) ?? id
    }

    private func factoryPost(_ path: String, _ fields: [String: String]) async throws {
        struct Reply: Decodable { let ok: Bool }
        guard try await (factoryPost(path, fields, what: "the project action reply") as Reply).ok else { throw DaemonError.malformed("the project action did not complete") }
    }

    private func factoryPost<T: Decodable>(_ path: String, _ fields: [String: String], what: String, structuredRefusal: Bool = false) async throws -> T {
        let body = try JSONSerialization.data(withJSONObject: fields, options: .withoutEscapingSlashes)
        return try await factoryReply(T.self, request: try request(encodedPath: path, method: "POST", body: body), what: what, structuredRefusal: structuredRefusal)
    }

    /// One factory read. Unlike `sendJSON`, a 404 is not "an older daemon":
    /// the factory routes answer it for an unknown project, and 503 for a
    /// daemon with no factory; both carry the daemon's words (`.rejected`).
    private func factoryGet<T: Decodable>(_ type: T.Type, encodedPath: String, what: String) async throws -> T {
        try await factoryReply(type, request: request(encodedPath: encodedPath), what: what)
    }

    private func factoryReply<T: Decodable>(_ type: T.Type, request: URLRequest, what: String, structuredRefusal: Bool = false) async throws -> T {
        let (data, response) = try await perform(request)
        guard let http = response as? HTTPURLResponse else { throw DaemonError.malformed("no HTTP response") }
        switch http.statusCode {
        case 200 ..< 300:
            do {
                return try DaemonClient.decoder.decode(T.self, from: data)
            } catch {
                throw DaemonError.malformed("\(what) did not decode")
            }
        case 401:
            throw DaemonError.unauthorized(endpoint: endpoint.display)
        default:
            let obj = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
            let msg = (obj?["error"] as? String) ?? "", code = (obj?["code"] as? String) ?? "", fix = (obj?["fix"] as? String)?.trimmed.nonEmpty
            if structuredRefusal, http.statusCode != 404, !code.isEmpty || fix != nil { throw FactoryRefusal(status: http.statusCode, code: code, message: msg, fix: fix) }
            throw DaemonError.rejected(status: http.statusCode, message: msg)
        }
    }
}
