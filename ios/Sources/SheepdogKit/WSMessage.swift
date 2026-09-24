import Foundation

/// The JSON control messages exchanged over the daemon's `/ws`.
///
/// A direct mirror of the daemon's `internal/voice/wsproto` package and of
/// `docs/ws-contract.md`. Binary frames carry raw PCM16 and need no type;
/// every text frame is one `WSMessage`.
///
/// Client → server: `mute`, `silence`, `cancel`, `text`, `ping`.
/// Server → client: `flush`, `status`, `transcript`, `error`, `pong`.
public struct WSMessage: Codable, Equatable, Sendable {
    /// Message type discriminator. Mirrors `wsproto.Type*`.
    public enum Kind: String, Codable, Sendable {
        case mute, silence, cancel, text, ping
        case flush, status, transcript, error, pong
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
    /// `mute` / `silence`.
    public var on: Bool?
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
    /// `error`.
    public var message: String?

    enum CodingKeys: String, CodingKey {
        case type, on, text, voice, muted, reason, stalled, dir, final, message, peak, provider, underruns, drains, rate
        case gatedFrames = "gated_frames"
        case bufferMs = "buffer_ms"
        case stretchedMs = "stretched_ms"
    }

    public init(
        type: Kind,
        on: Bool? = nil,
        gatedFrames: Int? = nil,
        peak: Int? = nil,
        underruns: Int? = nil,
        drains: Int? = nil,
        bufferMs: Int? = nil,
        rate: Double? = nil,
        stretchedMs: Int? = nil,
        text: String? = nil,
        voice: Voice? = nil,
        muted: Bool? = nil,
        reason: String? = nil,
        stalled: Bool? = nil,
        provider: String? = nil,
        dir: Direction? = nil,
        final: Bool? = nil,
        message: String? = nil
    ) {
        self.type = type
        self.on = on
        self.gatedFrames = gatedFrames
        self.peak = peak
        self.underruns = underruns
        self.drains = drains
        self.bufferMs = bufferMs
        self.rate = rate
        self.stretchedMs = stretchedMs
        self.text = text
        self.voice = voice
        self.muted = muted
        self.reason = reason
        self.stalled = stalled
        self.provider = provider
        self.dir = dir
        self.final = final
        self.message = message
    }

    // MARK: - Client → server constructors

    public static func mute(_ on: Bool) -> WSMessage { WSMessage(type: .mute, on: on) }
    public static func silence(_ on: Bool) -> WSMessage { WSMessage(type: .silence, on: on) }
    public static let cancel = WSMessage(type: .cancel)
    public static func simulateText(_ text: String) -> WSMessage { WSMessage(type: .text, text: text) }
    public static func ping(
        gatedFrames: Int = 0,
        peak: Int = 0,
        underruns: Int? = nil,
        drains: Int? = nil,
        bufferMs: Int? = nil,
        rate: Double? = nil,
        stretchedMs: Int? = nil
    ) -> WSMessage {
        WSMessage(
            type: .ping,
            gatedFrames: gatedFrames,
            peak: peak,
            underruns: underruns,
            drains: drains,
            bufferMs: bufferMs,
            rate: rate,
            stretchedMs: stretchedMs
        )
    }

    // MARK: - Server → client constructors (tests and fakes)

    public static let flush = WSMessage(type: .flush)
    public static func status(_ voice: Voice, muted: Bool = false, reason: String? = nil, stalled: Bool? = nil, provider: String? = nil) -> WSMessage {
        WSMessage(type: .status, voice: voice, muted: muted, reason: reason, stalled: stalled, provider: provider)
    }
    public static func transcript(_ dir: Direction, _ text: String, final: Bool) -> WSMessage {
        WSMessage(type: .transcript, text: text, dir: dir, final: final)
    }
    public static func error(_ message: String) -> WSMessage { WSMessage(type: .error, message: message) }
    public static let pong = WSMessage(type: .pong)

    // MARK: - Wire format

    /// Decodes one text frame. Unknown `type` values throw, mirroring
    /// `wsproto.Parse`; the caller keeps the connection open.
    public static func decode(_ data: Data) throws -> WSMessage {
        let m = try JSONDecoder().decode(WSMessage.self, from: data)
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
        return try encoder.encode(self)
    }

    public func encodedString() throws -> String {
        String(decoding: try encode(), as: UTF8.self)
    }

    /// Mirrors `wsproto.Message.Validate`: the fields the type requires.
    public func validate() throws {
        switch type {
        case .mute, .silence:
            if on == nil { throw WSMessageError.missingField(type.rawValue, "on") }
        case .text:
            if (text ?? "").isEmpty { throw WSMessageError.missingField("text", "text") }
        case .status:
            if voice == nil { throw WSMessageError.badValue("status", "voice") }
        case .transcript:
            if dir == nil { throw WSMessageError.badValue("transcript", "dir") }
        case .error:
            if (message ?? "").isEmpty { throw WSMessageError.missingField("error", "message") }
        case .cancel, .ping, .flush, .pong:
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
