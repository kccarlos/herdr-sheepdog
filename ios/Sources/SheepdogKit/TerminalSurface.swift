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

    /// The font size in points now (after a pinch).
    var fontSize: CGFloat { get }
    /// Called when a pinch settles on a new font size.
    var onFontSizeChange: ((CGFloat) -> Void)? { get set }
    /// A one-finger vertical drag, in whole lines: positive when the finger
    /// moves down (toward older output), negative when it moves up. herdr
    /// owns the scrollback (`term-scroll`), so the surface never scrolls
    /// itself.
    var onScroll: ((_ lines: Int) -> Void)? { get set }
    /// A tap on a cell, 0-based (`term-mouse` down and up).
    var onTap: ((_ col: Int, _ row: Int) -> Void)? { get set }
    /// Colours: background, text, cursor, selection and the 16 ANSI colours.
    func apply(theme: TerminalTheme)
}

/// An sRGB colour, 0–255 a channel, renderer-agnostic.
public struct TerminalRGB: Equatable, Sendable {
    public var r: UInt8, g: UInt8, b: UInt8

    public init(_ r: UInt8, _ g: UInt8, _ b: UInt8) {
        self.r = r
        self.g = g
        self.b = b
    }

    /// `#RRGGBB`.
    public init(hex: String) {
        let digits = hex.hasPrefix("#") ? String(hex.dropFirst()) : hex
        let v = UInt32(digits, radix: 16) ?? 0
        self.init(UInt8(v >> 16 & 0xff), UInt8(v >> 8 & 0xff), UInt8(v & 0xff))
    }

    /// WCAG relative luminance.
    public var luminance: Double {
        func channel(_ c: UInt8) -> Double {
            let s = Double(c) / 255
            return s <= 0.03928 ? s / 12.92 : pow((s + 0.055) / 1.055, 2.4)
        }
        return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
    }

    /// WCAG contrast ratio between two colours (1–21).
    public func contrast(with other: TerminalRGB) -> Double {
        let a = luminance, b = other.luminance
        return (max(a, b) + 0.05) / (min(a, b) + 0.05)
    }
}

/// The terminal's colours in one appearance. The brand's surfaces
/// (docs/dev/brand.md, assets/brand/colors.json): cream-cousin background
/// and ink text in light, the cool dark surface and cream text in dark.
/// The ANSI palette is tuned per appearance so every colour reads at 3:1
/// or better on its background (`TerminalThemeTests`), since agents draw
/// prompts and diffs in them.
public struct TerminalTheme: Equatable, Sendable {
    public var background: TerminalRGB
    public var foreground: TerminalRGB
    public var cursor: TerminalRGB
    /// Drawn at `selectionAlpha` over the background.
    public var selection: TerminalRGB
    public var selectionAlpha: Double
    /// black, red, green, yellow, blue, magenta, cyan, white, then the
    /// bright eight.
    public var ansi: [TerminalRGB]

    public static let light = TerminalTheme(
        background: TerminalRGB(hex: "#FAF8F3"),
        foreground: TerminalRGB(hex: "#1F1F22"),
        cursor: TerminalRGB(hex: "#1F1F22"),
        selection: TerminalRGB(hex: "#F39C1F"),
        selectionAlpha: 0.35,
        ansi: [
            "#1F1F22", "#B3261E", "#1D6B46", "#8A5700", "#1F5F9E", "#7A3E9D", "#0E6E78", "#6B6E75",
            "#5C6370", "#C4321F", "#237A52", "#9A6300", "#2C6FB5", "#8A4BB0", "#13808A", "#4A4D54",
        ].map(TerminalRGB.init(hex:)))

    public static let dark = TerminalTheme(
        background: TerminalRGB(hex: "#16181C"),
        foreground: TerminalRGB(hex: "#F5F2EA"),
        cursor: TerminalRGB(hex: "#F5F2EA"),
        selection: TerminalRGB(hex: "#F39C1F"),
        selectionAlpha: 0.4,
        ansi: [
            "#6E747E", "#F07A6E", "#5CC792", "#F5B83D", "#6AA8EB", "#C29BF2", "#5FC9D3", "#D9D6CE",
            "#8E929A", "#FF968A", "#7DDCAA", "#FFD06B", "#8DBEF5", "#D5B6FA", "#84DCE3", "#F5F2EA",
        ].map(TerminalRGB.init(hex:)))
}

/// The cell grid a view of a given size holds at a given font size, the
/// same arithmetic SwiftTerm uses: a cell is as wide as "W" and as tall as
/// the font's ascent + descent + leading, rounded up; the grid is the whole
/// cells that fit.
public struct TerminalGeometry: Equatable, Sendable {
    public var cellWidth: CGFloat
    public var cellHeight: CGFloat

    public init(cellWidth: CGFloat, cellHeight: CGFloat) {
        self.cellWidth = cellWidth
        self.cellHeight = cellHeight
    }

    /// Whole cells in `size`, never less than 1 × 1.
    public func grid(for size: CGSize) -> (cols: Int, rows: Int) {
        guard cellWidth > 0, cellHeight > 0 else { return (1, 1) }
        return (max(1, Int(size.width / cellWidth)), max(1, Int(size.height / cellHeight)))
    }

    /// The 0-based cell under a point, clamped to the grid.
    public func cell(at point: CGPoint, cols: Int, rows: Int) -> (col: Int, row: Int) {
        guard cellWidth > 0, cellHeight > 0 else { return (0, 0) }
        let col = min(max(0, Int(point.x / cellWidth)), max(0, cols - 1))
        let row = min(max(0, Int(point.y / cellHeight)), max(0, rows - 1))
        return (col, row)
    }

    /// Whole lines in a vertical drag of `dy` points, rounded toward zero.
    public func lines(forDrag dy: CGFloat) -> Int {
        guard cellHeight > 0 else { return 0 }
        return Int(dy / cellHeight)
    }
}
