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
            return Row(task: task, runLine: head.state == .stopped ? nil : run.nonEmpty)
        }, by: \.task.stage)
        let stages = [("owner_gate", "needs your approval", true), ("owner_answer", "waiting on you", true),
                      ("blocked_or_failed", "blocked", true), ("ci", "waiting on CI", false), ("working", "working", false),
                      ("waiting_on_dep", "waiting on others", false), ("queued", "queued", false)]
        let unknown = groups.keys.filter { key in key != "done" && !stages.contains { $0.0 == key } }.sorted()
        activeGroups = (stages + unknown.map { ($0, $0.replacingOccurrences(of: "_", with: " "), false) }).compactMap { stage, label, alert in
            groups[stage].map { (stage, label, alert, $0) }
        }
        doneTasks = groups["done"] ?? []
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
    public var stopAccessibilityLabel: String { "Stop this project" }
    public var resumeAccessibilityLabel: String { "Resume this project" }
    public var barNote: String? {
        if head.state == .stopped { return "Stopped. No agent is running. Resume goes on with the same goal." }
        if head.state == .finished { return live ? "Finished. An agent is still running." : "Finished. Give it a new goal." }
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
