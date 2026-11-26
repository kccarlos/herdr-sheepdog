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

    private var computer: ComputerModel? { model.fleet.model(computerID) }
    private var head: FactoryProjectHead { computer?.factoryHeads.first { $0.id == initialHead.id } ?? initialHead }

    var body: some View {
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
                    FactoryHeadCard(item: board.summary, computer: computer.snapshot, dialog: nil, answer: { _, _ in }, project: board, onGone: projectGone)
                    composer(board, on: computer)
                    if !board.openAsks.isEmpty { Text(board.asksHeading).font(.headline).foregroundStyle(Theme.blockedText).accessibilityAddTraits(.isHeader) }
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
                        DisclosureGroup(board.doneFoldLabel) { ForEach(board.doneTasks, content: row) }.accessibilityIdentifier("factoryProject.doneFold")
                    }
                }
                .frame(maxWidth: 660, alignment: .leading).padding(16)
                .accessibilityElement(children: .contain).accessibilityIdentifier("factoryProject.board")
            } else {
                ProgressView(FactoryProjectBoard.loadingMessage(projectName: head.name, computerName: name)).padding()
            }
        }
        .background(Theme.surface).navigationTitle(head.name)
        .navigationBarTitleDisplayMode(.inline).toolbar(.visible, for: .navigationBar)
        .task(id: "\(computer?.factoryChanges[head.id] ?? 0):\(head.state.rawValue)") { await load() }
        .onChange(of: head.state) { feedback = nil }
    }

    private func row(_ row: FactoryProjectBoard.Row) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("\(row.ref) \(row.task.title)").fontWeight(.semibold)
            Text(row.stepSummary).accessibilityValue(row.stepPosition ?? "")
            if let why = row.whyWaiting { Text(why).foregroundStyle(Theme.muted) }
            if let run = row.runLine { Text(run).foregroundStyle(Theme.muted) }
            if let decision = row.lastDecisionLine { Text(decision).foregroundStyle(Theme.muted) }
        }
        .font(.subheadline).padding(10).frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.card, in: .rect(cornerRadius: Theme.Radius.card))
        .accessibilityElement(children: .contain).accessibilityIdentifier("factoryProject.task.\(row.ref)")
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
                    .font(.body).lineLimit(1...4).accessibilityLabel(box.label).accessibilityIdentifier("factoryProject.say.field")
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
                if head.state == box.state { feedback = (box.sentStatus, box.hasCoordinator ? Theme.done : Theme.muted) }
            } catch where FactoryProjectBoard.isProjectGone(error) {
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
