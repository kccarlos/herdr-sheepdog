import SheepdogKit
import SwiftUI

/// The agent's changes, read-only (docs/dev/design/phone-terminal.md §5
/// "Changes view"): a file list, then a file's diff with word highlights.
/// Opened from the agent window's ⋯ menu as a sheet.
struct ChangesView: View {
    let paneID: String
    let name: String
    let load: () async throws -> AgentChanges?
    let loadDiff: (String) async throws -> FileDiff?

    @Environment(\.dismiss) private var dismiss
    @State private var phase: Phase = .loading

    enum Phase {
        case loading
        case loaded(AgentChanges)
        case failed(String)
    }

    var body: some View {
        NavigationStack {
            content
                .navigationTitle("Changes")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Done") { dismiss() }
                            .accessibilityIdentifier("changes.done")
                    }
                    ToolbarItem(placement: .primaryAction) {
                        Button {
                            Task { await refresh() }
                        } label: {
                            Image(systemName: "arrow.clockwise")
                        }
                        .accessibilityLabel("Refresh")
                        .accessibilityIdentifier("changes.refresh")
                    }
                }
                .navigationDestination(for: ChangedFile.self) { file in
                    DiffView(file: file, load: { try await loadDiff(file.path) })
                }
        }
        .task { await refresh() }
    }

    @ViewBuilder private var content: some View {
        switch phase {
        case .loading:
            ProgressView("Reading changes…")
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        case let .failed(message):
            ContentUnavailableView {
                Label("Could not read the changes", systemImage: "exclamationmark.triangle")
            } description: {
                Text(message)
            } actions: {
                Button("Try again") { Task { await refresh() } }
            }
        case let .loaded(changes) where !changes.repo:
            ContentUnavailableView {
                Label("No repository", systemImage: "folder")
            } description: {
                Text(changes.noRepoMessage)
            }
            .accessibilityIdentifier("changes.noRepo")
        case let .loaded(changes) where changes.files.isEmpty:
            ContentUnavailableView {
                Label("No changes yet", systemImage: "checkmark.circle")
            } description: {
                Text("\(name) has not changed any files\(changes.branch.map { " on \($0)" } ?? "").")
            }
            .refreshable { await refresh() }
            .accessibilityIdentifier("changes.empty")
        case let .loaded(changes):
            list(changes)
        }
    }

    private func list(_ changes: AgentChanges) -> some View {
        List {
            Section {
                ForEach(changes.files) { file in
                    NavigationLink(value: file) { FileRow(file: file) }
                        .accessibilityIdentifier("changes.file.\(file.path)")
                }
            } header: {
                HStack(spacing: 6) {
                    Text(changes.summary)
                        .lineLimit(1)
                        .truncationMode(.middle)
                    Spacer(minLength: 8)
                    LineCounts(added: changes.added, deleted: changes.deleted)
                }
                .font(.footnote)
                .textCase(nil)
                .accessibilityElement(children: .combine)
            } footer: {
                if changes.truncated == true {
                    Text("Only the first \(changes.files.count) files are shown.")
                }
            }
        }
        .listStyle(.insetGrouped)
        .refreshable { await refresh() }
        .accessibilityIdentifier("changes.list")
    }

    private func refresh() async {
        do {
            guard let changes = try await load() else {
                phase = .failed("Update sheepdog on your computer to see changes.")
                return
            }
            phase = .loaded(changes)
        } catch {
            phase = .failed(changesErrorText(error))
        }
    }
}

/// One file: its name, folder and counts.
private struct FileRow: View {
    let file: ChangedFile

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: symbol)
                .font(.body)
                .foregroundStyle(tint)
                .frame(width: 22)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(file.fileName)
                    .font(.body.weight(.medium))
                    .lineLimit(2)
                    .truncationMode(.middle)
                if !detail.isEmpty {
                    Text(detail)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                        .truncationMode(.head)
                }
            }
            Spacer(minLength: 8)
            if file.isBinary {
                Text("binary").font(.caption).foregroundStyle(.secondary)
            } else {
                LineCounts(added: file.added, deleted: file.deleted)
            }
        }
        .padding(.vertical, 2)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(file.path), \(statusWord), \(file.added) added, \(file.deleted) removed")
    }

    private var detail: String {
        if let old = file.oldPath { return "from \(old)" }
        return file.folder
    }

    private var statusWord: String {
        switch file.status {
        case "untracked", "added": "new"
        case "conflicted": "conflict"
        default: file.status
        }
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
        default: .secondary
        }
    }
}

/// "+42 −7" in green and red.
private struct LineCounts: View {
    let added: Int
    let deleted: Int

    var body: some View {
        HStack(spacing: 6) {
            Text("+\(added)").foregroundStyle(DiffColors.added)
            Text("−\(deleted)").foregroundStyle(DiffColors.removed)
        }
        .font(.footnote.monospacedDigit().weight(.medium))
        .accessibilityLabel("\(added) added, \(deleted) removed")
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

    @State private var lines: [DiffLine]?
    @State private var diff: FileDiff?
    @State private var error: String?
    @State private var wrap = true

    var body: some View {
        content
            .navigationTitle(file.fileName)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .primaryAction) {
                    Button {
                        wrap.toggle()
                    } label: {
                        Image(systemName: wrap ? "arrow.left.and.right" : "text.alignleft")
                    }
                    .accessibilityLabel(wrap ? "Do not wrap lines" : "Wrap lines")
                    .accessibilityIdentifier("diff.wrap")
                }
            }
            .task { await refresh() }
    }

    @ViewBuilder private var content: some View {
        if let error {
            ContentUnavailableView {
                Label("Could not read the diff", systemImage: "exclamationmark.triangle")
            } description: {
                Text(error)
            } actions: {
                Button("Try again") { Task { await refresh() } }
            }
        } else if let lines {
            if lines.isEmpty {
                ContentUnavailableView {
                    Label(file.isBinary || diff?.binary == true ? "Binary file" : "No text changes", systemImage: "doc")
                } description: {
                    Text(file.isBinary || diff?.binary == true ? "This file is not text, so there is no diff to show." : "Only the file's mode or name changed.")
                }
            } else {
                ScrollView(wrap ? .vertical : [.vertical, .horizontal]) {
                    LazyVStack(alignment: .leading, spacing: 0) {
                        if !file.folder.isEmpty {
                            Text(file.path)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                                .padding(.horizontal, 12)
                                .padding(.vertical, 8)
                        }
                        ForEach(lines) { DiffRow(line: $0, wrap: wrap) }
                        if diff?.truncated == true {
                            Text("The rest of this diff is too long to show here.")
                                .font(.footnote)
                                .foregroundStyle(.secondary)
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
                error = "This file has no changes any more."
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

/// The daemon's own words, or a plain sentence.
func changesErrorText(_ error: Error) -> String {
    if let daemon = error as? DaemonError {
        if case let .rejected(_, message) = daemon, !message.isEmpty { return message }
        return daemon.message
    }
    return error.localizedDescription
}
