import SheepdogKit
import SwiftUI

@main
struct SheepdogApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var model = SessionViewModel(settings: SheepdogApp.settings())

    /// The UI walkthrough that drives this app against a `--voice=off`
    /// daemon starts from a clean slate, so a run never depends on what the
    /// last one saved. Debug builds only.
    private static func settings() -> SettingsStore {
        let store = SettingsStore()
        #if DEBUG
        if DebugLaunch.resetState {
            store.address = ""
            store.token = ""
            store.hasBeenConfigured = false
            store.keepListeningWhenLocked = true
            try? FileManager.default.removeItem(at: ConversationCache.standard().directory)
            // The one-time hints are cleared by the view model (all of
            // them, as `-sheepdogResetHints` does).
        }
        if let address = DebugLaunch.address {
            store.address = address
            store.hasBeenConfigured = true
        }
        #endif
        return store
    }

    var body: some Scene {
        WindowGroup {
            #if DEBUG
            if ProcessInfo.processInfo.arguments.contains("-sheepdogLiveActivityGallery") {
                LiveActivityGallery()
            } else {
                mainView
            }
            #else
            mainView
            #endif
        }
    }

    private var mainView: some View {
        MainView()
            .environmentObject(model)
            .onAppear { PushCoordinator.shared.model = model }
            // sheepdog://talk, sheepdog://agent/<pane id> (AppRoute).
            .onOpenURL { url in model.open(url: url) }
    }
}
