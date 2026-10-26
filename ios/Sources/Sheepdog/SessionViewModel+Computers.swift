import Foundation
import SheepdogKit

/// Several computers (docs/dev/design/app-redesign.md sections 3 and 4,
/// option A, and docs/dev/design/ios-state-architecture.md 3.1): every
/// paired computer is a `ComputerModel` in `fleet`, the current one
/// included. The session attaches to the current one (its conversation,
/// its `/events`, the voice); each other one shows its agents and
/// questions and answers them by tap. Nothing from another computer
/// reaches the current daemon or the voice.
extension SessionViewModel {
    /// Keeps one model per paired computer, in the store's order, plus one
    /// for a bare address or the demo (a computer without a record). Called
    /// on launch and whenever the list, the current computer or its address
    /// changes. A computer whose address or pairing changed keeps its model
    /// and starts a new epoch, so nothing asked before lands; a removed one
    /// stops.
    func syncFleet() {
        var next: [ComputerModel] = []
        let store = settings.computers
        if demo == nil {
            for computer in store.computers {
                guard let endpoint = store.endpoint(for: computer.daemonID) else { continue }
                next.append(fleetModel(id: computer.daemonID, name: computer.displayName, endpoint: endpoint,
                                       certExpires: computer.certExpires, fingerprint: Self.fingerprint(computer, endpoint: endpoint)))
            }
        }
        if let endpoint = settings.endpoint, !next.contains(where: { $0.id == currentComputerID }) {
            next.insert(fleetModel(id: currentComputerID, name: currentComputerName, endpoint: endpoint,
                                   certExpires: endpoint.credentials?.computer?.certExpires,
                                   fingerprint: "\(endpoint.display)|\(endpoint.secure)"), at: 0)
        }
        for gone in fleet.models where !next.contains(where: { $0 === gone }) {
            fingerprints[gone.id] = nil
            endpoints[gone.id] = nil
        }
        fleet.set(next)
        if started { updateDemands() }
    }

    /// The model for one computer: the existing one (renamed, renewed when
    /// its address or pairing changed) or a new one, started once the app
    /// has started.
    private func fleetModel(id: String, name: String, endpoint: DaemonEndpoint, certExpires: Date?, fingerprint: String) -> ComputerModel {
        endpoints[id] = endpoint
        defer { fingerprints[id] = fingerprint }
        if let existing = fleet.model(id) {
            existing.rename(name)
            if fingerprints[id] != fingerprint {
                existing.renew(addresses: [endpoint.display], certExpires: certExpires)
            }
            return existing
        }
        let made = makeComputerModel(id: id, name: name, endpoint: endpoint, certExpires: certExpires)
        if started { made.start() }
        return made
    }

    /// What a computer's runtime depends on: its address and its pairing
    /// (not the pin: a promoted pin is a normal event, `certificateChanged`).
    private static func fingerprint(_ computer: PairedComputer, endpoint: DaemonEndpoint) -> String {
        "\(endpoint.display)|\(endpoint.secure)|\(computer.deviceID)|\(computer.pairedAt.timeIntervalSince1970)"
    }

    /// One computer's runtime: its connection (one client per address,
    /// made from the computer's endpoint when first used) and its model.
    /// The demo's clients answer from `DemoDaemon` inside the app and never
    /// open a network connection.
    private func makeComputerModel(id: String, name: String, endpoint: DaemonEndpoint, certExpires: Date?) -> ComputerModel {
        let endpoints = self.endpoints
        let demoSession = demo == nil ? nil : DemoDaemon.session()
        let connection = ComputerConnection(computerID: id, address: endpoint.display) { address in
            guard let endpoint = endpoints[id] else { return nil }
            let target = address == endpoint.display ? endpoint : DaemonEndpoint(text: address, secure: endpoint.secure, credentials: endpoint.credentials)
            guard let target else { return nil }
            return demoSession.map { DaemonClient(endpoint: target, session: $0) } ?? DaemonClient(endpoint: target)
        }
        let isCurrent = id == currentComputerID
        let model = ComputerModel(id: id, name: name, addresses: [endpoint.display], certExpires: certExpires,
                                  connection: connection, scheduler: LinkClock(), demand: isCurrent ? currentDemand : otherDemand,
                                  path: fleet.path,
                                  winners: demo == nil ? LinkWinnerDefaults(defaults: settings.defaults) : nil)
        model.onAnswerFeedback = { feedback in
            switch feedback {
            case .tapped: Haptics.tap()
            case .answered: Haptics.answered()
            case .failed: Haptics.answerFailed()
            }
        }
        model.onRenewTicket = { endpoints[id]?.credentials?.dropTicket() }
        return model
    }

    /// Who needs another computer's link: the app in front (it streams
    /// `/events` there and stops in the background, decision 2); it backs
    /// off further while the voice runs on the current computer.
    var otherDemand: LinkDemand {
        LinkDemand(isCurrent: false, phase: appPhase, voiceOn: false, keepListening: settings.keepListeningWhenLocked,
                   voiceOnAnotherComputer: wantsConnection)
    }


    /// A computer's certificate changed under its credentials (a promoted
    /// pin, a newly learned expiry): its link hears it, so the rotation
    /// window is current without a relaunch.
    func certificateChanged(daemonID: String, certExpires: Date?) {
        if daemonID == currentComputerID {
            currentCertificateChanged(certExpires: certExpires)
        } else {
            session(daemonID)?.certificateChanged(certExpires: certExpires)
        }
    }

    /// Another computer's model (nil for the current one).
    func session(_ id: String) -> ComputerModel? { others.first { $0.id == id } }

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
        // A computer that is backing off is not switched to, even while it
        // still reads "Connecting…".
        if let other = session(id), !other.canTalkTo {
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
            if !next.canTalkTo { continue }
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

/// Each computer's endpoint, read by its connection off the main actor
/// when it makes a client, and written when the store changes.
final class EndpointBook: @unchecked Sendable {
    private let lock = NSLock()
    private var endpoints: [String: DaemonEndpoint] = [:]

    subscript(id: String) -> DaemonEndpoint? {
        get { lock.withLock { endpoints[id] } }
        set { lock.withLock { endpoints[id] = newValue } }
    }
}
