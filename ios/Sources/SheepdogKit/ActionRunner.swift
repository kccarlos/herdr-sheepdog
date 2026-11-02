import Foundation
import Observation

/// Every tap that changes something on a computer goes through that
/// computer's runner (docs/dev/design/ios-state-architecture.md 3.5):
///
/// - **Captured at tap time.** The runner belongs to one `ComputerModel`
///   and the action keeps the epoch it was tapped in. Switching computers
///   does not cancel it: it finishes on its own computer (decision 6). A
///   re-pairing does: its reply is `ComputerModel.Stale`.
/// - **One at a time per key.** A second run with a key already in flight
///   joins the first and gets its result; buttons read `isInFlight` to
///   disable themselves.
/// - **A timeout per action**, on the model's clock, and one safe retry: an
///   answer is idempotent on the daemon by its question; a typed line, a New
///   agent, agent input and the Spaces actions carry an `Idempotency-Key`
///   and retry only on a daemon that honours it (`Hello.idempotencyKeys`).
/// - **The background.** While any action is in flight the app holds a
///   short background task (`ActionRunner.background`), so a tap just
///   before leaving the app still lands.
@MainActor
@Observable
public final class ActionRunner {
    /// How one kind of action runs.
    public struct Kind: Equatable, Sendable {
        public var timeout: TimeInterval
        /// The daemon does it only once whatever the repeats (an answer is
        /// keyed by its question): retry once after a transport failure.
        public var idempotent: Bool
        /// Send an `Idempotency-Key`, and retry once with the same key on a
        /// daemon that honours it.
        public var keyed: Bool

        public init(timeout: TimeInterval, idempotent: Bool = false, keyed: Bool = false) {
            self.timeout = timeout
            self.idempotent = idempotent
            self.keyed = keyed
        }

        /// `POST /api/pending/answer`.
        public static let answer = Kind(timeout: 10, idempotent: true)
        /// `POST /api/turn`: it waits for the orchestrator's text reply.
        /// The daemon bounds that turn at its 90 s tool timeout.
        public static let typedLine = Kind(timeout: 90, keyed: true)
        /// `POST /api/agents`: the daemon answers once the agent is ready.
        public static let newAgent = Kind(timeout: 60, keyed: true)
        /// `POST /api/agents/{pane}/input`.
        public static let input = Kind(timeout: 5, keyed: true)
        /// A Spaces action (new space, run, open a worktree, rename…).
        public static let space = Kind(timeout: 30, keyed: true)
        /// An agent action (rename, stop, close).
        public static let agent = Kind(timeout: 15, keyed: true)
        /// A call that changes nothing the user sees twice (push
        /// registration, unpairing).
        public static let quiet = Kind(timeout: 10, idempotent: true)
    }

    /// The dedup keys (3.5's table). The same key is used whichever surface
    /// the tap came from, so they converge.
    public enum Key {
        public static func answer(_ computer: String, _ question: String) -> String { "answer:\(computer):\(question)" }
        public static func typedLine(_ computer: String) -> String { "utter:\(computer)" }
        public static func newAgent(_ computer: String, _ sheet: String) -> String { "spawn:\(computer):\(sheet)" }
        public static func input(_ computer: String, _ pane: String) -> String { "input:\(computer):\(pane)" }
        public static func space(_ computer: String, _ what: String) -> String { "space:\(computer):\(what)" }
        public static func agent(_ computer: String, _ pane: String, _ what: String) -> String { "agent:\(computer):\(pane):\(what)" }
    }

    /// The dedup keys in flight now.
    public private(set) var inFlight: Set<String> = []

    public func isInFlight(_ key: String) -> Bool { inFlight.contains(key) }

    /// How the app holds a background task while actions run; nil (the
    /// tests, the extension) holds none.
    public static var background: ActionBackground?

    /// Requests made, by kind of outcome, for the tests: attempts and
    /// retries.
    @ObservationIgnored public private(set) var attempts = 0
    @ObservationIgnored public private(set) var retries = 0

    @ObservationIgnored private weak var model: ComputerModel?
    @ObservationIgnored private var running: [String: Task<Any, Error>] = [:]
    @ObservationIgnored private var endBackground: (() -> Void)?
    /// Whether any action is in flight changed (the model's link demand).
    @ObservationIgnored var onActiveChange: ((Bool) -> Void)?

    init(model: ComputerModel) {
        self.model = model
    }

    /// Runs `body` on this computer's address in use. A run with a key in
    /// flight joins it. Throws `ComputerModel.Stale` when the computer was
    /// paired again or removed meanwhile, `DaemonError.timedOut` after the
    /// kind's timeout (and its one retry), or what the request threw.
    ///
    /// - Parameter idempotencyKey: reuse one (the user's Try again after a
    ///   timeout); nil makes a fresh one for a keyed kind.
    public func run<Client, T>(_ key: String, _ kind: Kind, as type: Client.Type, idempotencyKey: String? = nil,
                               _ body: @escaping @Sendable (Client) async throws -> T) async throws -> T {
        if let existing = running[key] {
            guard let value = try await existing.value as? T else { throw InFlight() }
            return value
        }
        guard let model else { throw ComputerModel.Stale() }
        let epoch = model.epoch
        let idem = kind.keyed ? (idempotencyKey ?? UUID().uuidString) : nil
        let task = Task<Any, Error> { @MainActor [weak self] in
            guard let self else { throw ComputerModel.Stale() }
            return try await self.attempt(kind, epoch: epoch, idempotencyKey: idem, as: type, body)
        }
        begin(key, task)
        defer { finish(key) }
        guard let value = try await task.value as? T else { throw InFlight() }
        return value
    }

    /// A second run under a key whose first run returns another type.
    public struct InFlight: Error, Equatable {}

    private func attempt<Client, T>(_ kind: Kind, epoch: Int, idempotencyKey: String?, as type: Client.Type,
                                    _ body: @escaping @Sendable (Client) async throws -> T) async throws -> T {
        var retried = false
        while true {
            guard let model else { throw ComputerModel.Stale() }
            attempts += 1
            do {
                return try await model.perform(epoch: epoch, timeout: kind.timeout, idempotencyKey: idempotencyKey, as: type, body)
            } catch {
                guard !retried, Self.isTransport(error), canRetry(kind) else { throw error }
                retried = true
                retries += 1
            }
        }
    }

    /// One retry, with the same key: an idempotent action, or a keyed one
    /// on a daemon that keeps the first reply.
    private func canRetry(_ kind: Kind) -> Bool {
        if kind.idempotent { return true }
        return kind.keyed && model?.hello?.idempotencyKeys == true
    }

    /// No reply came: a timeout or a dropped connection. A reply, even an
    /// error, is the daemon's answer and is never retried.
    public static func isTransport(_ error: Error) -> Bool {
        switch error as? DaemonError {
        case .timedOut?, .unreachable?: return true
        default: return false
        }
    }

    private func begin(_ key: String, _ task: Task<Any, Error>) {
        running[key] = task
        inFlight.insert(key)
        if running.count == 1, endBackground == nil {
            endBackground = Self.background?.begin("sheepdog action")
        }
        if running.count == 1 { onActiveChange?(true) }
    }

    private func finish(_ key: String) {
        running[key] = nil
        inFlight.remove(key)
        if running.isEmpty, let end = endBackground {
            endBackground = nil
            end()
        }
        if running.isEmpty { onActiveChange?(false) }
    }

    /// Actions in flight now.
    public var count: Int { running.count }
}

/// A short background task while actions are in flight (the app's is
/// `UIApplication.beginBackgroundTask`, about 30 s). `begin` returns the
/// way to end it; ending twice is harmless.
@MainActor
public protocol ActionBackground: AnyObject {
    func begin(_ name: String) -> () -> Void
}

extension DaemonClient {
    /// The `Idempotency-Key` the runner gives an action's POSTs.
    @TaskLocal public static var idempotencyKey: String?
    /// The action's timeout, so a long one (New agent) is not cut at the
    /// session's 8 s.
    @TaskLocal public static var actionTimeout: TimeInterval?
}
