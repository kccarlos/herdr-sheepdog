import Foundation

// The user's own agent actions (the daemon's docs/dev/ws-contract.md
// "Agent actions", feature `agent-actions`): New agent, Rename, Stop and
// Close from the app.

/// `GET /api/projects`: what the New agent sheet offers.
public struct NewAgentOptions: Codable, Equatable, Sendable {
    public struct Project: Codable, Equatable, Sendable, Identifiable, Hashable {
        public var name: String
        public var path: String
        /// A herdr workspace already works there.
        public var open: Bool
        /// A git checkout: a fresh worktree is possible.
        public var git: Bool
        public var id: String { path }

        public init(name: String, path: String, open: Bool = false, git: Bool = false) {
            self.name = name
            self.path = path
            self.open = open
            self.git = git
        }
    }

    public var kinds: [String]
    public var projects: [Project]
    /// herdr can make worktrees.
    public var worktree: Bool

    public init(kinds: [String], projects: [Project], worktree: Bool) {
        self.kinds = kinds
        self.projects = projects
        self.worktree = worktree
    }

    /// The name the sheet shows for a kind.
    public static func kindName(_ kind: String) -> String {
        switch kind {
        case "claude": return "Claude Code"
        case "codex": return "Codex"
        default: return kind
        }
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
    /// `GET /api/projects`.
    public func newAgentOptions() async throws -> NewAgentOptions {
        let request = try request(encodedPath: "/api/projects")
        let (data, response) = try await perform(request)
        return try decodeAction(NewAgentOptions.self, data: data, response: response)
    }

    /// `POST /api/agents`.
    public func newAgent(kind: String, dir: String, worktree: String? = nil, text: String? = nil) async throws -> AgentActionReply {
        var body: [String: Any] = ["kind": kind, "dir": dir]
        if let worktree, !worktree.isEmpty { body["worktree"] = worktree }
        if let text, !text.isEmpty { body["text"] = text }
        return try await postAction(encodedPath: "/api/agents", body: body)
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
