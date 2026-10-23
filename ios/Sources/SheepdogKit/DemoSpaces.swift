import Foundation

/// The demo's spaces (`/api/spaces` and its routes): the scripted agents'
/// space "shop", a worktree space beside it, and two free folders. Rename,
/// close, worktrees and wrap-up commands change only this copy.
struct DemoSpaces {
    struct Space {
        var id: String
        var name: String
        var path: String
        var git: Bool
        var branch: String?
        var worktree: Bool
        /// The scripted agents work in "shop".
        var hasAgents: Bool
        var commands: [(id: String, name: String, command: String)]
        /// The worktree has uncommitted changes: Remove needs `force`.
        var dirty = false
    }

    private(set) var spaces: [Space] = [
        Space(id: "w1", name: "shop", path: "/Users/demo/code/shop", git: true, branch: "main", worktree: false, hasAgents: true,
              commands: [("c1", "Run tests", "npm test"), ("c2", "Lint", "npm run lint")]),
        Space(id: "w2", name: "shop-fix-login", path: "/Users/demo/code/shop-fix-login", git: true, branch: "fix-login", worktree: true,
              hasAgents: false, commands: [], dirty: true),
    ]
    private(set) var folders: [(name: String, path: String, git: Bool)] = [
        ("website", "/Users/demo/code/website", true),
        ("notes", "/Users/demo/code/notes", false),
    ]
    private var next = 3

    /// `GET /api/spaces`, with the scripted agents in "shop".
    func view(agents: [Agent]) -> [String: Any] {
        [
            "kinds": [["kind": "claude", "name": "Claude"], ["kind": "codex", "name": "Codex"], ["kind": "agy", "name": "Antigravity"]],
            "worktrees": true,
            "spaces": spaces.map { space -> [String: Any] in
                var object: [String: Any] = [
                    "id": space.id, "name": space.name, "path": space.path, "git": space.git, "worktree": space.worktree,
                    "repo": "shop", "agents": space.hasAgents ? agents.filter { $0.state != .exited }.map { agent -> [String: Any] in
                        ["pane_id": agent.paneID, "alias": agent.alias, "name": agent.alias, "kind": agent.kind ?? "", "state": agent.state.rawValue]
                    } : [],
                    "shells": [] as [Any],
                    "commands": space.commands.map { ["id": $0.id, "name": $0.name, "command": $0.command] },
                ]
                if let branch = space.branch { object["branch"] = branch }
                return object
            },
            "folders": folders.map { ["name": $0.name, "path": $0.path, "git": $0.git] },
        ]
    }

    /// The space routes; nil for a path that is not one. `busy` says
    /// whether the scripted agents are working or waiting.
    mutating func handle(method: String, path: String, body: [String: Any], busy: Bool) -> (status: Int, body: [String: Any])? {
        if method == "POST", path == "/api/spaces" {
            guard let dir = body["dir"] as? String, let i = folders.firstIndex(where: { $0.path == dir }) else {
                return (400, ["error": "that folder is not under your project roots"])
            }
            let folder = folders.remove(at: i)
            spaces.append(Space(id: "w\(next)", name: folder.name, path: folder.path, git: folder.git, branch: folder.git ? "main" : nil,
                                worktree: false, hasAgents: false, commands: []))
            next += 1
            return (200, ["ok": true, "space": spaces.last!.id, "pane_id": ""])
        }
        guard path.hasPrefix("/api/spaces/") else { return nil }
        let parts = path.dropFirst("/api/spaces/".count).split(separator: "/").map(String.init)
        guard let id = parts.first, let index = spaces.firstIndex(where: { $0.id == id }) else {
            return (404, ["error": "no such space"])
        }
        let rest = Array(parts.dropFirst())
        switch (method, rest) {
        case ("POST", ["rename"]):
            let name = (body["name"] as? String ?? "").trimmingCharacters(in: .whitespaces)
            guard !name.isEmpty, name.count <= 60 else { return (400, ["error": "a name is 1 to 60 characters"]) }
            spaces[index].name = name
            return (200, ["ok": true, "spoken": "Renamed it \(name)."])
        case ("POST", ["close"]):
            if spaces[index].hasAgents, busy { return (409, ["error": "an agent there is still working or waiting"]) }
            let gone = spaces.remove(at: index)
            return (200, ["ok": true, "spoken": "Closed \(gone.name)."])
        case ("GET", ["worktrees"]):
            guard spaces[index].git else { return (400, ["error": "\(spaces[index].name) is not in a git repository"]) }
            let trees: [[String: Any]] = spaces.filter { $0.git && $0.path.contains("/shop") }.map {
                ["path": $0.path, "branch": $0.branch ?? "", "label": $0.name, "is_linked_worktree": $0.worktree,
                 "is_detached": false, "is_prunable": false, "is_bare": false, "open_workspace_id": $0.id]
            }
            return (200, ["repo": "shop", "repo_root": "/Users/demo/code/shop", "worktrees": trees])
        case ("POST", ["worktrees", "open"]):
            return (200, ["ok": true, "pane_id": ""])
        case ("POST", ["worktree", "remove"]):
            guard spaces[index].worktree else { return (400, ["error": "that is the repository's main checkout"]) }
            if spaces[index].dirty, body["force"] as? Bool != true {
                return (409, ["error": "the worktree has uncommitted changes"])
            }
            spaces.remove(at: index)
            return (200, ["ok": true, "spoken": "Removed the worktree."])
        case ("POST", ["commands"]):
            let name = (body["name"] as? String ?? "").trimmingCharacters(in: .whitespaces)
            let command = (body["command"] as? String ?? "").trimmingCharacters(in: .whitespaces)
            guard !name.isEmpty, !command.isEmpty else { return (400, ["error": "a command needs a name and one line"]) }
            let cid = "c\(next)"
            next += 1
            spaces[index].commands.append((cid, name, command))
            return (200, ["ok": true, "command": ["id": cid, "name": name, "command": command]])
        case let ("PUT", tail) where tail.count == 2 && tail[0] == "commands":
            let name = (body["name"] as? String ?? "").trimmingCharacters(in: .whitespaces)
            let command = (body["command"] as? String ?? "").trimmingCharacters(in: .whitespaces)
            guard !name.isEmpty, !command.isEmpty else { return (400, ["error": "a command needs a name and one line"]) }
            guard let i = spaces[index].commands.firstIndex(where: { $0.id == tail[1] }) else { return (404, ["error": "no such command"]) }
            spaces[index].commands[i] = (tail[1], name, command)
            return (200, ["ok": true, "command": ["id": tail[1], "name": name, "command": command]])
        case ("POST", ["run"]):
            let command = (body["command"] as? String ?? "").trimmingCharacters(in: .whitespaces)
            guard !command.isEmpty, !command.contains("\n") else { return (400, ["error": "a command is one line"]) }
            return (200, ["ok": true, "pane_id": ""])
        case let ("DELETE", tail) where tail.count == 2 && tail[0] == "commands":
            spaces[index].commands.removeAll { $0.id == tail[1] }
            return (200, ["ok": true])
        case let ("POST", tail) where tail.count == 3 && tail[0] == "commands" && tail[2] == "run":
            guard spaces[index].commands.contains(where: { $0.id == tail[1] }) else { return (404, ["error": "no such command"]) }
            return (200, ["ok": true, "pane_id": ""])
        default:
            return (404, ["error": "not found"])
        }
    }
}
