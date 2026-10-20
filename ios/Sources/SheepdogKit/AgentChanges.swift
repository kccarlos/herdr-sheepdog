import Foundation

// Phase 4 of docs/dev/design/phone-terminal.md: attachments and the
// read-only changes view. Wire shapes are in docs/dev/ws-contract.md
// ("Attachments", "Changes").

/// One changed file (`GET /api/agents/{pane}/changes`).
public struct ChangedFile: Codable, Equatable, Hashable, Identifiable, Sendable {
    public var path: String
    public var oldPath: String?
    /// modified, added, deleted, renamed, typechange, untracked, conflicted.
    public var status: String
    public var added: Int
    public var deleted: Int
    public var binary: Bool?

    public var id: String { path }
    public var isBinary: Bool { binary == true }

    /// The last path component.
    public var fileName: String { path.split(separator: "/").last.map(String.init) ?? path }
    /// The folder, "" at the top level.
    public var folder: String {
        guard let slash = path.lastIndex(of: "/") else { return "" }
        return String(path[..<slash])
    }

    public init(path: String, oldPath: String? = nil, status: String, added: Int = 0, deleted: Int = 0, binary: Bool? = nil) {
        self.path = path
        self.oldPath = oldPath
        self.status = status
        self.added = added
        self.deleted = deleted
        self.binary = binary
    }

    enum CodingKeys: String, CodingKey {
        case path, status, added, deleted, binary
        case oldPath = "old_path"
    }
}

/// The body of `GET /api/agents/{pane}/changes`.
public struct AgentChanges: Codable, Equatable, Sendable {
    public var repo: Bool
    /// no_cwd, not_repo or no_git when `repo` is false.
    public var reason: String?
    public var root: String?
    public var branch: String?
    public var files: [ChangedFile]
    public var added: Int
    public var deleted: Int
    public var truncated: Bool?

    public init(repo: Bool, reason: String? = nil, root: String? = nil, branch: String? = nil, files: [ChangedFile] = [], added: Int = 0, deleted: Int = 0, truncated: Bool? = nil) {
        self.repo = repo
        self.reason = reason
        self.root = root
        self.branch = branch
        self.files = files
        self.added = added
        self.deleted = deleted
        self.truncated = truncated
    }

    /// "fix-login · 3 files"
    public var summary: String {
        let count = files.count == 1 ? "1 file" : "\(files.count) files"
        if let branch, !branch.isEmpty { return "\(branch) · \(count)" }
        return count
    }

    /// The plain words for a folder without a repository.
    public var noRepoMessage: String {
        switch reason {
        case "no_git": return "git is not installed on your computer."
        case "no_cwd": return "This agent's folder is not known or no longer exists."
        default: return "This folder is not a git repository."
        }
    }
}

/// The body of `GET /api/agents/{pane}/changes/{path}`.
public struct FileDiff: Codable, Equatable, Sendable {
    public var path: String
    public var status: String
    public var binary: Bool?
    public var diff: String
    public var truncated: Bool?

    public init(path: String, status: String, binary: Bool? = nil, diff: String, truncated: Bool? = nil) {
        self.path = path
        self.status = status
        self.binary = binary
        self.diff = diff
        self.truncated = truncated
    }
}

/// The body of `POST /api/agents/{pane}/attachments`.
public struct AttachmentReply: Codable, Equatable, Sendable {
    /// The file's path on the computer, put into the prompt.
    public var path: String
    public var name: String
    public var bytes: Int
    public var type: String
}

public enum Attachments {
    /// The daemon's cap.
    public static let maxBytes = 20 << 20
    public static let tooBigMessage = "Too big (max 20 MB)"

    /// `draft` with `path` added where a prompt reads it: after the text,
    /// on its own word.
    public static func inserting(_ path: String, into draft: String) -> String {
        if draft.isEmpty { return path + " " }
        let sep = draft.last?.isWhitespace == true ? "" : " "
        return draft + sep + path + " "
    }

    /// A multipart/form-data body with one `file` field.
    public static func multipartBody(data: Data, fileName: String, mimeType: String, boundary: String) -> Data {
        let safeName = fileName.replacingOccurrences(of: "\"", with: "").replacingOccurrences(of: "\r", with: "").replacingOccurrences(of: "\n", with: "")
        var body = Data()
        body.append(Data("--\(boundary)\r\n".utf8))
        body.append(Data("Content-Disposition: form-data; name=\"file\"; filename=\"\(safeName)\"\r\n".utf8))
        body.append(Data("Content-Type: \(mimeType)\r\n\r\n".utf8))
        body.append(data)
        body.append(Data("\r\n--\(boundary)--\r\n".utf8))
        return body
    }
}

extension DaemonClient {
    /// `GET /api/agents/{pane}/changes`. `nil` from a daemon without the route.
    public func agentChanges(paneID: String) async throws -> AgentChanges? {
        let request = try request(encodedPath: DaemonClient.agentPath(paneID: paneID, suffix: "changes"))
        let (data, response) = try await perform(request)
        return try decodeAgentReply(AgentChanges.self, data: data, response: response, what: "the changes")
    }

    /// `GET /api/agents/{pane}/changes/{path}`.
    public func agentDiff(paneID: String, path: String) async throws -> FileDiff? {
        var allowed = CharacterSet.urlPathAllowed
        allowed.remove(charactersIn: "/%?#;")
        let escaped = path.split(separator: "/", omittingEmptySubsequences: false)
            .map { String($0).addingPercentEncoding(withAllowedCharacters: allowed) ?? String($0) }
            .joined(separator: "/")
        let request = try request(encodedPath: DaemonClient.agentPath(paneID: paneID, suffix: "changes/" + escaped))
        let (data, response) = try await perform(request)
        return try decodeAgentReply(FileDiff.self, data: data, response: response, what: "the diff")
    }

    /// `POST /api/agents/{pane}/attachments`. A daemon without the route
    /// is a refusal with plain words.
    public func uploadAttachment(paneID: String, data: Data, fileName: String, mimeType: String) async throws -> AttachmentReply {
        guard data.count <= Attachments.maxBytes else {
            throw DaemonError.rejected(status: 413, message: Attachments.tooBigMessage)
        }
        let boundary = "sheepdog-\(UUID().uuidString)"
        var request = try request(encodedPath: DaemonClient.agentPath(paneID: paneID, suffix: "attachments"), method: "POST")
        request.httpBody = Attachments.multipartBody(data: data, fileName: fileName, mimeType: mimeType, boundary: boundary)
        request.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")
        request.timeoutInterval = 120
        let (body, response) = try await perform(request)
        if (response as? HTTPURLResponse)?.statusCode == 413 {
            throw DaemonError.rejected(status: 413, message: Attachments.tooBigMessage)
        }
        guard let reply = try decodeAgentReply(AttachmentReply.self, data: body, response: response, what: "the upload reply") else {
            throw DaemonError.rejected(status: 404, message: "Update sheepdog on your computer to attach files.")
        }
        return reply
    }
}

// MARK: - Diff

/// One line of a unified diff, ready to draw.
public struct DiffLine: Equatable, Identifiable, Sendable {
    public enum Kind: Equatable, Sendable {
        case hunk, context, added, removed, meta
    }

    /// A run of text; `changed` marks the words that differ from the
    /// paired line (the word highlight).
    public struct Segment: Equatable, Sendable {
        public var text: String
        public var changed: Bool
        public init(_ text: String, changed: Bool = false) {
            self.text = text
            self.changed = changed
        }
    }

    public var id: Int
    public var kind: Kind
    public var oldNumber: Int?
    public var newNumber: Int?
    public var segments: [Segment]

    public var text: String { segments.map(\.text).joined() }
}

public enum DiffParser {
    /// Parses git's unified diff for one file: the file header lines are
    /// dropped, hunk headers kept, and each removed line paired with the
    /// added line at the same place in its run gets word highlights.
    public static func parse(_ diff: String) -> [DiffLine] {
        var lines: [DiffLine] = []
        var oldNo = 0, newNo = 0
        var inHunk = false
        var removed: [Int] = [], added: [Int] = []

        func flushPairs() {
            for (r, a) in zip(removed, added) {
                let (old, new) = wordHighlights(lines[r].text, lines[a].text)
                lines[r].segments = old
                lines[a].segments = new
            }
            removed.removeAll()
            added.removeAll()
        }

        for raw in diff.split(separator: "\n", omittingEmptySubsequences: false) {
            var line = String(raw)
            if line.hasSuffix("\r") { line.removeLast() }
            if line.hasPrefix("@@") {
                flushPairs()
                inHunk = true
                (oldNo, newNo) = hunkStarts(line)
                lines.append(DiffLine(id: lines.count, kind: .hunk, segments: [.init(line)]))
                continue
            }
            guard inHunk else { continue }
            switch line.first {
            case "+":
                lines.append(DiffLine(id: lines.count, kind: .added, newNumber: newNo, segments: [.init(String(line.dropFirst()))]))
                added.append(lines.count - 1)
                newNo += 1
            case "-":
                if !added.isEmpty { flushPairs() }
                lines.append(DiffLine(id: lines.count, kind: .removed, oldNumber: oldNo, segments: [.init(String(line.dropFirst()))]))
                removed.append(lines.count - 1)
                oldNo += 1
            case " ":
                flushPairs()
                lines.append(DiffLine(id: lines.count, kind: .context, oldNumber: oldNo, newNumber: newNo, segments: [.init(String(line.dropFirst()))]))
                oldNo += 1
                newNo += 1
            case "\\":
                lines.append(DiffLine(id: lines.count, kind: .meta, segments: [.init(String(line.dropFirst(2)))]))
            default:
                // The empty string after the final newline, or a new file
                // header in a multi-file diff.
                flushPairs()
                if line.hasPrefix("diff ") { inHunk = false }
            }
        }
        flushPairs()
        return lines
    }

    /// "@@ -88,6 +88,8 @@" → (88, 88).
    static func hunkStarts(_ header: String) -> (Int, Int) {
        var old = 0, new = 0
        for part in header.split(separator: " ") {
            if part.hasPrefix("-"), let n = Int(part.dropFirst().split(separator: ",").first ?? "") { old = n }
            if part.hasPrefix("+"), let n = Int(part.dropFirst().split(separator: ",").first ?? "") { new = n }
        }
        return (old, new)
    }

    /// Splits into words and the runs between them.
    static func tokens(_ s: String) -> [String] {
        var out: [String] = []
        var current = ""
        var currentIsWord: Bool?
        for ch in s {
            let isWord = ch.isLetter || ch.isNumber || ch == "_"
            if isWord, currentIsWord == true {
                current.append(ch)
            } else {
                if !current.isEmpty { out.append(current) }
                current = String(ch)
                currentIsWord = isWord
            }
        }
        if !current.isEmpty { out.append(current) }
        return out
    }

    /// The changed middle of two lines, by tokens: the common prefix and
    /// suffix stay plain, the rest is highlighted. Lines with nothing in
    /// common are left plain (the whole-line tint says enough).
    public static func wordHighlights(_ old: String, _ new: String) -> ([DiffLine.Segment], [DiffLine.Segment]) {
        let a = tokens(old), b = tokens(new)
        var prefix = 0
        while prefix < a.count, prefix < b.count, a[prefix] == b[prefix] { prefix += 1 }
        var suffix = 0
        while suffix < a.count - prefix, suffix < b.count - prefix, a[a.count - 1 - suffix] == b[b.count - 1 - suffix] { suffix += 1 }
        if prefix == 0 && suffix == 0 { return ([.init(old)], [.init(new)]) }
        func split(_ t: [String]) -> [DiffLine.Segment] {
            let head = t[..<prefix].joined()
            let mid = t[prefix ..< (t.count - suffix)].joined()
            let tail = t[(t.count - suffix)...].joined()
            return [DiffLine.Segment(head), DiffLine.Segment(mid, changed: true), DiffLine.Segment(tail)].filter { !$0.text.isEmpty }
        }
        return (split(a), split(b))
    }
}
