import SheepdogKit
import SwiftUI

/// The Files tab: a name search, the path above the list with its sort,
/// then the folder's entries (or what to do when there are none).
struct FilesListView: View {
    @Bindable var model: FileBrowserModel
    let changes: ChangeIndex
    /// The open file's path, highlighted when the viewer sits beside the list.
    let selectedPath: String?
    let openFile: (FileEntry) -> Void
    /// For a daemon without files: the one thing that still works.
    let showChanges: (() -> Void)?

    @FocusState private var searchFocused: Bool
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 8) {
                searchField
                if model.isSearching {
                    searchStatus
                } else {
                    pathBar
                }
                content
            }
            .padding(.horizontal, 16)
            .padding(.top, 10)
            .padding(.bottom, 24)
        }
        .scrollDismissesKeyboard(.interactively)
        .refreshable { await model.reload() }
        .accessibilityIdentifier("files.list")
    }

    // MARK: Search and path

    private var searchField: some View {
        HStack(spacing: 8) {
            Image(systemName: "magnifyingglass")
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(Theme.muted)
                .accessibilityHidden(true)
            TextField("Search file names", text: $model.query)
                .focused($searchFocused)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .submitLabel(.search)
                .accessibilityIdentifier("files.search")
            if !model.query.isEmpty {
                Button { model.clearSearch() } label: {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(Theme.muted)
                        .frame(minWidth: 44, minHeight: 44)
                        .contentShape(Rectangle())
                }
                .accessibilityLabel("Clear search")
                .accessibilityIdentifier("files.search.clear")
            }
        }
        .padding(.leading, 12)
        .padding(.trailing, model.query.isEmpty ? 12 : 0)
        .frame(minHeight: 44)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 12, style: .continuous).strokeBorder(searchFocused ? Color.accentColor : Theme.line, lineWidth: 1))
    }

    private var pathBar: some View {
        let layout = dynamicTypeSize.isAccessibilitySize
            ? AnyLayout(VStackLayout(alignment: .leading, spacing: 8))
            : AnyLayout(HStackLayout(alignment: .top, spacing: 8))
        return layout {
            crumbs.frame(maxWidth: .infinity, alignment: .leading)
            sortMenu
        }
    }

    private var crumbs: some View {
        let all = model.crumbs
        return OptionFlow(spacing: 2) {
            ForEach(Array(all.enumerated()), id: \.element.id) { index, crumb in
                HStack(spacing: 2) {
                    if index > 0 {
                        Image(systemName: "chevron.right")
                            .font(.system(size: 10, weight: .bold))
                            .foregroundStyle(Theme.chevron)
                            .accessibilityHidden(true)
                    }
                    if index == all.count - 1 {
                        Text(crumb.name)
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(Theme.ink)
                            .padding(.horizontal, 4)
                            .frame(minHeight: 44)
                            .accessibilityLabel("Current folder, \(crumb.name)")
                            .accessibilityIdentifier("files.crumb.current")
                    } else {
                        Button { Task { await model.open(folder: crumb.path) } } label: {
                            Text(crumb.name)
                                .font(.subheadline.weight(.medium))
                                .foregroundStyle(Theme.primaryText)
                                .padding(.horizontal, 4)
                                .frame(minHeight: 44)
                                .contentShape(Rectangle())
                        }
                        .accessibilityLabel("Go to folder \(crumb.name)")
                        .accessibilityIdentifier("files.crumb.\(crumb.path.isEmpty ? "root" : crumb.path)")
                    }
                }
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Folder path")
    }

    private var sortMenu: some View {
        Menu {
            Picker("Sort", selection: $model.sort) {
                ForEach(FileSort.allCases) { Text($0.title).tag($0) }
            }
        } label: {
            HStack(spacing: 5) {
                Text(model.sort.title)
                Image(systemName: "chevron.up.chevron.down").font(.system(size: 10, weight: .bold))
            }
            .font(.footnote.weight(.medium))
            .foregroundStyle(Theme.ink)
            .padding(.horizontal, 12)
            .frame(minHeight: 44)
            .background(Capsule().fill(Theme.card))
            .overlay(Capsule().strokeBorder(Theme.line, lineWidth: 1))
        }
        .accessibilityLabel("Sort: \(model.sort.title)")
        .accessibilityIdentifier("files.sort")
    }

    @ViewBuilder private var searchStatus: some View {
        if let summary = model.searchSummary {
            Text(summary)
                .font(.footnote)
                .foregroundStyle(Theme.muted)
                .padding(.horizontal, 4)
                .accessibilityIdentifier("files.search.summary")
        } else if model.results == .loading {
            Text("Searching…")
                .font(.footnote)
                .foregroundStyle(Theme.muted)
                .padding(.horizontal, 4)
        }
    }

    // MARK: Entries

    @ViewBuilder private var content: some View {
        switch model.visible {
        case .loading:
            ProgressView(model.isSearching ? "Searching…" : "Reading files…")
                .frame(maxWidth: .infinity)
                .padding(.vertical, 48)
                .accessibilityIdentifier("files.loading")
        case let .failed(failure):
            failedState(failure)
        case let .loaded(listing):
            if listing.entries.isEmpty {
                empty(listing)
            } else {
                entries(listing)
            }
        }
    }

    /// Why the folder could not be read. From inside a folder, the way out
    /// is back to the top.
    private func failedState(_ failure: FilesFailure) -> BrowserState {
        let root = model.rootName.isEmpty ? "the folder" : model.rootName
        var back: (title: String, run: () -> Void)?
        if !model.path.isEmpty {
            let top = model.crumbs.first?.name ?? "the top"
            back = (title: "Back to \(top)", run: { Task { await model.open(folder: "") } })
        }
        return BrowserWords.state(failure, subject: .folder, root: root, retry: { Task { await model.reload() } }, back: back, changes: showChanges)
    }

    @ViewBuilder private func empty(_ listing: FileListing) -> some View {
        if model.isSearching {
            BrowserState(
                symbol: "magnifyingglass", title: "No files named “\(model.trimmedQuery)”",
                message: "Search looks at file names, not inside files. Hidden files like .env stay hidden.",
                action: .init(title: "Clear search", identifier: "files.search.clear.action") { model.clearSearch() },
                identifier: "files.noResults"
            )
        } else {
            BrowserState(
                symbol: "folder", title: "This folder is empty",
                message: "When an agent writes a file here, it shows up. Pull down to check again.",
                action: .init(title: "Refresh", identifier: "files.empty.refresh") { Task { await model.reload() } },
                identifier: "files.empty"
            )
        }
        if let note = FileRowText.hiddenNote(listing.hidden) { footnote(note, symbol: "lock") }
    }

    @ViewBuilder private func entries(_ listing: FileListing) -> some View {
        if let groups = model.groups, !model.isSearching {
            ForEach(groups) { group in
                Text(group.bucket.title)
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(Theme.muted)
                    .padding(.horizontal, 4)
                    .padding(.top, 6)
                    .accessibilityAddTraits(.isHeader)
                card(group.entries)
            }
        } else {
            card(model.entries)
        }
        if listing.truncated {
            footnote(FileRowText.truncatedNote(searching: model.isSearching, count: listing.entries.count), symbol: "ellipsis.circle")
        }
        if let note = FileRowText.hiddenNote(listing.hidden) { footnote(note, symbol: "lock") }
    }

    private func card(_ entries: [FileEntry]) -> some View {
        LazyVStack(spacing: 0) {
            ForEach(Array(entries.enumerated()), id: \.element.id) { index, entry in
                if index > 0 { Divider().overlay(Theme.line) }
                row(entry)
            }
        }
        .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).strokeBorder(Theme.line, lineWidth: 1))
        .clipShape(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous))
    }

    private func row(_ entry: FileEntry) -> some View {
        let detail = model.isSearching
            ? FileRowText.location(entry, rootName: model.rootName)
            : FileRowText.detail(entry, now: model.now)
        let changed = changes.isChanged(entry.path)
        return Button {
            tap(entry)
        } label: {
            FileRowView(entry: entry, detail: detail, changed: changed, selected: selectedPath == entry.path)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(FileRowText.spoken(entry, detail: detail, changed: changed))
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("files.row.\(entry.path)")
    }

    private func tap(_ entry: FileEntry) {
        switch entry.kind {
        case .dir:
            Task { await model.open(folder: entry.path) }
        case .link:
            Task {
                switch await model.resolve(link: entry) {
                case let .folder(path): await model.open(folder: path)
                case .file: openFile(entry)
                }
            }
        case .file:
            openFile(entry)
        }
    }

    private func footnote(_ text: String, symbol: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 6) {
            Image(systemName: symbol).font(.footnote).accessibilityHidden(true)
            Text(text).font(.footnote)
        }
        .foregroundStyle(Theme.muted)
        .padding(.horizontal, 4)
        .padding(.top, 2)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier(symbol == "lock" ? "files.hidden" : "files.truncated")
    }
}

/// One row: the kind's symbol, the name, a line under it, and a chevron for
/// what opens a folder. A name wraps; nothing truncates. The icon column
/// grows with the text, and at the accessibility sizes the icon goes above
/// the text (`BrowserRow`), so it never overlaps a name.
struct FileRowView: View {
    let entry: FileEntry
    let detail: String
    let changed: Bool
    let selected: Bool

    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        let kind = FileKind(entry)
        BrowserRow(chevron: kind == .folder) {
            Image(systemName: kind.symbol)
                .font(.title3)
                .foregroundStyle(entry.isBlocked ? Theme.blockedText : Theme.muted)
                .accessibilityHidden(true)
        } content: {
            VStack(alignment: .leading, spacing: 1) {
                // The mark sits after the name, or under it where the name
                // takes the whole line.
                let nameAndMark = dynamicTypeSize.isAccessibilitySize
                    ? AnyLayout(VStackLayout(alignment: .leading, spacing: 4))
                    : AnyLayout(HStackLayout(spacing: 6))
                nameAndMark {
                    Text(entry.name)
                        .font(.body.weight(.medium))
                        .foregroundStyle(Theme.ink)
                        .fixedSize(horizontal: false, vertical: true)
                    if changed { ChangedMark() }
                }
                if !detail.isEmpty {
                    Text(detail)
                        .font(.caption)
                        .foregroundStyle(Theme.muted)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 8)
        .frame(minHeight: 54)
        .background(selected ? Theme.accentSoft : Color.clear)
        .contentShape(Rectangle())
    }
}

/// The "changed" chip: a file the agent changed, or a folder with one in it.
private struct ChangedMark: View {
    var body: some View {
        Text("changed")
            .font(.caption2.weight(.semibold))
            .foregroundStyle(Theme.mutedOnFill)
            .padding(.horizontal, 6)
            .padding(.vertical, 1)
            .background(RoundedRectangle(cornerRadius: 6, style: .continuous).fill(Theme.fill))
            .fixedSize()
    }
}
