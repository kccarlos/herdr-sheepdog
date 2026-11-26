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

// Go can omit an ask's project and send null for nil collections.
private protocol FactoryEmptyValue {
    init()
}

extension Array: FactoryEmptyValue {}
extension Dictionary: FactoryEmptyValue {}
extension String: FactoryEmptyValue {}

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
        var allowed = CharacterSet.urlPathAllowed
        allowed.remove(charactersIn: "/%?#")
        let id = projectID.addingPercentEncoding(withAllowedCharacters: allowed) ?? projectID
        return try await factoryGet(FactoryGraphView.self, encodedPath: "/api/factory/projects/\(id)/graph", what: "the project graph")
    }

    /// `GET /api/factory/asks` — the open asks.
    public func factoryAsks() async throws -> [FactoryAsk] {
        struct Reply: Decodable { let asks: [FactoryAsk] }
        return try await factoryGet(Reply.self, encodedPath: "/api/factory/asks", what: "the ask list").asks
    }

    /// Resume with the project's existing goal using the paired-device start route.
    public func resumeFactory(folder: String, goal: String) async throws {
        struct Reply: Decodable { let ok: Bool }
        let body = try JSONEncoder().encode(["folder": folder, "goal": goal])
        let request = try request(path: "/api/factory/projects/start", method: "POST", body: body)
        let reply = try await factoryReply(Reply.self, request: request, what: "the resume reply")
        guard reply.ok else { throw DaemonError.malformed("the project did not resume") }
    }

    /// One factory read. Unlike `sendJSON`, a 404 is not "an older daemon":
    /// the factory routes answer it for an unknown project, and 503 for a
    /// daemon with no factory; both carry the daemon's words (`.rejected`).
    private func factoryGet<T: Decodable>(_ type: T.Type, encodedPath: String, what: String) async throws -> T {
        try await factoryReply(type, request: request(encodedPath: encodedPath), what: what)
    }

    private func factoryReply<T: Decodable>(_ type: T.Type, request: URLRequest, what: String) async throws -> T {
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
            let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
            throw DaemonError.rejected(status: http.statusCode, message: (object?["error"] as? String) ?? "")
        }
    }
}
