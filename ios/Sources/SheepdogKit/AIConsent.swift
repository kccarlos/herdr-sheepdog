import Foundation

/// Whether the person has agreed that what they say, type and attach goes
/// to the AI services their own computer is set up with.
///
/// The app developer receives nothing: the voice and the text go to the
/// sheepdog program on the person's computer, and from there to the voice
/// and orchestrator services the person configured. Because that is a
/// third party, the app asks once (App Review Guidelines 5.1.2(i)) and lets
/// the person take the answer back at any time. Stored per install in
/// `UserDefaults`, and versioned: a material change to what is sent raises
/// `currentVersion`, and everyone is asked again.
public final class AIConsent: @unchecked Sendable {
    /// Raise this when the consent text changes in a way that matters
    /// (a new kind of data, a new kind of recipient).
    public static let currentVersion = 1

    /// What the gate says about sending now.
    public enum Decision: Equatable, Sendable {
        /// Agreed (to this version) and not withdrawn.
        case allowed
        /// Never asked, or asked before an older version of the text.
        case needsConsent
        /// Agreed once and turned off since.
        case withdrawn
    }

    private enum Key {
        static let version = "privacy.aiConsentVersion"
        static let withdrawn = "privacy.aiConsentWithdrawn"
    }

    private let defaults: UserDefaults

    public init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    /// The version of the text the person agreed to, nil if never.
    public var agreedVersion: Int? {
        defaults.object(forKey: Key.version) as? Int
    }

    public var decision: Decision {
        guard let agreed = agreedVersion, agreed >= Self.currentVersion else { return .needsConsent }
        return defaults.bool(forKey: Key.withdrawn) ? .withdrawn : .allowed
    }

    public var isAllowed: Bool { decision == .allowed }

    /// The person tapped Agree (or turned the switch on).
    public func agree() {
        defaults.set(Self.currentVersion, forKey: Key.version)
        defaults.set(false, forKey: Key.withdrawn)
    }

    /// Back to never asked (a UI test's clean slate).
    public func forget() {
        defaults.removeObject(forKey: Key.version)
        defaults.removeObject(forKey: Key.withdrawn)
    }

    /// The person turned "Send to AI services" off. Keeps the version, so
    /// the app can tell "withdrawn" from "never asked".
    public func withdraw() {
        defaults.set(true, forKey: Key.withdrawn)
    }
}

/// The AI services named in the consent text, from what the computer's
/// daemon reports (`/api/hello` `voice_provider`, `/api/state` `brain`).
public enum AIServices {
    /// A provider id as a name a person knows: "gemini" is Google Gemini.
    public static func displayName(_ id: String) -> String? {
        let key = id.lowercased().trimmingCharacters(in: .whitespaces)
        switch key {
        case "", "off", "none": return nil
        case "gemini", "google", "vertex": return "Google Gemini"
        case "openai": return "OpenAI"
        case "anthropic", "claude": return "Anthropic Claude"
        case "deepseek": return "DeepSeek"
        case "ollama", "local": return "a model on your own computer"
        default: return id.trimmingCharacters(in: .whitespaces)
        }
    }

    /// The services this computer uses, as one phrase: "Google Gemini
    /// (voice) and DeepSeek (answers)". The generic words when the daemon
    /// has not said (not connected yet, an older daemon).
    public static func summary(voice: String?, brain: String?) -> String {
        let voiceName = voice.flatMap(displayName)
        let brainName = brain.flatMap(displayName)
        switch (voiceName, brainName) {
        case let (voice?, brain?) where voice == brain:
            return "\(voice) (voice and answers)"
        case let (voice?, brain?):
            return "\(voice) (voice) and \(brain) (answers)"
        case let (voice?, nil):
            return "\(voice) (voice)"
        case let (nil, brain?):
            return "\(brain) (answers)"
        case (nil, nil):
            return "the AI voice and answer services you set up, such as Google Gemini, DeepSeek or OpenAI"
        }
    }
}
