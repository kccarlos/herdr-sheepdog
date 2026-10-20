import SheepdogKit
import SwiftUI

/// New agent (docs/dev/design/phone-terminal.md section 5): which CLI, in
/// which project (only `supervisor.project_roots` and open workspaces),
/// in the project folder or a fresh worktree, and an optional first
/// prompt. The daemon starts it like the orchestrator does (`POST
/// /api/agents`).
struct NewAgentSheet: View {
    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.dismiss) private var dismiss

    private enum Load: Equatable {
        case loading
        case failed(String)
        case loaded(NewAgentOptions)
    }

    @State private var load: Load = .loading
    @State private var kind = "claude"
    @State private var projectPath = ""
    @State private var inWorktree = false
    @State private var branch = ""
    @State private var prompt = ""
    @State private var starting = false
    @State private var error: String?
    @State private var started: String?

    private var options: NewAgentOptions? {
        if case let .loaded(o) = load { return o }
        return nil
    }

    private var project: NewAgentOptions.Project? {
        options?.projects.first { $0.path == projectPath }
    }

    private var worktreePossible: Bool { (options?.worktree ?? false) && (project?.git ?? false) }

    private var canStart: Bool {
        guard project != nil, !starting, started == nil else { return false }
        return !inWorktree || AgentAction.validBranch(branch)
    }

    var body: some View {
        NavigationStack {
            content
                .navigationTitle("New agent")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Cancel") { dismiss() }
                    }
                    ToolbarItem(placement: .confirmationAction) {
                        if starting {
                            ProgressView()
                        } else {
                            Button("Start", action: start)
                                .disabled(!canStart)
                                .accessibilityIdentifier("newAgent.start")
                        }
                    }
                }
        }
        .task { await fetch() }
    }

    @ViewBuilder private var content: some View {
        switch load {
        case .loading:
            ProgressView("Loading projects…")
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        case let .failed(message):
            ContentUnavailableView {
                Label("Couldn't load the projects", systemImage: "exclamationmark.triangle")
            } description: {
                Text(message)
            } actions: {
                Button("Try again") { Task { await fetch() } }
            }
        case let .loaded(options) where options.projects.isEmpty:
            ContentUnavailableView {
                Label("No projects yet", systemImage: "folder")
            } description: {
                Text("Open a workspace in herdr, or list your code folders in supervisor.project_roots in sheepdog's config on your computer.")
            }
            .accessibilityIdentifier("newAgent.empty")
        case let .loaded(options):
            form(options)
        }
    }

    private func form(_ options: NewAgentOptions) -> some View {
        Form {
            Section {
                Picker("Agent", selection: $kind) {
                    ForEach(options.kinds, id: \.self) { Text(NewAgentOptions.kindName($0)).tag($0) }
                }
                .pickerStyle(.segmented)
                .accessibilityIdentifier("newAgent.kind")
                Picker("Project", selection: $projectPath) {
                    ForEach(options.projects) { p in
                        Text(p.name).tag(p.path)
                    }
                }
                .accessibilityIdentifier("newAgent.project")
            }
            if worktreePossible {
                Section {
                    Picker("Where", selection: $inWorktree) {
                        Text("Project folder").tag(false)
                        Text("New worktree").tag(true)
                    }
                    .pickerStyle(.segmented)
                    .accessibilityIdentifier("newAgent.where")
                    if inWorktree {
                        TextField("Branch, like fix-login", text: $branch)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .font(.body.monospaced())
                            .accessibilityIdentifier("newAgent.branch")
                    }
                } footer: {
                    if inWorktree, !branch.isEmpty, !AgentAction.validBranch(branch) {
                        Text("Use letters, digits and . _ / - only.").foregroundStyle(Color(.systemRed))
                    } else if inWorktree {
                        Text("herdr makes the branch's worktree beside \(project?.name ?? "the project") and opens it.")
                    }
                }
            }
            Section {
                TextField("Optional. Sent once the agent is ready.", text: $prompt, axis: .vertical)
                    .lineLimit(3 ... 8)
                    .accessibilityIdentifier("newAgent.prompt")
            } header: {
                Text("First prompt")
            }
            if let error {
                Section {
                    Label(error, systemImage: "exclamationmark.triangle")
                        .foregroundStyle(Theme.blockedText)
                        .accessibilityIdentifier("newAgent.error")
                }
            }
            if let started {
                Section {
                    Label(started, systemImage: "checkmark.circle")
                        .accessibilityIdentifier("newAgent.started")
                }
            }
        }
        .onChange(of: projectPath) { _, _ in
            if !worktreePossible { inWorktree = false }
        }
    }

    private func fetch() async {
        load = .loading
        do {
            let options = try await model.daemon { try await $0.newAgentOptions() }
            if !options.kinds.contains(kind), let first = options.kinds.first { kind = first }
            if projectPath.isEmpty { projectPath = options.projects.first?.path ?? "" }
            load = .loaded(options)
        } catch {
            load = .failed((error as? DaemonError)?.message ?? error.localizedDescription)
        }
    }

    private func start() {
        guard let project else { return }
        starting = true
        error = nil
        let text = prompt.trimmingCharacters(in: .whitespacesAndNewlines)
        Task {
            do {
                let reply = try await model.daemon {
                    try await $0.newAgent(kind: kind, dir: project.path, worktree: inWorktree ? branch : nil, text: text)
                }
                starting = false
                started = reply.spoken ?? "Starting."
                Haptics.answered()
                try? await Task.sleep(nanoseconds: 1_400_000_000)
                dismiss()
            } catch {
                starting = false
                self.error = (error as? DaemonError)?.message ?? error.localizedDescription
                Haptics.answerFailed()
            }
        }
    }
}
