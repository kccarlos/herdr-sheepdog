import Foundation

/// What Settings owns: the paired computers (`ComputerStore`) and the
/// phone's own switches. The session talks to the current computer through
/// `address`, `computer`, `credentials` and `endpoint`.
public final class SettingsStore {
    private enum Key {
        static let keepListening = "daemon.keepListeningLocked"
        static let hasBeenConfigured = "daemon.configured"
        static let talkMode = "voice.talkMode"
    }

    private let defaults: UserDefaults
    /// The paired computers: the shared keychain ones, or the demo's own
    /// (which must never touch the real ones).
    public let computers: ComputerStore

    public init(defaults: UserDefaults = .standard, computers: ComputerStore = .shared) {
        self.defaults = defaults
        self.computers = computers
    }

    /// The address the app talks to (`host:port`): the current computer's
    /// address in use, or a loopback fixture with no computer in the tests.
    /// Setting it changes the current computer's address in use.
    public var address: String {
        get {
            if let id = computers.currentID { return computers.address(for: id) }
            return computers.looseAddress
        }
        set {
            if let id = computers.currentID {
                computers.setAddress(newValue, for: id)
            } else {
                computers.looseAddress = newValue
            }
        }
    }

    /// The current computer, nil before pairing.
    public var computer: PairedComputer? { computers.current }

    /// The current computer's credentials, nil before pairing.
    public var credentials: DeviceCredentials? { computers.currentID.map(computers.credentials(for:)) }

    /// Keep the audio session alive with the screen locked.
    public var keepListeningWhenLocked: Bool {
        get { defaults.object(forKey: Key.keepListening) as? Bool ?? true }
        set { defaults.set(newValue, forKey: Key.keepListening) }
    }

    /// Open mic (the default) or hold-to-talk.
    public var talkMode: TalkMode {
        get { defaults.string(forKey: Key.talkMode).flatMap(TalkMode.init(rawValue:)) ?? .openMic }
        set { defaults.set(newValue.rawValue, forKey: Key.talkMode) }
    }

    /// False until the user has paired (or a test set an address); the
    /// first launch opens Settings on the strength of it.
    public var hasBeenConfigured: Bool {
        get { defaults.bool(forKey: Key.hasBeenConfigured) }
        set { defaults.set(newValue, forKey: Key.hasBeenConfigured) }
    }

    /// The current computer's endpoint, or the loose address's, nil while
    /// there is neither.
    public var endpoint: DaemonEndpoint? {
        if let id = computers.currentID { return computers.endpoint(for: id) }
        return DaemonEndpoint(text: computers.looseAddress)
    }
}
