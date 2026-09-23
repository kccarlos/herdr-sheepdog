import SheepdogKit
import SwiftUI

@main
struct SheepdogApp: App {
    @StateObject private var model = SessionViewModel(settings: SheepdogApp.settings())

    /// The UI walkthrough that drives this app against a `--voice=off`
    /// daemon starts from a clean slate, so a run never depends on what the
    /// last one saved. Debug builds only.
    private static func settings() -> SettingsStore {
        let store = SettingsStore()
        #if DEBUG
        let arguments = ProcessInfo.processInfo.arguments
        if arguments.contains("-sheepdogResetState") {
            store.address = ""
            store.token = ""
            store.hasBeenConfigured = false
            store.keepListeningWhenLocked = true
            try? FileManager.default.removeItem(at: ConversationCache.standard().directory)
            UserDefaults.standard.removeObject(forKey: "hint.mute.shown")
        }
        // `-sheepdogAddress 127.0.0.1:7799`: start configured, for the
        // history walkthrough and screenshots.
        if let i = arguments.firstIndex(of: "-sheepdogAddress"), i + 1 < arguments.count {
            store.address = arguments[i + 1]
            store.hasBeenConfigured = true
        }
        #endif
        return store
    }

    var body: some Scene {
        WindowGroup {
            MainView()
                .environmentObject(model)
        }
    }
}
