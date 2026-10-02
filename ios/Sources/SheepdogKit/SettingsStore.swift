import Foundation

/// What the Settings sheet owns: where the daemon is, its token (in the
/// keychain, never in defaults), and the switches.
public final class SettingsStore {
    private enum Key {
        static let address = "daemon.address"
        static let keepListening = "daemon.keepListeningLocked"
        static let hasBeenConfigured = "daemon.configured"
        static let tokenAccount = "daemon.token"
        static let talkMode = "voice.talkMode"
        static let transport = "voice.transport"
        static let bridgeAddress = "bridge.address"
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

    /// Open mic (the default) or hold-to-talk.
    public var talkMode: TalkMode {
        get { defaults.string(forKey: Key.talkMode).flatMap(TalkMode.init(rawValue:)) ?? .openMic }
        set { defaults.set(newValue.rawValue, forKey: Key.talkMode) }
    }

    /// How the voice travels: the daemon's `/ws` (the default) or LiveKit
    /// through sheepdog-bridge.
    public var voiceTransport: VoiceTransportKind {
        get { defaults.string(forKey: Key.transport).flatMap(VoiceTransportKind.init(rawValue:)) ?? .webSocket }
        set { defaults.set(newValue.rawValue, forKey: Key.transport) }
    }

    /// The bridge's address as typed; empty means the daemon's host on
    /// port 7778.
    public var bridgeAddress: String {
        get { defaults.string(forKey: Key.bridgeAddress) ?? "" }
        set { defaults.set(newValue, forKey: Key.bridgeAddress) }
    }

    /// The bridge, with the daemon's token as its bearer; nil while
    /// neither address is usable.
    public var bridgeEndpoint: DaemonEndpoint? {
        LiveKitBridge.endpoint(bridgeAddress: bridgeAddress, daemon: endpoint)
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
