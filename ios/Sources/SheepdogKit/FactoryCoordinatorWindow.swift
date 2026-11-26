import Foundation

public struct FactoryCoordinatorWindow: Equatable {
    public struct OffState {
        public let heading: String
        public let body: String
        public var alertCause: String? = nil
        public var alertFix: String? = nil
    }

    public let projectName: String
    public let coordinatorPane: String?
    public let askStrip: String?
    private let state: FactoryProjectState
    private let alert: FactoryProjectAlert?

    public init(head: FactoryProjectHead, openAsks: [PendingAction] = []) {
        projectName = head.name
        coordinatorPane = head.state == .running ? head.coordinatorPane?.trimmed.nonEmpty : nil
        state = head.state
        alert = head.alert
        switch openAsks.count {
        case 0:
            askStrip = nil
        case 1:
            let subject = openAsks[0].factory?.task?.trimmed.nonEmpty ?? "An ask"
            askStrip = "\(subject) is waiting on your answer. Answer it on its card; typing here is not an answer."
        default:
            askStrip = "\(openAsks.count) asks are waiting on your answer. Answer them on their cards; typing here is not an answer."
        }
    }

    public var isLive: Bool { coordinatorPane != nil }
    public var title: String { "\(projectName) · Coordinator" }
    public var headerAccessibilityLabel: String { "\(projectName) coordinator, \(isLive ? "running" : "not running")" }
    public var promptPlaceholder: String { "Ask the \(projectName) coordinator…" }
    public var promptAccessibilityLabel: String { "Message to the \(projectName) coordinator" }
    public var backButtonLabel: String { "Back to \(projectName)" }

    public var offState: OffState? {
        guard !isLive else { return nil }
        switch state {
        case .stopped:
            return OffState(heading: "\(projectName) is stopped", body: "Resume \(projectName) on its page; it goes on with the same goal.")
        case .finished:
            return OffState(heading: "\(projectName) is finished", body: "To go on, reopen \(projectName) with a new goal on its page.")
        default:
            return notRunningState
        }
    }

    public var notRunningState: OffState {
        OffState(
            heading: "The \(projectName) coordinator is not running",
            body: "This window comes back when a coordinator runs again. To tell it something meanwhile, use the box on the \(projectName) page; it gets it when it is back.",
            alertCause: alert?.cause, alertFix: alert?.fix.map { "Fix: \($0)" }
        )
    }

    public static func inputErrorMessage(_ error: Error, projectName: String, computerName: String) -> String {
        if case let .rejected(409, message) = error as? DaemonError,
           message.contains("waiting at a prompt") {
            return "Not sent: the \(projectName) coordinator is waiting at its own prompt. Answer it with the keys, or on its card in Agents."
        }
        return "Not sent to the \(projectName) coordinator on \(computerName). \((error as? DaemonError)?.message ?? error.localizedDescription)"
    }
}
