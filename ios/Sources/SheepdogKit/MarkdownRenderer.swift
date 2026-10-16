import Foundation

/// The markdown the workers and the orchestrator write, made readable on a phone
/// without letting it take over the layout: inline styles (bold, italic,
/// `code`, links) through Foundation's `AttributedString(markdown:)`, and a
/// small block pass on top — paragraphs, bullet and numbered lists, fenced
/// code as a monospaced block. Headings are bold body text, never large.
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

    public enum Block: Equatable, Sendable {
        case paragraph(String)
        case heading(String)
        case list([ListItem])
        case code(String)
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
            if !paragraph.isEmpty { blocks.append(.paragraph(paragraph.joined(separator: "\n"))) }
            paragraph = []
        }
        func flushList() {
            if !list.isEmpty { blocks.append(.list(list)) }
            list = []
        }

        let lines = text.replacingOccurrences(of: "\r\n", with: "\n").components(separatedBy: "\n")
        for line in lines {
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
    /// if Foundation will not parse it.
    public static func inline(_ text: String) -> AttributedString {
        let options = AttributedString.MarkdownParsingOptions(
            allowsExtendedAttributes: false,
            interpretedSyntax: .inlineOnlyPreservingWhitespace,
            failurePolicy: .returnPartiallyParsedIfPossible
        )
        return (try? AttributedString(markdown: text, options: options)) ?? AttributedString(text)
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
