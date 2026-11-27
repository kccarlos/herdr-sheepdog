import SheepdogKit
import SwiftUI

struct FactoryProjectView: View {
    let initialHead: FactoryProjectHead
    let computerID: String
    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.dismiss) private var dismiss
    @State private var loaded: Result<([FactoryTask], Bool), Error>?
    @State private var text = ""
    @State private var feedback: (message: String, color: Color)?
    @State private var showsCoordinator = false
    @State private var showsSettings = false
    @State private var showAsks = false
    @State private var selectedTask: FactoryTask?
    @State private var showsGoalSheet = false
    @State private var showDone = false
    @FocusState private var sayFocused: Bool

    private var computer: ComputerModel? { model.fleet.model(computerID) }
    private var head: FactoryProjectHead { computer?.factoryHeads.first { $0.id == initialHead.id } ?? initialHead }

    var body: some View {
        ScrollViewReader { proxy in
            boardScroll
                .onChange(of: showAsks) {
                    guard showAsks else { return }
                    proxy.scrollTo("factoryProject.asks", anchor: .top)
                    showAsks = false
                }
        }
        .navigationDestination(isPresented: $showsCoordinator) {
            FactoryCoordinatorView(initialHead: head, computerID: computerID) { showAsks = true }
        }
        .navigationDestination(isPresented: $showsSettings) {
            FactorySettingsView(initialHead: head, computerID: computerID, onGone: projectGone)
        }
        .navigationDestination(isPresented: Binding(get: { selectedTask != nil }, set: { if !$0 { selectedTask = nil } })) {
            if let task = selectedTask {
                let current = (try? loaded?.get())?.0.first { $0.ref == task.ref } ?? task
                FactoryTaskView(task: current, projectName: head.name, projectState: head.state)
            }
        }
        .sheet(isPresented: $showsGoalSheet) {
            if let computer {
                FactoryGoalSheet(head: head, computer: computer, onGone: projectGone)
            }
        }
    }

    private var boardScroll: some View {
        ScrollView {
            let name = computer?.name ?? "your computer"
            if case let .failure(error) = loaded {
                let failure = FactoryProjectBoard.loadFailure(projectName: head.name, computerName: name, error: error, linkUsable: computer?.link.isUsable == true)
                VStack(spacing: 12) {
                    Text(failure.heading).font(.headline)
                    Text(failure.detail).foregroundStyle(Theme.muted)
                    Button(failure.retryLabel) { Task { await load() } }
                        .buttonStyle(PillButtonStyle(fullWidth: false)).accessibilityIdentifier("factoryProject.retry")
                }.multilineTextAlignment(.center).padding()
            } else if case let .success((tasks, live)) = loaded, let computer {
                let board = FactoryProjectBoard(head: head, tasks: tasks, asks: computer.questions, computerName: name, live: live)
                VStack(alignment: .leading, spacing: 12) {
                    FactoryHeadCard(item: board.summary, computer: computer.snapshot, dialog: nil, answer: { _, _ in }, project: board, onGone: projectGone, onGoal: { showsGoalSheet = true })
                    OptionFlow(spacing: 8) {
                        if board.showsCoordinatorButton {
                            Button("Coordinator") { showsCoordinator = true }
                                .buttonStyle(.bordered).controlSize(.large)
                                .accessibilityLabel(board.coordinatorAccessibilityLabel)
                                .accessibilityIdentifier("factoryProject.coordinator")
                        }
                        Button("Settings") { showsSettings = true }
                            .buttonStyle(.bordered).controlSize(.large)
                            .accessibilityIdentifier("factoryProject.settings")
                    }
                    composer(board, on: computer)
                    if !board.openAsks.isEmpty {
                        Text(board.asksHeading).font(.headline).foregroundStyle(Theme.blockedText)
                            .accessibilityAddTraits(.isHeader).id("factoryProject.asks")
                    }
                    ForEach(board.openAsks, id: \.id) { ask in
                        ApprovalCardView(card: .init(pending: ask, phase: computer.phase(ask.id)), idPrefix: "question.\(ask.id)") { computer.answer(question: ask.id, option: $0) }
                    }
                    Text("Tasks").font(.headline).accessibilityAddTraits(.isHeader)
                    if board.isEmptyTasks {
                        Text(board.emptyTasksHeading)
                        Text(board.emptyTasksDetail).foregroundStyle(Theme.muted)
                    }
                    ForEach(board.activeGroups, id: \.stage) { group in
                        Text("\(group.label) \(group.tasks.count)").font(.subheadline.weight(.semibold))
                            .foregroundStyle(group.isAlert ? Theme.blockedText : Theme.muted).accessibilityAddTraits(.isHeader)
                        ForEach(group.tasks, content: row)
                    }
                    if !board.doneTasks.isEmpty {
                        Button { showDone.toggle() } label: {
                            HStack(spacing: 6) { Text(board.doneFoldLabel).font(.subheadline.weight(.semibold)); Spacer(); Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).rotationEffect(.degrees(showDone ? 90 : 0)).accessibilityHidden(true) }
                                .foregroundStyle(Theme.muted).frame(minHeight: 44).contentShape(Rectangle())
                        }.buttonStyle(.plain).accessibilityValue(showDone ? "Shown" : "Hidden").accessibilityIdentifier("factoryProject.doneFold")
                        if showDone { ForEach(board.doneTasks, content: row) }
                    }
                }
                .frame(maxWidth: 660, alignment: .leading).padding(16)
                .accessibilityElement(children: .contain).accessibilityIdentifier("factoryProject.board")
            } else {
                ProgressView(FactoryProjectBoard.loadingMessage(projectName: head.name, computerName: name)).padding()
            }
        }
        .scrollDismissesKeyboard(.interactively)
        .background(Theme.surface).navigationTitle(head.name)
        .navigationBarTitleDisplayMode(.inline).toolbar(.visible, for: .navigationBar)
        .task(id: "\(computer?.factoryChanges[head.id] ?? 0):\(head.state.rawValue):\(head.goal)") { await load() }
        .onChange(of: head.state) { feedback = nil }
    }

    private func row(_ row: FactoryProjectBoard.Row) -> some View {
        HStack(spacing: 8) {
            VStack(alignment: .leading, spacing: 4) {
                Text("\(row.ref) \(row.task.title)").fontWeight(.semibold).accessibilityAddTraits(.isButton).accessibilityHint("Opens task details").accessibilityAction { selectedTask = row.task }
                Text(row.stepSummary).accessibilityValue(row.stepPosition ?? "")
                if let why = row.whyWaiting { Text(why).foregroundStyle(Theme.muted) }
                if let run = row.runLine { Text(run).foregroundStyle(Theme.muted) }
                if let decision = row.lastDecisionLine { Text(decision).foregroundStyle(Theme.muted) }
            }
            Spacer(minLength: 0)
            Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(Theme.chevron).accessibilityHidden(true)
        }
        .font(.subheadline).padding(10).frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.card, in: .rect(cornerRadius: Theme.Radius.card))
        .contentShape(Rectangle())
        .onTapGesture { selectedTask = row.task }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("factoryProject.task.\(row.ref)")
    }

    private func composer(_ board: FactoryProjectBoard, on computer: ComputerModel) -> some View {
        let box = board.sayBox(text: text)
        let instruction = text.trimmedOrNil ?? ""
        let key = "say:\(board.head.id):\(instruction)"
        let message = box.tooLongMessage ?? feedback?.message
        return VStack(alignment: .leading, spacing: 6) {
            Text(box.label).font(.subheadline.weight(.semibold)).accessibilityRepresentation { EmptyView() }
            HStack {
                TextField(box.placeholder, text: Binding(get: { text }, set: edit), axis: .vertical)
                    .font(.body).lineLimit(1...4).focused($sayFocused)
                    .accessibilityLabel(box.label).accessibilityIdentifier("factoryProject.say.field")
                Button { send(instruction, box: box, on: computer, key: key) } label: { Image(systemName: "arrow.up") }
                    .buttonStyle(PillButtonStyle(fullWidth: false)).disabled(!box.canSend || computer.actions.isInFlight(key))
                    .accessibilityLabel(box.sendAccessibilityLabel).accessibilityIdentifier("factoryProject.say.send")
            }.padding(8).background(Theme.card, in: .rect(cornerRadius: Theme.Radius.card)).disabled(box.isFinished)
            box.counterText.map { Text($0).foregroundStyle(box.isTooLong ? Theme.danger : Theme.muted).accessibilityLabel(box.counterAccessibilityLabel) }
            Text(message ?? box.defaultNote)
                .foregroundStyle(box.isTooLong ? Theme.danger : feedback?.color ?? Theme.muted)
                .accessibilityIdentifier(feedback == nil ? "" : "factoryProject.say.status")
            Text(box.questionHint).foregroundStyle(Theme.muted)
        }
        .font(.footnote)
        .onChange(of: message) { if let message { AccessibilityNotification.Announcement(message).post() } }
    }

    private func edit(_ draft: String) {
        guard draft != text else { return }
        text = draft
        feedback = nil
    }

    private func load() async {
        if case .failure = loaded { loaded = nil }
        let head = head
        guard let computer else { return }
        do {
            let result = try await computer.request(as: DaemonClient.self) { client in
                let tasks = try await client.factoryGraph(projectID: head.id).tasks
                guard head.state == .finished else { return (tasks, false) }
                return (tasks, try await client.factoryProjects().contains { $0.id == head.id && $0.live == true })
            }
            if !Task.isCancelled { loaded = .success(result) }
        } catch {
            if Task.isCancelled { return }
            if FactoryProjectBoard.isProjectGone(error) { projectGone() }
            else if (try? loaded?.get()) == nil { loaded = .failure(error) }
        }
    }

    private func send(_ instruction: String, box: FactorySayBox, on computer: ComputerModel, key: String) {
        feedback = nil
        Task {
            do {
                let id = initialHead.id
                try await computer.actions.run(key, .agent, as: DaemonClient.self) { try await $0.sayFactory(projectID: id, text: instruction) }
                guard text == box.text else { return }
                text = ""
                sayFocused = false
                if head.state == box.state { feedback = (box.sentStatus, box.hasCoordinator ? Theme.done : Theme.muted) }
            } catch where FactoryProjectBoard.isProjectGone(error) {
                sayFocused = false
                projectGone()
            } catch {
                guard text == box.text, head.state == box.state else { return }
                feedback = (FactoryProjectBoard.sayErrorMessage(error, projectName: box.projectName, computerName: computer.name, linkUsable: computer.link.isUsable), Theme.danger)
            }
        }
    }

    private func projectGone() {
        model.notices.banner = .init(tone: .info, message: "This project is gone.")
        dismiss()
        Task { await computer?.refresh() }
    }
}
