import Foundation

public struct FactoryProjectBoard {
    public struct Row: Identifiable {
        public let task: FactoryTask
        public let runLine: String?
        public var id: String { task.ref }
        public var ref: String { task.ref }
        public var whyWaiting: String? { task.whyWaiting }
        private var stepIndex: Int? {
            task.steps.firstIndex { $0.state == "now" } ?? task.steps.lastIndex { $0.state != "todo" } ?? task.steps.indices.first
        }
        public var stepSummary: String {
            guard let stepIndex else { return task.stage }
            let step = task.steps[stepIndex]
            if step.name == "review", let text = step.text { return text }
            if step.name == "CI", let skippedReview = task.steps.first(where: { $0.name == "review" && $0.state == "skipped" }) {
                return "CI, review \(skippedReview.text ?? "skipped")"
            }
            let detail = step.name == "implementing" ? step.text?.components(separatedBy: "; ").first : step.text
            return detail.map { "\(step.name), \($0)" } ?? step.name
        }
        public var stepAccessibilityLabel: String { stepPosition.map { "\($0), \(stepSummary)" } ?? stepSummary }
        public var stepPosition: String? { stepIndex.map { "step \($0 + 1) of \(task.steps.count)" } }
        public var lastDecisionLine: String? {
            guard let entry = task.lastActorDecision, let decision = entry.decision else { return nil }
            return "\(entry.by == "owner" ? "you" : entry.by): \(decision)" + (entry.why.map { " — \($0)" } ?? "")
        }
        public var accessibilityLabel: String {
            [ref, task.title, task.stage.replacingOccurrences(of: "_", with: " "), whyWaiting, stepAccessibilityLabel, runLine, lastDecisionLine]
                .compactMap { $0 }.joined(separator: ", ")
        }
    }

    public typealias TaskGroup = (stage: String, label: String, isAlert: Bool, tasks: [Row])

    public let summary: FactoryAgentsList.Item
    public let openAsks: [PendingAction]
    public let activeGroups: [TaskGroup]
    public let doneTasks: [Row]
    public let caption: String?
    public let live: Bool

    public init(head: FactoryProjectHead, tasks: [FactoryTask], asks: [PendingAction] = [],
                computerName: String? = nil, live: Bool = false, now: Date = .now) {
        openAsks = asks.filter { $0.factory?.project == head.id }
        summary = .init(head: head, coordinatorDialog: nil, coordinatorBlocked: false, openAsks: openAsks.count)
        caption = summary.caption(computerName: computerName)
        self.live = live
        let groups = Dictionary(grouping: tasks.sorted { $0.layer < $1.layer }.map { task in
            let elapsed = task.runStartedAt.map { "\(Int(max(0, now.timeIntervalSince($0)) / 60)) min" }
            let run = [task.runRole, task.runCLI, elapsed].compactMap { $0 }.joined(separator: " · ")
            return Row(task: task, runLine: head.state == .running ? run.nonEmpty : nil)
        }, by: \.task.stage)
        let unknown = groups.keys.filter { key in key != "done" && !Self.stageOrder.contains { $0.0 == key } }.sorted()
        activeGroups = (Self.stageOrder + unknown.map { ($0, Self.stageLabel(for: $0), false) }).compactMap { stage, label, alert in
            groups[stage].map { (stage, label, alert, $0) }
        }
        doneTasks = groups["done"] ?? []
    }

    private static let stageOrder = [
        ("owner_gate", "needs your approval", true), ("owner_answer", "waiting on you", true), ("blocked_or_failed", "blocked", true),
        ("ci", "waiting on CI", false), ("working", "working", false), ("waiting_on_dep", "waiting on others", false), ("queued", "queued", false)]

    public static func stageLabel(for stage: String) -> String {
        stage == "done" ? "done" : stageOrder.first { $0.0 == stage }?.1 ?? stage.replacingOccurrences(of: "_", with: " ")
    }

    public var head: FactoryProjectHead { summary.head }
    public var stateWord: String { summary.stateWord }
    public var countsLine: String { summary.countsLine }
    public var asksHeading: String { "Asks for you · \(openAsks.count) open" }
    public func sayBox(text: String) -> FactorySayBox { .init(projectName: head.name, state: head.state, hasCoordinator: showsCoordinatorButton, text: text) }
    public var doneFoldLabel: String { "done \(doneTasks.count)" }
    public var alertHeading: String? { summary.alertLine }
    public var alertFix: String? { summary.alertFixLine }
    public var showsCoordinatorButton: Bool { FactoryCoordinatorWindow(head: head).isLive }
    public var coordinatorAccessibilityLabel: String { "Open the \(head.name) coordinator's window" }
    public var canStop: Bool { head.state == .running || (head.state == .finished && live) }
    public var canResume: Bool { summary.resumableFolder != nil }
    public var canChangeGoal: Bool { head.folder?.trimmed.nonEmpty != nil }
    public var goalActionLabel: String { head.state == .finished ? "Reopen with a new goal" : "New goal" }
    public func goalForm(draft: String) -> FactoryGoalForm { .init(state: head.state, hasFolder: canChangeGoal, draft: draft) }
    public var stopAccessibilityLabel: String { "Stop this project" }
    public var resumeAccessibilityLabel: String { "Resume this project" }
    public var barNote: String? {
        if head.state == .stopped { return "Stopped. No agent is running. Resume goes on with the same goal." }
        if head.state == .finished { return live ? "Finished. An agent is still running." : "Finished. To go on, reopen it with a new goal." }
        return nil
    }
    public var stopConfirm: (title: String, message: String, keepLabel: String, confirmLabel: String) {
        ("Stop this project?", head.state == .finished ? "Its last running agent ends now. The project stays finished."
            : "Its running agents end now. Resume goes on with the same goal.", "Keep it running", "Stop the project")
    }

    public var isEmptyTasks: Bool { activeGroups.isEmpty && doneTasks.isEmpty }
    public var emptyTasksHeading: String { "no tasks yet" }
    public var emptyTasksDetail: String { "The coordinator has not planned this project yet." }
    public static func loadingMessage(projectName: String, computerName: String) -> String { "Loading \(projectName) from \(computerName)…" }
    public static func loadFailure(projectName: String, computerName: String, error: Error, linkUsable: Bool = true) -> (heading: String, detail: String, retryLabel: String) {
        ("Can't load \(projectName). Trying again.", "\(RequestFailure(error, linkUsable: linkUsable, name: computerName).message) The rest of the app still works.", "Try again")
    }

    public static func isProjectGone(_ error: Error) -> Bool { if case .rejected(404, _) = error as? DaemonError { true } else { false } }
    public static func sayErrorMessage(_ error: Error, projectName: String, computerName: String, linkUsable: Bool = true) -> String {
        if isProjectGone(error) { return "This project is gone." }
        if case let .rejected(409, message) = error as? DaemonError, message == "already_closed" || message.hasSuffix(" is finished. Give it a new goal.") { return FactorySayBox.finishedNote(projectName) }
        return factoryActionError(error, computerName: computerName, linkUsable: linkUsable, prefix: "Not sent: ", retry: "send again")
    }
    public static func stopErrorMessage(_ error: Error, computerName: String, linkUsable: Bool = true) -> String {
        factoryActionError(error, computerName: computerName, linkUsable: linkUsable, prefix: "Not stopped: ", retry: "press Stop again")
    }
}

public struct FactorySayBox {
    public let projectName: String
    public let state: FactoryProjectState
    public let hasCoordinator: Bool
    public let text: String

    fileprivate static func finishedNote(_ name: String) -> String { "\(name) is finished. Give it a new goal." }
    public var label: String { "Tell the \(projectName) factory" }
    public var placeholder: String { "An instruction, in your own words" }
    public var sendAccessibilityLabel: String { "Send to the \(projectName) factory" }
    public var questionHint: String { "A question? Ask by voice, or ask Sheepdog in Conversation." }
    public var isFinished: Bool { state == .finished }
    private var count: Int { text.unicodeScalars.count }
    public var isTooLong: Bool { count > 1_000 }
    public var canSend: Bool { !isFinished && !isTooLong && !text.trimmed.isEmpty }
    private var formattedCount: String { count.formatted(.number.locale(Locale(identifier: "en_US"))) }
    public var counterText: String? { count >= 900 ? "\(formattedCount) / 1,000" : nil }
    public var counterAccessibilityLabel: String { "\(formattedCount) of 1,000 characters" }
    public var tooLongMessage: String? { isTooLong ? "Too long. Keep it under 1,000 characters." : nil }
    public var defaultNote: String {
        if isFinished { return Self.finishedNote(projectName) }
        if state == .stopped { return "\(projectName) is stopped. It gets this when you resume it." }
        if !hasCoordinator { return "The \(projectName) coordinator is not running. It gets this when it is back." }
        return "Delivered when the coordinator is next free. To answer an ask, use its card."
    }
    public var sentStatus: String { state == .running && hasCoordinator ? "Told the \(projectName) factory." : defaultNote }
}

public struct FactoryGoalForm: Equatable, Sendable {
    public let state: FactoryProjectState
    public let hasFolder: Bool
    public let draft: String
    public init(state: FactoryProjectState, hasFolder: Bool, draft: String) { self.state = state; self.hasFolder = hasFolder; self.draft = draft }

    public var isReopen: Bool { state == .finished }
    public var canSubmit: Bool { hasFolder && !draft.trimmed.isEmpty }
    public var title: String { isReopen ? "Reopen with a new goal" : "New goal" }
    public var fieldLabel: String { "New goal" }
    public var hint: String { "The folder and the settings stay as they are. " + (isReopen ? "To change a model first, use settings." : "The coordinator plans the new goal.") }
    public var quotaHint: String { "Starting runs agents on your model accounts. They use your quota." }
    public var submitLabel: String { state == .stopped ? "Start with this goal" : state == .finished ? "Reopen with this goal" : "Give it this goal" }
    public var cancelLabel: String { "Cancel" }
    public var emptyValidationMessage: String { "Write a goal first." }

    public static func errorMessage(_ error: Error, state: FactoryProjectState, computerName: String, linkUsable: Bool = true) -> String {
        factoryStartError(error, action: state == .finished ? "Reopen" : "New goal", computerName: computerName, linkUsable: linkUsable)
    }
}

public struct FactoryTaskDetail: Equatable, Sendable {
    public struct Step: Identifiable, Equatable, Sendable {
        public let id: Int
        public let name, state: String
        public let text: String?
        public var stateWord: String { state == "todo" ? "to come" : state }
        public var accessibilityLabel: String { text.map { "\(name), \(stateWord): \($0)" } ?? "\(name), \(stateWord)" }
    }

    public struct LastDecision: Equatable, Sendable {
        public let actorLabel, decision: String
        public let whyLine: String?
        public let at: Date?
        public var heading: String { "Last decision" }
        public var accessibilityLabel: String { [heading, "\(actorLabel): \(decision)", whyLine].compactMap { $0 }.joined(separator: ". ") }
    }

    public let task: FactoryTask
    public let stageLabel: String
    public let afterLine, runLine, runAccessibilityLabel, roundsLine, brief: String?
    public let steps: [Step]
    public let lastDecision: LastDecision?
    public let showsWorkerWindowButton = false
    public var isEmptySteps: Bool { steps.isEmpty }
    public var emptyStepsMessage: String { "No steps recorded yet." }

    public init(task: FactoryTask, projectName _: String = "", projectState: FactoryProjectState, now: Date = .now) {
        self.task = task
        stageLabel = FactoryProjectBoard.stageLabel(for: task.stage)
        afterLine = task.after.isEmpty ? nil : "after \(task.after.joined(separator: ", "))"
        roundsLine = task.rounds <= 0 ? nil : "\(task.rounds) review \(task.rounds == 1 ? "round" : "rounds")"
        brief = task.brief?.trimmed.nonEmpty
        steps = task.steps.enumerated().map { Step(id: $0.offset, name: $0.element.name, state: $0.element.state, text: $0.element.text?.trimmed.nonEmpty) }
        lastDecision = task.lastActorDecision.flatMap { e in
            e.decision?.trimmed.nonEmpty.map { LastDecision(actorLabel: e.by == "owner" ? "you" : e.by, decision: $0, whyLine: e.why?.trimmed.nonEmpty.map { "Why: \($0)" }, at: e.at) }
        }
        let role = task.runRole?.trimmed.nonEmpty, cli = task.runCLI?.trimmed.nonEmpty
        if projectState == .running, role != nil || cli != nil || task.runStartedAt != nil {
            let mins = task.runStartedAt.map { Int(max(0, now.timeIntervalSince($0)) / 60) }, model = task.model?.trimmed.nonEmpty
            runLine = [role, cli, model, mins.map { "\($0) min" }].compactMap { $0 }.joined(separator: " · ").nonEmpty
            let started = mins.map { "started \($0) \($0 == 1 ? "minute" : "minutes") ago" }
            let spoken = [role, cli, model, started].compactMap { $0 }.joined(separator: ", ")
            runAccessibilityLabel = spoken.isEmpty ? nil : "Running now: \(spoken)"
        } else { runLine = nil; runAccessibilityLabel = nil }
    }
}
