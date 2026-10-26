import Foundation
import Network

/// Where the phone's network path comes from
/// (docs/dev/design/ios-state-architecture.md 3.2 "Network path"): one
/// source for the app, which `ComputerFleet.setPath` hands to every
/// computer's link. The app uses `SystemNetworkMonitor` (`NWPathMonitor`);
/// the tests, the demo and the debug launch arguments use
/// `ManualNetworkPath`.
@MainActor
public protocol NetworkPathSource: AnyObject {
    /// The last path seen. Before the first update: satisfied, interface
    /// unknown.
    var path: NetworkPath { get }
    /// Starts reporting. `onChange` runs on the main actor, once per
    /// distinct path, never for a repeat of the last one.
    func start(_ onChange: @escaping @MainActor (NetworkPath) -> Void)
    func stop()
}

/// `NWPathMonitor`, as `NetworkPath` values on the main actor.
@MainActor
public final class SystemNetworkMonitor: NetworkPathSource {
    public private(set) var path = NetworkPath()
    private var monitor: NWPathMonitor?

    public init() {}

    public func start(_ onChange: @escaping @MainActor (NetworkPath) -> Void) {
        guard monitor == nil else { return }
        let monitor = NWPathMonitor()
        monitor.pathUpdateHandler = { [weak self] nwPath in
            let next = NetworkPath(nwPath)
            // The handler runs on the main queue (below).
            MainActor.assumeIsolated {
                guard let self, next != self.path else { return }
                self.path = next
                onChange(next)
            }
        }
        monitor.start(queue: .main)
        self.monitor = monitor
    }

    public func stop() {
        monitor?.cancel()
        monitor = nil
    }
}

extension NetworkPath {
    /// What an `NWPath` means for the links: whether it carries traffic,
    /// which kind of interface it uses (the race winners are remembered
    /// per kind), and Low Data Mode.
    public init(_ path: NWPath) {
        let satisfied = path.status == .satisfied
        let interface: NetworkInterface?
        if !satisfied {
            interface = nil
        } else if path.usesInterfaceType(.wifi) {
            interface = .wifi
        } else if path.usesInterfaceType(.cellular) {
            interface = .cellular
        } else if path.usesInterfaceType(.wiredEthernet) {
            interface = .wired
        } else {
            interface = .other
        }
        self.init(satisfied: satisfied, interface: interface, constrained: path.isConstrained)
    }
}

/// A path set by hand: the tests, the demo (always on Wi-Fi, whatever the
/// Mac's network), and `-sheepdogNoNetwork` for the "No network" screens.
@MainActor
public final class ManualNetworkPath: NetworkPathSource {
    public private(set) var path: NetworkPath
    private var onChange: (@MainActor (NetworkPath) -> Void)?

    public init(_ path: NetworkPath = NetworkPath(satisfied: true, interface: .wifi)) {
        self.path = path
    }

    public func start(_ onChange: @escaping @MainActor (NetworkPath) -> Void) {
        self.onChange = onChange
    }

    public func stop() {
        onChange = nil
    }

    /// The network changes to `path`.
    public func set(_ path: NetworkPath) {
        guard path != self.path else { return }
        self.path = path
        onChange?(path)
    }
}
