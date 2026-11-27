import SheepdogKit
import SwiftUI

struct FactoryNewProjectSheet: View {
    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.dismiss) private var dismiss

    @State private var spaces: Result<SpacesView, Error>?
    @State private var selectedFolder: String?
    @State private var goal = ""
    @State private var checks: [FactoryStartCheck]?
    @State private var busy: String?
    @State private var actionError: String?
    @State private var unavailable = false

    private static let intro =
        "A project is one folder with a git repository and one goal. The coordinator plans the tasks, "
        + "workers build them, reviewers check them, and you are asked only for decisions."

    private var computer: ComputerModel? { model.fleet.current }
    private var host: String { computer?.name ?? model.currentComputerName }
    private var form: FactoryNewProjectForm {
        let empty = SpacesView(spaces: [], folders: [], kinds: [], worktrees: false)
        return FactoryNewProjectForm(spaces: (try? spaces?.get()) ?? empty, heads: computer?.factoryHeads ?? [],
                                     selectedFolder: selectedFolder, goal: goal, checks: checks, computerName: host)
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    if form.showsFirstProjectIntro {
                        card(spacing: 4) {
                            Text("No project yet").font(.title3.weight(.bold)).fontDesign(.rounded)
                                .accessibilityAddTraits(.isHeader)
                            Text(Self.intro).font(.footnote).foregroundStyle(Theme.muted)
                        }
                    }
                    if unavailable { unavailableCard } else {
                        folderSection
                        Text("Goal").font(.footnote.weight(.semibold)).foregroundStyle(Theme.muted)
                            .accessibilityHidden(true)
                        TextField("", text: $goal, axis: .vertical).font(.body).lineLimit(2...5).padding(10)
                            .background(Theme.card, in: .rect(cornerRadius: 12))
                            .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(Theme.line))
                            .accessibilityLabel("Goal").accessibilityIdentifier("factoryNewProject.goal")
                        checks == nil
                            ? Text("What you want built, in your own words. The coordinator turns it into tasks.")
                                .font(.caption).foregroundStyle(Theme.muted) : nil
                        if let checks {
                            card(padding: 12, border: form.hasFailedChecks ? Theme.danger.opacity(0.45) : Theme.line) {
                                Text("Start checks · on \(host)").font(.footnote.weight(.semibold))
                                    .foregroundStyle(Theme.muted).accessibilityAddTraits(.isHeader)
                                ForEach(checks.indices, id: \.self) {
                                    FactoryStartCheckRow(check: checks[$0], computerName: host)
                                }
                                form.checksSummary.map { Text($0).font(.footnote.weight(.semibold))
                                    .foregroundStyle(form.hasFailedChecks ? Theme.danger : Theme.done) }
                            }
                        }
                        actionError.map { Text($0).font(.footnote.weight(.semibold)).foregroundStyle(Theme.danger) }
                        Button(busy == "check" ? "Checking the folder…" : form.checkButtonLabel) { run(start: false) }
                            .buttonStyle(PillButtonStyle(prominent: false)).disabled(!form.canCheck || busy != nil)
                        Button(busy == "start" ? "Starting…" : "Start the project and its agents") { run(start: true) }
                            .buttonStyle(PillButtonStyle(prominent: true)).disabled(!form.canStart || busy != nil)
                            .keyboardShortcut(.return, modifiers: .command)
                        Text(form.startHint).font(.caption).foregroundStyle(Theme.muted)
                    }
                }
                .frame(maxWidth: 660, alignment: .leading).padding(16)
            }
            .scrollDismissesKeyboard(.interactively).background(Theme.surface)
            .navigationTitle("New project").navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } } }
            .accessibilityElement(children: .contain).accessibilityIdentifier("factoryNewProject.sheet")
        }
        .task { await loadSpaces() }
        .onChange(of: selectedFolder) { checks = nil }
        .onChange(of: "\(selectedFolder ?? "")\u{0}\(goal)") { actionError = nil }
        .onChange(of: actionError ?? form.checksSummary ?? form.notGitMessage) { _, text in
            if let text, !text.isEmpty { AccessibilityNotification.Announcement(text).post() }
        }
    }

    private var unavailableCard: some View {
        card {
            Text("The factory is not available on \(host)").font(.headline).accessibilityAddTraits(.isHeader)
            Text("It needs sheepdog on herdr-core. \(host)'s sheepdog runs on herdr-tui, "
                 + "or herdr-core does not answer.").font(.subheadline).foregroundStyle(Theme.muted)
            Text("Fix, on \(host):").font(.footnote.weight(.semibold))
            Text("Open herdr-app, or start herdr-core in a terminal:").font(.subheadline)
            FactoryCommandCopyRow(command: "hc server start")
            Button("Check again") { if form.canCheck { run(start: false) } else { Task { await loadSpaces() } } }
                .buttonStyle(PillButtonStyle(fullWidth: false)).disabled(busy != nil)
        }
    }

    @ViewBuilder
    private var folderSection: some View {
        Text("Folder").font(.footnote.weight(.semibold)).foregroundStyle(Theme.muted).accessibilityAddTraits(.isHeader)
        switch spaces {
        case nil: card { ProgressView("Loading folders…") }
        case .failure, .success where form.isEmptyFolders:
            let msg = (try? spaces?.get()) == nil ? FactoryNewProjectForm.spacesFailureMessage(computerName: host)
                : "No folders to pick yet. Open a workspace in herdr on \(host), then try again."
            card(dashed: true) {
                Text(msg).font(.subheadline)
                Button("Try again") { Task { await loadSpaces() } }.buttonStyle(PillButtonStyle(fullWidth: false))
            }
        case .success:
            card(spacing: 0, padding: 0) {
                Text("folders sheepdog knows on \(host)").font(.caption).foregroundStyle(Theme.muted).padding(12)
                ForEach(form.folders, content: folderRow)
            }
        }
        form.notGitMessage.map { Text($0).font(.footnote.weight(.semibold)).foregroundStyle(Theme.danger) }
    }

    @ViewBuilder
    private func folderRow(_ option: FactoryNewProjectForm.FolderOption) -> some View {
        let selected = form.selectedFolder == option.path
        let markColor = !option.isExistingProject && !option.git ? Theme.danger : Theme.muted
        Divider().overlay(Theme.line)
        Button { selectedFolder = option.path } label: {
            HStack(spacing: 10) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(option.path).font(.footnote.monospaced())
                        .foregroundStyle(option.isSelectable ? Theme.ink : Theme.muted)
                    Text(option.mark).font(.caption).foregroundStyle(markColor)
                }
                Spacer(minLength: 8)
                Image(systemName: "checkmark").font(.subheadline.weight(.bold)).foregroundStyle(Theme.primaryText)
                    .opacity(selected ? 1 : 0).accessibilityHidden(true)
            }
            .padding(.horizontal, 14).frame(maxWidth: .infinity, minHeight: 52).contentShape(.rect)
        }
        .buttonStyle(.plain).disabled(!option.isSelectable)
        .accessibilityLabel(option.accessibilityLabel).accessibilityAddTraits(selected ? .isSelected : [])
    }

    private func card<Content: View>(spacing: CGFloat = 8, padding: CGFloat = 14, border: Color = Theme.line,
                                     dashed: Bool = false, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: spacing, content: content).padding(padding)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Theme.card, in: .rect(cornerRadius: Theme.Radius.card))
            .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card)
                .strokeBorder(dashed ? Theme.muted : border, style: StrokeStyle(dash: dashed ? [4, 4] : [])))
    }

    private func loadSpaces() async {
        unavailable = false
        spaces = nil
        do { spaces = .success(try await model.daemon { try await $0.spaces() }) }
        catch { spaces = .failure(error) }
    }

    private func run(start: Bool) {
        guard let computer, let folder = form.selectedFolder, busy == nil,
              start ? form.canStart : form.canCheck else { return }
        UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
        busy = start ? "start" : "check"
        actionError = nil
        unavailable = false
        Task { await perform(start: start, folder: folder, goal: goal.trimmedOrNil ?? "", on: computer) }
    }

    private func perform(start: Bool, folder: String, goal: String, on computer: ComputerModel) async {
        defer { busy = nil }
        do {
            guard start else {
                checks = try await computer.request(as: DaemonClient.self) { try await $0.checkFactory(folder: folder) }
                return
            }
            try await computer.actions.run("new:\(folder)", .init(timeout: 60, keyed: true), as: DaemonClient.self) {
                try await $0.startFactory(folder: folder, goal: goal)
            }
            await computer.refresh()
            let target = FactoryNewProjectForm.normalizePath(folder)
            let head = computer.factoryHeads.first { $0.folder.map(FactoryNewProjectForm.normalizePath) == target }
            guard let head else { return dismiss() }
            model.presentation.dismiss(then: .openWindow(.factoryProject(head, computerID: computer.id)))
        } catch where FactoryNewProjectForm.isFactoryUnavailable(error) {
            unavailable = true
            if start { checks = nil }
        } catch let err as FactoryStartChecksError { checks = err.results }
        catch {
            actionError = start
                ? FactoryNewProjectForm.startErrorMessage(error, computerName: host, linkUsable: computer.link.isUsable)
                : RequestFailure(error, linkUsable: computer.link.isUsable, name: host).message
        }
    }
}
