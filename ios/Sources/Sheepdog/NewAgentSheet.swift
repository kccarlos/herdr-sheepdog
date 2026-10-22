import SheepdogKit
import SwiftUI

/// New agent, the herdr way (docs/dev/design/app-redesign.md 5.6), on
/// `GET /api/spaces`:
///
/// 1. **Where:** the open spaces with their agents, then New space (the
///    project folders with no space yet).
/// 2. **Which agent:** the kinds installed on the computer; New worktree
///    (off; hidden when the folder is not a git repository; a hint when
///    agents already work in that space); an optional first prompt.
/// 3. **Start:** "Starting Claude in webapp…" until the agent is ready
///    (`POST /api/agents` answers then), and its window opens. A failure
///    stays on the sheet with the reason, the fix and Open the pane.
struct NewAgentSheet: View {
    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.dismiss) private var dismiss

    private enum Load: Equatable {
        case loading
        case failed(String)
        case loaded(SpacesView)
    }

    @State private var load: Load = .loading
    @State private var path: [NewAgentOptions.Project] = []

    var body: some View {
        NavigationStack(path: $path) {
            content
                .navigationTitle("New agent")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Cancel") { dismiss() }
                    }
                }
                .navigationDestination(for: NewAgentOptions.Project.self) { project in
                    if case let .loaded(view) = load {
                        NewAgentKindStep(project: project, spaces: view) { paneID in
                            dismiss()
                            if let paneID {
                                Task {
                                    try? await Task.sleep(for: .milliseconds(400))
                                    model.navigationPath.append(AgentWindowRoute(paneID: paneID))
                                }
                            }
                        }
                    }
                }
        }
        .task { await fetch() }
    }

    @ViewBuilder private var content: some View {
        switch load {
        case .loading:
            ProgressView("Loading spaces…")
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        case let .failed(message):
            ContentUnavailableView {
                Label("Couldn't reach your computer", systemImage: "wifi.slash")
            } description: {
                Text(message)
            } actions: {
                Button("Try again") { Task { await fetch() } }
                    .accessibilityIdentifier("newAgent.retry")
            }
            .accessibilityIdentifier("newAgent.offline")
        case let .loaded(view) where view.kinds.isEmpty:
            ContentUnavailableView {
                Label("No agent found", systemImage: "questionmark.app.dashed")
            } description: {
                Text("No coding agent is installed on your computer. Install Claude Code or Codex there, then try again.")
            } actions: {
                Button("Try again") { Task { await fetch() } }
            }
            .accessibilityIdentifier("newAgent.noKinds")
        case let .loaded(view) where view.spaces.isEmpty && view.folders.isEmpty:
            ContentUnavailableView {
                Label("No projects yet", systemImage: "folder")
            } description: {
                Text("Open a workspace in herdr, or list your code folders in supervisor.project_roots in sheepdog's config on your computer.")
            }
            .accessibilityIdentifier("newAgent.empty")
        case let .loaded(view):
            whereList(view)
        }
    }

    private func whereList(_ view: SpacesView) -> some View {
        let options = NewAgentOptions(spaces: view)
        let spaces = options.projects.filter(\.open)
        let folders = options.projects.filter { !$0.open }
        return List {
            Section {
                Text("Step 1 of 2 · Where")
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(Theme.muted)
                    .listRowBackground(Color.clear)
                    .listRowInsets(EdgeInsets(top: 0, leading: 4, bottom: 0, trailing: 0))
            }
            if !spaces.isEmpty {
                Section("Spaces") {
                    ForEach(spaces) { project in
                        NavigationLink(value: project) {
                            placeRow(project, detail: spaceDetail(view, project))
                        }
                        .accessibilityIdentifier("newAgent.space.\(project.name)")
                    }
                }
            }
            if !folders.isEmpty {
                Section {
                    ForEach(folders) { project in
                        NavigationLink(value: project) {
                            placeRow(project, detail: (project.path as NSString).abbreviatingWithTildeInPath)
                        }
                        .accessibilityIdentifier("newAgent.folder.\(project.name)")
                    }
                } header: {
                    Text("New space")
                } footer: {
                    Text("Folders under your project roots that have no space yet. Picking one makes a space there.")
                }
            }
        }
        .accessibilityIdentifier("newAgent.where")
    }

    private func placeRow(_ project: NewAgentOptions.Project, detail: String) -> some View {
        HStack(spacing: 12) {
            Image(systemName: project.open ? "square.stack" : "folder")
                .foregroundStyle(Theme.muted)
                .frame(width: 24)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(project.name)
                    .font(.body.weight(.semibold))
                    .foregroundStyle(Theme.ink)
                if !detail.isEmpty {
                    Text(detail)
                        .font(.footnote)
                        .foregroundStyle(Theme.muted)
                        .lineLimit(2)
                }
            }
        }
        .frame(minHeight: 44)
        .accessibilityElement(children: .combine)
    }

    private func spaceDetail(_ view: SpacesView, _ project: NewAgentOptions.Project) -> String {
        guard let space = view.spaces.first(where: { $0.id == project.space }) else { return "" }
        var parts: [String] = []
        if let branch = space.branch, !branch.isEmpty { parts.append(branch) }
        let names = space.agents.map(\.name)
        parts.append(names.isEmpty ? "No agents" : names.joined(separator: ", "))
        return parts.joined(separator: " · ")
    }

    private func fetch() async {
        load = .loading
        do {
            let view = try await model.daemon { try await $0.spaces() }
            load = .loaded(view)
        } catch {
            load = .failed((error as? DaemonError)?.message ?? error.localizedDescription)
        }
    }
}

/// Step 2: which agent, the worktree, the first prompt, and Start; then
/// the starting and failure states.
private struct NewAgentKindStep: View {
    let project: NewAgentOptions.Project
    let spaces: SpacesView
    /// Started: the new pane to open (nil in the demo or without one).
    let started: (String?) -> Void

    @EnvironmentObject private var model: SessionViewModel
    @AppStorage("newAgent.lastKind") private var lastKind = "claude"
    @State private var kind = ""
    @State private var inWorktree = false
    @State private var branch = ""
    @State private var prompt = ""
    @State private var phase: Phase = .editing

    private enum Phase: Equatable {
        case editing
        case starting
        case started(String)
        case failed(AgentStartError?, String)
    }

    private var options: NewAgentOptions { NewAgentOptions(spaces: spaces) }
    private var kindName: String { options.kindName(kind) }
    private var space: SpacesView.Space? { spaces.spaces.first { $0.id == project.space } }
    private var worktreePossible: Bool { spaces.worktrees && project.git }

    private var canStart: Bool {
        guard !kind.isEmpty, phase == .editing || isFailure else { return false }
        return !inWorktree || AgentAction.validBranch(branch)
    }

    private var isFailure: Bool {
        if case .failed = phase { return true }
        return false
    }

    var body: some View {
        Form {
            Section {
                Text("Step 2 of 2 · Which agent")
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(Theme.muted)
                    .listRowBackground(Color.clear)
                    .listRowInsets(EdgeInsets(top: 0, leading: 4, bottom: 0, trailing: 0))
            }
            Section {
                LazyVGrid(columns: [GridItem(.adaptive(minimum: 140), spacing: 8)], spacing: 8) {
                    ForEach(spaces.kinds, id: \.kind) { k in
                        kindTile(k)
                    }
                }
                .padding(.vertical, 4)
            } footer: {
                Text("Installed on this computer.")
            }
            if worktreePossible {
                Section {
                    Toggle(isOn: $inWorktree) {
                        VStack(alignment: .leading, spacing: 2) {
                            Text("New worktree")
                            Text("Its own branch and folder")
                                .font(.footnote)
                                .foregroundStyle(Theme.muted)
                        }
                    }
                    .accessibilityIdentifier("newAgent.worktree")
                    if inWorktree {
                        TextField("Branch, like fix-login", text: $branch)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .font(.body.monospaced())
                            .accessibilityIdentifier("newAgent.branch")
                    }
                } footer: {
                    if inWorktree, !branch.isEmpty, !AgentAction.validBranch(branch) {
                        Text("Use letters, digits and . _ / - only.").foregroundStyle(Theme.danger)
                    } else if case let .failed(error, _) = phase, error?.reason == .branchExists {
                        Text("That branch is checked out elsewhere. Pick another name.").foregroundStyle(Theme.danger)
                    } else if let hint = worktreeHint {
                        Text(hint)
                    }
                }
            } else if project.git == false {
                Section {
                    Label("\(project.name) isn't a git repository, so the agent works in the folder itself.", systemImage: "info.circle")
                        .font(.footnote)
                        .foregroundStyle(Theme.muted)
                        .accessibilityIdentifier("newAgent.notGit")
                }
            }
            Section {
                TextField("Optional. Sent once the agent is ready.", text: $prompt, axis: .vertical)
                    .lineLimit(3 ... 8)
                    .accessibilityIdentifier("newAgent.prompt")
            } header: {
                Text("First prompt")
            }
            phaseSection
        }
        .navigationTitle(project.name)
        .navigationBarTitleDisplayMode(.inline)
        .safeAreaInset(edge: .bottom) { startButton }
        .onAppear {
            if kind.isEmpty {
                kind = spaces.kinds.contains { $0.kind == lastKind } ? lastKind : spaces.kinds.first?.kind ?? ""
            }
        }
        .onChange(of: prompt) { _, text in
            if inWorktree, branch.isEmpty { branch = AgentAction.suggestedBranch(from: text) }
        }
        .onChange(of: inWorktree) { _, on in
            if on, branch.isEmpty { branch = AgentAction.suggestedBranch(from: prompt) }
        }
    }

    /// "auth-fix and payments already work in webapp. A worktree keeps
    /// their changes apart."
    private var worktreeHint: String? {
        guard let names = space?.agents.map(\.name), !names.isEmpty else { return nil }
        let who = names.count == 1 ? "\(names[0]) already works" : "\(names.dropLast().joined(separator: ", ")) and \(names.last!) already work"
        return "\(who) in \(project.name). A worktree keeps their changes apart."
    }

    private func kindTile(_ k: SpacesView.Kind) -> some View {
        let selected = k.kind == kind
        return Button {
            kind = k.kind
        } label: {
            HStack(spacing: 10) {
                Text(String(k.name.prefix(1)))
                    .font(.headline.weight(.bold))
                    .fontDesign(.rounded)
                    .foregroundStyle(selected ? Theme.onAccent : Theme.ink)
                    .frame(width: 32, height: 32)
                    .background(Circle().fill(selected ? Color.accentColor : Theme.fill))
                Text(k.name)
                    .font(.body.weight(selected ? .semibold : .regular))
                    .foregroundStyle(Theme.ink)
                    .lineLimit(2)
                Spacer(minLength: 0)
                if selected {
                    Image(systemName: "checkmark")
                        .font(.footnote.weight(.bold))
                        .foregroundStyle(Theme.ink)
                }
            }
            .padding(10)
            .frame(maxWidth: .infinity, minHeight: 52)
            .background(RoundedRectangle(cornerRadius: 12, style: .continuous).fill(Theme.card))
            .overlay(RoundedRectangle(cornerRadius: 12, style: .continuous).strokeBorder(selected ? Theme.ink : Theme.line, lineWidth: selected ? 1.5 : 1))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityIdentifier("newAgent.kind.\(k.kind)")
    }

    @ViewBuilder
    private var phaseSection: some View {
        switch phase {
        case .editing:
            EmptyView()
        case .starting:
            Section {
                HStack(spacing: 12) {
                    ProgressView()
                    Text("Starting \(kindName) in \(project.name)…")
                        .foregroundStyle(Theme.ink)
                }
                .frame(minHeight: 44)
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("newAgent.starting")
            } footer: {
                Text("Its window opens when it's ready.")
            }
        case let .started(line):
            Section {
                Label(line, systemImage: "checkmark.circle")
                    .foregroundStyle(Theme.done)
                    .accessibilityIdentifier("newAgent.started")
            }
        case let .failed(error, message):
            Section {
                VStack(alignment: .leading, spacing: 6) {
                    Label(error?.title(kind: kindName, place: project.name) ?? "\(kindName) didn't start", systemImage: "exclamationmark.triangle.fill")
                        .font(.body.weight(.semibold))
                        .foregroundStyle(Theme.blockedText)
                    Text(error?.fix ?? message)
                        .font(.footnote)
                        .foregroundStyle(Theme.muted)
                    if let error, !error.message.isEmpty {
                        Text(error.message)
                            .font(.footnote.monospaced())
                            .foregroundStyle(Theme.muted)
                    }
                }
                .padding(.vertical, 4)
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("newAgent.error")
                if let pane = error?.paneID {
                    Button("Open the pane") { started(pane) }
                        .accessibilityIdentifier("newAgent.openPane")
                }
            }
        }
    }

    private var startButton: some View {
        Button(action: start) {
            HStack(spacing: 8) {
                if phase == .starting { ProgressView().tint(Theme.onAccent) }
                Text(phase == .starting ? "Starting…" : (isFailure ? "Try again" : "Start \(kindName)"))
                    .font(.body.weight(.semibold))
            }
            .foregroundStyle(Theme.onAccent)
            .frame(maxWidth: .infinity, minHeight: 50)
            .background(Capsule().fill(Color.accentColor.opacity(canStart || phase == .starting ? 1 : 0.4)))
        }
        .buttonStyle(.plain)
        .disabled(!canStart)
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
        .background(.bar)
        .accessibilityIdentifier("newAgent.start")
    }

    private func start() {
        guard canStart else { return }
        // Keyboard down, so the starting line and any failure show.
        UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
        phase = .starting
        lastKind = kind
        let text = prompt.trimmingCharacters(in: .whitespacesAndNewlines)
        let worktree = inWorktree ? branch : nil
        Task {
            do {
                let reply = try await model.daemon {
                    try await $0.newAgent(kind: kind, project: project, worktree: worktree, text: text)
                }
                phase = .started(reply.spoken ?? "Started.")
                Haptics.answered()
                try? await Task.sleep(for: .milliseconds(reply.paneID?.isEmpty == false ? 300 : 1400))
                started(reply.paneID?.isEmpty == false ? reply.paneID : nil)
            } catch let error as AgentStartError {
                phase = .failed(error, error.message)
                Haptics.answerFailed()
            } catch {
                phase = .failed(nil, (error as? DaemonError)?.message ?? error.localizedDescription)
                Haptics.answerFailed()
            }
        }
    }
}
