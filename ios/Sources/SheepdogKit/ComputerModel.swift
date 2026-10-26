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
    /// The channels the session attached here (`attach`), for the tests.
    public var attachedChannels: Set<LinkChannel> { Set(clients.keys) }
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
    /// Every tap that changes something here runs through this
    /// (`ActionRunner`): dedup keys, the epoch at tap time, a timeout, a
    /// safe retry, the background task.
    @ObservationIgnored public private(set) var actions: ActionRunner!
    @ObservationIgnored private var refreshing = false
    @ObservationIgnored private var refreshAgain = false
    @ObservationIgnored private var eventsWanted = false
    @ObservationIgnored private var stopped = false
    /// The channels the session attached (the voice's `/ws`, the
    /// terminal's `/term`), each with the token its reports must carry.
    @ObservationIgnored private var clients: [LinkChannel: (client: LinkChannelClient, token: Int)] = [:]
    @ObservationIgnored private var nextClientToken = 0
    @ObservationIgnored private var lastAddress: String?

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
        runtime.onReopen = { [weak self] channel in self?.reopen(channel) }
        runtime.onUseAddress = { [weak self] address in
            guard let self else { return }
            let epoch = self.epoch
            let connection = self.connection
            let moved = self.lastAddress != nil && self.lastAddress != address
            self.lastAddress = address
            // The session's `/ws` and `/term` move first, so the reopen
            // below (and any open the channel set allows next) goes there.
            for entry in self.clients.values { entry.client.linkUses(address: address) }
            Task { [weak self] in
                await connection.use(address: address, epoch: epoch)
                // Open channels follow the link to its new address.
                guard moved, let self, epoch == self.epoch else { return }
                for channel in LinkChannel.allCases where self.link.channels.contains(channel) { self.reopen(channel) }
            }
        }
        runtime.onRenewTicket = { [weak self] in self?.onRenewTicket?() }
        actions = ActionRunner(model: self)
        actions.onActiveChange = { [weak self] _ in
            // An action in flight keeps the link up until it finishes.
            guard let self, !self.stopped else { return }
            self.runtime.send(.demandChanged(self.effectiveDemand))
        }
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

    /// The app came to the foreground or left it. Coming to the foreground
    /// kicks the link: the backoff starts over and it tries at once.
    public func setAppPhase(_ phase: AppPhase) {
        guard !stopped else { return }
        // Only `.appPhase`: a `demandChanged` with the new phase first would
        // leave the link nothing to compare, and the kick would be lost.
        demand.phase = phase
        runtime.send(.appPhase(phase))
    }

    /// What the network path is: no network stops the retries, a new
    /// interface re-checks at once, Low Data Mode keeps `/events` closed
    /// for a computer that is not the current one (it refreshes every
    /// 30 s). `ComputerFleet.setPath` calls it for every computer.
    public func setPath(_ path: NetworkPath) {
        guard !stopped else { return }
        runtime.send(.pathChanged(path))
    }

    // MARK: - Channels the session attaches

    /// Attaches the voice's `/ws` or the terminal's `/term` to this
    /// computer's link, which alone says when it may be open and when to
    /// open it again (`LinkChannelClient`). It hears the link's current
    /// answer at once. nil detaches: reports from the old client are
    /// dropped from then on.
    public func attach(_ channel: LinkChannel, _ client: LinkChannelClient?) {
        precondition(channel != .events, "the model owns /events")
        guard let client else {
            clients[channel] = nil
            return
        }
        if let existing = clients[channel], existing.client === client { return }
        nextClientToken += 1
        clients[channel] = (client, nextClientToken)
        if let address = runtime.link.addressInUse { client.linkUses(address: address) }
        client.linkAllows(link.channels.contains(channel), reporter: reporter(channel))
    }

    /// The reporter for an attached channel, tagged with this epoch.
    private func reporter(_ channel: LinkChannel) -> LinkChannelReporter {
        LinkChannelReporter(model: self, channel: channel, epoch: epoch, token: clients[channel]?.token ?? 0)
    }

    /// A report from an attached channel: applied only while that client
    /// is still attached and the epoch it was given is still this one.
    fileprivate func report(_ channel: LinkChannel, epoch: Int, token: Int, _ event: LinkEvent) {
        guard !stopped, epoch == self.epoch, let entry = clients[channel], entry.token == token else { return }
        runtime.send(event)
    }

    private func reopen(_ channel: LinkChannel) {
        if channel == .events {
            // Only a stream the link still allows.
            if link.channels.contains(.events) { openEvents() }
        } else if let entry = clients[channel] {
            entry.client.linkReopens(reporter: reporter(channel))
        }
    }

    /// Settings ▸ Addresses ▸ Prefer: the address goes to the front of the
    /// race, the remembered winners are forgotten, and the link re-races
    /// (without leaving online) unless it is already there. Not a new
    /// epoch: the pin, not the address, is the computer.
    public func prefer(_ address: String) {
        guard !stopped else { return }
        runtime.send(.preferAddress(address))
    }

    /// The computer's addresses changed (one added or removed in Settings),
    /// preferred first. Not a new epoch: the link keeps the address in use
    /// if it is still there, else races the new set.
    public func setAddresses(_ addresses: [String]) {
        guard !stopped, addresses != link.addresses else { return }
        runtime.send(.addressesChanged(addresses))
    }

    /// Where requests and `/events` go: the race's winner, else the
    /// preferred address (before the first race is won). Passed with each
    /// call, so the actor never uses an address the link has left.
    /// The runtime's link, not `link`: inside the link's effects (the
    /// channel set opening `/events`) `link` is not updated yet.
    private var targetAddress: String? { runtime.link.addressInUse ?? runtime.link.addresses.first }

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
        let target = targetAddress
        let reply = await connection.request(epoch: epoch, address: target) { client -> T in
            guard let typed = client as? Client else { throw DaemonError.malformed("this computer's client cannot do that") }
            return try await body(typed)
        }
        guard reply.epoch == self.epoch, !stopped else { throw Stale() }
        report(reply)
        return try reply.value.get()
    }

    /// One action's request, for `ActionRunner`: in the epoch it was tapped
    /// in, on the address in use, with its `Idempotency-Key` and timeout.
    /// No reply within `timeout` throws `DaemonError.timedOut` (the request
    /// is cancelled; the daemon may still finish it, which is what the key
    /// is for). The outcome of a reply tells the link.
    func perform<Client, T>(epoch: Int, timeout: TimeInterval, idempotencyKey: String?, as type: Client.Type,
                            _ body: @escaping @Sendable (Client) async throws -> T) async throws -> T {
        guard !stopped, epoch == self.epoch else { throw Stale() }
        let target = targetAddress
        let connection = connection
        let work = Task { () -> ComputerReply<Result<T, Error>> in
            await DaemonClient.$idempotencyKey.withValue(idempotencyKey) {
                await DaemonClient.$actionTimeout.withValue(timeout) {
                    await connection.request(epoch: epoch, address: target) { client -> T in
                        guard let typed = client as? Client else { throw DaemonError.malformed("this computer's client cannot do that") }
                        return try await body(typed)
                    }
                }
            }
        }
        let once = Once<ComputerReply<Result<T, Error>>?>()
        let scheduler = scheduler
        let reply = await withCheckedContinuation { continuation in
            once.continuation = continuation
            let timer = scheduler.schedule(at: scheduler.now.addingTimeInterval(timeout)) {
                work.cancel()
                once.resume(nil)
            }
            Task { @MainActor in
                let value = await work.value
                timer.cancel()
                once.resume(value)
            }
        }
        guard let reply else { throw DaemonError.timedOut(endpoint: target ?? name) }
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
        demand.actionsInFlight = (actions?.count ?? 0) > 0
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
        for (channel, entry) in clients {
            entry.client.linkAllows(channels.contains(channel), reporter: reporter(channel))
        }
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
        let target = targetAddress
        setLiveUpdates(reopening ? .reconnecting : .connecting)
        onStreamOpening?()
        Task { [weak self] in
            await connection.openEvents(epoch: epoch, address: target, since: since) { [weak self] update in
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
            let reply = await connection.request(epoch: epoch, address: targetAddress) { try await $0.state() }
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
    /// list may carry one the last state did not). It runs under the
    /// question's key (`ActionRunner.Key.answer`), so a tap on another
    /// surface for the same question joins it.
    public func answer(_ question: PendingAction, option index: Int) {
        let id = question.id
        guard !stopped, let option = question.options?.first(where: { $0.index == index }) else { return }
        let key = ActionRunner.Key.answer(self.id, id)
        if case .sending = phases[id] { return }
        if actions.isInFlight(key) { return }
        phases[id] = .sending(option)
        onAnswerFeedback?(.tapped)
        Task { [weak self] in
            guard let self else { return }
            do {
                let spoken = try await self.actions.run(key, .answer, as: ComputerRequests.self) { try await $0.answerPending(id: id, option: index) }
                self.phases[id] = .answered(outcome: "Answered: \(option.label)", spoken: spoken.isEmpty ? nil : spoken)
                self.onAnswerFeedback?(.answered)
            } catch is ComputerModel.Stale {
                return
            } catch DaemonError.pendingGone {
                self.phases[id] = .gone(message: ApprovalFlow.goneMessage)
                self.onAnswerFeedback?(.failed)
            } catch {
                self.phases[id] = .open(error: self.failure(error).message)
                self.onAnswerFeedback?(.failed)
            }
            await self.refresh()
        }
    }

    /// What a failed action says where it was tapped: one slow answer while
    /// this computer's link is up is not an unreachable computer.
    public func failure(_ error: Error) -> RequestFailure {
        RequestFailure(error, linkUsable: link.isUsable, name: name)
    }

    /// Answers in flight, for the tests.
    public var answersPending: Int { actions.count }

    fileprivate func attachedToken(_ channel: LinkChannel) -> Int? { clients[channel]?.token }

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

/// A channel the session attaches to a computer's link
/// (`ComputerModel.attach`): the voice's `/ws` socket, the terminal's
/// `/term`. It never reconnects by itself. The link says when it may be
/// open, and when a lost one may open again; it opens only while it is
/// also wanted (the voice is on, a window is open).
@MainActor
public protocol LinkChannelClient: AnyObject {
    /// The link allows the channel now, or no longer (offline, no network,
    /// the background). Not allowed: close, keep what to reopen, and do not
    /// report a loss. `reporter` is what to tell the link from now on.
    func linkAllows(_ allowed: Bool, reporter: LinkChannelReporter)
    /// A lost channel, confirmed reachable (or the interface changed): open
    /// it again if it is still wanted.
    func linkReopens(reporter: LinkChannelReporter)
    /// The link uses this address of the computer now (a race won there):
    /// open there from now on. It comes before the reopen that moves an
    /// open channel, and on `attach` when the link already has one.
    func linkUses(address: String)
}

/// What a channel tells its computer's link, tagged with the computer's
/// epoch and the attachment: a report from a socket of an older pairing,
/// or one the session has since detached (a switch), lands nowhere.
@MainActor
public struct LinkChannelReporter {
    private weak var model: ComputerModel?
    public let channel: LinkChannel
    public let epoch: Int
    let token: Int

    init(model: ComputerModel, channel: LinkChannel, epoch: Int, token: Int) {
        self.model = model
        self.channel = channel
        self.epoch = epoch
        self.token = token
    }

    /// The channel opened (the socket answered, `term-ready`).
    public func opened() {
        model?.report(channel, epoch: epoch, token: token, .channelOpened(channel))
    }

    /// The channel dropped or could not open, not because the app closed
    /// it: the link probes once and says when to open it again.
    public func lost(_ error: LinkError) {
        model?.report(channel, epoch: epoch, token: token, .channelLost(channel, error))
    }

    /// Whether this reporter still reaches its computer's link, for the
    /// tests.
    public var isCurrent: Bool {
        guard let model else { return false }
        return model.isAttached(channel, epoch: epoch, token: token)
    }
}

extension ComputerModel {
    fileprivate func isAttached(_ channel: LinkChannel, epoch: Int, token: Int) -> Bool {
        epoch == self.epoch && attachedToken(channel) == token
    }
}

/// Resumes a continuation once, whichever comes first (the reply or the
/// timeout).
@MainActor
private final class Once<Value> {
    var continuation: CheckedContinuation<Value, Never>?

    func resume(_ value: Value) {
        guard let continuation else { return }
        self.continuation = nil
        continuation.resume(returning: value)
    }
}
