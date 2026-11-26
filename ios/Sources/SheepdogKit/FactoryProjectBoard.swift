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
            let detail = step.name == "implementing" ? step.text?.components(separatedBy: "; ").first : step.text
            if step.name == "review", let text = step.text { return text }
            if step.name == "CI", let review = task.steps.first(where: { $0.name == "review" && $0.state == "skipped" }) {
                return "CI, review \(review.text ?? "skipped")"
            }
            return [step.name, detail].compactMap { $0 }.joined(separator: ", ")
        }
        public var stepAccessibilityLabel: String {
            stepIndex.map { "step \($0 + 1) of \(task.steps.count), \(stepSummary)" } ?? stepSummary
        }
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
        let rows = tasks.sorted { $0.layer < $1.layer }.map { task in
            let elapsed = task.runStartedAt.map { "\(Int(max(0, now.timeIntervalSince($0)) / 60)) min" }
            let run = [task.runRole, task.runCLI, elapsed].compactMap { $0 }.joined(separator: " · ")
            return Row(task: task, runLine: head.state == .stopped ? nil : run.nonEmpty)
        }
        let groups = Dictionary(grouping: rows, by: \.task.stage)
        let stages = ["owner_gate", "owner_answer", "blocked_or_failed", "ci", "working", "waiting_on_dep", "queued"]
        let labels = ["waiting on you", "waiting on you", "blocked or failed", "waiting on CI", "working", "waiting on others", "queued"]
        let unknown = groups.keys.filter { $0 != "done" && !stages.contains($0) }.sorted()
        activeGroups = (stages + unknown).compactMap { stage in
            guard let rows = groups[stage] else { return nil }
            let label = stages.firstIndex(of: stage).map { labels[$0] } ?? stage
            return (stage, label, ["owner_gate", "owner_answer", "blocked_or_failed"].contains(stage), rows)
        }
        doneTasks = groups["done"] ?? []
    }

    public var head: FactoryProjectHead { summary.head }
    public var stateWord: String { summary.stateWord }
    public var countsLine: String { summary.countsLine }
    public var asksHeading: String { "Asks for you · \(openAsks.count) open" }
    public func sayBox(text: String) -> FactorySayBox {
        FactorySayBox(projectName: head.name, state: head.state, hasCoordinator: showsCoordinatorButton, text: text)
    }
    public var doneFoldLabel: String { "done \(doneTasks.count)" }
    public var alertHeading: String? { summary.alertLine }
    public var alertFix: String? { summary.alertFixLine }
    public var showsCoordinatorButton: Bool { head.state == .running && head.coordinatorPane?.nonEmpty != nil }
    public var coordinatorAccessibilityLabel: String { "Open the \(head.name) coordinator's window" }
    public var canStop: Bool { head.state == .running || (head.state == .finished && live) }
    public var canResume: Bool { summary.resumableFolder != nil }
    public var stopAccessibilityLabel: String { "Stop this project" }
    public var resumeAccessibilityLabel: String { "Resume this project" }
    public var barNote: String? {
        switch head.state {
        case .stopped: return "Stopped. No agent is running. Resume goes on with the same goal."
        case .finished: return live ? "Finished. An agent is still running." : "Finished."
        default: return nil
        }
    }
    public var stopConfirm: (title: String, message: String, keepLabel: String, confirmLabel: String) {
        ("Stop this project?", head.state == .finished ? "Its last running agent ends now. The project stays finished."
            : "Its running agents end now. Resume goes on with the same goal.", "Keep it running", "Stop the project")
    }

    public var isEmptyTasks: Bool { activeGroups.isEmpty && doneTasks.isEmpty }
    public var emptyTasksHeading: String { "no tasks yet" }
    public var emptyTasksDetail: String { "The coordinator has not planned this project yet." }
    public static func loadingMessage(projectName: String, computerName: String) -> String { "Loading \(projectName) from \(computerName)…" }
    public static func loadFailure(projectName: String, computerName: String, error: Error) -> (heading: String, detail: String, retryLabel: String) {
        let failure = RequestFailure(error, linkUsable: true, name: computerName)
        return ("Can't load \(projectName). Trying again.", "\(failure.message) The rest of the app still works.", "Try again")
    }

    public static func isProjectGone(_ error: Error) -> Bool {
        if case .rejected(status: 404, message: _) = error as? DaemonError { return true }
        return false
    }
    public static func sayErrorMessage(_ error: Error, projectName: String, computerName: String) -> String {
        if isProjectGone(error) { return "This project is gone." }
        switch error as? DaemonError {
        case .rejected(status: 403, message: "proof_required"):
            return "Not sent: \(computerName) no longer knows this phone. Pair this phone again in Settings ▸ \(computerName), then send again."
        case .rejected(status: 409, message: _): return "\(projectName) is finished. Give it a new goal."
        default: return RequestFailure(error, linkUsable: true, name: computerName).message
        }
    }
    public static func stopErrorMessage(_ error: Error, computerName: String) -> String {
        if case .rejected(status: 403, message: "proof_required") = error as? DaemonError {
            return "Not stopped: \(computerName) no longer knows this phone. Pair this phone again in Settings ▸ \(computerName), then press Stop again."
        }
        return RequestFailure(error, linkUsable: true, name: computerName).message
    }
}

public struct FactorySayBox {
    public let projectName: String
    public let state: FactoryProjectState
    public let hasCoordinator: Bool
    public let text: String

    public var label: String { "Tell the \(projectName) factory" }
    public var placeholder: String { "An instruction, in your own words" }
    public var sendAccessibilityLabel: String { "Send to the \(projectName) factory" }
    public var questionHint: String { "A question? Ask by voice, or ask Sheepdog in Conversation." }
    public var isFinished: Bool { state == .finished }
    private var count: Int { text.unicodeScalars.count }
    public var isTooLong: Bool { count > 1_000 }
    public var canSend: Bool { !isFinished && !isTooLong && !text.trimmed.isEmpty }
    public var counterText: String? { count >= 900 ? "\(count.formatted()) / 1,000" : nil }
    public var counterAccessibilityLabel: String { "\(count.formatted()) of 1,000 characters" }
    public var tooLongMessage: String? { isTooLong ? "Too long. Keep it under 1,000 characters." : nil }
    public var defaultNote: String {
        if isFinished { return "\(projectName) is finished. Give it a new goal." }
        if state == .stopped { return "\(projectName) is stopped. It gets this when you resume it." }
        if !hasCoordinator { return "The \(projectName) coordinator is not running. It gets this when it is back." }
        return "Delivered when the coordinator is next free. To answer an ask, use its card."
    }
    public var sentStatus: String {
        state == .running && hasCoordinator ? "Told the \(projectName) factory." : defaultNote
    }
}
