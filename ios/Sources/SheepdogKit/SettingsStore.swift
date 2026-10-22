import Foundation

/// What the Settings sheet owns: where the daemon is, the paired computer
/// (`DeviceCredentials`, in the keychain), and the switches.
public final class SettingsStore {
    private enum Key {
        static let address = "daemon.address"
        static let keepListening = "daemon.keepListeningLocked"
        static let hasBeenConfigured = "daemon.configured"
        static let talkMode = "voice.talkMode"
    }

    private let defaults: UserDefaults
    /// Where the paired computer's record lives: the shared keychain one,
    /// or the demo's own (which must never touch the real one).
    public let credentials: DeviceCredentials

    public init(defaults: UserDefaults = .standard, credentials: DeviceCredentials = .shared) {
        self.defaults = defaults
        self.credentials = credentials
    }

    /// The address the app talks to (`host:port`): the paired computer's
    /// address that answered, or a loopback fixture in the tests.
    public var address: String {
        get { defaults.string(forKey: Key.address) ?? "" }
        set { defaults.set(newValue, forKey: Key.address) }
    }

    /// The paired computer, nil before pairing.
    public var computer: PairedComputer? {
        get { credentials.computer }
        set { credentials.computer = newValue }
    }

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

    /// False until the user has saved an address once; the first launch
    /// opens Settings on the strength of it.
    public var hasBeenConfigured: Bool {
        get { defaults.bool(forKey: Key.hasBeenConfigured) }
        set { defaults.set(newValue, forKey: Key.hasBeenConfigured) }
    }

    /// The endpoint, or nil while the address is empty or unparseable. The
    /// paired computer's is TLS with a ticket.
    public var endpoint: DaemonEndpoint? {
        DaemonEndpoint(text: address, secure: computer != nil)
    }
}
