import Foundation

// Light syntax colours for the code viewer: no grammar and no dependency,
// only the five things a reader's eye wants (comments, strings, numbers,
// keywords, calls). One pass over the bytes of each line, with the little
// state a block comment or a multi-line string carries from one line to the
// next, so a 1 MB file costs one linear scan, made once and off the main
// actor (`CodeLines.split`).

/// What a coloured run is.
public enum SyntaxKind: UInt8, Equatable, Sendable {
    case keyword, string, number, comment, call
}

/// A run of one line, in UTF-8 offsets of the line's text.
public struct SyntaxSpan: Equatable, Sendable {
    public var start: Int
    public var length: Int
    public var kind: SyntaxKind

    public init(start: Int, length: Int, kind: SyntaxKind) {
        self.start = start
        self.length = length
        self.kind = kind
    }
}

/// The few traits of a language that the colours depend on.
public struct SyntaxLanguage: Sendable {
    var keywords: Set<String>
    var lineComments: [[UInt8]]
    var blockComment: (open: [UInt8], close: [UInt8])?
    /// Quotes that end on their line (or at the line's end if unclosed).
    var quotes: [UInt8]
    /// Delimiters that can run over several lines (`"""`, a backtick).
    var multiline: [[UInt8]]
    /// A backslash in a string escapes the next byte.
    var escapes = true
    /// The same, inside a multi-line delimiter (not in Go's raw strings).
    var multilineEscapes = true
    /// A name followed by `(` is coloured as a call.
    var calls = true

    /// Lines longer than this are left plain: a minified bundle is no
    /// reading, and scanning it costs more than it is worth.
    public static let longestLine = 2_000

    /// The language of a file name (its extension, or a well-known name), or
    /// nil for a text that gets no colours.
    public static func of(name: String) -> SyntaxLanguage? {
        let lower = name.lowercased()
        if let special = byName[lower] { return special }
        guard let dot = lower.lastIndex(of: "."), dot != lower.startIndex else { return nil }
        return byExtension[String(lower[lower.index(after: dot)...])]
    }

    private static func make(
        _ words: String, line: [String], block: (String, String)? = nil, quotes: String = "\"'",
        multiline: [String] = [], multilineEscapes: Bool = true, calls: Bool = true
    ) -> SyntaxLanguage {
        SyntaxLanguage(
            keywords: Set(words.split(separator: " ").map(String.init)),
            lineComments: line.map { Array($0.utf8) },
            blockComment: block.map { (Array($0.0.utf8), Array($0.1.utf8)) },
            quotes: Array(quotes.utf8),
            multiline: multiline.map { Array($0.utf8) },
            multilineEscapes: multilineEscapes,
            calls: calls
        )
    }

    private static let slashes = ["//"]
    private static let cBlock = ("/*", "*/")

    private static let go = make(
        "break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var true false nil iota",
        line: slashes, block: cBlock, quotes: "\"'", multiline: ["`"], multilineEscapes: false)
    private static let swift = make(
        "actor any as associatedtype async await break case catch class continue default defer do else enum extension fallthrough false fileprivate for func guard if import in indirect init inout internal is let nil nonisolated open operator override precedencegroup private protocol public repeat rethrows return self Self static struct subscript super switch throw throws true try typealias var weak where while",
        line: slashes, block: cBlock, multiline: ["\"\"\""])
    private static let javascript = make(
        "as async await break case catch class const continue debugger default delete do else enum export extends false finally for from function if implements import in instanceof interface let new null of private protected public return static super switch this throw true try type typeof undefined var void while with yield",
        line: slashes, block: cBlock, quotes: "\"'", multiline: ["`"])
    private static let python = make(
        "False None True and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass raise return try while with yield self",
        line: ["#"], multiline: ["\"\"\"", "'''"])
    private static let rust = make(
        "as async await break const continue crate dyn else enum extern false fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait true type unsafe use where while",
        line: slashes, block: cBlock)
    private static let java = make(
        "abstract as assert boolean break byte case catch char class const continue data default do double else enum extends false final finally float for fun if implements import in instanceof int interface is long namespace new null object override package private protected public return short static super switch synchronized this throw throws true try val var void volatile when while using string",
        line: slashes, block: cBlock, multiline: ["\"\"\""])
    private static let c = make(
        "auto break case char const continue default do double else enum extern false float for goto if inline int long namespace new nullptr private protected public register return short signed sizeof static struct switch template this true typedef typename union unsigned using virtual void volatile while class delete bool include define ifdef ifndef endif",
        line: slashes, block: cBlock)
    private static let ruby = make(
        "BEGIN END alias and begin break case class def defined do else elsif end ensure false for if in module next nil not or redo rescue retry return self super then true undef unless until when while yield",
        line: ["#"], quotes: "\"'")
    private static let shell = make(
        "if then else elif fi for while until do done case esac in function select return exit export local readonly set unset source alias echo cd true false",
        line: ["#"], calls: false)
    private static let json = make("true false null", line: [], calls: false)
    private static let config = make("true false null yes no on off", line: ["#"], calls: false)
    private static let sql = make(
        "select from where and or not insert into values update set delete create table drop alter add index primary key foreign references join left right inner outer on group by order having limit offset as distinct union all null is in like between exists case when then else end",
        line: ["--"], block: cBlock, quotes: "\"'")

    private static let byExtension: [String: SyntaxLanguage] = {
        var out: [String: SyntaxLanguage] = [:]
        func add(_ language: SyntaxLanguage, _ extensions: String) {
            for ext in extensions.split(separator: " ") { out[String(ext)] = language }
        }
        add(go, "go")
        add(swift, "swift")
        add(javascript, "js jsx mjs cjs ts tsx")
        add(python, "py")
        add(rust, "rs")
        add(java, "java kt kts cs")
        add(c, "c h cc cpp hpp m mm")
        add(ruby, "rb")
        add(shell, "sh bash zsh fish")
        add(json, "json jsonc")
        add(config, "yaml yml toml ini cfg conf env")
        add(sql, "sql")
        return out
    }()

    private static let byName: [String: SyntaxLanguage] = [
        "makefile": config, "dockerfile": config, "justfile": config, "gemfile": ruby, "rakefile": ruby,
    ]
}

public enum SyntaxHighlighter {
    /// What a line starts in: code, or inside something from an earlier line.
    private enum State {
        case code
        case block
        case multiline([UInt8])
    }

    /// The coloured runs of each line, one array per line, in order. The
    /// lines are the displayed text (tabs already spaces).
    public static func spans(of lines: [String], in language: SyntaxLanguage) -> [[SyntaxSpan]] {
        var state = State.code
        var out: [[SyntaxSpan]] = []
        out.reserveCapacity(lines.count)
        for line in lines {
            guard line.utf8.count <= SyntaxLanguage.longestLine else {
                out.append([])
                continue
            }
            out.append(scan(line, language, &state))
        }
        return out
    }

    // MARK: One line

    private static func scan(_ line: String, _ language: SyntaxLanguage, _ state: inout State) -> [SyntaxSpan] {
        let bytes = Array(line.utf8)
        let n = bytes.count
        var spans: [SyntaxSpan] = []
        var i = 0

        func add(_ start: Int, _ end: Int, _ kind: SyntaxKind) {
            if end > start { spans.append(SyntaxSpan(start: start, length: end - start, kind: kind)) }
        }

        // What an earlier line left open.
        switch state {
        case .code: break
        case .block:
            guard let close = language.blockComment?.close, let end = find(close, in: bytes, from: 0) else {
                add(0, n, .comment)
                return spans
            }
            i = end + close.count
            add(0, i, .comment)
            state = .code
        case let .multiline(delimiter):
            guard let end = closing(delimiter, in: bytes, from: 0, escapes: language.multilineEscapes) else {
                add(0, n, .string)
                return spans
            }
            i = end + delimiter.count
            add(0, i, .string)
            state = .code
        }

        while i < n {
            let byte = bytes[i]
            if language.lineComments.contains(where: { matches($0, in: bytes, at: i) }) {
                add(i, n, .comment)
                return spans
            }
            if let block = language.blockComment, matches(block.open, in: bytes, at: i) {
                if let end = find(block.close, in: bytes, from: i + block.open.count) {
                    add(i, end + block.close.count, .comment)
                    i = end + block.close.count
                    continue
                }
                add(i, n, .comment)
                state = .block
                return spans
            }
            if let open = language.multiline.first(where: { matches($0, in: bytes, at: i) }) {
                if let end = closing(open, in: bytes, from: i + open.count, escapes: language.multilineEscapes) {
                    add(i, end + open.count, .string)
                    i = end + open.count
                    continue
                }
                add(i, n, .string)
                state = .multiline(open)
                return spans
            }
            if language.quotes.contains(byte) {
                let end = closing([byte], in: bytes, from: i + 1, escapes: language.escapes).map { $0 + 1 } ?? n
                add(i, end, .string)
                i = end
                continue
            }
            if isDigit(byte), i == 0 || !isWord(bytes[i - 1]) {
                var end = i + 1
                while end < n, isDigit(bytes[end]) || isLetter(bytes[end]) || bytes[end] == UInt8(ascii: ".") || bytes[end] == UInt8(ascii: "_") { end += 1 }
                add(i, end, .number)
                i = end
                continue
            }
            if isLetter(byte) || byte == UInt8(ascii: "_"), i == 0 || !isWord(bytes[i - 1]) {
                var end = i + 1
                while end < n, isWord(bytes[end]) { end += 1 }
                let word = String(decoding: bytes[i ..< end], as: UTF8.self)
                if language.keywords.contains(word) {
                    add(i, end, .keyword)
                } else if language.calls, nextNonSpace(bytes, from: end) == UInt8(ascii: "(") {
                    add(i, end, .call)
                }
                i = end
                continue
            }
            i += 1
        }
        return spans
    }

    // MARK: Bytes

    private static func isDigit(_ b: UInt8) -> Bool { b >= 48 && b <= 57 }
    private static func isLetter(_ b: UInt8) -> Bool { (b >= 65 && b <= 90) || (b >= 97 && b <= 122) }
    /// A letter, a digit, an underscore, or part of a non-ASCII character.
    private static func isWord(_ b: UInt8) -> Bool { isDigit(b) || isLetter(b) || b == UInt8(ascii: "_") || b >= 0x80 }

    private static func nextNonSpace(_ bytes: [UInt8], from index: Int) -> UInt8? {
        var i = index
        while i < bytes.count, bytes[i] == UInt8(ascii: " ") { i += 1 }
        return i < bytes.count ? bytes[i] : nil
    }

    private static func matches(_ pattern: [UInt8], in bytes: [UInt8], at index: Int) -> Bool {
        guard !pattern.isEmpty, index + pattern.count <= bytes.count else { return false }
        for k in 0 ..< pattern.count where bytes[index + k] != pattern[k] { return false }
        return true
    }

    private static func find(_ pattern: [UInt8], in bytes: [UInt8], from start: Int) -> Int? {
        guard !pattern.isEmpty, start >= 0 else { return nil }
        var i = start
        while i + pattern.count <= bytes.count {
            if matches(pattern, in: bytes, at: i) { return i }
            i += 1
        }
        return nil
    }

    /// Where `delimiter` closes, skipping what a backslash escapes.
    private static func closing(_ delimiter: [UInt8], in bytes: [UInt8], from start: Int, escapes: Bool) -> Int? {
        var i = start
        while i + delimiter.count <= bytes.count {
            if escapes, bytes[i] == UInt8(ascii: "\\") {
                i += 2
                continue
            }
            if matches(delimiter, in: bytes, at: i) { return i }
            i += 1
        }
        return nil
    }
}
