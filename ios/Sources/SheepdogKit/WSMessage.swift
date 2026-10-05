import Foundation

/// The JSON control messages exchanged over the daemon's `/ws`.
///
/// A direct mirror of the daemon's `internal/voice/wsproto` package and of
/// `docs/ws-contract.md`. Binary frames carry raw PCM16 and need no type;
/// every text frame is one `WSMessage`.
///
/// Client → server: `client-ready`, `mute`, `silence`, `speaking`,
/// `playback`, `cancel`, `text`, `ping`, `screen-subscribe`,
/// `screen-unsubscribe`.
/// Server → client: `bot-ready`, `flush`, `status`, `transcript`, `error`,
/// `pong`, `screen`, `screen-error`.
public struct WSMessage: Codable, Equatable, Sendable {
    /// Message type discriminator. Mirrors `wsproto.Type*`.
    public enum Kind: String, Codable, Sendable {
        case clientReady = "client-ready"
        case mute, silence, speaking, playback, cancel, text, ping
        case botReady = "bot-ready"
        case flush, status, transcript, error, pong
        /// The agent window over `/ws` (feature `screen`).
        case screenSubscribe = "screen-subscribe"
        case screenUnsubscribe = "screen-unsubscribe"
        case screen
        case screenError = "screen-error"
    }

    /// Who is talking, on `client-ready` (docs/ws-contract.md "Protocol
    /// version and handshake"). Mirrors `wsproto.ClientInfo`.
    public struct ClientInfo: Codable, Equatable, Sendable {
        public var name: String
        public var version: String
        public var platform: String

        public init(name: String, version: String, platform: String) {
            self.name = name
            self.version = version
            self.platform = platform
        }

        /// This app: "sheepdog-ios", "0.1.0 (10)", "ios".
        public static func app(bundle: Bundle = .main) -> ClientInfo {
            let short = bundle.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "?"
            let build = bundle.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "?"
            return ClientInfo(name: "sheepdog-ios", version: "\(short) (\(build))", platform: "ios")
        }
    }

    /// The daemon, on `bot-ready`. Mirrors `wsproto.DaemonInfo`.
    public struct DaemonInfo: Codable, Equatable, Sendable {
        public var version: String

        public init(version: String) {
            self.version = version
        }
    }

    /// What a `playback` report says the player did (docs/ws-contract.md
    /// "Playback reports"). Mirrors `wsproto.Playback*`.
    public enum PlaybackState: String, Codable, Sendable {
        /// Began playing after being empty (not on an underrun resume).
        case started
        /// Ran empty and stayed empty for the underrun window.
        case drained
        /// Dropped everything: every daemon `flush`, and a local stop.
        case flushed
    }

    /// Voice session states carried by a `status` message.
    public enum Voice: String, Codable, Sendable {
        case connecting, connected, reconnecting, closed
    }

    /// Transcript direction: `in` is what the model heard, `out` what it says.
    public enum Direction: String, Codable, Sendable {
        case incoming = "in"
        case outgoing = "out"
    }

    public var type: Kind
    /// `client-ready` / `bot-ready`: the protocol the client speaks, or
    /// the one the daemon will speak on this connection; `bot-ready` adds
    /// the oldest client protocol it serves, its version and features.
    public var `protocol`: Int?
    public var minProtocol: Int?
    public var client: ClientInfo?
    public var daemon: DaemonInfo?
    public var features: [String]?
    /// `mute` / `silence` / `speaking`.
    public var on: Bool?
    /// `playback`: what the player did, and the milliseconds of the run
    /// actually played, in stream time (24 kHz audio consumed, not wall
    /// clock). Required for `drained` and `flushed`.
    public var state: PlaybackState?
    public var playedMs: Int?
    /// `ping` counters, mirrored into the daemon's `web.client.stats`.
    public var gatedFrames: Int?
    public var peak: Int?
    /// `ping`: the playback buffer, as the browser page reports it —
    /// audible gaps (`underruns`), every time the player ran dry
    /// (`drains`), the audio buffered right now in milliseconds
    /// (`buffer_ms`), the current time-stretch `rate`, and the total
    /// wall-clock milliseconds played at a rate other than 1.0
    /// (`stretched_ms`). `nil` on older builds' pings — omitted from the
    /// wire, not sent as zero.
    public var underruns: Int?
    public var drains: Int?
    public var bufferMs: Int?
    public var rate: Double?
    public var stretchedMs: Int?
    /// `ping`: the audio engine's state (`running`, `suspended`,
    /// `interrupted`) — the browser's AudioContext state, the app's
    /// equivalent.
    public var audioState: String?
    /// `text` (client → server) and `transcript` (server → client).
    public var text: String?
    /// `status`.
    public var voice: Voice?
    public var muted: Bool?
    public var reason: String?
    public var stalled: Bool?
    /// `status`: the live session's provider (`gemini`, `openai`), from
    /// daemons that can switch provider.
    public var provider: String?
    /// `transcript`.
    public var dir: Direction?
    public var final: Bool?
    /// `error` and `screen-error`.
    public var message: String?
    /// `screen-subscribe` (`pane_id`, `lines`), `screen` (the window, the
    /// body of `GET /api/agents/{pane}/screen`) and `screen-error`
    /// (`pane_id`; `ended` when the subscription is over).
    public var paneID: String?
    public var lines: Int?
    public var screen: AgentScreen?
    public var ended: Bool?

    enum CodingKeys: String, CodingKey {
        case type, on, state, text, voice, muted, reason, stalled, dir, final, message, peak, provider, underruns, drains, rate
        case `protocol`, client, daemon, features
        case minProtocol = "min_protocol"
        case playedMs = "played_ms"
        case gatedFrames = "gated_frames"
        case bufferMs = "buffer_ms"
        case stretchedMs = "stretched_ms"
        case audioState = "audio_state"
        case paneID = "pane_id"
        case lines, screen, ended
    }

    public init(
        type: Kind,
        protocol: Int? = nil,
        minProtocol: Int? = nil,
        client: ClientInfo? = nil,
        daemon: DaemonInfo? = nil,
        features: [String]? = nil,
        on: Bool? = nil,
        state: PlaybackState? = nil,
        playedMs: Int? = nil,
        gatedFrames: Int? = nil,
        peak: Int? = nil,
        underruns: Int? = nil,
        drains: Int? = nil,
        bufferMs: Int? = nil,
        rate: Double? = nil,
        stretchedMs: Int? = nil,
        audioState: String? = nil,
        text: String? = nil,
        voice: Voice? = nil,
        muted: Bool? = nil,
        reason: String? = nil,
        stalled: Bool? = nil,
        provider: String? = nil,
        dir: Direction? = nil,
        final: Bool? = nil,
        message: String? = nil,
        paneID: String? = nil,
        lines: Int? = nil,
        screen: AgentScreen? = nil,
        ended: Bool? = nil
    ) {
        self.type = type
        self.protocol = `protocol`
        self.minProtocol = minProtocol
        self.client = client
        self.daemon = daemon
        self.features = features
        self.on = on
        self.state = state
        self.playedMs = playedMs
        self.gatedFrames = gatedFrames
        self.peak = peak
        self.underruns = underruns
        self.drains = drains
        self.bufferMs = bufferMs
        self.rate = rate
        self.stretchedMs = stretchedMs
        self.audioState = audioState
        self.text = text
        self.voice = voice
        self.muted = muted
        self.reason = reason
        self.stalled = stalled
        self.provider = provider
        self.dir = dir
        self.final = final
        self.message = message
        self.paneID = paneID
        self.lines = lines
        self.screen = screen
        self.ended = ended
    }

    // MARK: - Client → server constructors

    /// The handshake's first frame (docs/ws-contract.md "Protocol version
    /// and handshake").
    public static func clientReady(protocol p: Int = ProtocolHandshake.protocolVersion, client: ClientInfo) -> WSMessage {
        WSMessage(type: .clientReady, protocol: p, client: client)
    }

    public static func mute(_ on: Bool) -> WSMessage { WSMessage(type: .mute, on: on) }
    public static func silence(_ on: Bool) -> WSMessage { WSMessage(type: .silence, on: on) }
    /// The app's own speech detector opened (`true`) or closed.
    public static func speaking(_ on: Bool) -> WSMessage { WSMessage(type: .speaking, on: on) }
    /// A playback report; `playedMs` is stream time, never negative.
    public static func playback(_ state: PlaybackState, playedMs: Int) -> WSMessage {
        WSMessage(type: .playback, state: state, playedMs: max(0, playedMs))
    }
    public static let cancel = WSMessage(type: .cancel)
    public static func ping(
        gatedFrames: Int = 0,
        peak: Int = 0,
        underruns: Int? = nil,
        drains: Int? = nil,
        bufferMs: Int? = nil,
        rate: Double? = nil,
        stretchedMs: Int? = nil,
        audioState: String? = nil
    ) -> WSMessage {
        WSMessage(
            type: .ping,
            gatedFrames: gatedFrames,
            peak: peak,
            underruns: underruns,
            drains: drains,
            bufferMs: bufferMs,
            rate: rate,
            stretchedMs: stretchedMs,
            audioState: audioState
        )
    }

    /// Show me this agent's window (`screen` frames follow).
    public static func screenSubscribe(paneID: String, lines: Int? = nil) -> WSMessage {
        WSMessage(type: .screenSubscribe, paneID: paneID, lines: lines)
    }
    public static let screenUnsubscribe = WSMessage(type: .screenUnsubscribe)

    // MARK: - Server → client constructors (tests and fakes)

    public static func botReady(protocol p: Int, minProtocol: Int = 1, daemonVersion: String, features: [String] = []) -> WSMessage {
        WSMessage(type: .botReady, protocol: p, minProtocol: minProtocol, daemon: DaemonInfo(version: daemonVersion), features: features)
    }
    public static let flush = WSMessage(type: .flush)
    public static func status(_ voice: Voice, muted: Bool = false, reason: String? = nil, stalled: Bool? = nil, provider: String? = nil) -> WSMessage {
        WSMessage(type: .status, voice: voice, muted: muted, reason: reason, stalled: stalled, provider: provider)
    }
    public static func transcript(_ dir: Direction, _ text: String, final: Bool) -> WSMessage {
        WSMessage(type: .transcript, text: text, dir: dir, final: final)
    }
    public static func error(_ message: String) -> WSMessage { WSMessage(type: .error, message: message) }
    public static let pong = WSMessage(type: .pong)
    public static func screen(_ screen: AgentScreen) -> WSMessage { WSMessage(type: .screen, screen: screen) }
    public static func screenError(paneID: String, _ message: String, ended: Bool? = nil) -> WSMessage {
        WSMessage(type: .screenError, message: message, paneID: paneID, ended: ended)
    }

    // MARK: - Wire format

    /// Decodes one text frame. Unknown `type` values throw, mirroring
    /// `wsproto.Parse`; the caller keeps the connection open.
    public static func decode(_ data: Data) throws -> WSMessage {
        // The daemon's decoder: a `screen` frame carries an RFC 3339 date.
        let m = try DaemonClient.decoder.decode(WSMessage.self, from: data)
        try m.validate()
        return m
    }

    /// Encodes one text frame. `nil` fields are omitted, exactly as the Go
    /// side's `omitempty` does, so the daemon sees the same bytes the
    /// browser sends.
    public func encode() throws -> Data {
        try validate()
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        encoder.dateEncodingStrategy = .iso8601
        return try encoder.encode(self)
    }

    /// Mirrors `wsproto.Message.Validate`: the fields the type requires.
    public func validate() throws {
        switch type {
        case .clientReady:
            if `protocol` == nil { throw WSMessageError.missingField("client-ready", "protocol") }
        case .botReady:
            if `protocol` == nil { throw WSMessageError.missingField("bot-ready", "protocol") }
            if daemon == nil { throw WSMessageError.missingField("bot-ready", "daemon") }
        case .mute, .silence, .speaking:
            if on == nil { throw WSMessageError.missingField(type.rawValue, "on") }
        case .playback:
            switch state {
            case .started?:
                break
            case .drained?, .flushed?:
                if playedMs == nil { throw WSMessageError.missingField("playback", "played_ms") }
            case nil:
                throw WSMessageError.badValue("playback", "state")
            }
            if let playedMs, playedMs < 0 { throw WSMessageError.badValue("playback", "played_ms") }
        case .text:
            if (text ?? "").isEmpty { throw WSMessageError.missingField("text", "text") }
        case .status:
            if voice == nil { throw WSMessageError.badValue("status", "voice") }
        case .transcript:
            if dir == nil { throw WSMessageError.badValue("transcript", "dir") }
        case .error:
            if (message ?? "").isEmpty { throw WSMessageError.missingField("error", "message") }
        case .screenSubscribe:
            if (paneID ?? "").isEmpty { throw WSMessageError.missingField("screen-subscribe", "pane_id") }
            if let lines, lines < 1 { throw WSMessageError.badValue("screen-subscribe", "lines") }
        case .screen:
            if screen == nil { throw WSMessageError.missingField("screen", "screen") }
        case .screenError:
            if (paneID ?? "").isEmpty { throw WSMessageError.missingField("screen-error", "pane_id") }
            if (message ?? "").isEmpty { throw WSMessageError.missingField("screen-error", "message") }
        case .cancel, .ping, .flush, .pong, .screenUnsubscribe:
            break
        }
    }
}

public enum WSMessageError: Error, Equatable, CustomStringConvertible {
    case missingField(String, String)
    case badValue(String, String)

    public var description: String {
        switch self {
        case let .missingField(type, field): return "wsproto: \(type): missing \"\(field)\""
        case let .badValue(type, field): return "wsproto: \(type): bad \(field)"
        }
    }
}

/// Audio contract of the binary frames (docs/ws-contract.md).
public enum AudioContract {
    /// Sample rate of client → server PCM16 frames.
    public static let captureRate: Double = 16_000
    /// Sample rate of server → client PCM16 frames.
    public static let playbackRate: Double = 24_000
    /// One capture frame: 40 ms at 16 kHz = 640 samples = 1 280 bytes.
    public static let frameSamples = 640
    public static let frameMilliseconds = 40
}
