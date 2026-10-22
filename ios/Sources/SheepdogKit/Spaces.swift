import Foundation

// Space management from the app (the daemon's docs/dev/ws-contract.md
// "Spaces"): rename and close a space, its worktrees, and its saved
// wrap-up commands.

/// `GET /api/spaces/{id}/worktrees`.
public struct WorktreeList: Codable, Equatable, Sendable {
    public struct Worktree: Codable, Equatable, Sendable, Identifiable {
        public var path: String
        public var branch: String?
        public var label: String?
        public var isLinkedWorktree: Bool
        public var isDetached: Bool?
        public var openWorkspaceID: String?
        public var id: String { path }

        enum CodingKeys: String, CodingKey {
            case path, branch, label
            case isLinkedWorktree = "is_linked_worktree"
            case isDetached = "is_detached"
            case openWorkspaceID = "open_workspace_id"
        }

        public init(path: String, branch: String? = nil, label: String? = nil, isLinkedWorktree: Bool = true, isDetached: Bool? = nil, openWorkspaceID: String? = nil) {
            self.path = path
            self.branch = branch
            self.label = label
            self.isLinkedWorktree = isLinkedWorktree
            self.isDetached = isDetached
            self.openWorkspaceID = openWorkspaceID
        }

        /// The row's title: herdr's label, else the folder's name.
        public var title: String { label?.isEmpty == false ? label! : (path as NSString).lastPathComponent }
    }

    public var repo: String?
    public var worktrees: [Worktree]
}

/// What a space's state looks like at a glance, and whether it can close.
public enum SpaceSummary {
    /// "2 agents · 1 waiting", "1 agent · working", "No agents".
    public static func line(_ space: SpacesView.Space) -> String {
        let agents = space.agents
        guard !agents.isEmpty else { return "No agents" }
        let count = agents.count == 1 ? "1 agent" : "\(agents.count) agents"
        let waiting = agents.filter { $0.state == "blocked" }.count
        if waiting > 0 { return "\(count) · \(waiting) waiting" }
        if agents.contains(where: { $0.state == "working" }) { return "\(count) · working" }
        return agents.count == 1 ? "\(count) · \(agents[0].state)" : count
    }

    /// The agents that keep the space open: working or waiting. Close is
    /// refused while there are any (the daemon refuses too, `409`).
    public static func busy(_ space: SpacesView.Space) -> [SpacesView.SpaceAgent] {
        space.agents.filter { $0.state == "working" || $0.state == "blocked" }
    }

    /// Why Close is not offered yet, in plain words; nil when it can close.
    public static func closeBlocker(_ space: SpacesView.Space) -> String? {
        let busy = busy(space)
        guard !busy.isEmpty else { return nil }
        if let waiting = busy.first(where: { $0.state == "blocked" }) {
            return "You can close it once \(waiting.name) has its answer."
        }
        let names = busy.map(\.name)
        let who = names.count == 1 ? "\(names[0]) is" : "\(names.dropLast().joined(separator: ", ")) and \(names.last!) are"
        return "You can close it once \(who) done working."
    }

    /// What the Close confirmation says will end.
    public static func closeMessage(_ space: SpacesView.Space) -> String {
        let names = space.agents.map(\.name)
        let panes = space.shells.count
        var parts: [String] = []
        if !names.isEmpty {
            parts.append("stops " + (names.count == 1 ? names[0] : "\(names.dropLast().joined(separator: ", ")) and \(names.last!)"))
        }
        if panes > 0 { parts.append(panes == 1 ? "closes its shell" : "closes its \(panes) shells") }
        let what = parts.isEmpty ? "Closing it ends the space" : "Closing it " + parts.joined(separator: " and ")
        return "\(what). The folder and its files stay on your computer."
    }
}

extension DaemonClient {
    static func spacePath(_ id: String, _ suffix: String = "") -> String {
        var allowed = CharacterSet.urlPathAllowed
        allowed.remove(charactersIn: "/%?#")
        let escaped = id.addingPercentEncoding(withAllowedCharacters: allowed) ?? id
        return "/api/spaces/\(escaped)" + (suffix.isEmpty ? "" : "/" + suffix)
    }

    /// `POST /api/spaces`: a space with no agent yet, in `dir`.
    public func newSpace(dir: String) async throws -> AgentActionReply {
        try await postSpace("/api/spaces", ["dir": dir])
    }

    /// `POST /api/spaces/{id}/rename`.
    public func renameSpace(id: String, name: String) async throws -> AgentActionReply {
        try await postSpace(DaemonClient.spacePath(id, "rename"), ["name": name])
    }

    /// `POST /api/spaces/{id}/close` (the user confirmed).
    public func closeSpace(id: String) async throws -> AgentActionReply {
        try await postSpace(DaemonClient.spacePath(id, "close"), [:])
    }

    /// `GET /api/spaces/{id}/worktrees`.
    public func worktrees(space id: String) async throws -> WorktreeList {
        let request = try request(encodedPath: DaemonClient.spacePath(id, "worktrees"))
        let (data, response) = try await perform(request)
        try checkSpace(data: data, response: response)
        do {
            return try DaemonClient.decoder.decode(WorktreeList.self, from: data)
        } catch {
            throw DaemonError.malformed("the daemon's answer did not decode")
        }
    }

    /// `POST /api/spaces/{id}/worktrees/open`: a space for that checkout.
    public func openWorktree(space id: String, path: String) async throws -> AgentActionReply {
        try await postSpace(DaemonClient.spacePath(id, "worktrees/open"), ["path": path])
    }

    /// `POST /api/spaces/{id}/worktree/remove`: removes the worktree the
    /// space works in, and closes the space. `force` for a dirty checkout.
    public func removeWorktree(space id: String, force: Bool) async throws -> AgentActionReply {
        try await postSpace(DaemonClient.spacePath(id, "worktree/remove"), force ? ["force": true] : [:])
    }

    /// `POST /api/spaces/{id}/commands`: save a wrap-up command.
    public func addCommand(space id: String, name: String, command: String) async throws {
        _ = try await postSpace(DaemonClient.spacePath(id, "commands"), ["name": name, "command": command])
    }

    /// `DELETE /api/spaces/{id}/commands/{cid}`.
    public func deleteCommand(space id: String, command cid: String) async throws {
        let request = try request(encodedPath: DaemonClient.spacePath(id, "commands/" + (cid.addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? cid)), method: "DELETE")
        let (data, response) = try await perform(request)
        try checkSpace(data: data, response: response)
    }

    /// `POST /api/spaces/{id}/commands/{cid}/run`: answers with the new
    /// tab's pane, which the app opens as a shell window.
    public func runCommand(space id: String, command cid: String) async throws -> AgentActionReply {
        try await postSpace(DaemonClient.spacePath(id, "commands/" + (cid.addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? cid) + "/run"), [:])
    }

    private func postSpace(_ path: String, _ body: [String: Any]) async throws -> AgentActionReply {
        let data = try JSONSerialization.data(withJSONObject: body)
        let request = try request(encodedPath: path, method: "POST", body: data)
        let (reply, response) = try await perform(request)
        try checkSpace(data: reply, response: response)
        return (try? DaemonClient.decoder.decode(AgentActionReply.self, from: reply)) ?? AgentActionReply()
    }

    /// 2xx passes; anything else throws with the daemon's words.
    func checkSpace(data: Data, response: URLResponse) throws {
        guard let http = response as? HTTPURLResponse else { throw DaemonError.malformed("no HTTP response") }
        guard !(200 ..< 300).contains(http.statusCode) else { return }
        let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
        if http.statusCode == 401 { throw DaemonError.unauthorized(endpoint: endpoint.display) }
        let message = (object?["error"] as? String) ?? (http.statusCode == 404 ? "Update sheepdog on your computer to do this from the phone." : "")
        throw DaemonError.rejected(status: http.statusCode, message: message)
    }
}
