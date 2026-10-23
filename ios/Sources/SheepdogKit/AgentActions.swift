import Foundation

// The user's own agent actions (the daemon's docs/dev/ws-contract.md
// "Agent actions", feature `agent-actions`): New agent, Rename, Stop and
// Close from the app.

/// `GET /api/spaces`: herdr's workspaces with their agents, the project
/// folders that have no space yet, and the kinds a new agent may be.
public struct SpacesView: Codable, Equatable, Sendable {
    public struct Space: Codable, Equatable, Sendable, Identifiable {
        public var id: String
        public var name: String
        public var path: String
        public var git: Bool
        public var repo: String?
        public var branch: String?
        /// A linked git worktree (not the repository's main checkout).
        public var worktree: Bool
        public var agents: [SpaceAgent]
        public var shells: [String]
        public var commands: [WrapUp]
    }

    public struct SpaceAgent: Codable, Equatable, Sendable {
        public var paneID: String
        public var alias: String
        public var name: String
        public var kind: String
        public var state: String
        public var title: String?

        enum CodingKeys: String, CodingKey {
            case paneID = "pane_id"
            case alias, name, kind, state, title
        }
    }

    public struct Folder: Codable, Equatable, Sendable {
        public var name: String
        public var path: String
        public var git: Bool
    }

    /// A kind a new agent may be, with the name the user hears.
    public struct Kind: Codable, Equatable, Sendable {
        public var kind: String
        public var name: String
    }

    /// A saved wrap-up command of a space.
    public struct WrapUp: Codable, Equatable, Sendable, Identifiable {
        public var id: String
        public var name: String
        public var command: String
    }

    public var spaces: [Space]
    public var folders: [Folder]
    public var kinds: [Kind]
    /// herdr can make a fresh worktree for a new agent.
    public var worktrees: Bool
}

/// What the New agent sheet offers, built from `GET /api/spaces`.
public struct NewAgentOptions: Codable, Equatable, Sendable {
    public struct Project: Codable, Equatable, Sendable, Identifiable, Hashable {
        public var name: String
        public var path: String
        /// A herdr workspace already works there.
        public var open: Bool
        /// A git checkout: a fresh worktree is possible.
        public var git: Bool
        /// The open space (herdr workspace id) the agent joins; nil for a
        /// folder that has no space yet.
        public var space: String?
        public var id: String { space ?? path }

        public init(name: String, path: String, open: Bool = false, git: Bool = false, space: String? = nil) {
            self.name = name
            self.path = path
            self.open = open
            self.git = git
            self.space = space
        }
    }

    public var kinds: [String]
    /// The daemon's name for each kind ("agy": "Antigravity").
    public var kindNames: [String: String]
    public var projects: [Project]
    /// herdr can make worktrees.
    public var worktree: Bool

    public init(kinds: [String], projects: [Project], worktree: Bool, kindNames: [String: String] = [:]) {
        self.kinds = kinds
        self.kindNames = kindNames
        self.projects = projects
        self.worktree = worktree
    }

    /// The open spaces first, then the free project folders.
    public init(spaces view: SpacesView) {
        self.kinds = view.kinds.map(\.kind)
        self.kindNames = Dictionary(view.kinds.map { ($0.kind, $0.name) }, uniquingKeysWith: { a, _ in a })
        self.worktree = view.worktrees
        self.projects = view.spaces.filter { !$0.path.isEmpty }.map {
            Project(name: $0.name, path: $0.path, open: true, git: $0.git, space: $0.id)
        } + view.folders.map { Project(name: $0.name, path: $0.path, git: $0.git) }
    }

    /// The name the sheet shows for a kind: the daemon's.
    public func kindName(_ kind: String) -> String {
        kindNames[kind] ?? kind
    }

    /// The project roots as far as the app can tell: the folders that
    /// hold the free project folders (`GET /api/spaces` lists the folders
    /// directly inside each root), tilde-abbreviated, in order.
    public static func roots(_ view: SpacesView) -> [String] {
        var out: [String] = []
        for folder in view.folders {
            let parent = (folder.path as NSString).deletingLastPathComponent
            guard !parent.isEmpty, !out.contains(parent) else { continue }
            out.append(parent)
        }
        return out
    }

    /// "Choose another folder…": a typed path becomes a project, or nil
    /// when it cannot be one. It must be a full path (`/…` or `~/…`) with
    /// no `..`; the daemon still checks that it lies inside a project
    /// root and says so if not. A path that names a listed space or folder
    /// is that one.
    public static func typedFolder(_ text: String, in view: SpacesView) -> Project? {
        var path = text.trimmingCharacters(in: .whitespacesAndNewlines)
        while path.count > 1, path.hasSuffix("/") { path.removeLast() }
        guard path.hasPrefix("/") || path.hasPrefix("~/"), !path.split(separator: "/").contains("..") else { return nil }
        let name = (path as NSString).lastPathComponent
        guard !name.isEmpty, name != "~", name != "/" else { return nil }
        let options = NewAgentOptions(spaces: view)
        if let known = options.projects.first(where: { $0.path == path || ($0.path as NSString).abbreviatingWithTildeInPath == path }) {
            return known
        }
        return Project(name: name, path: path)
    }

    /// A folder the user typed: the app does not know whether it is a git
    /// repository, so it neither offers a worktree nor says it is not one.
    public static func isTyped(_ project: Project, in view: SpacesView) -> Bool {
        project.space == nil && !view.folders.contains { $0.path == project.path }
    }
}

/// The reply to an action: the line to show, and a new agent's pane.
public struct AgentActionReply: Codable, Equatable, Sendable {
    public var spoken: String?
    public var paneID: String?

    enum CodingKeys: String, CodingKey {
        case spoken
        case paneID = "pane_id"
    }

    public init(spoken: String? = nil, paneID: String? = nil) {
        self.spoken = spoken
        self.paneID = paneID
    }
}

/// A new agent that did not start: the daemon's `reason` code, its words
/// and the pane it left open, if any (`POST /api/agents`).
public struct AgentStartError: Error, Equatable, Sendable {
    public enum Reason: String, Sendable {
        case notInstalled = "not_installed"
        case notGit = "not_git"
        case branchExists = "branch_exists"
        case timeout
        case startFailed = "start_failed"
    }

    public var reason: Reason
    public var message: String
    public var paneID: String?

    public init(reason: Reason, message: String, paneID: String? = nil) {
        self.reason = reason
        self.message = message
        self.paneID = paneID
    }

    /// The headline the New agent sheet shows, in plain words.
    public func title(kind: String, place: String) -> String {
        switch reason {
        case .notInstalled: return "\(kind) isn't installed on this computer"
        case .notGit: return "\(place) isn't a git repository"
        case .branchExists: return "That branch is checked out elsewhere"
        case .timeout: return "\(kind) didn't start in time"
        case .startFailed: return "\(kind) didn't start"
        }
    }

    /// What to do about it.
    public var fix: String {
        switch reason {
        case .notInstalled: return "Install it on your computer, then try again, or pick another agent."
        case .notGit: return "Start it in the folder itself, without a worktree."
        case .branchExists: return "Pick another branch name."
        case .timeout, .startFailed: return paneID == nil ? "Try again in a moment." : "Its pane is still open: look at it to see why."
        }
    }
}

public enum AgentAction {
    /// The `bot-ready` feature.
    public static let feature = "agent-actions"

    /// A branch name the daemon accepts for a fresh worktree (it checks
    /// again).
    public static func validBranch(_ s: String) -> Bool {
        let allowed = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._/-")
        guard let first = s.unicodeScalars.first, CharacterSet.alphanumerics.contains(first), first.isASCII,
              s.count <= 80, s.unicodeScalars.allSatisfy(allowed.contains) else { return false }
        return !s.contains("..") && !s.contains("//") && !s.hasSuffix("/") && !s.hasSuffix(".lock") && !s.hasSuffix(".")
    }

    /// A worktree branch suggested from a first prompt or a project name:
    /// lowercase words joined by `-`.
    public static func suggestedBranch(from text: String) -> String {
        let words = text.lowercased().split { !$0.isLetter && !$0.isNumber }.filter { $0.allSatisfy(\.isASCII) }.prefix(4)
        let b = words.joined(separator: "-")
        return validBranch(b) ? b : ""
    }
}

extension DaemonClient {
    /// `GET /api/spaces`.
    public func spaces() async throws -> SpacesView {
        let request = try request(encodedPath: "/api/spaces")
        let (data, response) = try await perform(request)
        return try decodeAction(SpacesView.self, data: data, response: response)
    }

    /// The New agent sheet's choices (`GET /api/spaces`).
    public func newAgentOptions() async throws -> NewAgentOptions {
        NewAgentOptions(spaces: try await spaces())
    }

    /// `POST /api/agents`: in the project's open space, or in a new space
    /// in its folder (a fresh worktree always opens a new space). It
    /// answers once the agent is ready.
    public func newAgent(kind: String, project: NewAgentOptions.Project, worktree: String? = nil, text: String? = nil) async throws -> AgentActionReply {
        var body: [String: Any] = ["kind": kind]
        if let worktree, !worktree.isEmpty {
            body["dir"] = project.path
            body["worktree"] = ["branch": worktree]
        } else if let space = project.space {
            body["space"] = space
        } else {
            body["dir"] = project.path
        }
        if let text, !text.isEmpty { body["text"] = text }
        let data = try JSONSerialization.data(withJSONObject: body)
        let request = try request(encodedPath: "/api/agents", method: "POST", body: data)
        let (reply, response) = try await perform(request)
        if let failure = Self.startError(data: reply, response: response) { throw failure }
        return try decodeAction(AgentActionReply.self, data: reply, response: response)
    }

    /// A failed start with a `reason` code; nil for anything else.
    static func startError(data: Data, response: URLResponse) -> AgentStartError? {
        guard let http = response as? HTTPURLResponse, !(200 ..< 300).contains(http.statusCode),
              let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any],
              let code = object["reason"] as? String, let reason = AgentStartError.Reason(rawValue: code)
        else { return nil }
        let pane = (object["pane_id"] as? String).flatMap { $0.isEmpty ? nil : $0 }
        return AgentStartError(reason: reason, message: object["error"] as? String ?? "", paneID: pane)
    }

    /// `POST /api/agents/{pane}/rename`.
    public func renameAgent(paneID: String, name: String) async throws -> AgentActionReply {
        try await postAction(encodedPath: DaemonClient.agentPath(paneID: paneID, suffix: "rename"), body: ["name": name])
    }

    /// `POST /api/agents/{pane}/interrupt`: stop the current turn.
    public func interruptAgent(paneID: String) async throws -> AgentActionReply {
        try await postAction(encodedPath: DaemonClient.agentPath(paneID: paneID, suffix: "interrupt"), body: [:])
    }

    /// `POST /api/agents/{pane}/close`.
    public func closeAgent(paneID: String) async throws -> AgentActionReply {
        try await postAction(encodedPath: DaemonClient.agentPath(paneID: paneID, suffix: "close"), body: [:])
    }

    /// `POST /api/shells/{pane_id}/close`: end a phone shell (the user
    /// confirmed). Leaving the window only detaches.
    public func closeShell(paneID: String) async throws -> AgentActionReply {
        let agentPath = DaemonClient.agentPath(paneID: paneID, suffix: "close")
        let path = "/api/shells/" + agentPath.dropFirst("/api/agents/".count)
        return try await postAction(encodedPath: path, body: [:])
    }

    private func postAction(encodedPath: String, body: [String: Any]) async throws -> AgentActionReply {
        let data = try JSONSerialization.data(withJSONObject: body)
        let request = try request(encodedPath: encodedPath, method: "POST", body: data)
        let (reply, response) = try await perform(request)
        return try decodeAction(AgentActionReply.self, data: reply, response: response)
    }

    /// 2xx decodes; 401 is the token; anything else carries the daemon's
    /// plain words (a 404 without them is a daemon too old for actions).
    private func decodeAction<T: Decodable>(_ type: T.Type, data: Data, response: URLResponse) throws -> T {
        guard let http = response as? HTTPURLResponse else { throw DaemonError.malformed("no HTTP response") }
        let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
        switch http.statusCode {
        case 200 ..< 300:
            do {
                return try DaemonClient.decoder.decode(T.self, from: data)
            } catch {
                throw DaemonError.malformed("the daemon's answer did not decode")
            }
        case 401:
            throw DaemonError.unauthorized(endpoint: endpoint.display)
        default:
            let message = (object?["error"] as? String) ?? (http.statusCode == 404 ? "Update sheepdog on your computer to do this from the phone." : "")
            throw DaemonError.rejected(status: http.statusCode, message: message)
        }
    }
}
