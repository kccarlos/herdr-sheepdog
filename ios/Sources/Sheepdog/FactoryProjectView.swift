import SheepdogKit
import SwiftUI

struct FactoryProjectView: View {
    let initialHead: FactoryProjectHead
    let computerID: String
    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.dismiss) private var dismiss
    @Environment(\.dynamicTypeSize) private var typeSize
    @FocusState private var editing: Bool
    @State private var tasks: [FactoryTask]?
    @State private var live = false
    @State private var error: Error?
    @State private var reload = 0
    @State private var text = ""
    @State private var sent: String?
    @State private var refusal: String?
    @State private var confirmingStop = false
    @State private var busy = false

    private var computer: ComputerModel? { model.fleet.model(computerID) }
    private var head: FactoryProjectHead { computer?.factoryHeads.first { $0.id == initialHead.id } ?? initialHead }

    var body: some View {
        let name = computer?.name ?? "your computer"
        ScrollView {
            if let error {
                let failure = FactoryProjectBoard.loadFailure(projectName: head.name, computerName: name, error: error)
                VStack(spacing: 12) {
                    Text(failure.heading).font(.headline)
                    Text(failure.detail).foregroundStyle(Theme.muted)
                    Button(failure.retryLabel) { reload += 1 }.accessibilityIdentifier("factoryProject.retry")
                }.padding()
            } else if let tasks, let computer {
                let board = FactoryProjectBoard(head: head, tasks: tasks, asks: computer.questions, computerName: name, live: live)
                content(board, on: computer)
            } else {
                ProgressView(FactoryProjectBoard.loadingMessage(projectName: head.name, computerName: name)).padding()
            }
        }
        .background(Theme.surface).navigationTitle(head.name)
        .navigationBarTitleDisplayMode(.inline).toolbar(.visible, for: .navigationBar)
        .task(id: "\(reload):\(computer?.factoryChanges[initialHead.id] ?? 0):\(head.state.rawValue)") { await load() }
    }

    private func content(_ board: FactoryProjectBoard, on computer: ComputerModel) -> some View {
        let confirm = board.stopConfirm
        return VStack(alignment: .leading, spacing: 12) {
            FactoryHeadCard(item: board.summary, computer: computer.snapshot, dialog: nil, answer: { _, _ in }, project: board,
                            onStop: board.canStop ? { confirmingStop = true } : nil, didResume: { reload += 1 })
            composer(board)
            if !board.openAsks.isEmpty { Text(board.asksHeading).font(.headline).foregroundStyle(Theme.blockedText) }
            ForEach(board.openAsks, id: \.id) { ask in
                let card = ApprovalFlow.Card(pending: ask, phase: computer.phase(ask.id))
                ApprovalCardView(card: card, idPrefix: "question.\(ask.id)") { computer.answer(question: ask.id, option: $0) }
            }
            taskList(board)
        }
        .frame(maxWidth: 660, alignment: .leading).padding(16)
        .accessibilityElement(children: .contain).accessibilityIdentifier("factoryProject.board")
        .buttonStyle(.bordered).disabled(busy)
        .alert(confirm.title, isPresented: $confirmingStop) {
            Button(confirm.keepLabel, role: .cancel) {}
            Button(confirm.confirmLabel, role: .destructive) { act(board) }
        } message: { Text(confirm.message) }
    }

    @ViewBuilder
    private func taskList(_ board: FactoryProjectBoard) -> some View {
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
            DisclosureGroup(board.doneFoldLabel) { ForEach(board.doneTasks, content: row) }
                .accessibilityIdentifier("factoryProject.doneFold")
        }
    }

    private func row(_ row: FactoryProjectBoard.Row) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("\(row.ref) \(row.task.title)").fontWeight(.semibold)
            Text(row.stepSummary)
            if let why = row.whyWaiting { Text(why).foregroundStyle(Theme.muted) }
            if let run = row.runLine { Text(run).foregroundStyle(Theme.muted) }
            if let decision = row.lastDecisionLine { Text(decision).foregroundStyle(Theme.muted) }
        }
        .font(.subheadline).padding(10).frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.Radius.card))
        .accessibilityElement(children: .contain).accessibilityLabel(row.accessibilityLabel)
        .accessibilityIdentifier("factoryProject.task.\(row.ref)")
    }

    private func composer(_ board: FactoryProjectBoard) -> some View {
        let box = board.sayBox(text: text)
        return VStack(alignment: .leading, spacing: 6) {
            Text(box.label).font(.subheadline.weight(.semibold))
            HStack {
                TextField(box.placeholder, text: $text, axis: typeSize.isAccessibilitySize ? .vertical : .horizontal)
                    .focused($editing).lineLimit(1...4).accessibilityLabel(box.label)
                    .accessibilityIdentifier("factoryProject.say.field")
                Button { act(board, instruction: text.trimmedOrNil) } label: { Image(systemName: "arrow.up") }
                    .accessibilityLabel(box.sendAccessibilityLabel).accessibilityIdentifier("factoryProject.say.send").disabled(!box.canSend)
            }
            .padding(8).background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.Radius.card)).disabled(box.isFinished)
            if let count = box.counterText { Text(count).accessibilityLabel(box.counterAccessibilityLabel) }
            if let message = box.tooLongMessage ?? refusal { Text(message).foregroundStyle(Theme.danger) }
            Text(sent ?? box.defaultNote).foregroundStyle(sent == nil ? Theme.muted : Theme.done).accessibilityIdentifier("factoryProject.say.status")
            Text(box.questionHint).foregroundStyle(Theme.muted)
        }
        .font(.footnote)
    }

    private func load() async {
        do {
            guard let computer else { throw ComputerModel.Stale() }
            let head = head
            let rows = try await computer.request(as: DaemonClient.self) { try await $0.factoryGraph(projectID: head.id) }.tasks
            let projects = head.state == .finished ? try await computer.request(as: DaemonClient.self) { try await $0.factoryProjects() } : []
            try Task.checkCancellation()
            live = projects.first { $0.id == head.id }?.live == true
            tasks = rows
            error = nil
        } catch {
            if Task.isCancelled { return }
            if FactoryProjectBoard.isProjectGone(error) { projectGone() } else { self.error = error }
        }
    }

    private func act(_ board: FactoryProjectBoard, instruction: String? = nil) {
        guard !busy, let computer else { return }
        editing = false
        busy = true
        refusal = nil
        sent = nil
        let action = instruction != nil ? "say" : "stop"
        let id = board.head.id
        Task {
            defer { busy = false }
            do {
                try await computer.actions.run("\(action):\(id)", .init(timeout: 60, keyed: true), as: DaemonClient.self) { [id] in
                    if let instruction {
                        try await $0.sayFactory(projectID: id, text: instruction)
                    } else {
                        try await $0.stopFactory(projectID: id)
                    }
                }
                if instruction != nil {
                    text = ""
                    sent = board.sayBox(text: "").sentStatus
                    if let sent { AccessibilityNotification.Announcement(sent).post() }
                } else {
                    await computer.refresh()
                    reload += 1
                }
            } catch {
                let name = computer.name
                if FactoryProjectBoard.isProjectGone(error) { projectGone() }
                else if instruction != nil { refusal = FactoryProjectBoard.sayErrorMessage(error, projectName: board.head.name, computerName: name) }
                else { refusal = FactoryProjectBoard.stopErrorMessage(error, computerName: name) }
            }
        }
    }

    private func projectGone() {
        model.notices.banner = .init(message: "This project is gone.")
        dismiss()
    }
}
