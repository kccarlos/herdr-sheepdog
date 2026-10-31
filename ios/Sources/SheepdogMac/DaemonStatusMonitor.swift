import Foundation
import SheepdogKit

/// The daemon's state as the menu bar sees it: `nil` is "did not answer".
public protocol DaemonStatusSource: AnyObject {
    var onChange: (@MainActor (DaemonState?) -> Void)? { get set }
    func start()
    func stop()
}

/// `GET /api/state` kept fresh by the `/events` stream, over Kit's
/// `DaemonClient` (its request, SSE parsing and error words). Any event is
/// a hint that the state may have changed, so it re-reads `/api/state`;
/// bursts are coalesced into one read. When the stream drops (daemon down
/// or restarted) the status goes to `nil` and the loop retries.
public final class DaemonStatusMonitor: DaemonStatusSource {
    public var onChange: (@MainActor (DaemonState?) -> Void)?

    private let client: DaemonClient
    private let retryDelay: TimeInterval
    private let coalesce: TimeInterval
    private var loop: Task<Void, Never>?
    private let lock = NSLock()
    private var refreshScheduled = false

    public init(locator: DaemonLocating, session: URLSession? = nil, retryDelay: TimeInterval = 5, coalesce: TimeInterval = 0.3) {
        client = DaemonClient(endpoint: locator.endpoint, session: session)
        self.retryDelay = retryDelay
        self.coalesce = coalesce
    }

    deinit {
        loop?.cancel()
        client.invalidate()
    }

    public func start() {
        guard loop == nil else { return }
        loop = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                await self.follow()
                let delay = self.retryDelay
                try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            }
        }
    }

    public func stop() {
        loop?.cancel()
        loop = nil
    }

    /// One connection: read the state, then re-read it on every event until
    /// the stream ends. Returns when it is time to retry.
    private func follow() async {
        await publish()
        do {
            for try await output in client.events() {
                if Task.isCancelled { return }
                switch output {
                case .event:
                    scheduleRefresh()
                case .closed:
                    await send(nil)
                    return
                case .undecodable:
                    continue
                }
            }
            await send(nil)
        } catch {
            await send(nil)
        }
    }

    /// One read per burst of events: the first event schedules it, and the
    /// ones that arrive before it runs are covered by it.
    private func scheduleRefresh() {
        lock.lock()
        defer { lock.unlock() }
        guard !refreshScheduled else { return }
        refreshScheduled = true
        Task { [weak self] in
            guard let self else { return }
            try? await Task.sleep(nanoseconds: UInt64(self.coalesce * 1_000_000_000))
            self.lock.withLock { self.refreshScheduled = false }
            await self.publish()
        }
    }

    private func publish() async {
        await send(try? await client.state())
    }

    private func send(_ state: DaemonState?) async {
        guard !Task.isCancelled else { return }
        await MainActor.run { onChange?(state) }
    }
}
