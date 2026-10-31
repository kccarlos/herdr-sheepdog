import AppKit
import SheepdogKit
import SwiftUI

/// Sheepdog for the Mac: a menu-bar item, no windows and no Dock icon
/// (`LSUIElement`). The app has no scene of its own; everything is in the
/// delegate. docs/dev/design/desktop-client.md section 5.
@main
struct SheepdogMacApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate

    var body: some Scene {
        // SwiftUI wants a scene; an empty Settings scene opens nothing.
        Settings { EmptyView() }
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var controller: StatusItemController?
    private var voice: MacVoiceHost?
    private var notifications: NotificationService?
    private var routes: RouteHandler?

    /// Set up before LaunchServices hands over a `sheepdog://talk` that
    /// started the app.
    func applicationWillFinishLaunching(_ notification: Notification) {
        // The unit tests are hosted in this app. They use fakes, and must
        // not put an icon in the menu bar, open the microphone or ask a
        // daemon anything.
        guard ProcessInfo.processInfo.environment["XCTestConfigurationFilePath"] == nil else { return }
        let locator = DaemonLocator()
        let log = AppLog()
        let panel = PanelOpener()
        let voice = MacVoiceHost(
            channel: VoiceSocket(endpoint: locator.endpoint),
            audio: MacAudioEngine(log: log),
            prober: DaemonProbe(locator: locator),
            microphone: SystemMicrophonePermission(),
            settings: SystemSettingsOpener(),
            timers: TaskTimers(),
            log: log
        )
        let notifications = NotificationService(panel: panel, log: log)
        let source = DaemonStatusMonitor(locator: locator)
        source.onEvent = { [weak voice] event in
            notifications.receive(event, voice: voice?.state ?? VoiceMenuState())
        }
        let controller = StatusItemController(
            source: source,
            voice: voice,
            panel: panel,
            hotkey: HotkeyService(),
            loginItem: LoginItem(),
            log: log
        )
        controller.install()
        self.voice = voice
        self.notifications = notifications
        self.controller = controller
        routes = RouteHandler(voice: voice, log: log)
    }

    /// `sheepdog://…`, from LaunchServices (the panel's `open`, a link).
    func application(_ application: NSApplication, open urls: [URL]) {
        urls.forEach { routes?.handle($0) }
    }
}
