import SheepdogKit
import XCTest

/// R1 of docs/dev/design/phone-terminal.md: the renderer agrees with herdr
/// (ghostty-vt) on character widths. Recorded herdr frames go into the
/// headless grid at the frame's size, and the grid is compared with
///
/// - herdr's own text of the pane (`pane read --source visible`), row by
///   row, and
/// - herdr's cursor moves: herdr's encoder puts an absolute cursor move
///   (`ESC [ row ; col H`) in front of most cells after a non-ASCII one, so
///   each move tells which column herdr thinks the next grapheme is in.
///
/// A disagreement shows up as a row whose text differs (a wide character
/// clobbered its neighbour) or a grapheme in another column.
final class TerminalWidthTests: XCTestCase {
    /// Each row of the width fixture, by its label, for readable failures.
    private let widthRows = [
        "cjk", "vs16", "zwj", "emoji", "flag", "mix", "box", "comb", "amb", "kana", "TRUE",
    ]

    func testRowsMatchHerdrTextAfterFullFrame() throws {
        try assertRowsMatch("width")
    }

    func testRowsMatchHerdrTextAfterDiffsOnWideCells() throws {
        try assertRowsMatch("width-diff")
    }

    /// Wide cells overwritten by narrow ones, and narrow writes on a wide
    /// cell's right half: the orphaned half has to go blank, as herdr's
    /// frame says (it sends the blank explicitly).
    func testRowsMatchHerdrTextAfterNarrowWritesOnWideCells() throws {
        try assertRowsMatch("width-narrow")
        let grid = TerminalFixture.replay(try TerminalFixture.frames("width-narrow"))
        XCTAssertEqual(grid.lineText(0), "cjk ab x宽字符|")
        XCTAssertEqual(grid.cell(col: 7, row: 0), TerminalCell(text: "x", width: 1))
        XCTAssertEqual(grid.column(of: "宽", row: 0), 8)
        XCTAssertEqual(grid.lineText(3), "emoji z 🚀|")
    }

    /// After each frame, every grapheme the frame placed with a cursor move
    /// is in that column of the grid. herdr's encoder writes each changed
    /// cell once per frame, so nothing later in the same frame moves it.
    func testGraphemesSitInHerdrsColumns() throws {
        for name in ["width", "width-diff", "width-narrow", "burst", "colour"] {
            let frames = try TerminalFixture.frames(name)
            let grid = TerminalRenderer.makeGrid(cols: frames[0].cols, rows: frames[0].rows)
            var checked = 0
            var wrong: [String] = []
            for frame in frames {
                grid.resize(cols: frame.cols, rows: frame.rows)
                grid.feed(frame.bytes)
                for (pos, grapheme) in HerdrPlacements.parse(frame).sorted(by: { $0.key < $1.key }) {
                    checked += 1
                    let got = grid.cell(col: pos.col, row: pos.row).text
                    if got != grapheme {
                        wrong.append("seq \(frame.seq) row \(pos.row) col \(pos.col): herdr \(grapheme.debugDescription), grid \(got.debugDescription)")
                    }
                }
            }
            XCTAssertGreaterThan(checked, 0, name)
            XCTAssertEqual(wrong, [], "\(name): \(wrong.count) of \(checked) placements differ")
        }
    }

    /// The column of the `|` that ends each row of the width fixture, which
    /// is where herdr put it (from its cursor moves when recorded).
    func testTextAfterWideCharactersKeepsItsColumn() throws {
        let grid = TerminalFixture.replay(try TerminalFixture.frames("width"))
        let expected: [(String, Int)] = [
            ("cjk 中文宽字符", 14),
            ("vs16 ☁️", 7),
            ("zwj 👩‍💻", 6),
            ("emoji 🙂🚀", 10),
            ("flag 🇯🇵", 7),
            ("mix a中b☁️c👩‍💻d", 14),
            ("box ┌──┬──┐│ok│└─┘", 18),
            ("comb é ñ", 8),
            ("amb ±§αβ→…", 10),
            ("kana ｶﾀｶﾅ カタカナ 한글", 23),
            ("TRUE BG BOLD", 12),
        ]
        for (row, (label, col)) in expected.enumerated() {
            XCTAssertEqual(grid.column(of: "|", row: row), col, label)
        }
    }

    func testWideCellsAreTwoColumns() throws {
        let grid = TerminalFixture.replay(try TerminalFixture.frames("width"))
        // (row, col) of a wide grapheme's left half.
        let wide: [(Int, Int, String)] = [
            (0, 4, "中"), (1, 5, "☁️"), (2, 4, "👩‍💻"), (3, 6, "🙂"), (4, 5, "🇯🇵"),
            (5, 8, "☁️"), (9, 10, "カ"), (9, 19, "한"),
        ]
        for (row, col, text) in wide {
            XCTAssertEqual(grid.cell(col: col, row: row), TerminalCell(text: text, width: 2), text)
            XCTAssertEqual(grid.cell(col: col + 1, row: row).width, 0, "\(text) spacer")
        }
        // Half-width kana and ambiguous-width characters are one column.
        XCTAssertEqual(grid.cell(col: 5, row: 9).width, 1, "ｶ")
        XCTAssertEqual(grid.cell(col: 4, row: 8).width, 1, "±")
    }

    func testColoursSurviveTheStream() throws {
        let grid = TerminalFixture.replay(try TerminalFixture.frames("width"))
        XCTAssertEqual(grid.cell(col: 0, row: 10).foreground, .rgb(255, 120, 0), "true colour")
        XCTAssertEqual(grid.cell(col: 5, row: 10).foreground, .rgb(255, 255, 255), "true colour on a background")
        XCTAssertEqual(grid.cell(col: 8, row: 10).foreground, .indexed(46), "256 colours")
        XCTAssertEqual(grid.cell(col: 12, row: 10).foreground, .default, "reset")
    }

    func testBurstAndColourFixturesMatchHerdrText() throws {
        try assertRowsMatch("burst")
        try assertRowsMatch("colour")
    }

    // MARK: - Helpers

    private func assertRowsMatch(_ name: String, file: StaticString = #filePath, line: UInt = #line) throws {
        let frames = try TerminalFixture.frames(name)
        XCTAssertTrue(frames[0].full, "\(name) starts with a full frame", file: file, line: line)
        let grid = TerminalFixture.replay(frames)
        let herdr = try TerminalFixture.visible(name)
        XCTAssertEqual(grid.rows, frames.last!.rows, file: file, line: line)
        var wrong: [String] = []
        for row in 0..<grid.rows {
            let want = row < herdr.count ? herdr[row] : ""
            let got = grid.lineText(row)
            if got != want {
                wrong.append("row \(row): herdr \(want.debugDescription), grid \(got.debugDescription)")
            }
        }
        XCTAssertEqual(wrong, [], "\(name): rows differ from herdr's text", file: file, line: line)
    }
}

/// Where one herdr frame puts graphemes: the first grapheme after each
/// absolute cursor move (SGR in between skipped).
enum HerdrPlacements {
    static func parse(_ frame: RecordedFrame) -> [Pos: String] {
        var placed: [Pos: String] = [:]
        do {
            let chars = Array(String(decoding: frame.bytes, as: UTF8.self))
            var i = 0
            while i < chars.count {
                guard chars[i] == "\u{1b}", i + 1 < chars.count, chars[i + 1] == "[" else {
                    i += 1
                    continue
                }
                // A CSI sequence: parameters up to the final byte.
                var j = i + 2
                var params = ""
                while j < chars.count, let a = chars[j].asciiValue, (0x30...0x3f).contains(a) {
                    params.append(chars[j])
                    j += 1
                }
                guard j < chars.count else { break }
                let final = chars[j]
                i = j + 1
                guard final == "H" else { continue }
                let parts = params.split(separator: ";").compactMap { Int($0) }
                guard parts.count == 2 else { continue }
                // Skip SGR sequences between the move and the text.
                var k = i
                while k + 1 < chars.count, chars[k] == "\u{1b}", chars[k + 1] == "[" {
                    var m = k + 2
                    while m < chars.count, let a = chars[m].asciiValue, (0x30...0x3f).contains(a) { m += 1 }
                    guard m < chars.count, chars[m] == "m" else { break }
                    k = m + 1
                }
                guard k < chars.count, chars[k] != "\u{1b}", chars[k] != " " else { continue }
                placed[Pos(row: parts[0] - 1, col: parts[1] - 1)] = String(chars[k])
            }
        }
        return placed
    }

    struct Pos: Hashable, Comparable {
        let row: Int
        let col: Int
        static func < (a: Pos, b: Pos) -> Bool { (a.row, a.col) < (b.row, b.col) }
    }
}
