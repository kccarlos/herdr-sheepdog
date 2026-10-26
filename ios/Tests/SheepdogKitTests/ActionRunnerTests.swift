import XCTest
@testable import SheepdogKit

/// A daemon for actions: `say` is a non-idempotent POST that keeps its
/// effect per `Idempotency-Key` the way the daemon does (a repeat of a key
/// does nothing new), and every call can hang until the test resolves it.
final class FakeActionDaemon: @unchecked Sendable {
    struct Call {
        var id: Int
        var address: String
        var key: String?
        var text: String
        var resume: (Result<String, Error>) -> Void
    }

    private let lock = NSLock()
    private var _hang = true
    private var _calls: [Call] = []
    private var _made: [(address: String, key: String?)] = []
    private var _effects: [String] = []
    private var _seenKeys: Set<String> = []
    private var nextID = 0

    var hang: Bool {
        get { lock.withLock { _hang } }
        set { lock.withLock { _hang = newValue } }
    }

    /// Calls still waiting for an answer.
    var pending: [Call] { lock.withLock { _calls } }
    /// Every call made, with its address and key.
    var made: [(address: String, key: String?)] { lock.withLock { _made } }
    /// What the daemon did, once per key.
    var effects: [String] { lock.withLock { _effects } }

    func makeRequests(_ address: String) -> ComputerRequests? { FakeActionClient(daemon: self, address: address) }

    fileprivate func say(_ text: String, address: String) async throws -> String {
        let key = DaemonClient.idempotencyKey
        let (hang, id): (Bool, Int) = lock.withLock {
            _made.append((address, key))
            // The daemon acts when the request arrives, once per key.
            if let key {
                if !_seenKeys.contains(key) {
                    _seenKeys.insert(key)
                    _effects.append("\(address):\(text)")
                }
            } else {
                _effects.append("\(address):\(text)")
            }
            nextID += 1
            return (_hang, nextID)
        }
        guard hang else { return "\(address):\(id)" }
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<String, Error>) in
                lock.withLock {
                    _calls.append(Call(id: id, address: address, key: key, text: text) { continuation.resume(with: $0) })
                }
            }
        } onCancel: {
            let call: Call? = lock.withLock {
                guard let index = _calls.firstIndex(where: { $0.id == id }) else { return nil }
                return _calls.remove(at: index)
            }
            call?.resume(.failure(DaemonError.timedOut(endpoint: address)))
        }
    }

    /// Answers a waiting call (the oldest when no id is given).
    func resolve(_ id: Int? = nil, with result: Result<String, Error>? = nil) {
        let call: Call? = lock.withLock {
            guard let index = id.map({ id in _calls.firstIndex { $0.id == id } }) ?? (_calls.isEmpty ? nil : 0) else { return nil }
            return _calls.remove(at: index)
        }
        guard let call else { return }
        call.resume(result ?? .success("\(call.address):\(call.id)"))
    }
}

final class FakeActionClient: ComputerRequests, @unchecked Sendable {
    let daemon: FakeActionDaemon
    let address: String

    init(daemon: FakeActionDaemon, address: String) {
        self.daemon = daemon
        self.address = address
    }

    func state() async throws -> DaemonState { DaemonState(agents: [], voice: .init(mode: "live", status: "idle")) }
    func answerPending(id: String, option: Int) async throws -> String { try await daemon.say("answer \(id)", address: address) }
    func say(_ text: String) async throws -> String { try await daemon.say(text, address: address) }
    func events(since: UInt64, onOpen: (@Sendable () -> Void)?) -> AsyncThrowingStream<SSEParser.Output, Error> {
        AsyncThrowingStream { _ in }
    }

    func invalidate() {}
}

@MainActor
final class FakeActionBackground: ActionBackground {
    private(set) var begun = 0
    private(set) var ended = 0

    func begin(_ name: String) -> () -> Void {
        begun += 1
        var done = false
        return { [weak self] in
            guard !done else { return }
            done = true
            self?.ended += 1
        }
    }
}

/// `ActionRunner` on a scripted daemon and a manual clock: dedup, timeouts
/// and the safe retry, the computer captured at tap time, the background.
@MainActor
final class ActionRunnerTests: XCTestCase {
    var daemon: FakeActionDaemon!
    var clock: ManualLinkClock!
    var background: FakeActionBackground!

    override func setUp() async throws {
        daemon = FakeActionDaemon()
        clock = ManualLinkClock()
        background = FakeActionBackground()
        ActionRunner.background = background
    }

    override func tearDown() async throws {
        ActionRunner.background = nil
    }

    func makeModel(_ address: String, keys: Bool = true) -> ComputerModel {
        let daemon = daemon!
        let connection = ComputerConnection(computerID: address, address: address, makeRequests: { daemon.makeRequests($0) })
        let model = ComputerModel(id: address, name: address, addresses: [address], certExpires: nil, connection: connection,
                                  scheduler: clock, demand: LinkDemand(isCurrent: true), random: LinkRandom(seed: 3))
        if keys {
            var hello = Hello(name: "sheepdog", version: "test", auth: "device", ws: "/ws", audio: .init(inHz: 16000, outHz: 24000, format: "pcm16le mono"))
            hello.idempotencyKeys = true
            model.hello = hello
        }
        return model
    }

    /// Starts an action and returns its eventual result.
    func tap(_ model: ComputerModel, _ key: String, _ kind: ActionRunner.Kind = .typedLine, text: String = "hi",
             idempotencyKey: String? = nil) -> Task<Result<String, Error>, Never> {
        Task { @MainActor in
            do {
                return .success(try await model.actions.run(key, kind, as: FakeActionClient.self, idempotencyKey: idempotencyKey) { try await $0.say(text) })
            } catch {
                return .failure(error)
            }
        }
    }

    func testADoubleTapSendsOneRequest() async throws {
        let home = makeModel("home")
        let first = tap(home, "utter:home")
        let second = tap(home, "utter:home")
        await eventually("the request is out") { daemon.pending.count == 1 }
        await settle()
        XCTAssertTrue(home.actions.isInFlight("utter:home"))
        XCTAssertEqual(daemon.made.count, 1, "the second tap joined the first")
        daemon.resolve()
        let a = try await first.value.get()
        let b = try await second.value.get()
        XCTAssertEqual(a, b)
        XCTAssertEqual(daemon.effects, ["home:hi"])
        XCTAssertFalse(home.actions.isInFlight("utter:home"))
    }

    func testATimeoutRetriesWithTheSameKeyAndActsOnce() async throws {
        let home = makeModel("home")
        let result = tap(home, "utter:home", .typedLine)
        await eventually("first attempt") { daemon.pending.count == 1 }
        clock.advance(ActionRunner.Kind.typedLine.timeout)
        await eventually("the retry") { daemon.made.count == 2 && daemon.pending.count == 1 }
        let keys = daemon.made.map(\.key)
        XCTAssertNotNil(keys[0])
        XCTAssertEqual(keys[0], keys[1], "the retry carries the first attempt's key")
        daemon.resolve()
        _ = try await result.value.get()
        XCTAssertEqual(daemon.effects.count, 1, "the daemon acted once")
        XCTAssertEqual(home.actions.retries, 1)
    }

    func testATimeoutTwiceSaysTakingLongerThanUsual() async {
        let home = makeModel("home")
        home.start()
        await eventually("online") { home.link.isUsable }
        let result = tap(home, "utter:home", .typedLine)
        await eventually("first attempt") { daemon.pending.count == 1 }
        clock.advance(10)
        await eventually("the retry") { daemon.made.count == 2 }
        clock.advance(10)
        guard case let .failure(error) = await result.value else { return XCTFail("it succeeded") }
        let failure = home.failure(error)
        XCTAssertTrue(failure.isSlow, "\(failure)")
        XCTAssertEqual(failure.title, RequestFailure.slowTitle)
        XCTAssertTrue(failure.message.hasSuffix("Try again."))
        XCTAssertEqual(daemon.effects.count, 1)
    }

    func testNoRetryOnADaemonWithoutKeys() async {
        let home = makeModel("home", keys: false)
        let result = tap(home, "utter:home", .typedLine)
        await eventually("first attempt") { daemon.pending.count == 1 }
        clock.advance(10)
        guard case let .failure(error) = await result.value else { return XCTFail("it succeeded") }
        XCTAssertTrue(ActionRunner.isTransport(error))
        XCTAssertEqual(daemon.made.count, 1, "a non-idempotent action is not retried without keys")
    }

    func testAnAnswerRetriesWithoutAKey() async throws {
        let home = makeModel("home", keys: false)
        let result = tap(home, ActionRunner.Key.answer("home", "q1"), .answer)
        await eventually("first attempt") { daemon.pending.count == 1 }
        clock.advance(ActionRunner.Kind.answer.timeout)
        await eventually("the retry") { daemon.made.count == 2 && daemon.pending.count == 1 }
        XCTAssertEqual(daemon.made.map(\.key), [nil, nil], "answers are idempotent by question, no key")
        daemon.resolve()
        _ = try await result.value.get()
    }

    func testTryAgainReusesTheKey() async throws {
        let home = makeModel("home", keys: false)
        daemon.hang = true
        let first = tap(home, "utter:home", idempotencyKey: "attempt-1")
        await eventually("sent") { daemon.pending.count == 1 }
        clock.advance(10)
        _ = await first.value
        let again = tap(home, "utter:home", idempotencyKey: "attempt-1")
        await eventually("sent again") { daemon.pending.count == 1 }
        daemon.resolve()
        _ = try await again.value.get()
        XCTAssertEqual(daemon.made.map(\.key), ["attempt-1", "attempt-1"])
        XCTAssertEqual(daemon.effects.count, 1)
    }

    func testSwitchingComputersMidActionLandsOnTheOriginal() async throws {
        let fleet = ComputerFleet()
        let home = makeModel("home")
        let work = makeModel("work")
        fleet.set([home, work])
        fleet.makeCurrent("home")
        daemon.hang = true
        // The approval card's path: the computer captured at tap time.
        let captured = fleet.current!
        let result = tap(captured, ActionRunner.Key.answer(captured.id, "q1"), .answer)
        await eventually("sent") { daemon.pending.count == 1 }
        fleet.makeCurrent("work")
        daemon.resolve()
        let value = try await result.value.get()
        XCTAssertTrue(value.hasPrefix("home:"), value)
        XCTAssertEqual(daemon.made.map(\.address), ["home"])
        XCTAssertFalse(work.actions.isInFlight(ActionRunner.Key.answer("home", "q1")))
    }

    func testAnAnswerByTapLandsOnItsCardAfterASwitch() async {
        let fleet = ComputerFleet()
        let home = makeModel("home")
        let work = makeModel("work")
        fleet.set([home, work])
        fleet.makeCurrent("home")
        let question = PendingAction(id: "q1", paneID: "home:p1", kind: "question", question: "Run it?", options: [PendingOption(index: 1, label: "Yes")])
        home.answer(question, option: 1)
        home.answer(question, option: 1)
        await eventually("sent") { daemon.pending.count == 1 }
        fleet.makeCurrent("work")
        daemon.resolve()
        await eventually("answered on home") {
            if case .answered = home.phase("q1") { return true }
            return false
        }
        XCTAssertEqual(daemon.made.count, 1, "the second tap was ignored")
    }

    func testRepairingMidActionDropsTheReply() async {
        let home = makeModel("home")
        let result = tap(home, "utter:home")
        await eventually("sent") { daemon.pending.count == 1 }
        home.renew(addresses: ["home"], certExpires: nil)
        daemon.resolve()
        guard case let .failure(error) = await result.value else { return XCTFail("a reply from an older pairing landed") }
        XCTAssertTrue(error is ComputerModel.Stale, "\(error)")
    }

    func testAnActionTappedBeforeTheBackgroundCompletes() async throws {
        let home = makeModel("home")
        home.start()
        await eventually("online") { home.link.isUsable }
        let result = tap(home, "utter:home")
        await eventually("sent") { daemon.pending.count == 1 }
        XCTAssertEqual(background.begun, 1)
        XCTAssertTrue(home.effectiveDemand.actionsInFlight)
        home.setAppPhase(.background)
        await settle()
        XCTAssertTrue(home.link.isUsable, "the link stays up for the action in flight")
        XCTAssertEqual(background.ended, 0)
        daemon.resolve()
        _ = try await result.value.get()
        XCTAssertEqual(background.ended, 1, "the background task ends with the last action")
        XCTAssertFalse(home.effectiveDemand.actionsInFlight)
    }

    func testOneBackgroundTaskForSeveralActions() async {
        let home = makeModel("home")
        let a = tap(home, "a", text: "a")
        let b = tap(home, "b", text: "b")
        await eventually("both sent") { daemon.pending.count == 2 }
        XCTAssertEqual(background.begun, 1)
        daemon.resolve(daemon.pending.first { $0.text == "a" }?.id)
        _ = await a.value
        XCTAssertEqual(background.ended, 0)
        daemon.resolve(daemon.pending.first { $0.text == "b" }?.id)
        _ = await b.value
        XCTAssertEqual(background.ended, 1)
    }

    func testAReplyIsNeverRetried() async {
        let home = makeModel("home")
        let result = tap(home, "utter:home")
        await eventually("sent") { daemon.pending.count == 1 }
        daemon.resolve(with: .failure(DaemonError.rejected(status: 400, message: "no")))
        _ = await result.value
        XCTAssertEqual(daemon.made.count, 1)
    }

    func testTheKeyAndTimeoutGoOnTheRequest() throws {
        let client = DaemonClient(endpoint: DaemonEndpoint(host: "127.0.0.1"))
        let post = try DaemonClient.$idempotencyKey.withValue("k1") {
            try DaemonClient.$actionTimeout.withValue(60) { try client.request(path: "/api/agents", method: "POST", body: Data("{}".utf8)) }
        }
        XCTAssertEqual(post.value(forHTTPHeaderField: "Idempotency-Key"), "k1")
        XCTAssertEqual(post.timeoutInterval, 65, "past the runner's own timeout, and past the session's 8 s")
        let get = try DaemonClient.$idempotencyKey.withValue("k1") { try client.request(path: "/api/state") }
        XCTAssertNil(get.value(forHTTPHeaderField: "Idempotency-Key"), "a GET carries no key")
        let plain = try client.request(path: "/api/utterance", method: "POST", body: Data("{}".utf8))
        XCTAssertNil(plain.value(forHTTPHeaderField: "Idempotency-Key"))
    }

    /// Fast taps in any order: per key at most one request is out at a
    /// time, every tap ends with the result of the request it joined, and
    /// the daemon acts once per request.
    func testFastTapsProperty() async {
        for seed in 1 ... 25 {
            var random = LinkRandom(seed: UInt64(seed))
            daemon = FakeActionDaemon()
            let home = makeModel("home")
            let keys = ["a", "b", "c"]
            var taps: [(key: String, task: Task<Result<String, Error>, Never>)] = []
            for _ in 0 ..< 30 {
                if Int.random(in: 0 ..< 3, using: &random) > 0 {
                    let key = keys.randomElement(using: &random)!
                    taps.append((key, tap(home, key, text: key)))
                } else if !daemon.pending.isEmpty {
                    daemon.resolve(daemon.pending.randomElement(using: &random)!.id)
                }
                await settle(2)
                let out = daemon.pending.map { $0.key }
                XCTAssertEqual(out.count, Set(out).count, "seed \(seed): two requests out under one key")
                // A request may still be on its way to the daemon.
                XCTAssertLessThanOrEqual(daemon.pending.count, home.actions.count, "seed \(seed): the runner's count")
            }
            // From here on the daemon answers at once, so a tap whose task
            // has not started yet still finishes.
            daemon.hang = false
            while !daemon.pending.isEmpty || home.actions.count > 0 {
                daemon.resolve()
                await settle(2)
            }
            var results: [String: Set<String>] = [:]
            for tap in taps {
                guard case let .success(value) = await tap.task.value else { return XCTFail("seed \(seed): a tap failed") }
                results[value, default: []].insert(tap.key)
            }
            XCTAssertTrue(results.values.allSatisfy { $0.count == 1 }, "seed \(seed): a reply went to another key's tap")
            XCTAssertEqual(daemon.effects.count, daemon.made.count, "seed \(seed)")
            XCTAssertEqual(Set(results.keys).count, daemon.made.count, "seed \(seed): every request answered a tap")
            XCTAssertEqual(background.begun, background.ended, "seed \(seed): a background task left open")
        }
    }
}
