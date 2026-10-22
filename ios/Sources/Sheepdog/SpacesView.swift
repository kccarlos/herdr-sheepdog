import SheepdogKit
import SwiftUI

/// ⋯ ▸ Spaces (docs/dev/design/app-redesign.md 5.7): herdr's spaces on this
/// computer, each with its state. Swipe to rename or close; a space's page
/// holds its agents, worktrees and wrap-up commands.
struct SpacesSheet: View {
    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.dismiss) private var dismiss

    enum Load: Equatable {
        case loading
        case failed(String)
        case loaded(SpacesView)
    }

    @State private var load: Load = .loading
    @State private var renaming: SpacesView.Space?
    @State private var newName = ""
    @State private var closing: SpacesView.Space?
    @State private var notice: String?

    var body: some View {
        NavigationStack {
            content
                .navigationTitle("Spaces")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Done") { dismiss() }
                            .accessibilityIdentifier("spaces.done")
                    }
                    if case let .loaded(view) = load, !view.folders.isEmpty {
                        ToolbarItem(placement: .topBarLeading) {
                            Menu {
                                Section("New space in") {
                                    ForEach(view.folders, id: \.path) { folder in
                                        Button(folder.name) { Task { await newSpace(folder) } }
                                    }
                                }
                            } label: {
                                Image(systemName: "plus")
                            }
                            .accessibilityLabel("New space")
                            .accessibilityIdentifier("spaces.new")
                        }
                    }
                }
                .navigationDestination(for: String.self) { id in
                    if case let .loaded(view) = load, let space = view.spaces.first(where: { $0.id == id }) {
                        SpaceDetailView(space: space, reload: { await fetch() }, openWindow: openWindow)
                    } else {
                        ContentUnavailableView("That space is gone", systemImage: "square.stack", description: Text("It was closed on your computer."))
                    }
                }
        }
        .task { await fetch() }
        .alert("Rename space", isPresented: Binding(get: { renaming != nil }, set: { if !$0 { renaming = nil } })) {
            TextField("Name", text: $newName)
                .accessibilityIdentifier("spaces.rename.field")
            Button("Rename") {
                if let space = renaming { Task { await rename(space, to: newName) } }
            }
            Button("Cancel", role: .cancel) {}
        }
        .confirmationDialog(
            closing.map { "Close \($0.name)?" } ?? "",
            isPresented: Binding(get: { closing != nil }, set: { if !$0 { closing = nil } }),
            titleVisibility: .visible,
            presenting: closing
        ) { space in
            Button("Close space", role: .destructive) { Task { await close(space) } }
                .accessibilityIdentifier("spaces.close.confirm")
            Button("Cancel", role: .cancel) {}
        } message: { space in
            Text(SpaceSummary.closeMessage(space))
        }
        .alert(notice ?? "", isPresented: Binding(get: { notice != nil }, set: { if !$0 { notice = nil } })) {
            Button("OK", role: .cancel) {}
        }
    }

    @ViewBuilder private var content: some View {
        switch load {
        case .loading:
            ProgressView("Loading spaces…")
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        case let .failed(message):
            ContentUnavailableView {
                Label("Couldn't load the spaces", systemImage: "wifi.slash")
            } description: {
                Text(message)
            } actions: {
                Button("Try again") { Task { await fetch() } }
            }
            .accessibilityIdentifier("spaces.failed")
        case let .loaded(view) where view.spaces.isEmpty:
            ContentUnavailableView {
                Label("No spaces yet", systemImage: "square.stack")
            } description: {
                Text("Start an agent and herdr makes one.")
            }
            .accessibilityIdentifier("spaces.empty")
        case let .loaded(view):
            List {
                ForEach(view.spaces) { space in
                    NavigationLink(value: space.id) {
                        SpaceRow(space: space)
                    }
                    .swipeActions(edge: .trailing) {
                        Button {
                            askClose(space)
                        } label: {
                            Label("Close", systemImage: "xmark")
                        }
                        .tint(Theme.danger)
                        Button {
                            newName = space.name
                            renaming = space
                        } label: {
                            Label("Rename", systemImage: "pencil")
                        }
                        .tint(Theme.idle)
                    }
                    .accessibilityAction(named: "Rename") {
                        newName = space.name
                        renaming = space
                    }
                    .accessibilityAction(named: "Close") { askClose(space) }
                    .accessibilityIdentifier("spaces.row.\(space.name)")
                }
            }
            .refreshable { await fetch() }
        }
    }

    private func openWindow(_ paneID: String) {
        dismiss()
        Task {
            try? await Task.sleep(for: .milliseconds(400))
            model.navigationPath.append(AgentWindowRoute(paneID: paneID))
        }
    }

    private func askClose(_ space: SpacesView.Space) {
        if let blocker = SpaceSummary.closeBlocker(space) {
            notice = blocker
        } else {
            closing = space
        }
    }

    func fetch() async {
        if case .loaded = load {} else { load = .loading }
        do {
            load = .loaded(try await model.daemon { try await $0.spaces() })
        } catch {
            load = .failed((error as? DaemonError)?.message ?? error.localizedDescription)
        }
    }

    private func rename(_ space: SpacesView.Space, to name: String) async {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, trimmed != space.name else { return }
        await act { try await $0.renameSpace(id: space.id, name: trimmed) }
    }

    private func close(_ space: SpacesView.Space) async {
        await act { try await $0.closeSpace(id: space.id) }
    }

    private func newSpace(_ folder: SpacesView.Folder) async {
        await act { try await $0.newSpace(dir: folder.path) }
    }

    private func act(_ body: (DaemonClient) async throws -> AgentActionReply) async {
        do {
            _ = try await model.daemon(body)
            Haptics.answered()
        } catch {
            notice = (error as? DaemonError)?.message ?? error.localizedDescription
            Haptics.answerFailed()
        }
        await fetch()
    }
}

/// A space in the list: its name, the branch, and its agents at a glance.
struct SpaceRow: View {
    let space: SpacesView.Space

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 8) {
                Text(space.name)
                    .font(.body.weight(.semibold))
                    .foregroundStyle(Theme.ink)
                if let branch = space.branch, !branch.isEmpty {
                    Text(space.worktree ? "worktree · \(branch)" : branch)
                        .font(.caption.monospaced())
                        .foregroundStyle(Theme.muted)
                        .padding(.horizontal, 6)
                        .padding(.vertical, 1)
                        .background(Capsule().fill(Theme.fill))
                        .lineLimit(1)
                }
            }
            Text(SpaceSummary.line(space))
                .font(.footnote)
                .foregroundStyle(space.agents.contains { $0.state == "blocked" } ? Theme.blockedText : Theme.muted)
        }
        .frame(minHeight: 44, alignment: .leading)
        .accessibilityElement(children: .combine)
    }
}

/// A space's page: its agents, its worktrees and its wrap-up commands,
/// then Close space (or why it cannot close yet).
struct SpaceDetailView: View {
    let space: SpacesView.Space
    let reload: () async -> Void
    let openWindow: (String) -> Void

    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.dismiss) private var dismiss
    @State private var worktrees: WorktreeList?
    @State private var worktreeError: String?
    @State private var confirmClose = false
    @State private var confirmRemove = false
    @State private var confirmForce = false
    @State private var addingCommand = false
    @State private var renaming = false
    @State private var newName = ""
    @State private var notice: String?
    @State private var running: String?

    var body: some View {
        List {
            Section {
                Label {
                    Text(placeLine)
                        .font(.footnote.monospaced())
                        .foregroundStyle(Theme.muted)
                } icon: {
                    Image(systemName: "folder").foregroundStyle(Theme.muted)
                }
                .accessibilityLabel("Folder: \(placeLine)")
            }
            Section("Agents") {
                if space.agents.isEmpty {
                    Text("No agents here. Start one with + ▸ New agent.")
                        .font(.footnote)
                        .foregroundStyle(Theme.muted)
                }
                ForEach(space.agents, id: \.paneID) { agent in
                    Button { openWindow(agent.paneID) } label: {
                        HStack(spacing: 10) {
                            Circle().fill(color(agent.state)).frame(width: 8, height: 8)
                            Text(agent.name).foregroundStyle(Theme.ink)
                            Text(agent.state == "blocked" ? "waiting on you" : agent.state)
                                .font(.footnote)
                                .foregroundStyle(agent.state == "blocked" ? Theme.blockedText : Theme.muted)
                            Spacer()
                            Image(systemName: "apple.terminal").foregroundStyle(Theme.muted)
                        }
                        .frame(minHeight: 44)
                    }
                    .accessibilityHint("Opens its window")
                    .accessibilityIdentifier("space.agent.\(agent.alias)")
                }
            }
            if space.git {
                Section("Worktrees") { worktreeRows }
            }
            Section {
                ForEach(space.commands) { command in
                    Button { Task { await run(command) } } label: {
                        HStack {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(command.name).foregroundStyle(Theme.ink)
                                Text(command.command)
                                    .font(.footnote.monospaced())
                                    .foregroundStyle(Theme.muted)
                                    .lineLimit(1)
                            }
                            Spacer()
                            if running == command.id {
                                ProgressView()
                            } else {
                                Image(systemName: "play.fill").foregroundStyle(Theme.muted)
                            }
                        }
                        .frame(minHeight: 44)
                    }
                    .accessibilityLabel("Run \(command.name): \(command.command)")
                    .accessibilityIdentifier("space.command.\(command.name)")
                }
                .onDelete { offsets in
                    let ids = offsets.map { space.commands[$0].id }
                    Task { for id in ids { await delete(id) } }
                }
                Button {
                    addingCommand = true
                } label: {
                    Label("Add a command…", systemImage: "plus")
                }
                .accessibilityIdentifier("space.command.add")
            } header: {
                Text("Wrap up")
            } footer: {
                Text("Each runs in a new tab of this space on your computer.")
            }
            Section {
                if let blocker = SpaceSummary.closeBlocker(space) {
                    Text(blocker)
                        .font(.footnote)
                        .foregroundStyle(Theme.muted)
                        .accessibilityIdentifier("space.close.blocked")
                } else {
                    Button("Close space", role: .destructive) { confirmClose = true }
                        .accessibilityIdentifier("space.close")
                }
            }
        }
        .navigationTitle(space.name)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button("Rename") {
                    newName = space.name
                    renaming = true
                }
                .accessibilityIdentifier("space.rename")
            }
        }
        .task { await loadWorktrees() }
        .sheet(isPresented: $addingCommand) {
            AddCommandSheet { name, command in
                await act { try await $0.addCommand(space: space.id, name: name, command: command) }
            }
        }
        .alert("Rename space", isPresented: $renaming) {
            TextField("Name", text: $newName)
            Button("Rename") {
                let name = newName.trimmingCharacters(in: .whitespacesAndNewlines)
                guard !name.isEmpty else { return }
                Task { await act { _ = try await $0.renameSpace(id: space.id, name: name) } }
            }
            Button("Cancel", role: .cancel) {}
        }
        .confirmationDialog("Close \(space.name)?", isPresented: $confirmClose, titleVisibility: .visible) {
            Button("Close space", role: .destructive) {
                Task {
                    await act { _ = try await $0.closeSpace(id: space.id) }
                    dismiss()
                }
            }
            .accessibilityIdentifier("space.close.confirm")
            Button("Cancel", role: .cancel) {}
        } message: {
            Text(SpaceSummary.closeMessage(space))
        }
        .confirmationDialog("Remove this worktree?", isPresented: $confirmRemove, titleVisibility: .visible) {
            Button("Remove worktree", role: .destructive) { Task { await remove(force: false) } }
                .accessibilityIdentifier("space.worktree.remove.confirm")
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("It deletes the folder \(space.path.isEmpty ? "" : (space.path as NSString).lastPathComponent + " ")and closes this space. The branch stays in the repository.")
        }
        .confirmationDialog("It has uncommitted changes", isPresented: $confirmForce, titleVisibility: .visible) {
            Button("Remove anyway", role: .destructive) { Task { await remove(force: true) } }
                .accessibilityIdentifier("space.worktree.remove.force")
            Button("Keep it", role: .cancel) {}
        } message: {
            Text("Removing the worktree throws those changes away.")
        }
        .alert(notice ?? "", isPresented: Binding(get: { notice != nil }, set: { if !$0 { notice = nil } })) {
            Button("OK", role: .cancel) {}
        }
    }

    private var placeLine: String {
        let folder = space.path.isEmpty ? space.name : (space.path as NSString).abbreviatingWithTildeInPath
        guard let branch = space.branch, !branch.isEmpty else { return folder }
        return "\(folder) · \(branch)"
    }

    @ViewBuilder private var worktreeRows: some View {
        if let worktreeError {
            Text(worktreeError).font(.footnote).foregroundStyle(Theme.muted)
        } else if let list = worktrees {
            ForEach(list.worktrees) { tree in
                HStack {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(tree.title).foregroundStyle(Theme.ink)
                        Text(tree.branch ?? (tree.isDetached == true ? "detached" : ""))
                            .font(.footnote.monospaced())
                            .foregroundStyle(Theme.muted)
                    }
                    Spacer()
                    if tree.path == space.path {
                        Text("This space").font(.caption).foregroundStyle(Theme.muted)
                    }
                    let canOpen = tree.openWorkspaceID == nil
                    let canRemove = tree.path == space.path && tree.isLinkedWorktree
                    if canOpen || canRemove {
                    Menu {
                        if canOpen {
                            Button {
                                Task { await act { _ = try await $0.openWorktree(space: space.id, path: tree.path) } }
                            } label: {
                                Label("Open as a space", systemImage: "square.stack")
                            }
                        }
                        if canRemove {
                            Button(role: .destructive) { confirmRemove = true } label: {
                                Label("Remove worktree", systemImage: "trash")
                            }
                            .accessibilityIdentifier("space.worktree.remove")
                        }
                    } label: {
                        Image(systemName: "ellipsis")
                            .frame(width: 44, height: 44)
                            .contentShape(Rectangle())
                    }
                    .tint(Theme.ink)
                    .accessibilityLabel("Worktree actions for \(tree.title)")
                    .accessibilityIdentifier("space.worktree.\(tree.title)")
                    }
                }
                .frame(minHeight: 44)
            }
        } else {
            ProgressView()
        }
    }

    private func color(_ state: String) -> Color {
        switch state {
        case "blocked": return Theme.blocked
        case "working": return Theme.working
        case "done": return Theme.done
        default: return Theme.idle
        }
    }

    private func loadWorktrees() async {
        guard space.git else { return }
        do {
            worktrees = try await model.daemon { try await $0.worktrees(space: space.id) }
        } catch {
            worktreeError = (error as? DaemonError)?.message ?? error.localizedDescription
        }
    }

    private func run(_ command: SpacesView.WrapUp) async {
        running = command.id
        do {
            _ = try await model.daemon { try await $0.runCommand(space: space.id, command: command.id) }
            notice = "\(command.name) is running in a new tab of \(space.name) on your computer."
            Haptics.answered()
        } catch {
            notice = (error as? DaemonError)?.message ?? error.localizedDescription
            Haptics.answerFailed()
        }
        running = nil
    }

    private func delete(_ id: String) async {
        await act { try await $0.deleteCommand(space: space.id, command: id) }
    }

    private func remove(force: Bool) async {
        do {
            _ = try await model.daemon { try await $0.removeWorktree(space: space.id, force: force) }
            Haptics.answered()
            await reload()
            dismiss()
        } catch let DaemonError.rejected(status, message) where status == 409 && !force && message.contains("uncommitted") {
            confirmForce = true
        } catch {
            notice = (error as? DaemonError)?.message ?? error.localizedDescription
        }
    }

    private func act(_ body: (DaemonClient) async throws -> Void) async {
        do {
            try await model.daemon(body)
            Haptics.answered()
        } catch {
            notice = (error as? DaemonError)?.message ?? error.localizedDescription
            Haptics.answerFailed()
        }
        await reload()
        await loadWorktrees()
    }
}

/// Save a wrap-up command: a name and one line.
private struct AddCommandSheet: View {
    let save: (String, String) async -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var command = ""

    private var valid: Bool {
        let n = name.trimmingCharacters(in: .whitespaces)
        let c = command.trimmingCharacters(in: .whitespaces)
        return !n.isEmpty && n.count <= 40 && !c.isEmpty && !c.contains("\n")
    }

    var body: some View {
        NavigationStack {
            Form {
                TextField("Name, like Run tests", text: $name)
                    .accessibilityIdentifier("space.command.name")
                TextField("Command, like npm test", text: $command)
                    .font(.body.monospaced())
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .accessibilityIdentifier("space.command.line")
            }
            .navigationTitle("Wrap-up command")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") {
                        Task {
                            await save(name.trimmingCharacters(in: .whitespaces), command.trimmingCharacters(in: .whitespaces))
                            dismiss()
                        }
                    }
                    .disabled(!valid)
                    .accessibilityIdentifier("space.command.save")
                }
            }
        }
        .presentationDetents([.medium])
    }
}
