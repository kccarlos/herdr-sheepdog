import Combine
import Foundation
import SheepdogKit

/// A paired computer that is not the current one (app-redesign.md section
/// 4, option A): its agents and open questions, from its own `/api/state`,
/// refreshed every few seconds and at once on any `/events` event. No
/// voice, no conversation. Its questions are answered through its own
/// daemon, by tap.
///
/// Every property is assigned only when its value changes: the app's
/// model forwards this object's changes, and a poll every 5 s that found
/// nothing new must not re-render the app (`SessionViewModel+Computers`).
@MainActor
final class ComputerSession: ObservableObject {
    let id: String
    @Published private(set) var name: String
    @Published private(set) var reachability: Reachability = .connecting
    @Published private(set) var agents: [Agent] = []
    @Published private(set) var questions: [PendingAction] = []
    /// Each question's tap state (by the daemon's own question id).
    @Published private(set) var phases: [String: ApprovalFlow.Phase] = [:]

    private let client: DaemonClient
    private var lastSeen: Date?
    private var pollTask: Task<Void, Never>?
    private var eventTask: Task<Void, Never>?
    private var refreshing = false

    /// How often the state is fetched without an event.
    static let pollInterval: UInt64 = 5_000_000_000

    init(id: String, name: String, endpoint: DaemonEndpoint) {
        self.id = id
        self.name = name
        client = DaemonClient(endpoint: endpoint)
    }

    func start() {
        guard pollTask == nil else { return }
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                await self?.refresh()
                try? await Task.sleep(nanoseconds: Self.pollInterval)
            }
        }
        eventTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let client = self?.client else { return }
                do {
                    for try await output in client.events() {
                        if Task.isCancelled { return }
                        if case .event = output { await self?.refresh() }
                    }
                } catch {
                    // The poll keeps the state; the stream comes back on its own.
                }
                try? await Task.sleep(nanoseconds: 3_000_000_000)
            }
        }
    }

    func stop() {
        pollTask?.cancel()
        eventTask?.cancel()
        pollTask = nil
        eventTask = nil
    }

    func rename(_ name: String) {
        if self.name != name { self.name = name }
    }

    /// Retry on an offline row: fetch now.
    func retry() {
        if case .offline = reachability { set(\.reachability, .connecting) }
        Task { await refresh() }
    }

    func refresh() async {
        guard !refreshing else { return }
        refreshing = true
        defer { refreshing = false }
        do {
            let state = try await client.state()
            lastSeen = Date()
            set(\.reachability, .online)
            set(\.agents, state.agents)
            set(\.questions, state.questions ?? state.pendingAction.map { [$0] } ?? [])
            // Forget the tap state of questions that closed.
            let ids = Set(questions.map(\.id))
            set(\.phases, phases.filter { ids.contains($0.key) || $0.value.isSettledPhase })
        } catch {
            set(\.reachability, .offline(since: lastSeen))
        }
    }

    /// Assigns only a different value: an equal one would still announce
    /// a change.
    private func set<Value: Equatable>(_ key: ReferenceWritableKeyPath<ComputerSession, Value>, _ value: Value) {
        if self[keyPath: key] != value { self[keyPath: key] = value }
    }

    var snapshot: ComputerSnapshot {
        ComputerSnapshot(id: id, name: name, isCurrent: false, reachability: reachability, agents: agents, questions: questions)
    }

    /// The questions as the floating stack carries them, labelled with
    /// this computer and the agent's space.
    var remoteQuestions: [RemoteQuestion] {
        questions.filter(\.isAnswerPrompt).map { q in
            let agent = agents.first { $0.paneID == q.paneID }
            let space = agent?.workspaceLabel?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            return RemoteQuestion(computerID: id, computerName: name, place: space.isEmpty ? (q.agent ?? agent?.alias ?? "") : space, pending: q)
        }
    }

    func phase(_ questionID: String) -> ApprovalFlow.Phase {
        phases[questionID] ?? .open(error: nil)
    }

    /// Answers one of this computer's questions by tap, through its own
    /// `POST /api/pending/answer`.
    func answer(question id: String, option index: Int) {
        guard let question = questions.first(where: { $0.id == id }),
              let option = question.options?.first(where: { $0.index == index }) else { return }
        if case .sending = phases[id] { return }
        phases[id] = .sending(option)
        Haptics.tap()
        Task { [weak self] in
            guard let self else { return }
            do {
                let spoken = try await self.client.answerPending(id: id, option: index)
                self.phases[id] = .answered(outcome: "Answered: \(option.label)", spoken: spoken.isEmpty ? nil : spoken)
                Haptics.answered()
            } catch DaemonError.pendingGone {
                self.phases[id] = .gone(message: ApprovalFlow.goneMessage)
                Haptics.answerFailed()
            } catch let error as DaemonError {
                self.phases[id] = .open(error: error.message)
                Haptics.answerFailed()
            } catch {
                self.phases[id] = .open(error: error.localizedDescription)
            }
            await self.refresh()
        }
    }
}

private extension ApprovalFlow.Phase {
    var isSettledPhase: Bool {
        switch self {
        case .answered, .gone: return true
        case .open, .sending: return false
        }
    }
}
