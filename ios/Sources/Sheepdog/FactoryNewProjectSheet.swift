import SheepdogKit
import SwiftUI

struct FactoryNewProjectSheet: View {
    private enum Busy {
        case check, start
    }

    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.dismiss) private var dismiss
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    @State private var computerID: String?
    @State private var spaces: Result<SpacesView, Error>?
    @State private var selectedFolder: String?
    @State private var goal = ""
    @State private var checks: [FactoryStartCheck]?
    @State private var busy: Busy?
    @State private var actionError: String?
    @State private var unavailable = false
    @State private var startAttempt: (target: String, key: String)?
    @FocusState private var goalFocused: Bool

    private var computer: ComputerModel? { computerID.map(model.fleet.model) ?? model.fleet.current }
    private var host: String { computer?.name ?? model.currentComputerName }
    private var form: FactoryNewProjectForm {
        FactoryNewProjectForm(
            spaces: try? spaces?.get(), heads: computer?.factoryHeads ?? [],
            selectedFolder: selectedFolder, goal: goal, checks: checks
        )
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    if dynamicTypeSize.isAccessibilitySize {
                        Text("New project").font(.title2.weight(.bold)).fontDesign(.rounded)
                            .accessibilityAddTraits(.isHeader)
                    }
                    if form.showsFirstProjectIntro {
                        card(spacing: 4) {
                            Text("No project yet").font(.title3.weight(.bold)).fontDesign(.rounded)
                                .accessibilityAddTraits(.isHeader)
                            Text(FactoryNewProjectForm.firstProjectIntro)
                                .font(.footnote).foregroundStyle(Theme.muted)
                        }
                    }
                    if unavailable {
                        unavailableCard
                    } else {
                        folderSection
                        Text("Goal").font(.footnote.weight(.semibold)).foregroundStyle(Theme.muted)
                            .accessibilityHidden(true)
                        TextField("", text: $goal, axis: .vertical)
                            .font(.body).lineLimit(2...5).padding(10).focused($goalFocused)
                            .background(Theme.card, in: .rect(cornerRadius: 12))
                            .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(Theme.line))
                            .accessibilityLabel("Goal").accessibilityHint(FactoryNewProjectForm.goalHint)
                            .accessibilityIdentifier("factoryNewProject.goal")
                        if checks == nil {
                            Text(FactoryNewProjectForm.goalHint).font(.caption).foregroundStyle(Theme.muted)
                        }
                        if let checks {
                            card(padding: 12, border: form.hasFailedChecks ? Theme.danger.opacity(0.45) : Theme.line) {
                                Text("Start checks · on \(host)").font(.footnote.weight(.semibold))
                                    .foregroundStyle(Theme.muted).accessibilityAddTraits(.isHeader)
                                ForEach(checks.indices, id: \.self) { index in
                                    if index > 0 {
                                        Divider().overlay(Theme.line)
                                    }
                                    FactoryStartCheckRow(check: checks[index], computerName: host, framed: false)
                                }
                                if let summary = form.checksSummary {
                                    Text(summary).font(.footnote.weight(.semibold))
                                        .foregroundStyle(form.hasFailedChecks ? Theme.danger : Theme.done)
                                }
                            }
                        }
                        if let actionError {
                            Text(actionError).font(.footnote.weight(.semibold)).foregroundStyle(Theme.danger)
                        }
                        Button(busy == .check ? "Checking the folder…" : form.checkButtonLabel) {
                            run(start: false)
                        }
                        .buttonStyle(PillButtonStyle(prominent: false)).disabled(!form.canCheck || busy != nil)
                        Button(busy == .start ? "Starting…" : "Start the project and its agents") {
                            run(start: true)
                        }
                        .buttonStyle(PillButtonStyle(prominent: true)).disabled(!form.canStart || busy != nil)
                        .accessibilityHint(form.canStart ? "" : form.startHint)
                        Text(form.startHint).font(.caption).foregroundStyle(Theme.muted)
                    }
                }
                .frame(maxWidth: 660, alignment: .leading).padding(16)
            }
            .scrollDismissesKeyboard(.interactively).background(Theme.surface)
            .navigationTitle(dynamicTypeSize.isAccessibilitySize ? "" : "New project")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
            .accessibilityElement(children: .contain).accessibilityIdentifier("factoryNewProject.sheet")
        }
        .onChange(of: goal) { actionError = nil }
        .task {
            computerID = computerID ?? model.fleet.currentID
            await loadSpaces()
        }
    }

    private var unavailableCard: some View {
        card {
            Text("The factory is not available on \(host)").font(.headline).accessibilityAddTraits(.isHeader)
            Text("It needs sheepdog on herdr-core. \(host)'s sheepdog runs on herdr-tui, "
                + "or herdr-core does not answer.")
                .font(.subheadline).foregroundStyle(Theme.muted)
            Text("Fix, on \(host):").font(.footnote.weight(.semibold))
            Text("Open herdr-app, or start herdr-core in a terminal:").font(.subheadline)
            FactoryCommandCopyRow(command: "hc server start")
            Button("Check again") {
                Task { await loadSpaces() }
            }
            .buttonStyle(PillButtonStyle(fullWidth: false)).disabled(busy != nil)
        }
    }

    @ViewBuilder
    private var folderSection: some View {
        Text("Folder").font(.footnote.weight(.semibold)).foregroundStyle(Theme.muted).accessibilityAddTraits(.isHeader)
        switch spaces {
        case nil:
            card { ProgressView("Loading folders…") }
        case .failure, .success where form.isEmptyFolders:
            card(border: Theme.muted) {
                Text(FactoryNewProjectForm.spacesFailureMessage(
                    spaces, computerName: host, linkUsable: computer?.link.isUsable == true
                ))
                .font(.subheadline)
                Button("Try again") {
                    Task { await loadSpaces() }
                }
                .buttonStyle(PillButtonStyle(fullWidth: false))
            }
        case .success:
            card(spacing: 0, padding: 0) {
                Text("folders sheepdog knows on \(host)").font(.caption).foregroundStyle(Theme.muted).padding(12)
                ForEach(form.folders, content: folderRow)
            }
        }
        if let notGit = form.notGitMessage {
            Text(notGit).font(.footnote.weight(.semibold)).foregroundStyle(Theme.danger)
        }
    }

    @ViewBuilder
    private func folderRow(_ option: FactoryNewProjectForm.FolderOption) -> some View {
        let selected = form.selectedFolder == option.path
        let markColor = !option.isExistingProject && !option.git ? Theme.danger : Theme.muted
        Divider().overlay(Theme.line)
        Button {
            selectedFolder = option.path
            checks = nil
            actionError = nil
            if !option.git {
                announce(FactoryNewProjectForm.notGitText)
            }
        } label: {
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

    private func card<Content: View>(
        spacing: CGFloat = 8, padding: CGFloat = 14, border: Color = Theme.line, @ViewBuilder content: () -> Content
    ) -> some View {
        VStack(alignment: .leading, spacing: spacing, content: content).padding(padding)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Theme.card, in: .rect(cornerRadius: Theme.Radius.card))
            .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card).strokeBorder(border))
    }

    private func loadSpaces() async {
        unavailable = false
        spaces = nil
        guard let computer else {
            spaces = .failure(ComputerFleet.Moved())
            return
        }
        do {
            spaces = .success(try await computer.request(as: DaemonClient.self) { try await $0.spaces() })
        } catch {
            spaces = .failure(error)
        }
    }

    private func run(start: Bool) {
        guard let computer, let folder = form.selectedFolder, busy == nil,
              start ? form.canStart : form.canCheck else { return }
        goalFocused = false
        busy = start ? .start : .check
        actionError = nil
        unavailable = false
        let trimmedGoal = goal.trimmedOrNil ?? ""
        let target = "\(folder)\u{1F}\(trimmedGoal)"
        if start, startAttempt?.target != target {
            startAttempt = (target, UUID().uuidString)
        }
        let key = start ? startAttempt?.key : nil
        Task {
            defer { busy = nil }
            do {
                if !start {
                    let results = try await computer.actions.run(
                        "check:\(folder)", .init(timeout: 45, keyed: false), as: DaemonClient.self
                    ) { try await $0.checkFactory(folder: folder) }
                    checks = results
                    announce(FactoryNewProjectForm.summarize(results))
                    return
                }
                try await computer.actions.run(
                    "new:\(folder)", .init(timeout: 60, keyed: true), as: DaemonClient.self, idempotencyKey: key
                ) { try await $0.startFactory(folder: folder, goal: trimmedGoal) }
                startAttempt = nil
                await computer.refresh()
                let head = computer.factoryHeads.first { $0.folder.map(FactoryNewProjectForm.normalizePath) == folder }
                if let head {
                    model.presentation.dismiss(then: .openWindow(.factoryProject(head, computerID: computer.id)))
                } else {
                    model.notices.banner = .init(tone: .info, message: FactoryNewProjectForm.startedMessage)
                    dismiss()
                }
            } catch where FactoryNewProjectForm.isFactoryUnavailable(error) {
                startAttempt = nil
                if start {
                    checks = nil
                }
                unavailable = true
                announce("The factory is not available on \(host)")
            } catch let err as FactoryStartChecksError {
                startAttempt = nil
                checks = err.results
                announce(FactoryNewProjectForm.summarize(err.results))
            } catch {
                if !ActionRunner.isTransport(error) {
                    startAttempt = nil
                }
                actionError = start
                    ? FactoryNewProjectForm.startErrorMessage(
                        error, computerName: host, linkUsable: computer.link.isUsable)
                    : FactoryNewProjectForm.checkFailureMessage
                announce(actionError)
            }
        }
    }

    private func announce(_ text: String?) {
        if let text, !text.isEmpty {
            AccessibilityNotification.Announcement(text).post()
        }
    }
}
