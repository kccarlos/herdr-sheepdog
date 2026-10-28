import AppIntents
import Foundation

/// Where the App Intents hand their work to the app. Every intent is a
/// `sheepdog://` route (SheepdogKit `AppRoute`); the app installs the
/// handler once its session model exists, and a route that arrives before
/// that (a cold launch from Siri or the Action Button) waits here.
///
/// Compiled into the widget extension too, so its buttons and control can
/// name the intents; there the handler is never installed, and the system
/// runs these intents in the app's process anyway (`LiveActivityIntent`,
/// `openAppWhenRun`).
@MainActor
enum SheepdogIntentBridge {
    private static var handler: ((URL) -> Void)?
    private static var pending: [URL] = []

    static func send(_ url: URL) {
        if let handler {
            handler(url)
        } else {
            pending.append(url)
        }
    }

    /// Installs the app's handler and hands it whatever arrived first.
    static func install(_ handler: @escaping (URL) -> Void) {
        self.handler = handler
        let queued = pending
        pending.removeAll()
        queued.forEach(handler)
    }

    static func route(_ path: String) -> URL {
        URL(string: "sheepdog://\(path)")!
    }
}

/// "Talk to Sheepdog": opens the app and starts the voice session (or
/// unmutes the one that runs). Siri, Shortcuts, the Action Button and the
/// Control Center control all run this.
struct TalkToSheepdogIntent: AppIntent {
    static let title: LocalizedStringResource = "Talk to Sheepdog"
    static let description = IntentDescription("Opens Sheepdog and starts listening, so you can talk to your agents.")
    static let openAppWhenRun = true

    @MainActor
    func perform() async throws -> some IntentResult {
        SheepdogIntentBridge.send(SheepdogIntentBridge.route("talk"))
        return .result()
    }
}

/// The Live Activity's Mute / Unmute button.
struct SetVoiceMuteIntent: LiveActivityIntent {
    static let title: LocalizedStringResource = "Mute Sheepdog"
    static let description = IntentDescription("Turns the microphone of the voice session off or on.")
    static let isDiscoverable = false

    @Parameter(title: "Muted")
    var muted: Bool

    init() {}

    init(muted: Bool) {
        self.muted = muted
    }

    @MainActor
    func perform() async throws -> some IntentResult {
        SheepdogIntentBridge.send(SheepdogIntentBridge.route("mute?on=\(muted ? "true" : "false")"))
        return .result()
    }
}

/// The Live Activity's mic button in hold-to-talk mode. The lock screen
/// cannot hold a button, so it is tap-to-talk: the first tap opens the mic
/// gate, the next closes it and sends (as releasing the orb does).
struct ToggleTalkIntent: LiveActivityIntent {
    static let title: LocalizedStringResource = "Talk to Sheepdog"
    static let description = IntentDescription("Starts talking; tap again to send.")
    static let isDiscoverable = false

    init() {}

    @MainActor
    func perform() async throws -> some IntentResult {
        SheepdogIntentBridge.send(SheepdogIntentBridge.route("toggle-talk"))
        return .result()
    }
}

/// The Live Activity's End button: ends the voice session.
struct EndVoiceSessionIntent: LiveActivityIntent {
    static let title: LocalizedStringResource = "End the Sheepdog session"
    static let description = IntentDescription("Ends the voice session with Sheepdog.")
    static let isDiscoverable = false

    init() {}

    @MainActor
    func perform() async throws -> some IntentResult {
        SheepdogIntentBridge.send(SheepdogIntentBridge.route("end"))
        return .result()
    }
}
