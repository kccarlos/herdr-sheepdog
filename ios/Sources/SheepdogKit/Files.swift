import Foundation

// The read-only file browser (docs/dev/ws-contract.md "Files", feature
// `files`): the wire shapes, the routes, and what each refusal means in
// plain words. Nothing here writes a file, on the phone or on the computer.

public enum FileBrowser {
    /// The `bot-ready` feature.
    public static let feature = "files"

    /// The daemon's own limits (docs/dev/design/file-browser.md section 3),
    /// kept here so the app can say them and not fetch what would be refused.
    public static let textLimit = 1 << 20
    public static let imageLimit = 16 << 20
    public static let listLimit = 2_000
    public static let searchLimit = 200
}

/// Which folder a files route serves: an agent's, or a space's. The client
/// never names a folder, only a path relative to it.
public enum FileScope: Hashable, Sendable {
    case agent(paneID: String)
    case space(id: String)

    /// `/api/agents/<pane>/files` or `/api/spaces/<id>/files`, percent-encoded.
    var basePath: String {
        switch self {
        case let .agent(paneID): DaemonClient.agentPath(paneID: paneID, suffix: "files")
        case let .space(id): DaemonClient.spacePath(id, "files")
        }
    }
}

// MARK: - Wire shapes

/// One entry of a folder (`GET …/files`).
public struct FileEntry: Codable, Equatable, Hashable, Identifiable, Sendable {
    public enum Kind: String, Codable, Sendable {
        case file, dir, link

        /// A kind from a newer daemon reads as a file: the viewer asks the
        /// daemon what it really is.
        public init(from decoder: Decoder) throws {
            let raw = try decoder.singleValueContainer().decode(String.self)
            self = Kind(rawValue: raw) ?? .file
        }
    }

    public var name: String
    /// Relative to the folder the scope serves, `/` between segments.
    public var path: String
    public var kind: Kind
    /// A file's size in bytes; 0 for a folder or a link.
    public var size: Int
    public var modified: Date?
    /// A link whose target leaves the folder: the daemon will refuse it.
    public var blocked: Bool?

    public var id: String { path }
    public var isFolder: Bool { kind == .dir }
    public var isLink: Bool { kind == .link }
    public var isBlocked: Bool { blocked == true }

    public init(name: String, path: String? = nil, kind: Kind = .file, size: Int = 0, modified: Date? = nil, blocked: Bool? = nil) {
        self.name = name
        self.path = path ?? name
        self.kind = kind
        self.size = size
        self.modified = modified
        self.blocked = blocked
    }
}

/// The body of `GET …/files?path=&q=`: a folder, or with `q` a search of
/// the names under it.
public struct FileListing: Codable, Equatable, Sendable {
    /// The folder's own name ("shop").
    public var rootName: String
    public var path: String
    /// More than the daemon sends: 2,000 entries, or a search cut short.
    public var truncated: Bool
    /// Names the sensitive rule left out.
    public var hidden: Int
    public var entries: [FileEntry]

    enum CodingKeys: String, CodingKey {
        case rootName = "root_name"
        case path, truncated, hidden, entries
    }

    public init(rootName: String, path: String = "", truncated: Bool = false, hidden: Int = 0, entries: [FileEntry] = []) {
        self.rootName = rootName
        self.path = path
        self.truncated = truncated
        self.hidden = hidden
        self.entries = entries
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        rootName = try c.decodeIfPresent(String.self, forKey: .rootName) ?? ""
        path = try c.decodeIfPresent(String.self, forKey: .path) ?? ""
        truncated = try c.decodeIfPresent(Bool.self, forKey: .truncated) ?? false
        hidden = try c.decodeIfPresent(Int.self, forKey: .hidden) ?? 0
        entries = try c.decodeIfPresent([FileEntry].self, forKey: .entries) ?? []
    }
}

/// The body of `GET …/files/{path}`: the file's kind from its first bytes,
/// and a text's characters (an image's bytes come from `?raw=1`).
public struct FileContent: Codable, Equatable, Sendable {
    public enum Kind: String, Codable, Sendable {
        case text, image, binary

        /// A kind from a newer daemon is something this app cannot show.
        public init(from decoder: Decoder) throws {
            let raw = try decoder.singleValueContainer().decode(String.self)
            self = Kind(rawValue: raw) ?? .binary
        }
    }

    public var path: String
    public var size: Int
    public var modified: Date?
    public var kind: Kind
    /// Absent for an image or a binary file, and for an empty one.
    public var text: String?
    /// The text was cut at 1 MiB.
    public var truncated: Bool

    enum CodingKeys: String, CodingKey { case path, size, modified, kind, text, truncated }

    public init(path: String, size: Int = 0, modified: Date? = nil, kind: Kind, text: String? = nil, truncated: Bool = false) {
        self.path = path
        self.size = size
        self.modified = modified
        self.kind = kind
        self.text = text
        self.truncated = truncated
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        path = try c.decode(String.self, forKey: .path)
        size = try c.decodeIfPresent(Int.self, forKey: .size) ?? 0
        modified = try c.decodeIfPresent(Date.self, forKey: .modified)
        kind = try c.decodeIfPresent(Kind.self, forKey: .kind) ?? .binary
        text = try c.decodeIfPresent(String.self, forKey: .text)
        truncated = try c.decodeIfPresent(Bool.self, forKey: .truncated) ?? false
    }
}

// MARK: - Refusals

/// Why the daemon would not list or read something: its `reason`
/// (docs/dev/ws-contract.md "Files", Errors), plus the one answer that is
/// not a refusal: a daemon from before the feature.
public enum FileProblem: String, Equatable, Sendable {
    case noAgent = "no_agent"
    case noSpace = "no_space"
    case noCwd = "no_cwd"
    case notFound = "not_found"
    case outside
    case sensitive
    case notFile = "not_file"
    case denied
    case tooLarge = "too_large"
    case notImage = "not_image"
    case badPath = "bad_path"
    case failed
    /// A 404 with no reason: the daemon has no files routes.
    case olderDaemon = "older_daemon"

    /// A refusal's `reason`; anything unknown is `failed`.
    public init(reason: String?) {
        self = reason.flatMap(FileProblem.init(rawValue:)) ?? .failed
    }

    /// What was being opened, for words that name it.
    public enum Subject: Sendable { case folder, file }

    /// The problem as a short state: a headline, a plain sentence with the
    /// fix, and the symbol. `root` is the folder's name ("shop") and
    /// `name` the file's.
    public struct Words: Equatable, Sendable {
        public var title: String
        public var message: String
        public var symbol: String
    }

    public func words(subject: Subject = .folder, root: String = "the folder", name: String = "This file", size: Int? = nil) -> Words {
        let what = subject == .folder ? "folder" : "file"
        switch self {
        case .noAgent:
            return Words(title: "That agent is gone", message: "Its pane closed or herdr no longer reports it.", symbol: "terminal")
        case .noSpace:
            return Words(title: "That space is gone", message: "It was closed on your computer.", symbol: "folder")
        case .noCwd:
            return Words(title: "No folder yet", message: "This agent's folder is not known or no longer exists.", symbol: "folder")
        case .notFound:
            return Words(title: "That \(what) is gone",
                         message: subject == .folder
                             ? "It was moved or deleted on your computer, or the space was closed."
                             : "It was moved or deleted on your computer.",
                         symbol: subject == .folder ? "folder" : "doc")
        case .outside:
            return Words(title: "This \(what) is outside \(root)",
                         message: "Sheepdog only shows what is inside the space's folder. Open it on your computer, or ask the agent to copy it in.",
                         symbol: "folder")
        case .sensitive:
            return Words(title: "Hidden for safety",
                         message: "Sheepdog keeps files like .env and keys off your phone. Open it on your computer.",
                         symbol: "lock")
        case .notFile:
            return Words(title: "Not a regular file",
                         message: "Sheepdog reads files and folders. Pipes, sockets and devices cannot be shown.",
                         symbol: "doc")
        case .denied:
            return Words(title: "Sheepdog can't read this \(what)",
                         message: "macOS has not let sheepdog into it. On your computer, allow it in System Settings ▸ Privacy & Security ▸ Files and Folders.",
                         symbol: "lock")
        case .tooLarge:
            let limit = "\(FileBrowser.imageLimit >> 20) MB"
            let how = size.map { "\(name) is \(FileSizeText.text($0)). " } ?? "\(name) is too big. "
            return Words(title: "Too big to show here",
                         message: "\(how)The phone shows images up to \(limit), so open it on your computer.",
                         symbol: "doc")
        case .notImage:
            return Words(title: "This file is not an image", message: "There is nothing to show here, so open it on your computer.", symbol: "doc")
        case .badPath:
            return Words(title: "That path is not valid", message: "Sheepdog can only open what is inside the folder.", symbol: "folder")
        case .failed:
            return Words(title: "Could not read this \(what)", message: "Your computer could not read it. Try again.", symbol: "exclamationmark.triangle")
        case .olderDaemon:
            return Words(title: "Update sheepdog", message: "Update sheepdog on your computer to browse files. Changes still work.", symbol: "arrow.clockwise")
        }
    }

    /// A refusal that a second try might undo.
    public var canRetry: Bool {
        switch self {
        case .denied, .failed, .notFound, .noCwd: true
        default: false
        }
    }
}

/// The daemon answered, and said no (a 4xx or 5xx with a `reason`). A
/// transport failure stays a `DaemonError`.
public struct FilesRefusal: Error, Equatable, Sendable {
    public var status: Int
    public var problem: FileProblem
    /// The daemon's own fixed words (never a path on the computer).
    public var message: String
}

// MARK: - The client

extension DaemonClient {
    /// `GET …/files?path=&q=`: a folder, or with `query` the names under it
    /// that contain it. `nil` from a daemon without the routes.
    public func files(_ scope: FileScope, path: String = "", query: String? = nil) async throws -> FileListing? {
        var pairs: [(String, String)] = []
        if !path.isEmpty { pairs.append(("path", path)) }
        if let query, !query.isEmpty { pairs.append(("q", query)) }
        let request = try filesRequest(encodedPath: scope.basePath, query: pairs)
        let (data, response) = try await perform(request)
        return try decodeFilesReply(FileListing.self, data: data, response: response)
    }

    /// `GET …/files/{path}`: a text's characters, or an image's or binary
    /// file's kind and size. `nil` from a daemon without the routes.
    public func file(_ scope: FileScope, path: String) async throws -> FileContent? {
        let request = try filesRequest(encodedPath: scope.basePath + "/" + Self.escapedFilePath(path), query: [])
        let (data, response) = try await perform(request)
        return try decodeFilesReply(FileContent.self, data: data, response: response)
    }

    /// `GET …/files/{path}?raw=1`: an image's bytes. Never decoded here.
    public func fileImage(_ scope: FileScope, path: String) async throws -> Data {
        var request = try filesRequest(encodedPath: scope.basePath + "/" + Self.escapedFilePath(path), query: [("raw", "1")])
        // 16 MiB over a slow link: the timeout is for silence, not the total.
        request.timeoutInterval = 60
        let (data, response) = try await perform(request)
        guard let http = response as? HTTPURLResponse else { throw DaemonError.malformed("no HTTP response") }
        if (200 ..< 300).contains(http.statusCode) { return data }
        throw refusal(data: data, status: http.statusCode) ?? DaemonError.http(status: http.statusCode, endpoint: endpoint.display)
    }

    /// A path in a URL: each segment escaped, the slashes kept.
    static func escapedFilePath(_ path: String) -> String {
        var allowed = CharacterSet.urlPathAllowed
        allowed.remove(charactersIn: "/%?#;")
        return path.split(separator: "/", omittingEmptySubsequences: false)
            .map { String($0).addingPercentEncoding(withAllowedCharacters: allowed) ?? String($0) }
            .joined(separator: "/")
    }

    /// A GET with a query whose values are escaped here: a file name can
    /// hold a `+` or a `&`, which a query item would leave as it is (and
    /// the daemon would read `+` as a space).
    func filesRequest(encodedPath: String, query: [(String, String)]) throws -> URLRequest {
        var request = try self.request(encodedPath: encodedPath)
        guard !query.isEmpty, let url = request.url, var components = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return request }
        var allowed = CharacterSet.urlQueryAllowed
        allowed.remove(charactersIn: "&+=#%;")
        components.percentEncodedQuery = query
            .map { "\($0.0)=\($0.1.addingPercentEncoding(withAllowedCharacters: allowed) ?? $0.1)" }
            .joined(separator: "&")
        request.url = components.url
        return request
    }

    /// 2xx decodes; a refusal with a `reason` is a `FilesRefusal`; a 404
    /// with no JSON is a daemon from before the feature (`nil`); 401 is
    /// the token.
    private func decodeFilesReply<T: Decodable>(_ type: T.Type, data: Data, response: URLResponse) throws -> T? {
        guard let http = response as? HTTPURLResponse else { throw DaemonError.malformed("no HTTP response") }
        switch http.statusCode {
        case 200 ..< 300:
            do {
                return try DaemonClient.decoder.decode(T.self, from: data)
            } catch {
                throw DaemonError.malformed("the files did not decode")
            }
        case 404 where (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] == nil:
            return nil
        default:
            throw refusal(data: data, status: http.statusCode) ?? DaemonError.http(status: http.statusCode, endpoint: endpoint.display)
        }
    }

    /// The error for a non-2xx answer: `unauthorized` for a 401, a
    /// `FilesRefusal` when the daemon said why, else nil.
    private func refusal(data: Data, status: Int) -> Error? {
        if status == 401 { return DaemonError.unauthorized(endpoint: endpoint.display) }
        guard let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] else { return nil }
        let message = object["error"] as? String ?? ""
        return FilesRefusal(status: status, problem: FileProblem(reason: object["reason"] as? String), message: message)
    }
}
