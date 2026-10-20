import Foundation
import SwiftTerm
import UIKit

// The SwiftTerm renderer: the only file that imports SwiftTerm
// (TerminalSurface.swift explains the seam). Replacing the renderer means
// replacing this file and keeping `TerminalRenderer`'s two factories.

/// Makes the renderer's grid and surface without naming the renderer.
public enum TerminalRenderer {
    /// The renderer's name and pinned version, for diagnostics.
    public static let name = "SwiftTerm 1.99.0"

    /// A headless grid of `cols × rows`.
    public static func makeGrid(cols: Int, rows: Int) -> TerminalGrid {
        SwiftTermGrid(cols: cols, rows: rows)
    }

    /// A drawn surface; put `view` in the window.
    @MainActor
    public static func makeSurface(fontSize: CGFloat = SwiftTermSurface.defaultFontSize) -> SwiftTermSurface {
        SwiftTermSurface(fontSize: fontSize)
    }
}

/// herdr's frames are already composed by ghostty-vt, so the emulator here
/// keeps no scrollback (herdr scrolls on the computer, `term-scroll`) and
/// answers nothing back (there is no program on this side to answer).
private func streamOptions(cols: Int, rows: Int) -> TerminalOptions {
    TerminalOptions(cols: cols, rows: rows, scrollback: 0)
}

private func cellFrom(_ data: CharData?) -> TerminalCell {
    guard let data else { return TerminalCell(text: "", width: 1) }
    let width = Int(data.width)
    if width == 0 { return TerminalCell(text: "", width: 0) }
    var text = data.getText()
    if text == "\u{0}" { text = "" }
    return TerminalCell(text: text, width: width, foreground: colorFrom(data.attribute.fg))
}

private func colorFrom(_ color: Attribute.Color) -> TerminalColor {
    switch color {
    case .ansi256(let code): return .indexed(code)
    case .trueColor(let r, let g, let b): return .rgb(r, g, b)
    case .defaultColor, .defaultInvertedColor: return .default
    }
}

// MARK: - Headless grid

final class SwiftTermGrid: TerminalGrid, TerminalDelegate {
    private var terminal: Terminal!

    init(cols: Int, rows: Int) {
        terminal = Terminal(delegate: self, options: streamOptions(cols: cols, rows: rows))
    }

    var cols: Int { terminal.cols }
    var rows: Int { terminal.rows }

    func feed(_ bytes: [UInt8]) {
        terminal.terminalLock.withLock { terminal.feed(byteArray: bytes) }
    }

    func resize(cols: Int, rows: Int) {
        guard cols != terminal.cols || rows != terminal.rows else { return }
        terminal.terminalLock.withLock { terminal.resize(cols: cols, rows: rows) }
    }

    func cell(col: Int, row: Int) -> TerminalCell {
        terminal.terminalLock.withLock { cellFrom(terminal.getCharData(col: col, row: row)) }
    }

    // TerminalDelegate: herdr's stream asks nothing that needs an answer.
    func send(source: Terminal, data: ArraySlice<UInt8>) {}
}

// MARK: - Drawn surface

/// SwiftTerm's UIKit `TerminalView` behind `TerminalSurface`.
@MainActor
public final class SwiftTermSurface: NSObject, TerminalSurface {
    /// SF Mono 11 pt: about 58 columns on an iPhone 17 Pro in portrait
    /// (phone-terminal.md section 4, "Resize").
    public static let defaultFontSize: CGFloat = 11

    private let terminalView: TerminalView
    private var lastSize: (cols: Int, rows: Int) = (0, 0)

    public var onSizeChange: ((_ cols: Int, _ rows: Int) -> Void)?
    public var onInput: (([UInt8]) -> Void)?

    /// The view to put on screen.
    public var view: UIView { terminalView }

    init(fontSize: CGFloat) {
        terminalView = TerminalView(
            frame: .zero,
            font: UIFont.monospacedSystemFont(ofSize: fontSize, weight: .regular),
            options: streamOptions(cols: 80, rows: 24))
        super.init()
        terminalView.terminalDelegate = self
        terminalView.accessibilityLabel = "Terminal"
    }

    public var cols: Int { terminalView.terminalDimensions.cols }
    public var rows: Int { terminalView.terminalDimensions.rows }

    public func feed(_ bytes: [UInt8]) {
        terminalView.feed(byteArray: bytes[...])
    }

    public func snapshotText() -> String {
        var lines = accessibilityLines
        while lines.last?.isEmpty == true { lines.removeLast() }
        return lines.joined(separator: "\n")
    }

    public func setFontSize(_ size: CGFloat) {
        terminalView.font = UIFont.monospacedSystemFont(ofSize: size, weight: .regular)
    }

    public var accessibilityLines: [String] {
        terminalView.visibleRowsText(0..<rows).map { line in
            var line = line
            while line.hasSuffix(" ") { line.removeLast() }
            return line
        }
    }

    fileprivate func reportSize(cols: Int, rows: Int) {
        guard cols > 0, rows > 0, (cols, rows) != lastSize else { return }
        lastSize = (cols, rows)
        onSizeChange?(cols, rows)
    }
}

extension SwiftTermSurface: TerminalViewDelegate {
    public func sizeChanged(source: TerminalView, newCols: Int, newRows: Int) {
        reportSize(cols: newCols, rows: newRows)
    }

    public func send(source: TerminalView, data: ArraySlice<UInt8>) {
        onInput?(Array(data))
    }

    public func setTerminalTitle(source: TerminalView, title: String) {}
    public func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {}
    public func scrolled(source: TerminalView, position: Double) {}
    public func requestOpenLink(source: TerminalView, link: String, params: [String: String]) {}
    public func bell(source: TerminalView) {}
    public func clipboardCopy(source: TerminalView, content: Data) {
        UIPasteboard.general.string = String(decoding: content, as: UTF8.self)
    }
    public func iTermContent(source: TerminalView, content: ArraySlice<UInt8>) {}
    public func rangeChanged(source: TerminalView, startY: Int, endY: Int) {}
}
