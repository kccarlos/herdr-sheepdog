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
        if ProcessInfo.processInfo.arguments.contains("-sheepdogResetState") {
            store.address = ""
            store.token = ""
            store.hasBeenConfigured = false
            store.keepListeningWhenLocked = true
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
