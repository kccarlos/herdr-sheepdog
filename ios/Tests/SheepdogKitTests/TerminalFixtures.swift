import Foundation
import SheepdogKit
import XCTest

/// Recorded herdr terminal-session frames (Terminal/*.jsonl).
///
/// Each line is what `herdr terminal session control <pane> --cols C --rows R`
/// wrote to stdout, verbatim: `{"type":"terminal.frame","seq":N,
/// "encoding":"ansi","width":W,"height":H,"full":bool,"bytes":"<base64>"}`.
/// `<case>.visible.txt` next to it is herdr's `pane read --source visible` of
/// the same pane right after the last frame. Recorded from a scratch herdr
/// 0.9.5 session (`herdr --session <scratch> server`) running a bare
/// `/bin/bash --norc` with `PS1='$ '`; never the user's session.
///
/// - width: one row per width case (CJK, VS16, ZWJ, emoji, flag, mixed, box
///   drawing, combining marks, ambiguous width, half-width kana, Hangul,
///   true colour), 40×14.
/// - width-diff: the same rows, then single wide cells rewritten in place,
///   so the last frames are diffs that touch wide cells.
/// - width-narrow: the same rows, then wide cells overwritten by narrow
///   ones and narrow writes on the right half of wide cells.
/// - burst: `seq 1 200000`, 58×30 (herdr coalesces it to a few frames).
/// - colour: 3000 lines of 256-colour and true-colour text with CJK and a
///   VS16 emoji, 58×30.
struct RecordedFrame {
    let seq: UInt32
    let cols: Int
    let rows: Int
    let full: Bool
    let bytes: [UInt8]
}

enum TerminalFixture {
    private final class BundleToken {}

    static func url(_ name: String, _ ext: String) throws -> URL {
        let bundle = Bundle(for: BundleToken.self)
        return try XCTUnwrap(bundle.url(forResource: name, withExtension: ext),
                             "missing fixture \(name).\(ext) in the test bundle")
    }

    static func frames(_ name: String) throws -> [RecordedFrame] {
        let text = try String(contentsOf: url(name, "jsonl"), encoding: .utf8)
        return try text.split(separator: "\n").map { line in
            let obj = try XCTUnwrap(
                JSONSerialization.jsonObject(with: Data(line.utf8)) as? [String: Any])
            XCTAssertEqual(obj["type"] as? String, "terminal.frame")
            let b64 = try XCTUnwrap(obj["bytes"] as? String)
            return RecordedFrame(
                seq: UInt32(try XCTUnwrap(obj["seq"] as? Int)),
                cols: try XCTUnwrap(obj["width"] as? Int),
                rows: try XCTUnwrap(obj["height"] as? Int),
                full: try XCTUnwrap(obj["full"] as? Bool),
                bytes: [UInt8](try XCTUnwrap(Data(base64Encoded: b64))))
        }
    }

    /// herdr's own text of the pane, one string per row, trailing spaces trimmed.
    static func visible(_ name: String) throws -> [String] {
        let text = try String(contentsOf: url(name + ".visible", "txt"), encoding: .utf8)
        var rows = text.components(separatedBy: "\n")
        if rows.last == "" { rows.removeLast() }
        return rows.map { row in
            var row = row
            while row.hasSuffix(" ") { row.removeLast() }
            return row
        }
    }

    /// A grid with every frame fed, sized like the first frame.
    static func replay(_ frames: [RecordedFrame]) -> TerminalGrid {
        let grid = TerminalRenderer.makeGrid(cols: frames[0].cols, rows: frames[0].rows)
        for frame in frames {
            grid.resize(cols: frame.cols, rows: frame.rows)
            grid.feed(frame.bytes)
        }
        return grid
    }
}
