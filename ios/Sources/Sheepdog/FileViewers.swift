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
    /// first thing on the page instead.
    private var titleBlock: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(viewer.name).font(.headline).fixedSize(horizontal: false, vertical: true)
            Text(location).font(.subheadline).foregroundStyle(Theme.muted).fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
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
        case let .image(data):
            ImageViewer(data: data, name: viewer.name, bytes: viewer.content?.size ?? data.count)
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
            failure, subject: .file, root: rootName, name: viewer.name, size: viewer.content?.size,
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
                MarkdownDocument(source: viewer.source, path: path, services: services, openPath: openPath, truncated: truncated)
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
                ) { viewer.mode = $0 }
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
                    if !change.isBinary {
                        HStack(spacing: 6) {
                            Text("+\(change.added)").foregroundStyle(DiffColors.added)
                            if change.deleted > 0 { Text("−\(change.deleted)").foregroundStyle(DiffColors.removed) }
                        }
                        .font(.footnote.monospacedDigit().weight(.medium))
                    }
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
            .accessibilityLabel("\(Self.changeWord(change)), \(change.added) added, \(change.deleted) removed. Opens its diff.")
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
/// other files. A link to anywhere else goes to the system, or nowhere.
private struct MarkdownDocument: View {
    let source: String
    let path: String
    let services: BrowserServices
    let openPath: (String) -> Void
    let truncated: Bool

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                MarkdownBlocksView(source) { alt, source in
                    AnyView(MarkdownPicture(alt: alt, source: source, from: path, services: services, openPath: openPath))
                }
                .textSelection(.enabled)
                if truncated { TruncatedNote() }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 14)
        }
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

    @State private var image: UIImage?
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
                    Image(uiImage: image)
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
                let data = try await services.image(target)
                image = await Task.detached { UIImage(data: data) }.value
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
                        Text(line.text.isEmpty ? " " : line.text)
                            .fixedSize(horizontal: !wraps, vertical: true)
                            .frame(maxWidth: wraps ? .infinity : nil, alignment: .leading)
                    }
                    .font(.system(.footnote, design: .monospaced))
                    .padding(.trailing, 12)
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel(line.text.isEmpty ? "Blank line" : line.text)
                }
                if truncated { TruncatedNote().padding(16) }
            }
            .padding(.vertical, 10)
            .textSelection(.enabled)
        }
        .scrollBounceBehavior(.basedOnSize)
    }
}

// MARK: - Image

/// An image, fitted, with pinch and double-tap to zoom, and a Fit | 100%
/// switch under it. The bytes stay in memory, in this view.
private struct ImageViewer: View {
    let data: Data
    let name: String
    let bytes: Int

    @State private var image: UIImage?
    @State private var failed = false
    @State private var zoom: CGFloat = 1
    @State private var fitting = true
    @State private var command: ZoomChoice?

    var body: some View {
        VStack(spacing: 0) {
            ZStack {
                Theme.fill
                if let image {
                    ZoomableImage(image: image, name: name, zoom: $zoom, fitting: $fitting, command: $command)
                } else if failed {
                    BrowserState(symbol: "photo", title: "This image can't be shown", message: "\(name) is not a picture this phone can open.", identifier: "viewer.image.failed")
                } else {
                    ProgressView()
                }
            }
            if let image { bar(image) }
        }
        .task {
            let decoded = await Task.detached { UIImage(data: data) }.value
            image = decoded
            failed = decoded == nil
        }
    }

    private func bar(_ image: UIImage) -> some View {
        let selected: ZoomChoice? = fitting ? .fit : (abs(zoom - 1) < 0.005 ? .actual : nil)
        return HStack(spacing: 10) {
            VStack(alignment: .leading, spacing: 1) {
                Text(fitting ? "Pinch or double-tap to zoom" : "\(Int((zoom * 100).rounded()))%")
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(Theme.ink)
                    .accessibilityIdentifier("viewer.image.zoom")
                Text("\(Int(image.size.width)) × \(Int(image.size.height)) · \(FileSizeText.text(bytes))")
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
