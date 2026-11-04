import Foundation

// What the file browser says and how it orders things: pure, so the words
// and the order are tested without a screen.

// MARK: - Kind (by name)

/// What a file looks like from its name, for its icon and for the viewer
/// it opens first. The daemon decides what is really inside (`FileContent.kind`,
/// from the first bytes); a name only guesses.
public enum FileKind: Equatable, Sendable {
    case folder, link, markdown, image, code, text, other

    /// A folder, a link, or a file by its name.
    public init(_ entry: FileEntry) {
        switch entry.kind {
        case .dir: self = .folder
        case .link: self = .link
        case .file: self = Self.of(name: entry.name)
        }
    }

    public static func of(name: String) -> FileKind {
        let lower = name.lowercased()
        if let special = specialNames[lower] { return special }
        guard let dot = lower.lastIndex(of: "."), dot != lower.startIndex else { return .other }
        let ext = String(lower[lower.index(after: dot)...])
        if markdownExtensions.contains(ext) { return .markdown }
        if imageExtensions.contains(ext) { return .image }
        if codeExtensions.contains(ext) { return .code }
        if textExtensions.contains(ext) { return .text }
        return .other
    }

    private static let markdownExtensions: Set<String> = ["md", "markdown", "mdx"]
    private static let imageExtensions: Set<String> = ["png", "jpg", "jpeg", "gif", "webp"]
    private static let textExtensions: Set<String> = ["txt", "log", "csv", "tsv", "rst", "text", "out"]
    private static let codeExtensions: Set<String> = [
        "go", "swift", "js", "jsx", "mjs", "cjs", "ts", "tsx", "py", "rb", "rs", "java", "kt", "kts", "c", "h", "cc", "cpp", "hpp",
        "cs", "php", "sh", "bash", "zsh", "fish", "sql", "html", "htm", "css", "scss", "json", "jsonc", "yaml", "yml", "toml", "xml",
        "svg", "lua", "pl", "r", "m", "mm", "gradle", "tf", "proto", "graphql", "vue", "svelte", "ini", "cfg", "conf", "mod", "sum",
        "lock", "plist", "env", "example", "sample", "template",
    ]
    private static let specialNames: [String: FileKind] = [
        "makefile": .code, "dockerfile": .code, "gemfile": .code, "rakefile": .code, "justfile": .code,
        "readme": .text, "license": .text, "licence": .text, "changelog": .text, "notice": .text, "authors": .text,
    ]

    /// An SF Symbol.
    public var symbol: String {
        switch self {
        case .folder: "folder"
        case .link: "link"
        case .markdown, .text: "doc.text"
        case .image: "photo"
        case .code: "chevron.left.forwardslash.chevron.right"
        case .other: "doc"
        }
    }

    /// What VoiceOver says before the name.
    public var word: String {
        switch self {
        case .folder: "Folder"
        case .link: "Link"
        case .markdown: "Markdown document"
        case .image: "Image"
        case .code: "Code file"
        case .text: "Text file"
        case .other: "File"
        }
    }
}

// MARK: - Sizes and times

public enum FileSizeText {
    /// "812 B", "2.4 KB", "184 KB", "4.2 MB": decimal units, one decimal
    /// below 10, the way a Mac's Finder counts files.
    public static func text(_ bytes: Int) -> String {
        guard bytes >= 1000 else { return "\(max(bytes, 0)) B" }
        var value = Double(bytes)
        for unit in ["KB", "MB", "GB", "TB"] {
            value /= 1000
            let rounded = (value * 10).rounded() / 10
            if rounded < 1000 || unit == "TB" {
                if rounded >= 10 || rounded == rounded.rounded() { return "\(Int(rounded.rounded())) \(unit)" }
                return "\(rounded) \(unit)"
            }
        }
        return "\(bytes) B"
    }
}

public enum FileTimeText {
    /// "just now", "3 min ago", "2 hr ago", "yesterday", "3 days ago",
    /// "last week", then "Oct 21" (with the year when it is not this one).
    public static func relative(_ date: Date, now: Date, calendar: Calendar = .current) -> String {
        let seconds = now.timeIntervalSince(date)
        if seconds < 0 { return "just now" }
        if seconds < 60 { return "just now" }
        if seconds < 3600 { return "\(Int(seconds / 60)) min ago" }
        let days = dayDistance(from: date, to: now, calendar: calendar)
        if days == 0 { return "\(Int(seconds / 3600)) hr ago" }
        if days == 1 { return "yesterday" }
        if days < 7 { return "\(days) days ago" }
        if days < 14 { return "last week" }
        let sameYear = calendar.component(.year, from: date) == calendar.component(.year, from: now)
        var style = Date.FormatStyle(calendar: calendar, timeZone: calendar.timeZone).month(.abbreviated).day()
        if !sameYear { style = style.year() }
        return date.formatted(style.locale(Locale(identifier: "en_US")))
    }

    /// "4:12 PM".
    public static func clock(_ date: Date, calendar: Calendar = .current) -> String {
        date.formatted(Date.FormatStyle(calendar: calendar, timeZone: calendar.timeZone).hour().minute().locale(Locale(identifier: "en_US")))
    }

    /// Whole calendar days from `from` to `to` (0: the same day).
    static func dayDistance(from: Date, to: Date, calendar: Calendar) -> Int {
        let a = calendar.startOfDay(for: from)
        let b = calendar.startOfDay(for: to)
        return calendar.dateComponents([.day], from: a, to: b).day ?? 0
    }
}

// MARK: - Sorting and grouping

/// How a folder's entries are ordered.
public enum FileSort: String, CaseIterable, Identifiable, Sendable {
    /// The daemon's order: folders first, then by name ignoring case.
    case name
    /// Newest first, folders among the files.
    case recent

    public var id: String { rawValue }

    /// The pill's words.
    public var title: String { self == .name ? "Name" : "Recently changed" }

    public func ordered(_ entries: [FileEntry]) -> [FileEntry] {
        switch self {
        case .name:
            return entries
        case .recent:
            // An entry with no time is the oldest; ties keep the daemon's order.
            return entries.enumerated().sorted { a, b in
                let (x, y) = (a.element.modified ?? .distantPast, b.element.modified ?? .distantPast)
                return x != y ? x > y : a.offset < b.offset
            }.map(\.element)
        }
    }
}

/// A run of entries under one day word, for the recent order.
public struct FileGroup: Equatable, Identifiable, Sendable {
    public enum Bucket: Int, Comparable, Sendable {
        case today, yesterday, lastWeek, older

        public static func < (a: Bucket, b: Bucket) -> Bool { a.rawValue < b.rawValue }

        public var title: String {
            switch self {
            case .today: "Today"
            case .yesterday: "Yesterday"
            case .lastWeek: "Last 7 days"
            case .older: "Older"
            }
        }
    }

    public var bucket: Bucket
    public var entries: [FileEntry]
    public var id: Int { bucket.rawValue }

    public static func bucket(for date: Date?, now: Date, calendar: Calendar = .current) -> Bucket {
        guard let date else { return .older }
        switch FileTimeText.dayDistance(from: date, to: now, calendar: calendar) {
        case ..<1: return .today
        case 1: return .yesterday
        case 2 ..< 7: return .lastWeek
        default: return .older
        }
    }

    /// `entries` (already in the recent order) under their day words.
    public static func groups(_ entries: [FileEntry], now: Date, calendar: Calendar = .current) -> [FileGroup] {
        var out: [FileGroup] = []
        for entry in entries {
            let bucket = bucket(for: entry.modified, now: now, calendar: calendar)
            if let last = out.last, last.bucket == bucket {
                out[out.count - 1].entries.append(entry)
            } else {
                out.append(FileGroup(bucket: bucket, entries: [entry]))
            }
        }
        return out
    }
}

// MARK: - Row words

public enum FileRowText {
    /// The line under a name: "12 KB · 3 min ago". A folder has only its
    /// time (the daemon does not count what is inside); a link says where
    /// it stands. In the recent order, yesterday's files read the time of
    /// day, which is what the day word above them leaves out.
    public static func detail(_ entry: FileEntry, sort: FileSort = .name, now: Date, calendar: Calendar = .current) -> String {
        var parts: [String] = []
        switch entry.kind {
        case .file: parts.append(FileSizeText.text(entry.size))
        case .link: parts.append(entry.isBlocked ? "Link out of this folder" : "Link")
        case .dir: break
        }
        if let date = entry.modified {
            if sort == .recent, FileGroup.bucket(for: date, now: now, calendar: calendar) == .yesterday {
                parts.append(FileTimeText.clock(date, calendar: calendar))
            } else {
                parts.append(FileTimeText.relative(date, now: now, calendar: calendar))
            }
        }
        return parts.joined(separator: " · ")
    }

    /// A search result's second line: the folder it is in ("docs/reports"),
    /// the folder's own name at the top.
    public static func location(_ entry: FileEntry, rootName: String) -> String {
        let parent = FilePath.parent(of: entry.path)
        return parent.isEmpty ? rootName : parent
    }

    /// What VoiceOver reads for a row: kind, name, then the detail.
    public static func spoken(_ entry: FileEntry, detail: String, changed: Bool) -> String {
        var parts = ["\(FileKind(entry).word) \(entry.name)"]
        if changed { parts.append("changed") }
        if entry.isBlocked { parts.append("outside this folder") }
        if !detail.isEmpty { parts.append(detail) }
        return parts.joined(separator: ", ")
    }

    /// "3 files named “login” in shop".
    public static func searchSummary(_ listing: FileListing, query: String, scope: String) -> String {
        let count = listing.entries.count
        let allFiles = listing.entries.allSatisfy { !$0.isFolder }
        let noun = allFiles ? (count == 1 ? "file" : "files") : (count == 1 ? "item" : "items")
        return "\(count) \(noun) named “\(query)” in \(scope)"
    }

    /// The footer for what the sensitive rule left out.
    public static func hiddenNote(_ hidden: Int) -> String? {
        guard hidden > 0 else { return nil }
        return "\(hidden) \(hidden == 1 ? "file" : "files") hidden for safety, like .env and keys."
    }

    /// The footer for a list the daemon cut short.
    public static func truncatedNote(searching: Bool, count: Int) -> String {
        searching
            ? "Showing the first \(count) matches. Type more of the name to narrow it down."
            : "Showing the first \(count.formatted(.number.locale(Locale(identifier: "en_US")))) items. Search by name to find the rest."
    }
}

// MARK: - Paths

public enum FilePath {
    /// "docs/reports/a.md" → "docs/reports"; "a.md" → "".
    public static func parent(of path: String) -> String {
        guard let slash = path.lastIndex(of: "/") else { return "" }
        return String(path[..<slash])
    }

    public static func name(of path: String) -> String {
        path.split(separator: "/").last.map(String.init) ?? path
    }

    /// Where a link written in the file `file` points, as a path relative
    /// to the folder: `../images/a.png` from `docs/reports/a.md` is
    /// `docs/images/a.png`. Nil for what is not a file of this folder: a
    /// link with a scheme (http, mailto), an absolute path, one that climbs
    /// above the folder, or just a `#fragment`.
    public static func resolve(_ link: String, from file: String) -> String? {
        var text = link.trimmingCharacters(in: .whitespaces)
        if let cut = text.firstIndex(where: { $0 == "#" || $0 == "?" }) { text = String(text[..<cut]) }
        guard !text.isEmpty, !text.hasPrefix("/"), !text.hasPrefix("\\"), !hasScheme(text) else { return nil }
        let decoded = text.removingPercentEncoding ?? text
        var parts = parent(of: file).split(separator: "/").map(String.init)
        for part in decoded.split(separator: "/", omittingEmptySubsequences: true) {
            switch part {
            case ".": continue
            case "..":
                guard !parts.isEmpty else { return nil }
                parts.removeLast()
            default: parts.append(String(part))
            }
        }
        return parts.isEmpty ? nil : parts.joined(separator: "/")
    }

    private static func hasScheme(_ text: String) -> Bool {
        guard let colon = text.firstIndex(of: ":") else { return false }
        let scheme = text[..<colon]
        // "a:b/c" in a file name is not a scheme if a slash comes first.
        return !scheme.isEmpty && !scheme.contains("/") && scheme.first?.isLetter == true
    }
}

/// One step of the path above a list: "shop ▸ docs ▸ reports".
public struct FileCrumb: Equatable, Identifiable, Sendable {
    public var name: String
    /// The folder this step opens ("" for the root).
    public var path: String
    public var id: String { path }

    public static func crumbs(rootName: String, path: String) -> [FileCrumb] {
        var out = [FileCrumb(name: rootName, path: "")]
        var walked: [String] = []
        for part in path.split(separator: "/") {
            walked.append(String(part))
            out.append(FileCrumb(name: String(part), path: walked.joined(separator: "/")))
        }
        return out
    }
}

// MARK: - Text, in lines

/// A text cut into numbered lines, for the code view.
public struct CodeLine: Equatable, Identifiable, Sendable {
    public var number: Int
    public var text: String
    public var id: Int { number }
}

public enum CodeLines {
    /// The lines of `text`, tabs as four spaces; a final newline does not
    /// make an extra empty line, and an empty text has none.
    public static func split(_ text: String) -> [CodeLine] {
        guard !text.isEmpty else { return [] }
        var lines = text.replacingOccurrences(of: "\r\n", with: "\n").replacingOccurrences(of: "\r", with: "\n")
            .split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        if lines.last == "" { lines.removeLast() }
        return lines.enumerated().map { CodeLine(number: $0.offset + 1, text: $0.element.replacingOccurrences(of: "\t", with: "    ")) }
    }
}

// MARK: - Which files changed

/// The agent's changes (git paths, from the repository's root) seen from
/// the browsed folder (paths from `cwd`): which listed file is changed.
/// The two agree when the folder is the repository's root; below it, the
/// folder's own path is cut from the front.
public struct ChangeIndex: Equatable, Sendable {
    private var byPath: [String: ChangedFile] = [:]

    public init(_ changes: AgentChanges?, folder: String?) {
        guard let changes, changes.repo else { return }
        var prefix = ""
        if let root = changes.root?.trimmingTrailingSlash, let folder = folder?.trimmingTrailingSlash,
           folder.hasPrefix(root + "/") {
            prefix = String(folder.dropFirst(root.count + 1)) + "/"
        }
        for file in changes.files {
            if prefix.isEmpty {
                byPath[file.path] = file
            } else if file.path.hasPrefix(prefix) {
                byPath[String(file.path.dropFirst(prefix.count))] = file
            }
        }
        self.prefix = prefix
    }

    /// What the changes' paths carry in front of the browsed folder's.
    public private(set) var prefix = ""

    public func change(at path: String) -> ChangedFile? { byPath[path] }
    public func isChanged(_ path: String) -> Bool { byPath[path] != nil }
}

private extension String {
    var trimmingTrailingSlash: String { hasSuffix("/") && count > 1 ? String(dropLast()) : self }
}
