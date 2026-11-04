import Foundation

/// The markdown the workers and the orchestrator write, made readable on a phone
/// without letting it take over the layout: inline styles (bold, italic,
/// `code`, links) through Foundation's `AttributedString(markdown:)`, and a
/// small block pass on top — paragraphs, bullet and numbered lists, fenced
/// code as a monospaced block, GitHub-style tables. Headings are bold body
/// text, never large.
///
/// Everything comes out as one `AttributedString`, so a SwiftUI `Text`
/// shows it with its own `lineLimit`, selection and VoiceOver reading.
public enum MarkdownRenderer {
    public struct ListItem: Equatable, Sendable {
        /// "•", or "3." for a numbered item.
        public var marker: String
        /// Leading spaces in the source, in levels of two.
        public var depth: Int
        public var text: String
    }

    /// A GitHub-style table: a header row, a delimiter row, body rows.
    /// Every row has as many cells as the header.
    public struct Table: Equatable, Sendable {
        public enum Alignment: Equatable, Sendable { case leading, center, trailing }

        public var header: [String]
        public var alignments: [Alignment]
        public var rows: [[String]]

        public init(header: [String], alignments: [Alignment]? = nil, rows: [[String]]) {
            self.header = header
            self.alignments = alignments ?? Array(repeating: .leading, count: header.count)
            self.rows = rows
        }
    }

    public enum Block: Equatable, Sendable {
        case paragraph(String)
        case heading(String)
        case list([ListItem])
        case code(String)
        case table(Table)
        /// A paragraph that is only `![alt](source)`. A view with no way to
        /// draw it shows the alt text, as the one-`Text` rendering does.
        case image(alt: String, source: String)
    }

    // MARK: - Blocks

    /// Splits the text into blocks. Lines are kept as written inside a
    /// paragraph (a worker's line breaks are usually meant).
    public static func blocks(_ text: String) -> [Block] {
        var blocks: [Block] = []
        var paragraph: [String] = []
        var list: [ListItem] = []
        var code: [String]?

        func flushParagraph() {
            if !paragraph.isEmpty {
                let text = paragraph.joined(separator: "\n")
                if let (alt, source) = loneImage(text) {
                    blocks.append(.image(alt: alt, source: source))
                } else {
                    blocks.append(.paragraph(text))
                }
            }
            paragraph = []
        }
        func flushList() {
            if !list.isEmpty { blocks.append(.list(list)) }
            list = []
        }

        let lines = text.replacingOccurrences(of: "\r\n", with: "\n").components(separatedBy: "\n")
        var index = 0
        while index < lines.count {
            let line = lines[index]
            index += 1
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if var open = code {
                if trimmed.hasPrefix("```") || trimmed.hasPrefix("~~~") {
                    blocks.append(.code(open.joined(separator: "\n")))
                    code = nil
                } else {
                    open.append(line)
                    code = open
                }
                continue
            }
            if trimmed.hasPrefix("```") || trimmed.hasPrefix("~~~") {
                flushParagraph(); flushList()
                code = []
                continue
            }
            if trimmed.isEmpty {
                flushParagraph(); flushList()
                continue
            }
            // A header row followed by a delimiter row starts a table.
            if trimmed.contains("|"), index < lines.count,
               let alignments = delimiterRow(lines[index]) {
                let header = cells(trimmed)
                if header.count == alignments.count {
                    flushParagraph(); flushList()
                    index += 1
                    var rows: [[String]] = []
                    while index < lines.count {
                        let row = lines[index].trimmingCharacters(in: .whitespaces)
                        guard !row.isEmpty, row.contains("|") else { break }
                        var c = cells(row)
                        if c.count < header.count { c += Array(repeating: "", count: header.count - c.count) }
                        rows.append(Array(c.prefix(header.count)))
                        index += 1
                    }
                    blocks.append(.table(Table(header: header, alignments: alignments, rows: rows)))
                    continue
                }
            }
            if let heading = heading(trimmed) {
                flushParagraph(); flushList()
                blocks.append(.heading(heading))
                continue
            }
            if let item = listItem(line) {
                flushParagraph()
                list.append(item)
                continue
            }
            if !list.isEmpty, line.hasPrefix("  ") {
                // A continuation line of the last item.
                list[list.count - 1].text += " " + trimmed
                continue
            }
            flushList()
            paragraph.append(trimmed)
        }
        if let open = code { blocks.append(.code(open.joined(separator: "\n"))) }
        flushParagraph(); flushList()
        return blocks
    }

    /// `![alt](source)` and nothing else → (alt, source). A title after the
    /// source (`"…"`) is left out.
    static func loneImage(_ text: String) -> (alt: String, source: String)? {
        let trimmed = text.trimmingCharacters(in: .whitespaces)
        guard trimmed.hasPrefix("!["), trimmed.hasSuffix(")"), !trimmed.contains("\n"),
              let close = trimmed.range(of: "](") else { return nil }
        let alt = String(trimmed[trimmed.index(trimmed.startIndex, offsetBy: 2) ..< close.lowerBound])
        let inside = String(trimmed[close.upperBound ..< trimmed.index(before: trimmed.endIndex)])
        guard !alt.contains("]"), !inside.contains(")"), !inside.contains("(") else { return nil }
        let source = inside.split(separator: " ", maxSplits: 1).first.map(String.init) ?? ""
        return source.isEmpty ? nil : (alt, source)
    }

    /// `| a | b |` → ["a", "b"]; `\|` is a pipe inside a cell.
    static func cells(_ row: String) -> [String] {
        var line = row.trimmingCharacters(in: .whitespaces)
        if line.hasPrefix("|") { line.removeFirst() }
        if line.hasSuffix("|"), !line.hasSuffix("\\|") { line.removeLast() }
        var out: [String] = []
        var cell = ""
        var escaped = false
        for ch in line {
            if escaped {
                if ch != "|" { cell.append("\\") }
                cell.append(ch)
                escaped = false
            } else if ch == "\\" {
                escaped = true
            } else if ch == "|" {
                out.append(cell.trimmingCharacters(in: .whitespaces))
                cell = ""
            } else {
                cell.append(ch)
            }
        }
        if escaped { cell.append("\\") }
        out.append(cell.trimmingCharacters(in: .whitespaces))
        return out
    }

    /// `|---|:---:|--:|` → the columns' alignments; nil if it is not one.
    static func delimiterRow(_ line: String) -> [Table.Alignment]? {
        let trimmed = line.trimmingCharacters(in: .whitespaces)
        guard trimmed.contains("-"), trimmed.contains("|") || trimmed.allSatisfy({ "-: ".contains($0) }) else { return nil }
        var out: [Table.Alignment] = []
        for cell in cells(trimmed) {
            guard !cell.isEmpty, cell.allSatisfy({ $0 == "-" || $0 == ":" }), cell.contains("-") else { return nil }
            switch (cell.hasPrefix(":"), cell.hasSuffix(":")) {
            case (true, true): out.append(.center)
            case (false, true): out.append(.trailing)
            default: out.append(.leading)
            }
        }
        return out.isEmpty ? nil : out
    }

    static func heading(_ trimmed: String) -> String? {
        var hashes = 0
        for ch in trimmed {
            if ch == "#" { hashes += 1 } else { break }
        }
        guard (1 ... 6).contains(hashes) else { return nil }
        let rest = trimmed.dropFirst(hashes)
        guard rest.first == " " else { return nil }
        var text = rest.trimmingCharacters(in: .whitespaces)
        // A closing "##" run is decoration.
        while text.hasSuffix("#") { text.removeLast() }
        text = text.trimmingCharacters(in: .whitespaces)
        return text.isEmpty ? nil : text
    }

    static func listItem(_ line: String) -> ListItem? {
        let indent = line.prefix { $0 == " " }.count
        let body = line.dropFirst(indent)
        if let first = body.first, "-*+".contains(first), body.dropFirst().first == " " {
            let text = body.dropFirst(2).trimmingCharacters(in: .whitespaces)
            guard !text.isEmpty else { return nil }
            // "- [ ] task" / "- [x] task".
            if text.hasPrefix("[ ] ") { return ListItem(marker: "☐", depth: indent / 2, text: String(text.dropFirst(4))) }
            if text.lowercased().hasPrefix("[x] ") { return ListItem(marker: "☑", depth: indent / 2, text: String(text.dropFirst(4))) }
            return ListItem(marker: "•", depth: indent / 2, text: text)
        }
        let digits = body.prefix { $0.isNumber }
        guard !digits.isEmpty, digits.count <= 4 else { return nil }
        let after = body.dropFirst(digits.count)
        guard let delimiter = after.first, delimiter == "." || delimiter == ")", after.dropFirst().first == " " else { return nil }
        let text = after.dropFirst(2).trimmingCharacters(in: .whitespaces)
        guard !text.isEmpty else { return nil }
        return ListItem(marker: "\(digits).", depth: indent / 2, text: text)
    }

    // MARK: - Inline

    /// Bold, italic, `code`, strikethrough and links; the text as written
    /// if Foundation will not parse it. With `keepingPaths`, a path such as
    /// `/login` or `docs/reports/a.md` is not broken across two lines.
    public static func inline(_ text: String, keepingPaths: Bool = false) -> AttributedString {
        let options = AttributedString.MarkdownParsingOptions(
            allowsExtendedAttributes: false,
            interpretedSyntax: .inlineOnlyPreservingWhitespace,
            failurePolicy: .returnPartiallyParsedIfPossible
        )
        let parsed = (try? AttributedString(markdown: text, options: options)) ?? AttributedString(text)
        return keepingPaths ? gluingPaths(parsed) : parsed
    }

    /// An invisible mark that forbids a line break on either side (U+2060,
    /// WORD JOINER): the text engine would otherwise break after a "/".
    static let wordJoiner: Character = "\u{2060}"

    /// `text` with a word joiner after each "/" that has more of its word
    /// behind it, so a path is wrapped whole or not at all. Words with no
    /// slash are left alone. (A copy of the text carries the invisible marks.)
    static func gluingPaths(_ text: String) -> String {
        guard text.contains("/") else { return text }
        var out = ""
        out.reserveCapacity(text.count + 8)
        var previous: Character?
        for ch in text {
            if previous == "/", !ch.isWhitespace, ch != wordJoiner { out.append(wordJoiner) }
            out.append(ch)
            previous = ch
        }
        return out
    }

    /// The same for styled text: each run keeps its own style.
    static func gluingPaths(_ text: AttributedString) -> AttributedString {
        guard text.characters.contains("/") else { return text }
        var out = AttributedString()
        var carry: Character?
        for run in text.runs {
            let words = String(text[run.range].characters)
            // A "/" ending the previous run still has this run's first
            // character behind it.
            var glued = gluingPaths(words)
            if carry == "/", let first = words.first, !first.isWhitespace { glued.insert(wordJoiner, at: glued.startIndex) }
            var piece = AttributedString(glued)
            piece.mergeAttributes(run.attributes)
            out += piece
            carry = words.last
        }
        return out
    }

    // MARK: - Rendering

    /// The whole text as one attributed string: blocks separated by a blank
    /// line, list items on their own lines with their marker, headings in
    /// bold, fenced code monospaced (the `code` presentation intent, which
    /// SwiftUI's `Text` draws in the monospaced font).
    public static func render(_ text: String) -> AttributedString {
        var out = AttributedString()
        for (i, block) in blocks(text).enumerated() {
            if i > 0 { out += AttributedString("\n\n") }
            switch block {
            case let .paragraph(p):
                out += inline(p)
            case let .heading(h):
                var bold = inline(h)
                for run in bold.runs {
                    let existing = bold[run.range].inlinePresentationIntent ?? []
                    bold[run.range].inlinePresentationIntent = existing.union(.stronglyEmphasized)
                }
                out += bold
            case let .list(items):
                for (j, item) in items.enumerated() {
                    if j > 0 { out += AttributedString("\n") }
                    out += AttributedString(String(repeating: "    ", count: item.depth) + item.marker + " ")
                    out += inline(item.text)
                }
            case let .code(c):
                var mono = AttributedString(c)
                mono.inlinePresentationIntent = .code
                out += mono
            case let .image(alt, source):
                out += inline("![\(alt)](\(source))")
            case let .table(table):
                // One Text has no grid: the header in bold, then a line
                // per row. The card sheet draws the real table.
                var head = inline(table.header.joined(separator: " · "))
                for run in head.runs {
                    let existing = head[run.range].inlinePresentationIntent ?? []
                    head[run.range].inlinePresentationIntent = existing.union(.stronglyEmphasized)
                }
                out += head
                for row in table.rows {
                    out += AttributedString("\n")
                    out += inline(row.joined(separator: " · "))
                }
            }
        }
        return out
    }

    /// The text without its markdown, for VoiceOver labels and one-line
    /// previews.
    public static func plain(_ text: String) -> String {
        String(render(text).characters)
    }
}
