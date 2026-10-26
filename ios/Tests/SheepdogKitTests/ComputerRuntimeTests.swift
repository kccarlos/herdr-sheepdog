import XCTest
@testable import SheepdogKit

/// A scripted network for computer runtimes: every address answers from
/// here. `state()` and `answerPending` either answer at once or hang until
/// the test resolves them; `/events` streams are held open for the test to
/// push into or end.
final class FakeComputerNetwork: @unchecked Sendable {
    enum Mode { case answer, hang }

    struct Pending {
        var id: Int
        var address: String
        var kind: Kind
        var generation: Int
        var resume: (Result<Any, Error>) -> Void
    }

    enum Kind: Equatable { case state, answer(String) }

    private let lock = NSLock()
    private var _mode: [String: Mode] = [:]
    private var _failure: [String: Error] = [:]
    private var _generation: [String: Int] = [:]
    private var _pending: [Pending] = []
    private var _streams: [String: [Int: AsyncThrowingStream<SSEParser.Output, Error>.Continuation]] = [:]
    private var _stateCalls: [String: Int] = [:]
    private var _answerCalls: [String: Int] = [:]
    private var _eventsOpened: [String: Int] = [:]
    private var nextID = 0
    /// Questions each address reports.
    var questions: [String: [PendingAction]] {
        get { lock.withLock { _questions } }
        set { lock.withLock { _questions = newValue } }
    }
    private var _questions: [String: [PendingAction]] = [:]

    func setMode(_ mode: Mode, for address: String) { lock.withLock { _mode[address] = mode } }
    func setFailure(_ error: Error?, for address: String) { lock.withLock { _failure[address] = error } }
    /// A re-pairing: replies made from now on carry the new generation.
    func bump(_ address: String) { lock.withLock { _generation[address, default: 0] += 1 } }
    func generation(_ address: String) -> Int { lock.withLock { _generation[address, default: 0] } }
    func stateCalls(_ address: String) -> Int { lock.withLock { _stateCalls[address, default: 0] } }
    func answerCalls(_ address: String) -> Int { lock.withLock { _answerCalls[address, default: 0] } }
    func eventsOpened(_ address: String) -> Int { lock.withLock { _eventsOpened[address, default: 0] } }
    func openStreams(_ address: String) -> Int { lock.withLock { _streams[address, default: [:]].count } }
    var pending: [Pending] { lock.withLock { _pending } }

    /// The state an address reports in a generation: agents named after
    /// the address and the generation, so a reply that landed on the wrong
    /// model, or from an older pairing, is visible.
    func state(_ address: String, generation: Int) -> DaemonState {
        DaemonState(agents: [Agent(paneID: "\(address):p1", alias: "\(address)#\(generation)", state: .blocked)],
                    questions: questions[address] ?? [],
                    voice: .init(mode: "live", status: "idle"))
    }

    func makeRequests(_ address: String) -> ComputerRequests? {
        FakeComputerClient(network: self, address: address, generation: generation(address))
    }

    fileprivate func call(_ address: String, generation: Int, kind: Kind) async throws -> Any {
        let (mode, failure): (Mode, Error?) = lock.withLock {
            switch kind {
            case .state: _stateCalls[address, default: 0] += 1
            case .answer: _answerCalls[address, default: 0] += 1
            }
            return (_mode[address] ?? .answer, _failure[address])
        }
        if mode == .answer {
            if let failure { throw failure }
            return reply(address, generation: generation, kind: kind)
        }
        let id: Int = lock.withLock { nextID += 1; return nextID }
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Any, Error>) in
                lock.withLock {
                    _pending.append(Pending(id: id, address: address, kind: kind, generation: generation) { continuation.resume(with: $0) })
                }
            }
        } onCancel: {
            let cancelled: Pending? = lock.withLock {
                guard let index = _pending.firstIndex(where: { $0.id == id }) else { return nil }
                return _pending.remove(at: index)
            }
            cancelled?.resume(.failure(CancellationError()))
        }
    }

    private func reply(_ address: String, generation: Int, kind: Kind) -> Any {
        switch kind {
        case .state: return state(address, generation: generation)
        case let .answer(id): return "\(address)#\(generation):\(id)"
        }
    }

    /// Resolves a hanging call: with its reply, or a failure.
    func resolve(_ id: Int, failing error: Error? = nil) {
        let found: Pending? = lock.withLock {
            guard let index = _pending.firstIndex(where: { $0.id == id }) else { return nil }
            return _pending.remove(at: index)
        }
        guard let found else { return }
        if let error {
            found.resume(.failure(error))
        } else {
            found.resume(.success(reply(found.address, generation: found.generation, kind: found.kind)))
        }
    }

    fileprivate func openStream(_ address: String, onOpen: (@Sendable () -> Void)?) -> AsyncThrowingStream<SSEParser.Output, Error> {
        AsyncThrowingStream { continuation in
            let id: Int = lock.withLock {
                nextID += 1
                _eventsOpened[address, default: 0] += 1
                _streams[address, default: [:]][nextID] = continuation
                return nextID
            }
            continuation.onTermination = { [weak self] _ in
                guard let self else { return }
                self.lock.withLock { _ = self._streams[address]?.removeValue(forKey: id) }
            }
            onOpen?()
        }
    }

    /// Pushes an event into every open stream of an address.
    func push(_ address: String, seq: UInt64 = 1) {
        let streams = lock.withLock { Array(_streams[address, default: [:]].values) }
        for stream in streams { stream.yield(.event(DaemonEvent(seq: seq, source: "test", type: "agent.state"))) }
    }

    /// Ends every open stream of an address with a failure.
    func dropStreams(_ address: String) {
        let streams: [AsyncThrowingStream<SSEParser.Output, Error>.Continuation] = lock.withLock {
            defer { _streams[address] = [:] }
            return Array(_streams[address, default: [:]].values)
        }
        for stream in streams { stream.finish(throwing: DaemonError.unreachable(endpoint: address, detail: "dropped")) }
    }
}

final class FakeComputerClient: ComputerRequests, @unchecked Sendable {
    let network: FakeComputerNetwork
    let address: String
    let generation: Int
    private(set) var invalidated = false

    init(network: FakeComputerNetwork, address: String, generation: Int) {
        self.network = network
        self.address = address
        self.generation = generation
    }

    func state() async throws -> DaemonState {
        try await network.call(address, generation: generation, kind: .state) as! DaemonState
    }

    func answerPending(id: String, option: Int) async throws -> String {
        try await network.call(address, generation: generation, kind: .answer(id)) as! String
    }

    func events(since: UInt64, onOpen: (@Sendable () -> Void)?) -> AsyncThrowingStream<SSEParser.Output, Error> {
        network.openStream(address, onOpen: onOpen)
    }

    func invalidate() { invalidated = true }
}

/// Waits until `condition` holds, letting the actors and main-actor tasks
/// run; fails after `timeout`.
@MainActor
func eventually(_ what: String, timeout: TimeInterval = 2, file: StaticString = #filePath, line: UInt = #line, _ condition: () -> Bool) async {
    let end = Date().addingTimeInterval(timeout)
    while !condition() {
        if Date() > end { return XCTFail("timed out: \(what)", file: file, line: line) }
        try? await Task.sleep(nanoseconds: 2_000_000)
    }
}

/// Lets queued actor and main-actor work run.
@MainActor
func settle(_ rounds: Int = 6) async {
    for _ in 0 ..< rounds {
        try? await Task.sleep(nanoseconds: 1_000_000)
    }
}

/// One computer's runtime (`ComputerConnection` + `ComputerModel`) on a
/// scripted network and a manual clock.
@MainActor
final class ComputerRuntimeTests: XCTestCase {
    var network: FakeComputerNetwork!
    var clock: ManualLinkClock!

    override func setUp() async throws {
        network = FakeComputerNetwork()
        clock = ManualLinkClock()
    }

    func question(_ id: String, pane: String) -> PendingAction {
        PendingAction(id: id, paneID: pane, kind: "question", question: "Go?", options: [PendingOption(index: 1, label: "Yes"), PendingOption(index: 2, label: "No")])
    }

    func makeModel(_ address: String, certExpires: Date? = nil, demand: LinkDemand = LinkDemand(isCurrent: false)) -> ComputerModel {
        let network = network!
        let connection = ComputerConnection(computerID: address, address: address, makeRequests: { network.makeRequests($0) }, sleep: { _ in })
        return ComputerModel(id: address, name: address, addresses: [address], certExpires: certExpires, connection: connection,
                             scheduler: clock, demand: demand, random: LinkRandom(seed: 7))
    }

    func testTwoComputersEachApplyTheirOwnState() async {
        let home = makeModel("home")
        let work = makeModel("work")
        home.start()
        work.start()
        await eventually("both online") { home.link.isUsable && work.link.isUsable }
        XCTAssertEqual(home.agents.map(\.alias), ["home#0"])
        XCTAssertEqual(work.agents.map(\.alias), ["work#0"])
        XCTAssertEqual(home.reachability, .online)
        // Both stream `/events` in the foreground (decision 2).
        await eventually("streams open") { self.network.openStreams("home") == 1 && self.network.openStreams("work") == 1 }
    }

    func testAnEventRefreshesOnlyItsComputer() async {
        let home = makeModel("home")
        let work = makeModel("work")
        home.start()
        work.start()
        await eventually("streams open") { self.network.openStreams("home") == 1 && self.network.openStreams("work") == 1 }
        let homeCalls = network.stateCalls("home")
        let workCalls = network.stateCalls("work")
        network.questions["home"] = [question("q1", pane: "home:p1")]
        network.push("home")
        await eventually("home refreshed") { home.questions.map(\.id) == ["q1"] }
        XCTAssertEqual(network.stateCalls("home"), homeCalls + 1)
        XCTAssertEqual(network.stateCalls("work"), workCalls)
        XCTAssertTrue(work.questions.isEmpty)
    }

    func testALateReplyFromAnOlderPairingIsDropped() async {
        let home = makeModel("home")
        home.start()
        await eventually("online") { home.link.isUsable }
        network.setMode(.hang, for: "home")
        Task { await home.refresh() }
        await eventually("refresh in flight") { self.network.pending.count == 1 }
        let late = network.pending[0].id
        // Re-paired: a new epoch, and the network's replies change with it.
        network.bump("home")
        home.renew(addresses: ["home"], certExpires: nil)
        network.resolve(late)
        await settle()
        XCTAssertFalse(home.agents.contains { $0.alias == "home#0" }, "the old pairing's reply is not applied")
        // The new epoch's probe lands.
        await eventually("new probe") { self.network.pending.contains { $0.generation == 1 } }
        for p in network.pending where p.generation == 1 { network.resolve(p.id) }
        await eventually("new state") { home.agents.map(\.alias) == ["home#1"] }
    }

    func testAnAnswerFinishesOnItsComputerWhenTheAppLeaves() async {
        network.questions["home"] = [question("q1", pane: "home:p1")]
        let home = makeModel("home")
        home.start()
        await eventually("question") { home.questions.count == 1 }
        network.setMode(.hang, for: "home")
        home.answer(question: "q1", option: 1)
        if case .sending = home.phase("q1") {} else { XCTFail("sending") }
        await eventually("answer in flight") { self.network.pending.contains { $0.kind == .answer("q1") } }
        // A second tap while the first is in flight is ignored.
        home.answer(question: "q1", option: 2)
        await settle()
        XCTAssertEqual(network.answerCalls("home"), 1)
        // The app goes to the background: the answer still needs the link.
        home.setAppPhase(.background)
        XCTAssertNotEqual(home.link.state, .idle, "an answer in flight keeps the link")
        let answer = network.pending.first { $0.kind == .answer("q1") }!.id
        network.resolve(answer)
        await eventually("answered") {
            if case .answered = home.phase("q1") { return true }
            return false
        }
        await eventually("idle once done") { home.link.state == .idle }
        await eventually("stream closed") { self.network.openStreams("home") == 0 }
    }

    func testAGoneQuestionSaysSo() async {
        network.questions["home"] = [question("q1", pane: "home:p1")]
        let home = makeModel("home")
        var feedback: [ComputerModel.AnswerFeedback] = []
        home.onAnswerFeedback = { feedback.append($0) }
        home.start()
        await eventually("question") { home.questions.count == 1 }
        network.setFailure(DaemonError.pendingGone, for: "home")
        home.answer(question: "q1", option: 1)
        await eventually("gone") { home.phase("q1") == .gone(message: ApprovalFlow.goneMessage) }
        XCTAssertEqual(feedback, [.tapped, .failed])
    }

    func testLowDataModePollsInsteadOfStreaming() async {
        let work = makeModel("work")
        work.start()
        await eventually("stream open") { self.network.openStreams("work") == 1 }
        work.setPath(NetworkPath(satisfied: true, interface: .cellular, constrained: true))
        await eventually("stream closed") { self.network.openStreams("work") == 0 }
        // The 30 s refresh still runs, as a confirming probe.
        let calls = network.stateCalls("work")
        clock.advance(ComputerLink.refreshInterval + 1)
        await eventually("refreshed on the timer") { self.network.stateCalls("work") > calls }
        XCTAssertTrue(work.link.isUsable)
        // The current computer keeps its stream on the same path.
        let current = makeModel("home", demand: LinkDemand(isCurrent: true))
        current.start()
        current.setPath(NetworkPath(satisfied: true, interface: .cellular, constrained: true))
        await eventually("current streams") { self.network.openStreams("home") == 1 }
    }

    func testTheBackgroundStopsAnotherComputer() async {
        let work = makeModel("work")
        work.start()
        await eventually("stream open") { self.network.openStreams("work") == 1 }
        work.setAppPhase(.background)
        XCTAssertEqual(work.link.state, .idle)
        await eventually("stream closed") { self.network.openStreams("work") == 0 }
        let calls = network.stateCalls("work")
        clock.advance(120)
        await settle()
        XCTAssertEqual(network.stateCalls("work"), calls, "nothing runs in the background")
        // Back in front: a probe at once, and the stream reopens.
        work.setAppPhase(.active)
        await eventually("reopened") { self.network.openStreams("work") == 1 }
        XCTAssertGreaterThan(network.stateCalls("work"), calls)
    }

    func testALostStreamIsReopenedThroughTheLink() async {
        let work = makeModel("work")
        work.start()
        await eventually("stream open") { self.network.eventsOpened("work") == 1 }
        network.dropStreams("work")
        // The link probes once, and the stream opens again.
        await eventually("reopened") { self.network.eventsOpened("work") == 2 }
        XCTAssertTrue(work.link.isUsable)
    }

    func testAComputerThatDoesNotAnswerIsNotTalkedToDuringTheQuietPeriod() async {
        network.setFailure(DaemonError.unreachable(endpoint: "studio", detail: "refused"), for: "studio")
        let studio = makeModel("studio")
        XCTAssertTrue(studio.canTalkTo, "before the first attempt")
        studio.start()
        await eventually("waiting") { !studio.canTalkTo }
        XCTAssertEqual(studio.reachability, .connecting, "still inside the quiet period")
        clock.advance(ComputerLink.quietPeriod + 1)
        await eventually("offline") {
            if case .offline = studio.reachability { return true }
            return false
        }
        XCTAssertFalse(studio.snapshot.canTalkTo)
        // Retry tries at once.
        network.setFailure(nil, for: "studio")
        studio.retry()
        await eventually("back") { studio.reachability == .online && studio.canTalkTo }
    }

    func testAPromotedPinMovesTheRotationWindowWithoutARebuild() async {
        // Paired long before expiry: a mismatch is an untrusted answer.
        let home = makeModel("home", certExpires: clock.now.addingTimeInterval(60 * 86400))
        network.setFailure(DaemonError.untrusted(endpoint: "home"), for: "home")
        home.start()
        await eventually("untrusted") {
            if case .untrustedAnswer = home.link.state { return true }
            return false
        }
        // A sign-in learned that the certificate expires tomorrow: inside
        // the rotation window, the same mismatch asks for a new scan.
        home.certificateChanged(certExpires: clock.now.addingTimeInterval(86400))
        home.retry()
        await eventually("renewed while away") { home.link.state == .refused(.renewedWhileAway) }
    }

    func testStopDropsEverythingStillInFlight() async {
        let home = makeModel("home")
        home.start()
        await eventually("online") { home.link.isUsable }
        network.setMode(.hang, for: "home")
        network.questions["home"] = [question("q1", pane: "home:p1")]
        Task { await home.refresh() }
        await eventually("in flight") { !self.network.pending.isEmpty }
        home.stop()
        for p in network.pending { network.resolve(p.id) }
        await settle()
        XCTAssertTrue(home.questions.isEmpty, "a stopped model applies nothing")
        await eventually("stream closed") { self.network.openStreams("home") == 0 }
    }

    func testAnUnchangedRefreshAssignsNothing() async {
        let home = makeModel("home")
        home.start()
        await eventually("online") { home.agents.count == 1 }
        var notified = 0
        withObservationTracking {
            _ = home.agents
            _ = home.questions
            _ = home.phases
            _ = home.reachability
            _ = home.canTalkTo
        } onChange: {
            notified += 1
        }
        await home.refresh()
        await home.refresh()
        XCTAssertEqual(notified, 0, "the same state again notifies no view")
    }
}
