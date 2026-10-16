import Foundation

/// One voice provider the daemon can run the live session on, from
/// `GET /api/voice/providers`.
///
/// Lenient on purpose: every field but `id` is optional, so a daemon that
/// is a version ahead or behind still decodes.
public struct VoiceProviderOption: Codable, Equatable, Identifiable, Hashable, Sendable {
    public var id: String
    public var label: String?
    public var model: String?
    /// Missing means available: an older daemon that lists providers
    /// without the flag only lists the ones it can run.
    public var available: Bool?
    /// Why it is unavailable ("no OPENAI_API_KEY"), when it is.
    public var reason: String?
    /// `per_minute` for GPT-Live; absent for the others.
    public var billing: String?

    public init(id: String, label: String? = nil, model: String? = nil, available: Bool? = true, reason: String? = nil, billing: String? = nil) {
        self.id = id
        self.label = label
        self.model = model
        self.available = available
        self.reason = reason
        self.billing = billing
    }

    public var isAvailable: Bool { available ?? true }
    public var billedPerMinute: Bool { billing == "per_minute" }
    public var displayName: String { label?.trimmed.isEmpty == false ? label! : id }
}

/// `GET /api/voice/providers` and the reply to `PUT /api/voice/provider`:
/// the provider the live session uses now, its model, and the choices.
public struct VoiceProviders: Codable, Equatable, Sendable {
    public var provider: String?
    public var model: String?
    public var providers: [VoiceProviderOption]

    public init(provider: String? = nil, model: String? = nil, providers: [VoiceProviderOption] = []) {
        self.provider = provider
        self.model = model
        self.providers = providers
    }

    enum CodingKeys: String, CodingKey {
        case provider, model, providers
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        provider = try c.decodeIfPresent(String.self, forKey: .provider)
        model = try c.decodeIfPresent(String.self, forKey: .model)
        providers = try c.decodeIfPresent([VoiceProviderOption].self, forKey: .providers) ?? []
    }

    public var current: VoiceProviderOption? {
        guard let provider else { return nil }
        return providers.first { $0.id == provider }
    }

    /// "Gemini Live · gemini-3.8-live" — for the connection sheet.
    public var display: String? {
        VoiceProviders.display(provider: current?.displayName ?? provider, model: model ?? current?.model)
    }

    public static func display(provider: String?, model: String?) -> String? {
        let parts = [provider, model].compactMap { $0?.trimmed }.filter { !$0.isEmpty }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
}

/// The phone's microphone mode (Control Center ▸ Mic Mode), as words.
///
/// iOS applies Voice Isolation *before* the app sees a sample, so in a
/// noisy room it is the single biggest help — the detector then only has to
/// tell the user's voice from what little is left.
public enum MicrophoneModeName: String, Equatable, Sendable {
    case standard
    case voiceIsolation
    case wideSpectrum
    case unknown

    /// `AVCaptureDevice.MicrophoneMode.rawValue`: 0 standard, 1 wide
    /// spectrum, 2 voice isolation.
    public init(rawValue: Int) {
        switch rawValue {
        case 0: self = .standard
        case 1: self = .wideSpectrum
        case 2: self = .voiceIsolation
        default: self = .unknown
        }
    }

    public var label: String {
        switch self {
        case .standard: return "Standard"
        case .voiceIsolation: return "Voice Isolation"
        case .wideSpectrum: return "Wide Spectrum"
        case .unknown: return "Unknown"
        }
    }

    /// The footnote under the row: what to choose, and why.
    public var advice: String {
        switch self {
        case .voiceIsolation:
            return "Voice Isolation is on: iOS filters out the room before Sheepdog hears it."
        case .wideSpectrum:
            return "Wide Spectrum picks up the whole room — switch to Voice Isolation unless you are alone in a quiet room."
        case .standard, .unknown:
            return "In a noisy room choose Voice Isolation: iOS filters out the room before Sheepdog hears it."
        }
    }
}
