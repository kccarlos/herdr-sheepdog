import Foundation

/// What the Settings sheet owns: where the daemon is, its token (in the
/// keychain, never in defaults), and the two switches.
public final class SettingsStore {
    private enum Key {
        static let address = "daemon.address"
        static let keepListening = "daemon.keepListeningLocked"
        static let hasBeenConfigured = "daemon.configured"
        static let tokenAccount = "daemon.token"
    }

    private let defaults: UserDefaults
    private let secrets: SecretStore

    public init(defaults: UserDefaults = .standard, secrets: SecretStore = KeychainSecretStore()) {
        self.defaults = defaults
        self.secrets = secrets
    }

    /// The address exactly as typed, e.g. `192.168.8.141:7777`.
    public var address: String {
        get { defaults.string(forKey: Key.address) ?? "" }
        set { defaults.set(newValue, forKey: Key.address) }
    }

    /// The daemon's `web.token`. Keychain-backed.
    public var token: String {
        get { secrets.secret(for: Key.tokenAccount) ?? "" }
        set { secrets.setSecret(newValue, for: Key.tokenAccount) }
    }

    /// Keep the audio session alive with the screen locked.
    public var keepListeningWhenLocked: Bool {
        get { defaults.object(forKey: Key.keepListening) as? Bool ?? true }
        set { defaults.set(newValue, forKey: Key.keepListening) }
    }

    /// False until the user has saved an address once; the first launch
    /// opens Settings on the strength of it.
    public var hasBeenConfigured: Bool {
        get { defaults.bool(forKey: Key.hasBeenConfigured) }
        set { defaults.set(newValue, forKey: Key.hasBeenConfigured) }
    }

    /// The endpoint, or nil while the address is empty or unparseable.
    public var endpoint: DaemonEndpoint? {
        DaemonEndpoint(text: address, token: token)
    }
}
