import CoreGraphics
import Foundation

// The terminal renderer seam (docs/dev/design/phone-terminal.md section 2).
//
// The app draws herdr's terminal stream through these protocols and never
// names the renderer. SwiftTermSurface.swift is the only file that imports
// SwiftTerm; moving to libghostty-spm, or to a view on libghostty-vt,
// replaces that one file and keeps everything here.
//
// What the renderer is fed is herdr's TerminalAnsi render stream: herdr has
// already run the program in its own emulator (ghostty-vt) and sends only
// the changed cells as ANSI, with absolute cursor moves and DEC 2026
// synchronized output around each frame. So the renderer needs a small VT
// subset, but it must agree with ghostty-vt on character widths
// (TerminalWidthTests).

/// One cell of a `TerminalGrid`.
public struct TerminalCell: Equatable, Sendable {
    /// The grapheme drawn in the cell, "" for an empty cell and for the
    /// right half of a wide character.
    public var text: String
    /// Columns the cell's grapheme takes: 1, 2 for a wide character, 0 for
    /// the spacer to the right of a wide character.
    public var width: Int
    /// The text colour.
    public var foreground: TerminalColor

    public init(text: String, width: Int, foreground: TerminalColor = .default) {
        self.text = text
        self.width = width
        self.foreground = foreground
    }
}

/// A cell colour as the stream set it.
public enum TerminalColor: Equatable, Sendable {
    case `default`
    /// One of the 256 palette colours.
    case indexed(UInt8)
    /// True colour.
    case rgb(UInt8, UInt8, UInt8)
}

/// A terminal emulator with no view: the cell grid herdr's frames produce.
/// Used by tests (width agreement, replay timing) and anywhere the app
/// needs the screen's text without drawing it.
public protocol TerminalGrid: AnyObject {
    var cols: Int { get }
    var rows: Int { get }
    /// Parses ANSI bytes into the grid.
    func feed(_ bytes: [UInt8])
    func resize(cols: Int, rows: Int)
    /// The cell at a 0-based column and row of the visible screen.
    func cell(col: Int, row: Int) -> TerminalCell
}

public extension TerminalGrid {
    /// A visible row as text: each cell's grapheme, spacers skipped, empty
    /// cells as spaces, trailing spaces trimmed. The same shape as herdr's
    /// `pane read --source visible`.
    func lineText(_ row: Int) -> String {
        var line = ""
        for col in 0..<cols {
            let c = cell(col: col, row: row)
            if c.width == 0 { continue }
            line += c.text.isEmpty ? " " : c.text
        }
        while line.hasSuffix(" ") { line.removeLast() }
        return line
    }

    /// Every visible row, joined with newlines, trailing empty rows dropped.
    func snapshotText() -> String {
        var lines = (0..<rows).map(lineText)
        while lines.last?.isEmpty == true { lines.removeLast() }
        return lines.joined(separator: "\n")
    }

    /// The 0-based column where `needle` starts on a row, counting cells
    /// (a wide character takes two), or nil.
    func column(of needle: String, row: Int) -> Int? {
        for col in 0..<cols where cell(col: col, row: row).text == needle {
            return col
        }
        return nil
    }
}

/// A drawn terminal: the view the agent window shows.
///
/// The surface owns its grid size. It computes `cols × rows` from its
/// bounds and font size and reports every change through `onSizeChange`;
/// the caller sends that size to the daemon (`term-resize`). Bytes the user
/// types into the surface come out of `onInput`, ready to send as raw
/// terminal input.
@MainActor
public protocol TerminalSurface: AnyObject {
    /// Feeds one frame's ANSI bytes. Safe to call at any rate; the surface
    /// draws at most once per display refresh.
    func feed(_ bytes: [UInt8])
    /// The current grid size.
    var cols: Int { get }
    var rows: Int { get }
    /// Called with the new `(cols, rows)` whenever the view's size or font
    /// changes the grid.
    var onSizeChange: ((_ cols: Int, _ rows: Int) -> Void)? { get set }
    /// Called with the raw bytes of keys typed into the surface (UTF-8 text,
    /// `\r` for Return, escape sequences for arrows).
    var onInput: (([UInt8]) -> Void)? { get set }
    /// The visible screen as text, rows joined with newlines.
    func snapshotText() -> String
    /// The font size in points; changes the grid size (pinch to zoom).
    func setFontSize(_ size: CGFloat)
    /// The visible rows, for VoiceOver and the "read the screen" action.
    var accessibilityLines: [String] { get }
}
