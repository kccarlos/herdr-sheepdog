import SheepdogKit
import SwiftUI

@main
struct SheepdogApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var model = SessionViewModel(settings: SheepdogApp.settings())
    /// The splash covers the first moment after the launch screen, then
    /// fades. Touches pass through it.
    @State private var showsSplash = true
    /// Until the splash has faded: sheets wait for it.
    @State private var splashCovering = true

    /// The UI walkthrough that drives this app against a `--voice=off`
    /// daemon starts from a clean slate, so a run never depends on what the
    /// last one saved. Debug builds only.
    private static func settings() -> SettingsStore {
        let store = SettingsStore()
        #if DEBUG
        if DebugLaunch.resetState {
            store.address = ""
            store.credentials.forget()
            store.hasBeenConfigured = false
            store.keepListeningWhenLocked = true
            // A test that tried Hold to talk must not leave it on for the
            // next one (ReconnectUITests waits for "Listening").
            store.talkMode = .openMic
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
            } else if let i = ProcessInfo.processInfo.arguments.firstIndex(of: "-sheepdogShowPrimer"),
                      i + 1 < ProcessInfo.processInfo.arguments.count,
                      let primer = PermissionPrimer(rawValue: ProcessInfo.processInfo.arguments[i + 1]) {
                PermissionPrimerView(primer: primer) { _ in }
            } else if let i = ProcessInfo.processInfo.arguments.firstIndex(of: "-sheepdogPairingScreen"),
                      i + 1 < ProcessInfo.processInfo.arguments.count {
                // The pairing screens for screenshots (no camera needed).
                PairingFlowView(stage: Self.pairingStage(ProcessInfo.processInfo.arguments[i + 1]))
                    .environmentObject(model)
            } else if ProcessInfo.processInfo.arguments.contains("-sheepdogCapsuleGallery") {
                CapsuleGallery()
            } else {
                mainView
            }
            #else
            mainView
            #endif
        }
    }

    #if DEBUG
    private static func pairingStage(_ name: String) -> PairingStage {
        switch name {
        case "primer": return .primer
        case "denied": return .denied
        case "noCamera": return .noCamera
        case "scanning": return .scanning
        case "confirm": return .confirm(PairingCode(daemonID: "d", name: "Studio Mac", pin: "p", code: "c", addresses: ["192.168.1.20:7777"]))
        case "working": return .working("Studio Mac")
        case "paired": return .paired("Studio Mac")
        case "expired": return .problem(.codeUsed)
        case "revoked": return .problem(.revoked)
        case "pinMismatch": return .problem(.pinMismatch)
        default: return .failed(title: "None of the code's addresses answered", detail: "Tried 192.168.1.20:7777. " + SetupHelp.unreachableFix)
        }
    }
    #endif

    private var mainView: some View {
        content
            .environment(\.splashShowing, splashCovering)
            .overlay {
                if showsSplash {
                    SplashView()
                        .allowsHitTesting(false)
                        .transition(.opacity)
                }
            }
            .task {
                #if DEBUG
                if DebugLaunch.splashHold { return }
                #endif
                try? await Task.sleep(for: .milliseconds(600))
                withAnimation(.easeOut(duration: 0.3)) { showsSplash = false }
                try? await Task.sleep(for: .milliseconds(300))
                splashCovering = false
                #if DEBUG
                // After start() has shown the first-run sheet, which
                // openDemo() closes first.
                if DebugLaunch.openDemo { model.openDemo() }
                #endif
            }
    }

    private var content: some View {
        MainView()
            .environmentObject(model)
            .onAppear { PushCoordinator.shared.model = model }
            // sheepdog://talk, sheepdog://agent/<pane id> (AppRoute).
            .onOpenURL { url in model.open(url: url) }
            // The offline demo (Settings ▸ Try the demo): a second view
            // model on a scripted daemon inside the app, over the real one.
            .fullScreenCover(isPresented: $model.demoActive) {
                DemoShell(close: { model.demoClosed() })
            }
    }
}

/// The offline demo's screen: the app's own main view, on a view model
/// whose daemon is `DemoDaemon`. Closing it drops that view model and
/// everything the demo did.
struct DemoShell: View {
    let close: () -> Void
    @StateObject private var demo = SessionViewModel.makeDemo()

    var body: some View {
        MainView()
            .environmentObject(demo)
            .onAppear { demo.onLeaveDemo = close }
    }
}
