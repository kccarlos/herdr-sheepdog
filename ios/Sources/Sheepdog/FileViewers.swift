import SheepdogKit
import SwiftUI
import UIKit

/// One open file: rendered Markdown with a raw toggle, code with line
/// numbers, an image you can zoom, or the quiet screen that says why not.
/// Read only: nothing here saves, shares or keeps the file.
struct FileViewer: View {
    let path: String
    let rootName: String
    let services: BrowserServices
    /// This file among the agent's changes, when it is one.
    let change: ChangedFile?
    let openDiff: (ChangedFile) -> Void
    /// A link in a document to another file of the folder.
    let openPath: (String) -> Void
    /// Leaves the file, where the viewer sits on its own (the phone).
    let onBack: (() -> Void)?
    let showChanges: (() -> Void)?

    @State private var viewer: FileViewerModel
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    init(
        path: String, rootName: String, services: BrowserServices, change: ChangedFile?,
        openDiff: @escaping (ChangedFile) -> Void, openPath: @escaping (String) -> Void,
        onBack: (() -> Void)?, showChanges: (() -> Void)?
    ) {
        self.path = path
        self.rootName = rootName
        self.services = services
        self.change = change
        self.openDiff = openDiff
        self.openPath = openPath
        self.onBack = onBack
        self.showChanges = showChanges
        _viewer = State(initialValue: FileViewerModel(path: path, read: services.read, image: services.image, describe: services.describe))
    }

    private var isCode: Bool { !viewer.isMarkdown }

    var body: some View {
        VStack(spacing: 0) {
            if dynamicTypeSize.isAccessibilitySize { titleBlock }
            content
        }
        .background(Theme.surface)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .principal) {
                if !dynamicTypeSize.isAccessibilitySize { header }
            }
            ToolbarItem(placement: .primaryAction) { trailing }
        }
        .task { await viewer.load() }
    }

    // MARK: Header

    private var location: String { viewer.folder.isEmpty ? rootName : viewer.folder }

    private var header: some View {
        VStack(spacing: 0) {
            Text(viewer.name)
                .font(.headline)
                .lineLimit(1)
                .truncationMode(.middle)
            Text(location)
                .font(.caption)
                .foregroundStyle(Theme.muted)
                .lineLimit(1)
                .truncationMode(.head)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(viewer.name), in \(location)")
        .accessibilityAddTraits(.isHeader)
    }

    /// At the accessibility sizes the bar cannot hold the name: it is the
    /// first thing on the page instead, in a calmer size than the text of
    /// the file below it (a headline at the largest size would fill the
    /// top of the screen).
    private var titleBlock: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(viewer.name).font(.subheadline.weight(.semibold)).fixedSize(horizontal: false, vertical: true)
            Text(location).font(.footnote).foregroundStyle(Theme.muted).fixedSize(horizontal: false, vertical: true)
        }
        .dynamicTypeSize(...DynamicTypeSize.accessibility2)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 16)
        .padding(.vertical, 6)
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isHeader)
    }

    @ViewBuilder private var trailing: some View {
        if case .text = viewer.phase, isCode {
            // Code, as the diff: one button for the one thing to change.
            Button { viewer.wraps.toggle() } label: {
                Image(systemName: viewer.wraps ? "arrow.left.and.right" : "text.alignleft")
            }
            .accessibilityLabel(viewer.wraps ? "Do not wrap lines" : "Wrap lines")
            .accessibilityIdentifier("viewer.wrap")
        } else {
            Menu {
                Button { Task { await viewer.load() } } label: { Label("Refresh", systemImage: "arrow.clockwise") }
                    .accessibilityIdentifier("viewer.menu.refresh")
                Button { UIPasteboard.general.string = path } label: { Label("Copy path", systemImage: "doc.on.doc") }
                    .accessibilityIdentifier("viewer.menu.copyPath")
                if case .text = viewer.phase, viewer.mode == .raw {
                    Button { viewer.wraps.toggle() } label: {
                        Label(viewer.wraps ? "Do not wrap lines" : "Wrap lines", systemImage: viewer.wraps ? "arrow.left.and.right" : "text.alignleft")
                    }
                    .accessibilityIdentifier("viewer.menu.wrap")
                }
                if let change {
                    Button { openDiff(change) } label: { Label("Show in Changes", systemImage: "plusminus") }
                        .accessibilityIdentifier("viewer.menu.changes")
                }
            } label: {
                Image(systemName: "ellipsis.circle").accessibilityLabel("More")
            }
            .accessibilityIdentifier("viewer.menu")
        }
    }

    // MARK: Content

    @ViewBuilder private var content: some View {
        switch viewer.phase {
        case .loading:
            ProgressView("Reading \(viewer.name)…")
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .accessibilityIdentifier("viewer.loading")
        case let .text(truncated):
            text(truncated: truncated)
        case let .image(image):
            ImageViewer(image: image, name: viewer.name, bytes: viewer.content?.size ?? 0)
                .id(ObjectIdentifier(image.cgImage))
        case .unreadableImage:
            BrowserState(
                symbol: "photo", title: "This image can't be shown",
                message: "\(viewer.name) is not a picture this phone can open.",
                action: backAction, identifier: "viewer.image.failed"
            )
            .scrolling()
        case .binary:
            let size = viewer.content.map { FileSizeText.text($0.size) } ?? ""
            BrowserState(
                symbol: "doc", title: "This file is not text",
                message: "\(viewer.name)\(size.isEmpty ? "" : " · \(size)"). There is nothing to show here, so open it on your computer.",
                action: backAction, identifier: "viewer.binary"
            )
            .scrolling()
        case let .failed(failure):
            failedState(failure).scrolling()
        }
    }

    private func failedState(_ failure: FilesFailure) -> BrowserState {
        var back: (title: String, run: () -> Void)?
        if let onBack { back = (title: "Back", run: onBack) }
        return BrowserWords.state(
            failure, subject: viewer.failureSubject, root: rootName, name: viewer.name, size: viewer.content?.size,
            retry: { Task { await viewer.load() } }, back: back, changes: showChanges
        )
    }

    private var backAction: BrowserState.Action? {
        onBack.map { back in .init(title: "Back", identifier: "browser.state.back", run: back) }
    }

    private func text(truncated: Bool) -> some View {
        VStack(spacing: 0) {
            infoBar
            if viewer.isMarkdown && viewer.mode == .rendered {
                MarkdownDocument(blocks: viewer.blocks, path: path, services: services, openPath: openPath, truncated: truncated)
                    .accessibilityIdentifier("viewer.markdown")
            } else {
                CodeView(lines: viewer.lines, wraps: viewer.wraps, truncated: truncated)
                    .accessibilityIdentifier("viewer.code")
            }
        }
        .safeAreaInset(edge: .bottom, spacing: 0) { changesBar }
    }

    /// Rendered | Raw (Markdown only), and the file's size and time.
    private var infoBar: some View {
        let meta = viewer.content.map { Self.meta($0) } ?? ""
        let layout = dynamicTypeSize.isAccessibilitySize
            ? AnyLayout(VStackLayout(alignment: .leading, spacing: 6))
            : AnyLayout(HStackLayout(alignment: .center, spacing: 10))
        return layout {
            if viewer.isMarkdown {
                SegmentedChoice(
                    options: FileViewerModel.Mode.allCases, selected: viewer.mode, title: { $0.title },
                    identifier: "viewer.mode", accessibilityName: "View"
                ) { mode in Task { await viewer.select(mode) } }
            }
            if !dynamicTypeSize.isAccessibilitySize { Spacer(minLength: 0) }
            Text(meta)
                .font(.caption)
                .foregroundStyle(Theme.muted)
                .multilineTextAlignment(.trailing)
                .accessibilityIdentifier("viewer.meta")
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
        .overlay(alignment: .bottom) { Rectangle().fill(Theme.line).frame(height: 1) }
    }

    static func meta(_ content: FileContent) -> String {
        var parts = [FileSizeText.text(content.size)]
        if let modified = content.modified { parts.append(FileTimeText.relative(modified, now: Date())) }
        return parts.joined(separator: " · ")
    }

    /// "New in Changes · +41": the way into this file's diff.
    @ViewBuilder private var changesBar: some View {
        if let change {
            Button { openDiff(change) } label: {
                HStack(spacing: 8) {
                    Image(systemName: "plusminus").accessibilityHidden(true)
                    Text(Self.changeWord(change)).frame(maxWidth: .infinity, alignment: .leading)
                    if !change.isBinary { LineCounts(added: change.added, deleted: change.deleted) }
                    Image(systemName: "chevron.right")
                        .font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(Theme.chevron)
                        .accessibilityHidden(true)
                }
                .font(.subheadline)
                .foregroundStyle(Theme.ink)
                .padding(.horizontal, 16)
                .frame(minHeight: 44)
                .background(Theme.card)
                .overlay(alignment: .top) { Rectangle().fill(Theme.line).frame(height: 1) }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel("\(Self.changeWord(change)), \(LineCountText.spoken(added: change.added, deleted: change.deleted)). Opens its diff.")
            .accessibilityIdentifier("viewer.changes")
        }
    }

    static func changeWord(_ change: ChangedFile) -> String {
        switch change.status {
        case "untracked", "added": "New in Changes"
        default: "Changed in Changes"
        }
    }
}

// MARK: - Markdown

/// A Markdown file as a document: the app's own Markdown blocks (paragraphs,
/// lists, code, tables), pictures from the folder, and links to the folder's
/// other files. A link to anywhere else goes to the system, or nowhere. The
/// blocks are made off the main actor (`FileViewerModel`) and drawn lazily,
/// so a long document costs what is on screen.
private struct MarkdownDocument: View {
    let blocks: [MarkdownRenderer.Block]
    let path: String
    let services: BrowserServices
    let openPath: (String) -> Void
    let truncated: Bool

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                MarkdownBlocksView(blocks: blocks, lazy: true, keepsPathsWhole: true) { alt, source in
                    AnyView(MarkdownPicture(alt: alt, source: source, from: path, services: services, openPath: openPath))
                }
                .textSelection(.enabled)
                if truncated { TruncatedNote() }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 14)
        }
        .softTopEdge()
        .environment(\.openURL, OpenURLAction { url in
            if let scheme = url.scheme?.lowercased() {
                return scheme == "http" || scheme == "https" ? .systemAction : .discarded
            }
            guard let target = FilePath.resolve(url.relativeString, from: path) else { return .discarded }
            openPath(target)
            return .handled
        })
    }
}

/// An image a document points at, read from the same folder.
private struct MarkdownPicture: View {
    let alt: String
    let source: String
    let from: String
    let services: BrowserServices
    let openPath: (String) -> Void

    @State private var image: FileImage?
    @State private var failed = false

    init(alt: String, source: String, from: String, services: BrowserServices, openPath: @escaping (String) -> Void) {
        self.alt = alt
        self.source = source
        self.from = from
        self.services = services
        self.openPath = openPath
    }

    private var target: String? { FilePath.resolve(source, from: from) }
    private var title: String { alt.isEmpty ? FilePath.name(of: source) : alt }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if let image {
                Button {
                    if let target { openPath(target) }
                } label: {
                    Image(decorative: image.cgImage, scale: 1)
                        .resizable()
                        .scaledToFit()
                        .frame(maxWidth: .infinity)
                        .clipShape(RoundedRectangle(cornerRadius: 6, style: .continuous))
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Picture: \(title)")
                .accessibilityHint("Opens it full size")
            } else if target == nil || failed {
                Label(target == nil ? "\(title) is not in this folder" : "\(title) could not be shown", systemImage: "photo")
                    .font(.footnote)
                    .foregroundStyle(Theme.muted)
            } else {
                ProgressView().frame(maxWidth: .infinity, minHeight: 80)
            }
            if let target, image != nil {
                Text("\(title) · \(target)")
                    .font(.footnote)
                    .foregroundStyle(Theme.muted)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .task {
            guard let target else { return }
            do {
                // The bytes live only in this scope: what stays is the
                // picture, decoded and cut down to the width of the page.
                let data = try await services.image(target)
                image = await FileImage.decode(data, maxPixel: FileImage.inlineMaxPixel)
                failed = image == nil
            } catch {
                failed = true
            }
        }
    }
}

private struct TruncatedNote: View {
    var body: some View {
        Label("Only the first 1 MB is shown.", systemImage: "ellipsis.circle")
            .font(.footnote)
            .foregroundStyle(Theme.muted)
            .accessibilityIdentifier("viewer.truncated")
    }
}

// MARK: - Code

/// A text as numbered lines, in the monospaced font: wrapping, or scrolling
/// sideways. The numbers are for the eye; VoiceOver reads the lines.
private struct CodeView: View {
    let lines: [CodeLine]
    let wraps: Bool
    let truncated: Bool

    @ScaledMetric(relativeTo: .footnote) private var digitWidth: CGFloat = 8

    private var gutter: CGFloat { CGFloat(String(lines.count).count) * digitWidth + 18 }

    var body: some View {
        ScrollView(wraps ? .vertical : [.vertical, .horizontal]) {
            LazyVStack(alignment: .leading, spacing: 0) {
                if lines.isEmpty {
                    Text("This file is empty.")
                        .font(.footnote)
                        .foregroundStyle(Theme.muted)
                        .padding(16)
                }
                ForEach(lines) { line in
                    HStack(alignment: .firstTextBaseline, spacing: 0) {
                        Text("\(line.number)")
                            .foregroundStyle(Theme.muted)
                            .frame(width: gutter, alignment: .trailing)
                            .padding(.trailing, 10)
                            .accessibilityHidden(true)
                        // Selection is on the text of the line only, so the
                        // numbers never come along with a copy.
                        Text(Self.styled(line))
                            .fixedSize(horizontal: !wraps, vertical: true)
                            .frame(maxWidth: wraps ? .infinity : nil, alignment: .leading)
                            .textSelection(.enabled)
                    }
                    .font(.system(.footnote, design: .monospaced))
                    .padding(.trailing, 12)
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel(line.text.isEmpty ? "Blank line" : line.text)
                }
                if truncated { TruncatedNote().padding(16) }
            }
            .padding(.vertical, 10)
        }
        .scrollBounceBehavior(.basedOnSize)
        .softTopEdge()
    }

    /// A line in its colours: made for the lines on screen only.
    private static func styled(_ line: CodeLine) -> AttributedString {
        guard !line.text.isEmpty else { return AttributedString(" ") }
        var out = AttributedString()
        for piece in line.pieces {
            var part = AttributedString(piece.text)
            if let kind = piece.kind { part.foregroundColor = SyntaxColors.color(kind) }
            out += part
        }
        return out
    }
}

/// The colours of the code viewer, from the design's code board.
private enum SyntaxColors {
    static func color(_ kind: SyntaxKind) -> Color {
        switch kind {
        case .keyword: keyword
        case .string: string
        case .number: number
        case .comment: Theme.muted
        case .call: call
        }
    }

    private static func dynamic(light: UInt32, dark: UInt32) -> Color {
        func ui(_ hex: UInt32) -> UIColor {
            UIColor(red: CGFloat((hex >> 16) & 0xFF) / 255, green: CGFloat((hex >> 8) & 0xFF) / 255, blue: CGFloat(hex & 0xFF) / 255, alpha: 1)
        }
        return Color(UIColor { $0.userInterfaceStyle == .dark ? ui(dark) : ui(light) })
    }

    private static let keyword = dynamic(light: 0x6A46B0, dark: 0xB195F0)
    private static let string = dynamic(light: 0x1D6B46, dark: 0x5CC792)
    private static let number = dynamic(light: 0x8A5700, dark: 0xF5B83D)
    private static let call = dynamic(light: 0x1F5F9E, dark: 0x6AA8EB)
}

// MARK: - Image

/// An image, fitted, with pinch and double-tap to zoom, and a Fit | 100%
/// switch under it. What is held is the decoded picture (cut down to what
/// the phone can draw); the file's own bytes are already gone.
private struct ImageViewer: View {
    let image: FileImage
    let name: String
    let bytes: Int

    @State private var picture: UIImage
    @State private var zoom: CGFloat = 1
    @State private var fitting = true
    @State private var command: ZoomChoice?

    init(image: FileImage, name: String, bytes: Int) {
        self.image = image
        self.name = name
        self.bytes = bytes
        // The scale makes its size in points the original's size in pixels,
        // so 100% is the real picture even when it was cut down.
        _picture = State(initialValue: UIImage(cgImage: image.cgImage, scale: image.scale, orientation: .up))
    }

    var body: some View {
        VStack(spacing: 0) {
            ZStack {
                Theme.fill
                ZoomableImage(image: picture, name: name, zoom: $zoom, fitting: $fitting, command: $command)
            }
            bar
        }
    }

    private var bar: some View {
        let selected: ZoomChoice? = fitting ? .fit : (abs(zoom - 1) < 0.005 ? .actual : nil)
        return HStack(spacing: 10) {
            VStack(alignment: .leading, spacing: 1) {
                Text(ImageZoomText.hint(zoom: Double(zoom), fitting: fitting))
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(Theme.ink)
                    .accessibilityIdentifier("viewer.image.zoom")
                Text("\(image.sizeText) · \(FileSizeText.text(bytes))")
                    .font(.caption)
                    .foregroundStyle(Theme.muted)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            SegmentedChoice(
                options: ZoomChoice.allCases, selected: selected, title: { $0.title }, spoken: { $0.spoken },
                identifier: "viewer.image.zoomChoice", accessibilityName: "Zoom"
            ) { command = $0 }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
        .background(Theme.card)
        .overlay(alignment: .top) { Rectangle().fill(Theme.line).frame(height: 1) }
    }
}
