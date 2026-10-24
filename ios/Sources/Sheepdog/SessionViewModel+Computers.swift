import Combine
import Foundation
import SheepdogKit

/// Several computers (docs/dev/design/app-redesign.md sections 3 and 4,
/// option A): the session is the current computer's; each other paired
/// computer is a `ComputerSession` that shows its agents and questions and
/// answers them by tap. Nothing from another computer reaches the current
/// daemon or the voice.
extension SessionViewModel {
    /// Keeps one `ComputerSession` per other computer, started, in the
    /// store's order. Called on start and whenever the list or the current
    /// computer changes.
    func rebuildOthers() {
        guard demo == nil else { return }
        let store = settings.computers
        let current = store.currentID
        var kept: [ComputerSession] = []
        for computer in store.computers where computer.daemonID != current {
            guard let endpoint = store.endpoint(for: computer.daemonID) else { continue }
            if let existing = others.first(where: { $0.id == computer.daemonID }) {
                existing.rename(computer.displayName)
                kept.append(existing)
            } else {
                let made = ComputerSession(id: computer.daemonID, name: computer.displayName, endpoint: endpoint)
                // The views read the session through this model. The session
                // announces only real changes (it assigns nothing equal),
                // so a quiet poll re-renders nothing here.
                otherSinks[made.id] = made.objectWillChange.sink { [weak self] _ in self?.objectWillChange.send() }
                made.start()
                kept.append(made)
            }
        }
        for gone in others where !kept.contains(where: { $0 === gone }) {
            gone.stop()
            otherSinks[gone.id] = nil
        }
        if !kept.elementsEqual(others, by: ===) { others = kept }
    }

    func session(_ id: String) -> ComputerSession? { others.first { $0.id == id } }

    /// The capsule's name: the voice's with one computer, the current
    /// computer's with several (the voice itself never names one).
    var capsuleName: String { showsComputerLabels ? currentComputerName : voiceName }

    /// Every paired computer, in the store's order, the current one as the
    /// session sees it.
    var computerSnapshots: [ComputerSnapshot] {
        let store = settings.computers
        return store.computers.map { computer in
            if computer.daemonID == store.currentID {
                return ComputerSnapshot(id: computer.daemonID, name: computer.displayName, isCurrent: true,
                                        reachability: Reachability(link),
                                        agents: agents, questions: questions)
            }
            return session(computer.daemonID)?.snapshot
                ?? ComputerSnapshot(id: computer.daemonID, name: computer.displayName, isCurrent: false, reachability: .connecting)
        }
    }

    /// The other computers' snapshots for their sections; none with one
    /// computer.
    var otherSnapshots: [ComputerSnapshot] {
        guard showsComputerLabels else { return [] }
        return computerSnapshots.filter { !$0.isCurrent }
    }

    /// Another computer's question on its agent's card, with a namespaced
    /// id so a tap routes to that computer.
    func remoteCard(computer: String, paneID: String) -> ApprovalFlow.Card? {
        guard let session = session(computer),
              let remote = session.remoteQuestions.first(where: { $0.pending.paneID == paneID }) else { return nil }
        return ApprovalFlow.Card(pending: remote.namespaced, phase: session.phase(remote.pending.id))
    }

    /// The other computers' open questions, oldest first; none with one
    /// computer.
    var remoteQuestions: [RemoteQuestion] {
        guard showsComputerLabels else { return [] }
        return QuestionOrder.others(others.flatMap(\.remoteQuestions))
    }

    /// The label of a floating card from another computer (by its
    /// namespaced id), nil for the current computer's.
    func remoteQuestion(_ cardID: String) -> RemoteQuestion? {
        guard RemoteQuestion.parse(cardID) != nil else { return nil }
        return remoteQuestions.first { $0.namespaced.id == cardID }
    }

    /// The Agents badge: waiting agents on every computer.
    var waitingEverywhere: Int {
        flock.waitingCount + (showsComputerLabels ? others.reduce(0) { $0 + $1.snapshot.waitingCount } : 0)
    }

    /// Blocked agents on other computers, for the agent strip, with the
    /// computer's name.
    var blockedElsewhere: [(agent: Agent, computer: String)] {
        guard showsComputerLabels else { return [] }
        return others.flatMap { session in
            session.agents.filter { $0.state == .blocked }.map { ($0, session.name) }
        }
    }

    /// Talk to: makes another computer the current one. A computer that
    /// does not answer stays where it is and says why.
    func talk(to id: String) {
        guard id != currentComputerID else { return }
        if let other = session(id), case .offline = other.reachability {
            banner = Banner(tone: .warning, message: "Can't reach \(other.name).",
                            hint: "Still talking to \(currentComputerName). Is \(other.name) awake and on the same network?", retryTitle: nil)
            other.retry()
            return
        }
        switchComputer(to: id)
    }

    /// ⌘K: the next computer that answers, in the list's order.
    func talkToNext() {
        let all = computerSnapshots
        guard all.count > 1, let index = all.firstIndex(where: \.isCurrent) else { return }
        for step in 1 ..< all.count {
            let next = all[(index + step) % all.count]
            if case .offline = next.reachability { continue }
            talk(to: next.id)
            return
        }
    }

    /// Settings ▸ Computers ▸ a computer ▸ Name.
    func renameComputer(_ id: String, to name: String?) {
        settings.computers.rename(id, to: name)
        computersChanged()
    }
}
