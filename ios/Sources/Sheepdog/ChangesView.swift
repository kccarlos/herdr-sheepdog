import SheepdogKit
import SwiftUI

/// What the Changes tab shows while it reads.
enum ChangesPhase {
    case loading
    case loaded(AgentChanges)
    /// A daemon from before the route.
    case olderDaemon
    /// The words, and the request's own failure when the computer did not
    /// answer at all.
    case failed(String, RequestFailure?)
}

/// The agent's changes, read-only (docs/dev/design/phone-terminal.md §5
/// "Changes view"): what changed in the folder since the last commit, a
/// file at a time. A tab of the browser sheet beside Files.
struct ChangesListView: View {
    let phase: ChangesPhase
    let name: String
    /// The open diff's path, highlighted when the diff sits beside the list.
    let selectedPath: String?
    let open: (ChangedFile) -> Void
    let reload: () async -> Void
    /// Where the folder's files are, when they can be browsed.
    let showFiles: (() -> Void)?

    var body: some View {
        switch phase {
        case .loading:
            ProgressView("Reading changes…")
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .accessibilityIdentifier("changes.loading")
        case .olderDaemon:
            BrowserState(
                symbol: "arrow.clockwise", title: "Update sheepdog", message: "Update sheepdog on your computer to see changes.",
                identifier: "changes.older"
            )
            .scrolling()
        case let .failed(message, request):
            if let request, request.isUnreachable {
                BrowserWords.state(.request(request), subject: .folder, root: name, retry: { Task { await reload() } }).scrolling()
            } else {
                BrowserState(
                    symbol: "exclamationmark.triangle", title: "Could not read the changes", message: message,
                    action: .init(title: "Try again", identifier: "changes.retry") { Task { await reload() } },
                    identifier: "changes.failed"
                )
                .scrolling()
            }
        case let .loaded(changes) where !changes.repo:
            noRepository(changes)
        case let .loaded(changes) where changes.files.isEmpty:
            BrowserState(
                symbol: "checkmark", title: "No changes yet",
                message: (changes.branch.flatMap { $0.isEmpty ? nil : $0 }.map { "Nothing has changed on \($0) since the last commit." }
                    ?? "Nothing has changed since the last commit.") + (changes.hidden > 0 ? " \(changes.hidden) hidden files excluded for safety." : ""),
                action: .init(title: "Refresh", identifier: "changes.empty.refresh") { Task { await reload() } },
                identifier: "changes.empty"
            )
            .scrolling()
        case let .loaded(changes):
            list(changes)
        }
    }

    private func noRepository(_ changes: AgentChanges) -> some View {
        let filesStillThere = showFiles != nil && changes.reason != "no_cwd"
        return BrowserState(
            symbol: "folder", title: "No repository",
            message: changes.noRepoMessage + (filesStillThere ? " Its files are still here." : ""),
            action: filesStillThere ? .init(title: "Show Files", identifier: "changes.showFiles") { showFiles?() } : nil,
            identifier: "changes.noRepo"
        )
        .scrolling()
    }

    private func list(_ changes: AgentChanges) -> some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 8) {
                    Text(changes.summary)
                        .font(.footnote)
                        .foregroundStyle(Theme.muted)
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    LineCounts(added: changes.added, deleted: changes.deleted)
                }
                .padding(.horizontal, 4)
                .accessibilityElement(children: .combine)
                .accessibilityAddTraits(.isHeader)
                LazyVStack(spacing: 0) {
                    ForEach(Array(changes.files.enumerated()), id: \.element.id) { index, file in
                        if index > 0 { Divider().overlay(Theme.line) }
                        Button { open(file) } label: {
                            ChangeRow(file: file, selected: selectedPath == file.path)
                        }
                        .buttonStyle(.plain)
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel(ChangeRow.spoken(file))
                        .accessibilityAddTraits(.isButton)
                        .accessibilityIdentifier("changes.file.\(file.path)")
                    }
                }
                .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).strokeBorder(Theme.line, lineWidth: 1))
                .clipShape(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous))
                if changes.truncated == true {
                    Text("Only the first \(changes.files.count) files are shown.")
                        .font(.footnote)
                        .foregroundStyle(Theme.muted)
                        .padding(.horizontal, 4)
                }
                Text("Everything changed in this folder since the last commit, by any agent. Read only.")
                    .font(.footnote)
                    .foregroundStyle(Theme.muted)
                    .padding(.horizontal, 4)
            }
            .padding(.horizontal, 16)
            .padding(.top, 10)
            .padding(.bottom, 24)
        }
        .refreshable { await reload() }
        .accessibilityIdentifier("changes.list")
    }
}

/// One file: its name, folder and status, and its counts. In the list of
/// files to jump to, the file being read has a check instead of a chevron.
struct ChangeRow: View {
    let file: ChangedFile
    let selected: Bool
    var current = false

    var body: some View {
        BrowserRow(chevron: !current) {
            Image(systemName: symbol)
                .font(.title3)
                .foregroundStyle(tint)
                .accessibilityHidden(true)
        } content: {
            VStack(alignment: .leading, spacing: 1) {
                Text(file.fileName)
                    .font(.body.weight(.medium))
                    .foregroundStyle(Theme.ink)
                    .fixedSize(horizontal: false, vertical: true)
                if !file.detailLine.isEmpty {
                    Text(file.detailLine)
                        .font(.caption)
                        .foregroundStyle(Theme.muted)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        } accessory: {
            HStack(spacing: 8) {
                if file.isBinary {
                    Text("binary").font(.caption).foregroundStyle(Theme.muted)
                } else {
                    LineCounts(added: file.added, deleted: file.deleted)
                }
                if current {
                    Image(systemName: "checkmark")
                        .font(.footnote.weight(.bold))
                        .foregroundStyle(Theme.primaryText)
                        .accessibilityHidden(true)
                }
            }
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 8)
        .frame(minHeight: 54)
        .background(selected ? Theme.accentSoft : Color.clear)
        .contentShape(Rectangle())
    }

    static func spoken(_ file: ChangedFile) -> String {
        let counts = file.isBinary ? "binary" : LineCountText.spoken(added: file.added, deleted: file.deleted)
        return "\(file.path), \(file.statusWord), \(counts)"
    }

    private var symbol: String {
        switch file.status {
        case "added", "untracked": "plus.square"
        case "deleted": "minus.square"
        case "renamed": "arrow.right.square"
        case "conflicted": "exclamationmark.triangle"
        default: "square.and.pencil"
        }
    }

    private var tint: Color {
        switch file.status {
        case "added", "untracked": DiffColors.added
        case "deleted": DiffColors.removed
        case "conflicted": Theme.blockedText
        default: Theme.muted
        }
    }
}

enum DiffColors {
    static let added = Color(UIColor { $0.userInterfaceStyle == .dark ? UIColor(red: 0.42, green: 0.82, blue: 0.52, alpha: 1) : UIColor(red: 0.10, green: 0.50, blue: 0.22, alpha: 1) })
    static let removed = Color(UIColor { $0.userInterfaceStyle == .dark ? UIColor(red: 1.0, green: 0.48, blue: 0.45, alpha: 1) : UIColor(red: 0.75, green: 0.16, blue: 0.14, alpha: 1) })
    static let addedFill = Color(.systemGreen).opacity(0.12)
    static let removedFill = Color(.systemRed).opacity(0.12)
    static let addedWord = Color(.systemGreen).opacity(0.32)
    static let removedWord = Color(.systemRed).opacity(0.32)
}

/// One file's diff.
struct DiffView: View {
    let file: ChangedFile
    let load: () async throws -> FileDiff?
    /// Every changed file, for "go to another file"; one file has no jump.
    var files: [ChangedFile] = []
    var jump: (ChangedFile) -> Void = { _ in }

    @State private var lines: [DiffLine]?
    @State private var diff: FileDiff?
    @State private var error: String?
    @State private var wrap = true
    @State private var jumping = false
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    private var canJump: Bool { files.count > 1 }

    var body: some View {
        VStack(spacing: 0) {
            if dynamicTypeSize.isAccessibilitySize {
                VStack(alignment: .leading, spacing: 2) {
                    Text(file.fileName).font(.subheadline.weight(.semibold)).fixedSize(horizontal: false, vertical: true)
                    Text(file.detailLine).font(.footnote).foregroundStyle(Theme.muted).fixedSize(horizontal: false, vertical: true)
                }
                .dynamicTypeSize(...DynamicTypeSize.accessibility2)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 16)
                .padding(.vertical, 6)
                .accessibilityElement(children: .combine)
                .accessibilityAddTraits(.isHeader)
            }
            content
        }
        .background(Theme.surface)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .principal) {
                if !dynamicTypeSize.isAccessibilitySize {
                    VStack(spacing: 0) {
                        Text(file.fileName).font(.headline).lineLimit(1).truncationMode(.middle)
                        if !file.detailLine.isEmpty {
                            Text(file.detailLine).font(.caption).foregroundStyle(Theme.muted).lineLimit(1).truncationMode(.head)
                        }
                    }
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel("\(file.fileName), \(file.detailLine)")
                    .accessibilityAddTraits(.isHeader)
                }
            }
            ToolbarItemGroup(placement: .primaryAction) {
                if canJump {
                    Button { jumping = true } label: { Image(systemName: "list.bullet") }
                        .accessibilityLabel("Go to another file")
                        .accessibilityIdentifier("diff.jump")
                }
                Button {
                    wrap.toggle()
                } label: {
                    Image(systemName: wrap ? "arrow.left.and.right" : "text.alignleft")
                }
                .accessibilityLabel(wrap ? "Do not wrap lines" : "Wrap lines")
                .accessibilityIdentifier("diff.wrap")
            }
        }
        .sheet(isPresented: $jumping) {
            DiffJumpSheet(files: files, current: file.path, pick: jump)
        }
        .task { await refresh() }
    }

    @ViewBuilder private var content: some View {
        if let error {
            BrowserState(
                symbol: "exclamationmark.triangle", title: "Could not read the diff", message: error,
                action: .init(title: "Try again", identifier: "diff.retry") { Task { await refresh() } }, identifier: "diff.failed"
            )
            .scrolling()
        } else if let lines {
            if lines.isEmpty {
                let binary = file.isBinary || diff?.binary == true
                BrowserState(
                    symbol: "doc", title: binary ? "Binary file" : "No text changes",
                    message: binary ? "This file is not text, so there is no diff to show." : "Only the file's mode or name changed.",
                    identifier: binary ? "diff.binary" : "diff.noText"
                )
                .scrolling()
            } else {
                ScrollView(wrap ? .vertical : [.vertical, .horizontal]) {
                    // The file's folder and status are in the bar above, so
                    // the lines start at once.
                    LazyVStack(alignment: .leading, spacing: 0) {
                        ForEach(lines) { DiffRow(line: $0, wrap: wrap) }
                        if diff?.truncated == true {
                            Text("The rest of this diff is too long to show here.")
                                .font(.footnote)
                                .foregroundStyle(Theme.muted)
                                .padding(12)
                        }
                    }
                    .padding(.bottom, 24)
                }
                .accessibilityIdentifier("diff.lines")
            }
        } else {
            ProgressView()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private func refresh() async {
        do {
            guard let got = try await load() else {
                error = "Update sheepdog on your computer to read diffs."
                return
            }
            diff = got
            lines = DiffParser.parse(got.diff)
            error = nil
        } catch {
            self.error = changesErrorText(error)
        }
    }
}

private struct DiffRow: View {
    let line: DiffLine
    let wrap: Bool

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 0) {
            Text(marker)
                .foregroundStyle(markerColor)
                .frame(width: 18, alignment: .center)
                .accessibilityHidden(true)
            styled
                .fixedSize(horizontal: !wrap, vertical: true)
                .frame(maxWidth: wrap ? .infinity : nil, alignment: .leading)
        }
        .font(.system(.footnote, design: .monospaced))
        .padding(.vertical, line.kind == .hunk ? 6 : 1)
        .padding(.trailing, 12)
        .background(fill)
        .padding(.top, line.kind == .hunk && line.id > 0 ? 10 : 0)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(accessibilityText)
    }

    private var styled: Text {
        if line.kind == .hunk {
            return Text(line.text).foregroundColor(.secondary)
        }
        let wordFill = line.kind == .added ? DiffColors.addedWord : DiffColors.removedWord
        var out = AttributedString()
        for seg in line.segments {
            var part = AttributedString(seg.text.replacingOccurrences(of: "\t", with: "    "))
            if seg.changed { part.backgroundColor = wordFill }
            out += part
        }
        if line.text.isEmpty { out = AttributedString(" ") }
        return Text(out).foregroundColor(line.kind == .meta ? .secondary : .primary)
    }

    private var marker: String {
        switch line.kind {
        case .added: "+"
        case .removed: "−"
        default: " "
        }
    }

    private var markerColor: Color {
        line.kind == .added ? DiffColors.added : DiffColors.removed
    }

    private var fill: Color {
        switch line.kind {
        case .added: DiffColors.addedFill
        case .removed: DiffColors.removedFill
        case .hunk: Color(.secondarySystemBackground)
        default: .clear
        }
    }

    private var accessibilityText: String {
        switch line.kind {
        case .added: "Added: \(line.text)"
        case .removed: "Removed: \(line.text)"
        case .hunk: "Section \(line.text)"
        default: line.text
        }
    }
}

/// "Go to file" (the design's DiffJump board): the files that changed, to
/// read another one's diff without going back to the list. The one being
/// read has a check.
private struct DiffJumpSheet: View {
    let files: [ChangedFile]
    let current: String
    let pick: (ChangedFile) -> Void

    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ScrollView {
                LazyVStack(spacing: 0) {
                    ForEach(Array(files.enumerated()), id: \.element.id) { index, file in
                        if index > 0 { Divider().overlay(Theme.line) }
                        Button {
                            if file.path != current { pick(file) }
                            dismiss()
                        } label: {
                            ChangeRow(file: file, selected: false, current: file.path == current)
                        }
                        .buttonStyle(.plain)
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel(ChangeRow.spoken(file) + (file.path == current ? ", showing" : ""))
                        .accessibilityAddTraits(file.path == current ? [.isButton, .isSelected] : .isButton)
                        .accessibilityIdentifier("diff.jump.\(file.path)")
                    }
                }
                .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).strokeBorder(Theme.line, lineWidth: 1))
                .clipShape(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous))
                .padding(.horizontal, 16)
                .padding(.vertical, 10)
            }
            .background(Theme.surface)
            .navigationTitle("Go to file")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                        .accessibilityIdentifier("diff.jump.done")
                }
            }
        }
        .tint(Theme.primaryText)
        .presentationDetents([.medium, .large])
    }
}

/// The daemon's own words, or a plain sentence.
func changesErrorText(_ error: Error) -> String {
    if let refusal = error as? FilesRefusal { return refusal.problem.words(subject: .file, root: "this folder").message }
    if let daemon = error as? DaemonError {
        if case let .rejected(_, message) = daemon, !message.isEmpty { return message }
        return daemon.message
    }
    return error.localizedDescription
}
