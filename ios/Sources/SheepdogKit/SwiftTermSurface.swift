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

    /// The cell size the surface uses at `fontSize`, so a caller can
    /// predict the grid a view holds (SwiftTerm's own arithmetic: the width
    /// of "W", and ascent + descent + leading rounded up).
    public static func geometry(fontSize: CGFloat) -> TerminalGeometry {
        let font = SwiftTermSurface.font(ofSize: fontSize)
        let width = "W".size(withAttributes: [.font: font]).width
        let ct = font as CTFont
        let height = ceil(CTFontGetAscent(ct) + CTFontGetDescent(ct) + CTFontGetLeading(ct))
        return TerminalGeometry(cellWidth: width, cellHeight: height)
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
///
/// herdr owns everything interactive about the pane (scrollback, mouse
/// modes), so SwiftTerm's own handling is switched off and replaced:
///
/// - one-finger vertical drag → `onScroll` (whole lines; SwiftTerm's scroll
///   view is disabled, it keeps no scrollback anyway);
/// - tap → `onTap` with the cell (`term-mouse`); SwiftTerm's single, double
///   and triple taps are disabled, so a tap never pops the keyboard or a
///   menu;
/// - pinch → a new font size, applied when the pinch ends (the view scales
///   as a preview meanwhile), so a pinch costs one `term-resize`;
/// - long-press stays SwiftTerm's: Select or Select All, drag to extend,
///   then Copy. (Its Paste goes out through `onInput`, which the window
///   forwards only on a live `control` channel.) Becoming first responder for that menu must not bring up a
///   keyboard, so `inputView` is an empty view and there is no accessory
///   bar; `keyboardEnabled` (phase 3's typing mode) brings the keyboard
///   back.
@MainActor
public final class SwiftTermSurface: NSObject, TerminalSurface {
    /// SF Mono 11 pt: about 58 columns on an iPhone 17 Pro in portrait
    /// (phone-terminal.md section 4, "Resize").
    public static let defaultFontSize: CGFloat = 11
    public static let fontSizeRange: ClosedRange<CGFloat> = 7 ... 20

    nonisolated static func font(ofSize size: CGFloat) -> UIFont {
        UIFont.monospacedSystemFont(ofSize: size, weight: .regular)
    }

    private let terminalView: GatedTerminalView
    private var lastSize: (cols: Int, rows: Int) = (0, 0)
    public private(set) var fontSize: CGFloat
    private var pinchStart: CGFloat = SwiftTermSurface.defaultFontSize
    private var dragRemainder: CGFloat = 0
    private var theme: TerminalTheme?
    private var contentChangePending = false

    public var onSizeChange: ((_ cols: Int, _ rows: Int) -> Void)?
    public var onInput: (([UInt8]) -> Void)?
    public var onFontSizeChange: ((CGFloat) -> Void)?
    public var onScroll: ((_ lines: Int) -> Void)?
    public var onTap: ((_ col: Int, _ row: Int) -> Void)?
    /// Called shortly after frames changed what is on screen (at most about
    /// three times a second), to refresh VoiceOver's copy of the text.
    public var onContentChange: (() -> Void)?

    /// Whether becoming first responder shows the system keyboard (phase 3
    /// typing mode). Off: long-press selection works without a keyboard.
    public var keyboardEnabled = false {
        didSet {
            guard keyboardEnabled != oldValue else { return }
            terminalView.inputView = keyboardEnabled ? nil : UIView(frame: .zero)
            terminalView.reloadInputViews()
        }
    }

    /// Whether the long-press menu offers Paste: only on a window whose
    /// input goes somewhere (a live `control` channel).
    public var pasteEnabled: Bool {
        get { terminalView.pasteEnabled }
        set { terminalView.pasteEnabled = newValue }
    }

    /// Typing mode on or off: the system keyboard (with IME and dictation,
    /// which SwiftTerm commits as text) comes up, or goes away.
    public func setTyping(_ on: Bool) {
        keyboardEnabled = on
        if on {
            terminalView.becomeFirstResponder()
        } else {
            terminalView.resignFirstResponder()
        }
    }

    /// The view to put on screen.
    public var view: UIView { container }
    private let container = TerminalContainerView()

    init(fontSize: CGFloat) {
        let size = min(max(fontSize, Self.fontSizeRange.lowerBound), Self.fontSizeRange.upperBound)
        self.fontSize = size
        terminalView = GatedTerminalView(
            frame: .zero,
            font: SwiftTermSurface.font(ofSize: size),
            options: streamOptions(cols: 80, rows: 24))
        super.init()
        terminalView.terminalDelegate = self
        // VoiceOver reads the container (`TerminalContainerView`): the
        // visible lines, one rotor line each. SwiftTerm's own reading
        // content is hidden.
        terminalView.isAccessibilityElement = false
        terminalView.accessibilityElementsHidden = true
        terminalView.frame = container.bounds
        terminalView.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        container.addSubview(terminalView)
        container.lines = { [weak self] in self?.accessibilityLines ?? [] }
        container.lineHeight = { [weak self] in self?.geometry.cellHeight ?? 14 }
        terminalView.allowMouseReporting = false
        terminalView.isScrollEnabled = false
        terminalView.showsVerticalScrollIndicator = false
        terminalView.showsHorizontalScrollIndicator = false
        terminalView.inputAccessoryView = nil
        terminalView.inputView = UIView(frame: .zero)
        installGestures()
    }

    public var cols: Int { terminalView.terminalDimensions.cols }
    public var rows: Int { terminalView.terminalDimensions.rows }

    public func feed(_ bytes: [UInt8]) {
        terminalView.feed(byteArray: bytes[...])
        scheduleContentChange()
    }

    public func snapshotText() -> String {
        var lines = accessibilityLines
        while lines.last?.isEmpty == true { lines.removeLast() }
        return lines.joined(separator: "\n")
    }

    public func setFontSize(_ size: CGFloat) {
        let size = min(max(size, Self.fontSizeRange.lowerBound), Self.fontSizeRange.upperBound)
        guard size != fontSize else { return }
        fontSize = size
        terminalView.font = Self.font(ofSize: size)
        if let theme { apply(theme: theme) }
    }

    public var accessibilityLines: [String] {
        terminalView.visibleRowsText(0..<rows).map { line in
            var line = line
            while line.hasSuffix(" ") { line.removeLast() }
            return line
        }
    }

    public func apply(theme: TerminalTheme) {
        self.theme = theme
        func ui(_ c: TerminalRGB, _ alpha: CGFloat = 1) -> UIColor {
            UIColor(red: CGFloat(c.r) / 255, green: CGFloat(c.g) / 255, blue: CGFloat(c.b) / 255, alpha: alpha)
        }
        func st(_ c: TerminalRGB) -> SwiftTerm.Color {
            SwiftTerm.Color(red: UInt16(c.r) * 257, green: UInt16(c.g) * 257, blue: UInt16(c.b) * 257)
        }
        terminalView.installColors(theme.ansi.map(st))
        terminalView.nativeBackgroundColor = ui(theme.background)
        terminalView.nativeForegroundColor = ui(theme.foreground)
        terminalView.backgroundColor = ui(theme.background)
        terminalView.caretColor = ui(theme.cursor)
        terminalView.selectedTextBackgroundColor = ui(theme.selection, CGFloat(theme.selectionAlpha))
        terminalView.setNeedsDisplay()
    }

    /// The selection, if any.
    public var selectedText: String? { terminalView.getSelection() }

    // MARK: Gestures

    private func installGestures() {
        // SwiftTerm's taps focus the view (the keyboard), select or pop a
        // menu; here a tap is a click on the pane.
        for recognizer in terminalView.gestureRecognizers ?? [] where recognizer is UITapGestureRecognizer {
            recognizer.isEnabled = false
        }
        let tap = UITapGestureRecognizer(target: self, action: #selector(tapped(_:)))
        terminalView.addGestureRecognizer(tap)

        let pan = UIPanGestureRecognizer(target: self, action: #selector(panned(_:)))
        pan.maximumNumberOfTouches = 1
        pan.delegate = self
        terminalView.addGestureRecognizer(pan)

        let pinch = UIPinchGestureRecognizer(target: self, action: #selector(pinched(_:)))
        terminalView.addGestureRecognizer(pinch)
    }

    /// The font's cell size, pulled into the range the grid SwiftTerm
    /// actually laid out allows (its font metrics differ from UIKit's by a
    /// fraction of a point), so a tap lands on the cell drawn under it.
    private var geometry: TerminalGeometry {
        var g = TerminalRenderer.geometry(fontSize: fontSize)
        let size = terminalView.bounds.size
        if cols > 0, size.width > 0 {
            let lo = size.width / CGFloat(cols + 1), hi = size.width / CGFloat(cols)
            g.cellWidth = min(max(g.cellWidth, lo + 0.001), hi)
        }
        if rows > 0, size.height > 0 {
            let lo = size.height / CGFloat(rows + 1), hi = size.height / CGFloat(rows)
            g.cellHeight = min(max(g.cellHeight, lo + 0.001), hi)
        }
        return g
    }

    @objc private func tapped(_ recognizer: UITapGestureRecognizer) {
        guard recognizer.state == .ended else { return }
        if terminalView.selectionActive {
            terminalView.selectNone()
            terminalView.setNeedsDisplay()
            UIMenuController.shared.hideMenu()
            return
        }
        let cell = geometry.cell(at: recognizer.location(in: terminalView), cols: cols, rows: rows)
        onTap?(cell.col, cell.row)
    }

    @objc private func panned(_ recognizer: UIPanGestureRecognizer) {
        switch recognizer.state {
        case .began:
            dragRemainder = 0
        case .changed:
            let dy = recognizer.translation(in: terminalView).y + dragRemainder
            recognizer.setTranslation(.zero, in: terminalView)
            let lines = geometry.lines(forDrag: dy)
            dragRemainder = dy - CGFloat(lines) * geometry.cellHeight
            if lines != 0 { onScroll?(lines) }
        default:
            dragRemainder = 0
        }
    }

    @objc private func pinched(_ recognizer: UIPinchGestureRecognizer) {
        switch recognizer.state {
        case .began:
            pinchStart = fontSize
        case .changed:
            // A preview only; the grid changes once, when the fingers lift.
            let s = clampedSize(pinchStart * recognizer.scale) / fontSize
            terminalView.transform = CGAffineTransform(scaleX: s, y: s)
        case .ended, .cancelled, .failed:
            terminalView.transform = .identity
            let target = clampedSize(pinchStart * recognizer.scale)
            if target != fontSize {
                setFontSize(target)
                onFontSizeChange?(fontSize)
            }
        default:
            break
        }
    }

    /// Clamped, and rounded to half points.
    private func clampedSize(_ size: CGFloat) -> CGFloat {
        let rounded = (size * 2).rounded() / 2
        return min(max(rounded, Self.fontSizeRange.lowerBound), Self.fontSizeRange.upperBound)
    }

    private func scheduleContentChange() {
        guard onContentChange != nil, !contentChangePending else { return }
        contentChangePending = true
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) { [weak self] in
            guard let self else { return }
            self.contentChangePending = false
            self.onContentChange?()
        }
    }

    fileprivate func reportSize(cols: Int, rows: Int) {
        guard cols > 0, rows > 0, (cols, rows) != lastSize else { return }
        lastSize = (cols, rows)
        onSizeChange?(cols, rows)
    }
}

/// The surface's outer view, and what VoiceOver reads: the terminal's
/// visible lines as the value, and line by line through the rotor
/// (`UIAccessibilityReadingContent`).
final class TerminalContainerView: UIView, UIAccessibilityReadingContent {
    var lines: () -> [String] = { [] }
    var lineHeight: () -> CGFloat = { 14 }

    override init(frame: CGRect) {
        super.init(frame: frame)
        isAccessibilityElement = true
        accessibilityLabel = "Terminal"
        accessibilityTraits = [.staticText, .causesPageTurn]
        clipsToBounds = true
    }

    required init?(coder: NSCoder) { fatalError("init(coder:) is not used") }

    func accessibilityLineNumber(for point: CGPoint) -> Int {
        let height = max(1, lineHeight())
        return min(max(0, Int(point.y / height)), max(0, lines().count - 1))
    }

    func accessibilityContent(forLineNumber lineNumber: Int) -> String? {
        let all = lines()
        return all.indices.contains(lineNumber) ? all[lineNumber] : nil
    }

    func accessibilityFrame(forLineNumber lineNumber: Int) -> CGRect {
        let height = lineHeight()
        let rect = CGRect(x: 0, y: CGFloat(lineNumber) * height, width: bounds.width, height: height)
        return UIAccessibility.convertToScreenCoordinates(rect, in: self)
    }

    func accessibilityPageContent() -> String? {
        lines().joined(separator: "\n")
    }
}

extension SwiftTermSurface: UIGestureRecognizerDelegate {
    public func gestureRecognizerShouldBegin(_ recognizer: UIGestureRecognizer) -> Bool {
        guard let pan = recognizer as? UIPanGestureRecognizer else { return true }
        // After a long-press a drag moves the selection (SwiftTerm's own
        // pan), and a sideways drag is not a scroll.
        if terminalView.selectionActive { return false }
        let v = pan.velocity(in: terminalView)
        return abs(v.y) >= abs(v.x)
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

/// SwiftTerm's view with Paste gated: its `canPerformAction` always offers
/// Paste and is not overridable, so the action's target is withheld
/// instead, which hides the menu item.
final class GatedTerminalView: TerminalView {
    var pasteEnabled = false

    override func target(forAction action: Selector, withSender sender: Any?) -> Any? {
        if action == #selector(paste(_:)), !pasteEnabled { return nil }
        return super.target(forAction: action, withSender: sender)
    }

    override func paste(_ sender: Any?) {
        guard pasteEnabled else { return }
        super.paste(sender)
    }
}
