import AppKit
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
    private let routes = RouteHandler(log: AppLog())

    func applicationDidFinishLaunching(_ notification: Notification) {
        // The unit tests are hosted in this app. They use fakes, and must
        // not put an icon in the menu bar or ask a daemon anything.
        guard ProcessInfo.processInfo.environment["XCTestConfigurationFilePath"] == nil else { return }
        let controller = StatusItemController(
            source: DaemonStatusMonitor(locator: DaemonLocator()),
            panel: PanelOpener(),
            loginItem: LoginItem(),
            log: AppLog()
        )
        controller.install()
        self.controller = controller
    }

    /// `sheepdog://…`, from LaunchServices (the panel's `open`, a link).
    func application(_ application: NSApplication, open urls: [URL]) {
        urls.forEach(routes.handle)
    }
}
