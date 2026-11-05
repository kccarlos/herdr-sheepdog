import SheepdogKit
import SwiftUI

/// Files or Changes: the two tabs of the read-only browser sheet.
enum BrowserTab: String, Identifiable, CaseIterable, Hashable {
    case files, changes
    var id: String { rawValue }
}

/// What is open beside (or on top of) the list.
enum BrowserSelection: Hashable {
    case file(String)
    case change(ChangedFile)
}

/// Where the browser looks: an agent's folder, or a space's.
struct BrowserTarget {
    /// The bar's title: the agent's name, or the space's.
    var title: String
    /// Under it: "Work Mac · shop".
    var subtitle: String?
    var scope: FileScope
    /// The agent whose changes the Changes tab shows (a space has none of
    /// its own, so its first agent's); nil hides the tab.
    var changesPane: String?
    /// The folder's own path on the computer, to line the changes (which
    /// start at the repository's root) up with the folder's files.
    var folder: String?
    /// The daemon has the file browser. Without it the sheet is Changes only.
    var filesAvailable: Bool
}

/// The reads the browser makes, as closures: the session hands them the
/// current computer's client, the tests and previews hand it anything.
struct BrowserServices {
    var list: FileBrowserModel.List
    var read: FileViewerModel.Read
    var image: FileViewerModel.Image
    var changes: (@MainActor () async throws -> AgentChanges?)?
    var diff: @MainActor (String) async throws -> FileDiff?
    var describe: (Error) -> RequestFailure
}

/// The read-only sheet: the folder's files and what changed in it, behind
/// a Files | Changes switch. On the phone a tapped file or diff opens on top
/// of the list; on an iPad it opens beside it. Nothing here writes a file
/// or keeps one on the phone.
struct BrowserSheet: View {
    let target: BrowserTarget
    let services: BrowserServices

    @State private var tab: BrowserTab
    @State private var selection: BrowserSelection?
    @State private var files: FileBrowserModel
    @State private var changes: ChangesPhase = .loading

    @Environment(\.dismiss) private var dismiss
    @Environment(\.horizontalSizeClass) private var sizeClass

    init(target: BrowserTarget, services: BrowserServices, start: BrowserTab) {
        self.target = target
        self.services = services
        let hasChanges = services.changes != nil
        // A sheet asked for what it cannot show opens on what it can.
        _tab = State(initialValue: !target.filesAvailable ? .changes : (!hasChanges ? .files : start))
        _files = State(initialValue: FileBrowserModel(list: services.list, describe: services.describe))
    }

    private var hasChanges: Bool { services.changes != nil }
    private var showsSwitch: Bool { target.filesAvailable && hasChanges }
    private var isCompact: Bool { sizeClass != .regular }

    private var changeIndex: ChangeIndex {
        if case let .loaded(loaded) = changes { return ChangeIndex(loaded, folder: target.folder) }
        return ChangeIndex(nil, folder: nil)
    }

    private var changeCount: Int? {
        if case let .loaded(loaded) = changes, loaded.repo, !loaded.files.isEmpty { return loaded.files.count }
        return nil
    }

    /// Every changed file, for the diff's jump to another.
    private var changedFiles: [ChangedFile] {
        if case let .loaded(loaded) = changes, loaded.repo { return loaded.files }
        return []
    }

    var body: some View {
        Group {
            if isCompact {
                NavigationStack {
                    sidebar.navigationDestination(item: $selection) { detail($0) }
                }
            } else {
                HStack(spacing: 0) {
                    NavigationStack { sidebar }.frame(width: 390)
                    Divider()
                    NavigationStack { regularDetail }
                }
            }
        }
        .tint(Theme.primaryText)
        .task { await files.start() }
        .task { await loadChanges() }
    }

    // MARK: The list column

    private var sidebar: some View {
        VStack(spacing: 0) {
            if showsSwitch { switcher }
            switch tab {
            case .files:
                FilesListView(
                    model: files, changes: changeIndex, selectedPath: selectedFile,
                    openFile: { selection = .file($0.path) },
                    showChanges: hasChanges ? { tab = .changes } : nil
                )
            case .changes:
                ChangesListView(
                    phase: changes, name: target.title, selectedPath: selectedChange,
                    open: { selection = .change($0) }, reload: { await loadChanges() },
                    showFiles: target.filesAvailable ? { tab = .files } : nil
                )
            }
        }
        .background(Theme.surface)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("Done") { dismiss() }
                    .accessibilityIdentifier("browser.done")
            }
            ToolbarItem(placement: .principal) { header }
            ToolbarItem(placement: .primaryAction) {
                Button {
                    Task {
                        await files.reload()
                        await loadChanges()
                    }
                } label: {
                    Image(systemName: "arrow.clockwise")
                }
                .accessibilityLabel("Refresh")
                .accessibilityIdentifier("browser.refresh")
            }
        }
    }

    private var header: some View {
        VStack(spacing: 0) {
            Text(target.title).font(.headline).lineLimit(1)
            if let subtitle = target.subtitle {
                Text(subtitle).font(.caption).foregroundStyle(Theme.muted).lineLimit(1)
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([target.title, target.subtitle].compactMap { $0 }.joined(separator: ", "))
        .accessibilityAddTraits(.isHeader)
    }

    private var switcher: some View {
        SegmentedChoice(
            options: BrowserTab.allCases, selected: tab,
            title: { $0 == .files ? "Files" : "Changes" },
            detail: { $0 == .changes ? changeCount.map(String.init) : nil },
            spoken: { option in
                guard option == .changes, let count = changeCount else { return nil }
                return "Changes, \(count) \(count == 1 ? "file" : "files")"
            },
            identifier: "browser.tab", accessibilityName: "Files or Changes", fills: true
        ) { files.cancelPendingLink(); tab = $0; selection = nil }
        .padding(.horizontal, 16)
        .padding(.top, 2)
        .padding(.bottom, 8)
    }

    private var selectedFile: String? {
        if case let .file(path) = selection { return path }
        return nil
    }

    private var selectedChange: String? {
        if case let .change(file) = selection { return file.path }
        return nil
    }

    // MARK: The viewer

    @ViewBuilder private func detail(_ selection: BrowserSelection) -> some View {
        switch selection {
        case let .file(path):
            FileViewer(
                path: path, rootName: files.rootName.isEmpty ? target.title : files.rootName, services: services,
                change: changeIndex.change(at: path), openDiff: showDiff, openPath: { self.selection = .file($0) },
                onBack: isCompact ? { self.selection = nil } : nil,
                showChanges: hasChanges ? { tab = .changes; self.selection = nil } : nil
            )
            .id(path)
        case let .change(file):
            DiffView(
                file: file, load: { try await services.diff(file.path) },
                files: changedFiles, jump: { self.selection = .change($0) }
            )
            .id(file.path)
        }
    }

    @ViewBuilder private var regularDetail: some View {
        if let selection {
            detail(selection)
        } else {
            BrowserState(
                symbol: tab == .files ? "doc.text" : "plusminus",
                title: tab == .files ? "Pick a file" : "Pick a file to see its changes",
                message: tab == .files ? "It opens here." : "Its diff opens here.",
                identifier: "browser.placeholder"
            )
            .frame(maxHeight: .infinity)
            .background(Theme.surface)
        .onChange(of: tab) { _, _ in files.cancelPendingLink(); selection = nil }
        }
    }

    private func showDiff(_ change: ChangedFile) {
        tab = .changes
        selection = .change(change)
    }

    // MARK: Changes

    /// Reads the changes (the tab's list, its count, the marks on the
    /// files). A refresh keeps what is shown until the answer comes.
    private func loadChanges() async {
        guard let read = services.changes else { return }
        if case .loaded = changes {} else { changes = .loading }
        do {
            guard let loaded = try await read() else {
                changes = .olderDaemon
                return
            }
            changes = .loaded(loaded)
        } catch {
            if error is CancellationError { return }
            changes = .failed(error, describe: services.describe)
        }
    }
}
