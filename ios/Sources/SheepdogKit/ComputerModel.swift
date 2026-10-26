import Foundation
import Observation

/// One paired computer as the views see it (docs/dev/design/ios-state-architecture.md
/// 3.1): its link, agents and open questions with their tap phases. Its
/// I/O is a `ComputerConnection` (an actor); the link's machine runs here,
/// in a `LinkRuntime`, on the main actor.
///
/// Every reply is tagged with the epoch it was asked in and applied only
/// while that epoch is still this model's: new credentials or a new address
/// set start a new epoch (`renew`), and `stop` ends the last one. Switching
/// computers does not: an answer already sent finishes on its own computer
/// (decision 6).
///
/// Every observed property is assigned only when its value changes, so a
/// refresh that found nothing new re-renders nothing.
@MainActor
@Observable
public final class ComputerModel {
    public let id: String
    public private(set) var name: String
    /// The computer as the several-computers rows show it, read from the
    /// link.
    public private(set) var reachability: Reachability = .connecting
    /// Talk to may switch here now: the link is online or still on its
    /// first attempt. A computer that is backing off, offline, not
    /// verified or refused is not switched to.
    public private(set) var canTalkTo = true
    public private(set) var agents: [Agent] = []
    public private(set) var questions: [PendingAction] = []
    /// Each question's tap state (by the daemon's own question id).
    public private(set) var phases: [String: ApprovalFlow.Phase] = [:]

    /// The link as a value. Not observed: `lastSeen` moves on every
    /// request. `onLinkChange` hears every change.
    @ObservationIgnored public private(set) var link: ComputerLink
    /// The epoch replies must carry to be applied.
    @ObservationIgnored public private(set) var epoch = 0
    @ObservationIgnored public let connection: ComputerConnection

    /// The link changed (state, last seen…).
    @ObservationIgnored public var onLinkChange: ((ComputerLink) -> Void)?
    /// A tap's feedback, for haptics.
    @ObservationIgnored public var onAnswerFeedback: ((AnswerFeedback) -> Void)?
    /// A verified computer wants a fresh ticket.
    @ObservationIgnored public var onRenewTicket: (() -> Void)?

    // MARK: The current computer
    //
    // The session attaches to the current computer's model through these
    // (docs/dev/design/ios-state-architecture.md 3.1); another computer
    // leaves them nil.

    /// This computer's conversation, kept with it: switching away and back
    /// shows it again without reloading, and nothing from another
    /// computer lands in it.
    @ObservationIgnored public var conversation = ComputerConversation()
    /// The last `/api/state` this computer answered, for an instant switch.
    @ObservationIgnored public private(set) var lastState: DaemonState?
    /// What `/api/hello` said, and the features its last `bot-ready`
    /// listed (`term`, agent actions), learned per computer.
    @ObservationIgnored public var hello: Hello?
    @ObservationIgnored public var features: [String]?
    /// Every `/api/state` reply of the current epoch, after the model's own
    /// copy is updated.
    @ObservationIgnored public var onState: ((DaemonState) -> Void)?
    /// Takes the `/events` stream's output instead of the model's own
    /// refresh on each event (the current computer folds it into the
    /// conversation).
    @ObservationIgnored public var onStreamOutput: ((SSEParser.Output) -> Void)?
    /// The stream (re)opens: what it missed is in the history.
    @ObservationIgnored public var onStreamOpening: (() -> Void)?
    /// The stream's own life, for Health ▸ Live updates.
    @ObservationIgnored public var onLiveUpdates: ((SocketLife) -> Void)?
    /// Where `/events` resumes (the last event id the conversation saw).
    @ObservationIgnored public var eventsSince: (() -> UInt64)?
    /// The stream's own life.
    @ObservationIgnored public private(set) var liveUpdates: SocketLife = .idle

    public enum AnswerFeedback: Equatable, Sendable {
        case tapped
        case answered
        case failed
    }

    @ObservationIgnored private var runtime: LinkRuntime!
    @ObservationIgnored private let scheduler: LinkScheduler
    @ObservationIgnored private var demand: LinkDemand
    @ObservationIgnored private var answersInFlight = 0
    @ObservationIgnored private var refreshing = false
    @ObservationIgnored private var refreshAgain = false
    @ObservationIgnored private var eventsWanted = false
    @ObservationIgnored private var stopped = false

    /// - Parameters:
    ///   - addresses: the addresses the link races (the one in use first).
    ///   - certExpires: the served certificate's expiry, for the rotation
    ///     window.
    ///   - demand: who needs the link now (`setDemand` changes it).
    ///   - winners: where the race winners are kept; nil keeps none.
    public init(id: String, name: String, addresses: [String], certExpires: Date?, connection: ComputerConnection,
                scheduler: LinkScheduler, demand: LinkDemand, path: NetworkPath = NetworkPath(),
                winners: LinkWinnerStore? = nil, random: LinkRandom = LinkRandom()) {
        self.id = id
        self.name = name
        self.connection = connection
        self.scheduler = scheduler
        self.demand = demand
        let link = ComputerLink(addresses: addresses, certExpires: certExpires, demand: demand, path: path)
        self.link = link
        let transport = ConnectionProbeTransport(connection: connection)
        let runtime = LinkRuntime(computerID: id, link: link, transport: transport, scheduler: scheduler, winners: winners, random: random)
        transport.epoch = { [weak self] in self?.epoch ?? -1 }
        self.runtime = runtime
        self.link = runtime.link
        runtime.onChange = { [weak self] link in self?.linkChanged(link) }
        runtime.onState = { [weak self] state in self?.apply(state) }
        runtime.onChannels = { [weak self] channels in self?.channelsChanged(channels) }
        runtime.onReopen = { [weak self] channel in
            guard channel == .events else { return }
            self?.openEvents()
        }
        runtime.onUseAddress = { [weak self] address in
            guard let self else { return }
            let epoch = self.epoch
            Task { await self.connection.use(address: address, epoch: epoch) }
        }
        runtime.onRenewTicket = { [weak self] in self?.onRenewTicket?() }
        linkChanged(runtime.link)
    }

    /// Starts the link with the demand it was made with.
    public func start() {
        guard !stopped else { return }
        runtime.send(.demandChanged(effectiveDemand))
    }

    /// Ends this computer's runtime: probes, timers and the stream stop,
    /// and nothing that still arrives is applied.
    public func stop() {
        guard !stopped else { return }
        stopped = true
        epoch += 1
        runtime.stop()
        let connection = connection
        Task { await connection.stop() }
    }

    // MARK: - Inputs

    /// Who needs the link: the app's phase, the voice, Keep listening. An
    /// answer in flight adds its own demand.
    public func setDemand(_ demand: LinkDemand) {
        guard demand != self.demand else { return }
        self.demand = demand
        runtime.send(.demandChanged(effectiveDemand))
    }

    /// The app came to the foreground or left it.
    public func setAppPhase(_ phase: AppPhase) {
        var next = demand
        next.phase = phase
        setDemand(next)
        runtime.send(.appPhase(phase))
    }

    /// What the network path is (Low Data Mode keeps `/events` closed for a
    /// computer that is not the current one; it refreshes every 30 s).
    public func setPath(_ path: NetworkPath) {
        runtime.send(.pathChanged(path))
    }

    /// Retry, Try now: the link tries at once.
    public func retry() {
        runtime.send(.retryTapped)
    }

    /// Anything else the link hears about this computer: the voice's `/ws`
    /// opening and dropping, a handshake mismatch.
    public func send(_ event: LinkEvent) {
        guard !stopped else { return }
        runtime.send(event)
    }

    /// Thrown by `request` when its reply belongs to an epoch (or a
    /// runtime) that is gone.
    public struct Stale: LocalizedError, Equatable {
        public init() {}
        public var errorDescription: String? { "The computer was paired again before the answer came." }
    }

    /// One request on the address in use, for the current computer's REST
    /// calls. Its outcome tells the link. A reply from an older epoch, or
    /// after `stop`, throws `Stale` and is never applied.
    public func request<Client, T>(as type: Client.Type, _ body: @escaping @Sendable (Client) async throws -> T) async throws -> T {
        guard !stopped else { throw Stale() }
        let epoch = epoch
        let reply = await connection.request(epoch: epoch) { client -> T in
            guard let typed = client as? Client else { throw DaemonError.malformed("this computer's client cannot do that") }
            return try await body(typed)
        }
        guard reply.epoch == self.epoch, !stopped else { throw Stale() }
        report(reply)
        return try reply.value.get()
    }

    public func rename(_ name: String) {
        set(\.name, name)
    }

    /// The computer's next pin was promoted, or a sign-in learned the
    /// certificate's expiry: a normal event, not an error.
    public func certificateChanged(certExpires: Date?) {
        runtime.send(.credentialsChanged(.pinPromoted(certExpires: certExpires)))
    }

    /// New credentials or a new address set: a new epoch, so nothing asked
    /// before lands, and the link starts over.
    public func renew(addresses: [String], certExpires: Date?) {
        guard !stopped else { return }
        epoch += 1
        let epoch = epoch
        let connection = connection
        let address = addresses.first
        Task { await connection.renew(epoch: epoch, address: address) }
        eventsWanted = false
        setLiveUpdates(.idle)
        lastState = nil
        hello = nil
        features = nil
        if addresses != link.addresses { runtime.send(.addressesChanged(addresses)) }
        runtime.send(.credentialsChanged(.newPairing(certExpires: certExpires)))
        set(\.agents, [])
        set(\.questions, [])
        set(\.phases, [:])
    }

    var effectiveDemand: LinkDemand {
        var demand = demand
        demand.actionsInFlight = answersInFlight > 0
        return demand
    }

    // MARK: - The link

    private func linkChanged(_ next: ComputerLink) {
        link = next
        set(\.reachability, Reachability(next))
        set(\.canTalkTo, Self.canTalkTo(next))
        onLinkChange?(next)
    }

    /// Talk to may switch to a computer in this link state.
    public static func canTalkTo(_ link: ComputerLink) -> Bool {
        switch link.state {
        case .online, .degraded, .idle:
            return true
        case let .connecting(_, outage):
            return outage == nil
        case .waiting, .offline, .noNetwork, .untrustedAnswer, .refused, .incompatible:
            return false
        }
    }

    private func channelsChanged(_ channels: Set<LinkChannel>) {
        if channels.contains(.events) {
            if !eventsWanted { openEvents() }
        } else if eventsWanted {
            eventsWanted = false
            setLiveUpdates(.idle)
            let connection = connection
            Task { await connection.closeEvents() }
        }
    }

    private func openEvents() {
        guard !stopped else { return }
        let reopening = liveUpdates == .reconnecting
        eventsWanted = true
        let epoch = epoch
        let connection = connection
        let since = eventsSince?() ?? 0
        setLiveUpdates(reopening ? .reconnecting : .connecting)
        onStreamOpening?()
        Task { [weak self] in
            await connection.openEvents(epoch: epoch, since: since) { [weak self] update in
                self?.streamUpdate(update)
            }
        }
    }

    private func streamUpdate(_ update: ComputerReply<ComputerStreamUpdate>) {
        guard !stopped, update.epoch == epoch, eventsWanted else { return }
        switch update.value {
        case .opened:
            guard liveUpdates != .open else { return }
            setLiveUpdates(.open)
            runtime.send(.channelOpened(.events))
        case let .output(output):
            if let onStreamOutput {
                onStreamOutput(output)
            } else if case .event = output {
                Task { await refresh() }
            }
        case let .lost(error):
            eventsWanted = false
            setLiveUpdates(.reconnecting)
            runtime.send(.channelLost(.events, error))
        }
    }

    private func setLiveUpdates(_ life: SocketLife) {
        guard liveUpdates != life else { return }
        liveUpdates = life
        onLiveUpdates?(life)
    }

    /// A request's outcome tells the link.
    private func report<T>(_ reply: ComputerReply<Result<T, Error>>) {
        switch reply.value {
        case .success: runtime.send(.requestSucceeded(rtt: reply.rtt))
        case let .failure(error): runtime.send(.requestFailed(LinkError(error)))
        }
    }

    // MARK: - State

    /// Fetches `/api/state` now. Calls while one is in flight coalesce into
    /// one more after it.
    public func refresh() async {
        guard !stopped else { return }
        if refreshing {
            refreshAgain = true
            return
        }
        refreshing = true
        defer { refreshing = false }
        repeat {
            refreshAgain = false
            let epoch = epoch
            let reply = await connection.request(epoch: epoch) { try await $0.state() }
            guard reply.epoch == self.epoch, !stopped else { return }
            report(reply)
            if case let .success(state) = reply.value { apply(state) }
        } while refreshAgain && !stopped
    }

    private func apply(_ state: DaemonState) {
        lastState = state
        set(\.agents, state.agents)
        set(\.questions, state.questions ?? state.pendingAction.map { [$0] } ?? [])
        // Forget the tap state of questions that closed.
        let ids = Set(questions.map(\.id))
        set(\.phases, phases.filter { ids.contains($0.key) || $0.value.isSettledPhase })
        onState?(state)
    }

    // MARK: - Answers

    public func phase(_ questionID: String) -> ApprovalFlow.Phase {
        phases[questionID] ?? .open(error: nil)
    }

    /// Answers one of this computer's questions by tap, through its own
    /// `POST /api/pending/answer`. A second tap while one is in flight is
    /// ignored. The answer finishes on this computer whatever the app
    /// switches to meanwhile (decision 6), and a reply from an older epoch
    /// is dropped.
    public func answer(question id: String, option index: Int) {
        guard let question = questions.first(where: { $0.id == id }) else { return }
        answer(question, option: index)
    }

    /// The same, for a question the caller holds (the current computer's
    /// list may carry one the last state did not).
    public func answer(_ question: PendingAction, option index: Int) {
        let id = question.id
        guard !stopped, let option = question.options?.first(where: { $0.index == index }) else { return }
        if case .sending = phases[id] { return }
        phases[id] = .sending(option)
        onAnswerFeedback?(.tapped)
        answersInFlight += 1
        if answersInFlight == 1 { runtime.send(.demandChanged(effectiveDemand)) }
        let epoch = epoch
        Task { [weak self] in
            guard let self else { return }
            let reply = await self.connection.request(epoch: epoch) { try await $0.answerPending(id: id, option: index) }
            self.answersInFlight -= 1
            if self.answersInFlight == 0, !self.stopped { self.runtime.send(.demandChanged(self.effectiveDemand)) }
            guard reply.epoch == self.epoch, !self.stopped else { return }
            self.report(reply)
            switch reply.value {
            case let .success(spoken):
                self.phases[id] = .answered(outcome: "Answered: \(option.label)", spoken: spoken.isEmpty ? nil : spoken)
                self.onAnswerFeedback?(.answered)
            case .failure(DaemonError.pendingGone):
                self.phases[id] = .gone(message: ApprovalFlow.goneMessage)
                self.onAnswerFeedback?(.failed)
            case let .failure(error as DaemonError):
                self.phases[id] = .open(error: error.message)
                self.onAnswerFeedback?(.failed)
            case let .failure(error):
                self.phases[id] = .open(error: error.localizedDescription)
                self.onAnswerFeedback?(.failed)
            }
            await self.refresh()
        }
    }

    /// Answers in flight, for the tests.
    public var answersPending: Int { answersInFlight }

    // MARK: - What the several-computers UI reads

    public var snapshot: ComputerSnapshot {
        ComputerSnapshot(id: id, name: name, isCurrent: false, reachability: reachability, agents: agents, questions: questions, canTalkTo: canTalkTo)
    }

    /// The questions as the floating stack carries them, labelled with this
    /// computer and the agent's space.
    public var remoteQuestions: [RemoteQuestion] {
        questions.filter(\.isAnswerPrompt).map { q in
            let agent = agents.first { $0.paneID == q.paneID }
            let space = agent?.workspaceLabel?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            return RemoteQuestion(computerID: id, computerName: name, place: space.isEmpty ? (q.agent ?? agent?.alias ?? "") : space, pending: q)
        }
    }

    /// Assigns only a different value: an equal one would still notify
    /// every observer.
    private func set<Value: Equatable>(_ key: ReferenceWritableKeyPath<ComputerModel, Value>, _ value: Value) {
        if self[keyPath: key] != value { self[keyPath: key] = value }
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

/// One computer's conversation, kept on its model: the server's history,
/// the live bubbles, the orchestrator's activity and where `/events`
/// resumes.
public struct ComputerConversation: Sendable {
    public var history = ConversationHistory()
    public var reducer = ConversationReducer()
    public var activity = BrainActivity()
    public var lastSeq: UInt64 = 0
    /// The cache was read (or there was none): a switch back does not read
    /// it again over what is newer.
    public var loaded = false

    public init() {}
}

/// The link's probe through the computer's connection: one immutable
/// client per address, and a reply from an older epoch never reaches the
/// link.
@MainActor
final class ConnectionProbeTransport: LinkTransport {
    private let connection: ComputerConnection
    var epoch: () -> Int = { 0 }

    init(connection: ComputerConnection) {
        self.connection = connection
    }

    func probe(address: String, done: @escaping @MainActor (LinkProbeResult) -> Void) -> LinkHandle {
        let epoch = epoch()
        let connection = connection
        let task = Task { @MainActor [weak self] in
            let reply = await connection.probe(address: address, epoch: epoch)
            guard !Task.isCancelled, let self, reply.epoch == self.epoch() else { return }
            done(reply.value)
        }
        return LinkHandle { task.cancel() }
    }
}
