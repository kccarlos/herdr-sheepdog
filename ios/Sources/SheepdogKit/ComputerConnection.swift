import Foundation

/// The requests a computer's runtime makes to one of its addresses.
/// `DaemonClient` is the real one; the tests pass a scripted network.
public protocol ComputerRequests: AnyObject, Sendable {
    func state() async throws -> DaemonState
    func answerPending(id: String, option: Int) async throws -> String
    func events(since: UInt64, onOpen: (@Sendable () -> Void)?) -> AsyncThrowingStream<SSEParser.Output, Error>
    /// Closes the client's sessions; it is never used again.
    func invalidate()
}

extension DaemonClient: ComputerRequests {}

/// A reply from one computer, tagged with the computer and the epoch it
/// was asked in (docs/dev/design/ios-state-architecture.md 3.1). The model
/// applies it only while that epoch is still its own.
public struct ComputerReply<Value> {
    public let computerID: String
    public let epoch: Int
    public let value: Value
    /// How long the request took (zero for stream updates).
    public let rtt: TimeInterval

    public init(computerID: String, epoch: Int, value: Value, rtt: TimeInterval = 0) {
        self.computerID = computerID
        self.epoch = epoch
        self.value = value
        self.rtt = rtt
    }
}

extension ComputerReply: Sendable where Value: Sendable {}

/// What one computer's `/events` stream did.
public enum ComputerStreamUpdate: Equatable, Sendable {
    /// The daemon answered 200: the stream is up, even if it is quiet.
    case opened
    case output(SSEParser.Output)
    /// The stream ended or failed (not because the app closed it).
    case lost(LinkError)
}

/// One computer's I/O (docs/dev/design/ios-state-architecture.md 3.1,
/// decision 7): one immutable client per address, the REST requests and the
/// `/events` loop. It is an actor because URLSession's callbacks and the
/// stream's task touch this state off the main thread; the view-facing
/// copy is `ComputerModel`, and the link's machine runs there.
///
/// **Epochs.** Every call names the epoch it belongs to. A newer epoch
/// (new credentials, a new address set) drops every client, so nothing is
/// repointed, and a call from an older epoch is refused as a cancellation.
/// Every reply carries the epoch it was asked in, and the model applies it
/// only while that epoch is still its own.
public actor ComputerConnection {
    public nonisolated let computerID: String

    /// The client for one address, nil when the address cannot be used.
    public typealias MakeRequests = @Sendable (_ address: String) -> ComputerRequests?

    private let makeRequests: MakeRequests
    private let sleep: @Sendable (TimeInterval) async -> Void
    private var epoch = 0
    private var addressInUse: String?
    private var clients: [String: ComputerRequests] = [:]
    private var eventsTask: Task<Void, Never>?
    private var eventsToken = 0
    /// `/events` failures in a row, for the small wait before reopening.
    private var eventFailures = 0
    private var stopped = false

    /// - Parameters:
    ///   - address: the address requests go to until the link picks one.
    ///   - sleep: how the stream waits before reopening after a failure;
    ///     the tests pass one that returns at once.
    public init(computerID: String, address: String?, makeRequests: @escaping MakeRequests,
                sleep: @escaping @Sendable (TimeInterval) async -> Void = { try? await Task.sleep(nanoseconds: UInt64(max(0, $0) * 1_000_000_000)) }) {
        self.computerID = computerID
        self.addressInUse = address
        self.makeRequests = makeRequests
        self.sleep = sleep
    }

    // MARK: - Epochs and addresses

    /// Moves to `epoch` when it is newer: every client is dropped and the
    /// stream closes. Returns false for a call from an older epoch.
    private func enter(_ epoch: Int) -> Bool {
        guard !stopped else { return false }
        if epoch < self.epoch { return false }
        if epoch > self.epoch {
            self.epoch = epoch
            closeEvents()
            for client in clients.values { client.invalidate() }
            clients = [:]
        }
        return true
    }

    /// New credentials or a new address set: a new epoch, and `address` is
    /// the one in use.
    public func renew(epoch: Int, address: String?) {
        guard enter(epoch) else { return }
        addressInUse = address
    }

    /// The link picked this address: every request and channel goes there.
    public func use(address: String, epoch: Int) {
        guard enter(epoch) else { return }
        addressInUse = address
    }

    /// Ends this connection: the stream closes, every client is dropped,
    /// and nothing more is asked.
    public func stop() {
        closeEvents()
        for client in clients.values { client.invalidate() }
        clients = [:]
        stopped = true
    }

    private func client(for address: String) -> ComputerRequests? {
        if let client = clients[address] { return client }
        guard let client = makeRequests(address) else { return nil }
        clients[address] = client
        return client
    }

    // MARK: - Requests

    /// The link's probe: a pinned `GET /api/state` on one address.
    public func probe(address: String, epoch: Int) async -> ComputerReply<LinkProbeResult> {
        guard enter(epoch) else { return reply(epoch, .failure(.cancelled)) }
        guard let client = client(for: address) else { return reply(epoch, .failure(.transport)) }
        let started = Date()
        do {
            let state = try await client.state()
            let rtt = Date().timeIntervalSince(started)
            return reply(epoch, .success(rtt: rtt, state: state), rtt: rtt)
        } catch {
            return reply(epoch, .failure(LinkError(error)), rtt: Date().timeIntervalSince(started))
        }
    }

    /// One request to the address in use.
    public func request<T: Sendable>(epoch: Int, _ body: @Sendable (ComputerRequests) async throws -> T) async -> ComputerReply<Result<T, Error>> {
        guard enter(epoch) else { return reply(epoch, .failure(CancellationError())) }
        guard let address = addressInUse, let client = client(for: address) else {
            return reply(epoch, .failure(DaemonError.malformed("no address for this computer")))
        }
        let started = Date()
        do {
            let value = try await body(client)
            return reply(epoch, .success(value), rtt: Date().timeIntervalSince(started))
        } catch {
            return reply(epoch, .failure(error), rtt: Date().timeIntervalSince(started))
        }
    }

    private func reply<V>(_ epoch: Int, _ value: V, rtt: TimeInterval = 0) -> ComputerReply<V> {
        ComputerReply(computerID: computerID, epoch: epoch, value: value, rtt: rtt)
    }

    // MARK: - /events

    /// Opens `/events` on the address in use, once. It does not retry by
    /// itself: a drop is delivered as `.lost`, and the link says when to
    /// open it again. A stream that keeps failing while the computer
    /// answers waits a little longer before each reopen, so it cannot spin.
    ///
    /// `deliver` runs on the main actor, in order, and only for the stream
    /// opened last: an older stream's updates are dropped here.
    public func openEvents(epoch: Int, since: UInt64, deliver: @escaping @MainActor @Sendable (ComputerReply<ComputerStreamUpdate>) -> Void) {
        guard enter(epoch) else { return }
        eventsTask?.cancel()
        eventsToken += 1
        let token = eventsToken
        let failures = eventFailures
        eventsTask = Task { [weak self] in
            guard let self else { return }
            await self.runEvents(token: token, epoch: epoch, failures: failures, since: since, deliver: deliver)
        }
    }

    private func runEvents(token: Int, epoch: Int, failures: Int, since: UInt64,
                           deliver: @escaping @MainActor @Sendable (ComputerReply<ComputerStreamUpdate>) -> Void) async {
        if failures > 0 { await sleep(LinkBackoff.ceiling(attempt: failures, cap: LinkBackoff.cap)) }
        guard !Task.isCancelled, token == eventsToken, let address = addressInUse, let client = client(for: address) else { return }
        var failure: LinkError = .transport
        var opened = false
        do {
            let stream = client.events(since: since, onOpen: { [weak self] in
                Task { await self?.deliverOpened(token: token, epoch: epoch, deliver: deliver) }
            })
            for try await output in stream {
                if Task.isCancelled || token != eventsToken { return }
                if !opened {
                    opened = true
                    await deliver(reply(epoch, .opened))
                }
                // A stream that delivers is healthy: the next drop reopens
                // without waiting.
                if case .event = output { eventFailures = 0 }
                await deliver(reply(epoch, .output(output)))
            }
        } catch {
            failure = LinkError(error)
        }
        // Ended or failed, not closed by the app: tell the link.
        guard !Task.isCancelled, token == eventsToken else { return }
        eventsTask = nil
        eventFailures += 1
        await deliver(reply(epoch, .lost(failure)))
    }

    private func deliverOpened(token: Int, epoch: Int, deliver: @MainActor @Sendable (ComputerReply<ComputerStreamUpdate>) -> Void) async {
        guard token == eventsToken, eventsTask != nil else { return }
        await deliver(reply(epoch, .opened))
    }

    /// Closes `/events`; nothing more from it is delivered.
    public func closeEvents() {
        eventsTask?.cancel()
        eventsTask = nil
        eventsToken += 1
        eventFailures = 0
    }

    /// Whether a stream is open or opening, for the tests.
    public var eventsOpen: Bool { eventsTask != nil }
}
