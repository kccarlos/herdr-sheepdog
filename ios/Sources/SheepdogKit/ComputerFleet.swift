import Foundation
import Observation

/// Every paired computer's runtime, and which one is current
/// (docs/dev/design/ios-state-architecture.md 3.1). The current computer is
/// a `ComputerModel` like the others, with the voice attached: switching
/// changes one id, and nothing is rebuilt or repointed.
///
/// What stays with each computer when it stops being current: its link,
/// its answers in flight (they finish there, decision 6), its conversation.
/// What moves: the session's attachment (`onState`, the stream's output,
/// the voice), which the caller re-attaches on `makeCurrent`.
@MainActor
@Observable
public final class ComputerFleet {
    public private(set) var models: [ComputerModel] = []
    public private(set) var currentID: String?
    /// The phone's network path, as the `NetworkPathSource` last said:
    /// every model has it, including one added later.
    @ObservationIgnored public private(set) var path: NetworkPath

    public init(path: NetworkPath = NetworkPath()) {
        self.path = path
    }

    /// The network changed: every computer's link hears it. No network
    /// stops every retry; a network that comes back, or a new interface
    /// (Wi-Fi to cellular), makes every link with demand try at once; Low
    /// Data Mode keeps other computers' `/events` closed.
    public func setPath(_ path: NetworkPath) {
        guard path != self.path else { return }
        self.path = path
        for model in models { model.setPath(path) }
    }

    public var current: ComputerModel? {
        guard let currentID else { return nil }
        return model(currentID)
    }

    /// Every computer but the current one, in order.
    public var others: [ComputerModel] { models.filter { $0.id != currentID } }

    public func model(_ id: String) -> ComputerModel? { models.first { $0.id == id } }

    /// The computers in this order. A model no longer listed is stopped.
    public func set(_ next: [ComputerModel]) {
        for gone in models where !next.contains(where: { $0 === gone }) { gone.stop() }
        for added in next where added.link.path != path { added.setPath(path) }
        if !next.elementsEqual(models, by: ===) { models = next }
        if let currentID, model(currentID) == nil { self.currentID = nil }
    }

    /// Makes another computer current. Returns the previous current model
    /// (still running) when the current one changed, nil otherwise.
    @discardableResult
    public func makeCurrent(_ id: String?) -> (previous: ComputerModel?, changed: Bool) {
        let target = id.flatMap { model($0) == nil ? nil : $0 }
        guard target != currentID else { return (nil, false) }
        let previous = current
        currentID = target
        return (previous, true)
    }

    /// Thrown by `onCurrent` when the current computer changed while the
    /// request was in flight: its reply belongs to the computer it was
    /// asked of, not to the one the app now talks to.
    public struct Moved: LocalizedError, Equatable {
        public init() {}
        public var errorDescription: String? { "The app switched to another computer before the answer came." }
    }

    /// A request on the current computer, captured when it is made: its
    /// reply is returned only if that computer is still current, at the
    /// same epoch. Otherwise it throws (`Moved`, `ComputerModel.Stale`), and
    /// the caller applies nothing.
    public func onCurrent<Client, T>(as type: Client.Type, _ body: @escaping @Sendable (Client) async throws -> T) async throws -> T {
        guard let model = current else { throw Moved() }
        let value = try await model.request(as: type, body)
        guard current === model else { throw Moved() }
        return value
    }
}
