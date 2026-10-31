import AppKit
import SheepdogKit

/// One row of the status menu. The menu is built from these, so the layout
/// and what is enabled are tested without an `NSMenu`.
public struct MenuItemSpec: Equatable {
    public enum Action: Equatable {
        case openPanel
        case toggleLoginItem
        case quit
        /// Talk, Mute, Stop and Use here: PR 9 gives them an action.
        case voice
    }

    public enum Row: Equatable {
        case status(String)
        case item(title: String, action: Action, enabled: Bool, checked: Bool)
        case separator
    }
}

/// The menu-bar item: a template icon (with the "needs you" count beside it)
/// and the menu (docs/dev/design/desktop-client.md section 5.4). No window,
/// no Dock icon. The talk, mute, stop and use-here rows are shown disabled
/// until PR 9 brings the voice session.
public final class StatusItemController: NSObject {
    public static func rows(for state: MenuBarState, loginItemEnabled: Bool) -> [MenuItemSpec.Row] {
        [
            .status(state.line),
            .separator,
            .item(title: "Open panel", action: .openPanel, enabled: true, checked: false),
            .separator,
            .item(title: "Talk", action: .voice, enabled: false, checked: false),
            .item(title: "Mute", action: .voice, enabled: false, checked: false),
            .item(title: "Stop", action: .voice, enabled: false, checked: false),
            .item(title: "Use here", action: .voice, enabled: false, checked: false),
            .separator,
            .item(title: "Launch at login", action: .toggleLoginItem, enabled: true, checked: loginItemEnabled),
            .item(title: "Quit Sheepdog", action: .quit, enabled: true, checked: false),
        ]
    }

    private let source: DaemonStatusSource
    private let panel: PanelOpening
    private let loginItem: LoginItemControlling
    private let log: AppLogging
    private let quit: () -> Void
    private var statusItem: NSStatusItem?

    /// The state the menu shows; the daemon is "down" until it answers.
    public private(set) var state = MenuBarState(state: nil)

    public init(source: DaemonStatusSource, panel: PanelOpening, loginItem: LoginItemControlling, log: AppLogging, quit: @escaping () -> Void = { NSApp.terminate(nil) }) {
        self.source = source
        self.panel = panel
        self.loginItem = loginItem
        self.log = log
        self.quit = quit
        super.init()
    }

    /// Puts the item in the menu bar and starts following the daemon.
    public func install() {
        let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        statusItem = item
        if let button = item.button {
            let image = NSImage(named: "MenuBarIcon")
            image?.isTemplate = true
            image?.size = NSSize(width: 18, height: 18)
            button.image = image
            button.imagePosition = .imageLeading
        }
        render()
        source.onChange = { [weak self] daemon in
            self?.update(daemon)
        }
        source.start()
    }

    public func update(_ daemon: DaemonState?) {
        state = MenuBarState(state: daemon)
        render()
    }

    public func perform(_ action: MenuItemSpec.Action) {
        switch action {
        case .openPanel:
            do {
                try panel.openPanel()
            } catch {
                log.log("could not run herdr-app: \(error.localizedDescription)")
            }
        case .toggleLoginItem:
            do {
                try loginItem.setEnabled(!loginItem.isEnabled)
            } catch {
                log.log("could not change launch at login: \(error.localizedDescription)")
            }
            render()
        case .quit:
            source.stop()
            quit()
        case .voice:
            break
        }
    }

    private func render() {
        guard let item = statusItem else { return }
        if let button = item.button {
            button.title = state.needsYou > 0 ? " \(state.needsYou)" : ""
            button.appearsDisabled = state.isDimmed
            button.setAccessibilityLabel(state.accessibilityLabel)
        }
        item.menu = menu()
    }

    private func menu() -> NSMenu {
        let menu = NSMenu()
        menu.autoenablesItems = false
        for row in Self.rows(for: state, loginItemEnabled: loginItem.isEnabled) {
            switch row {
            case let .status(text):
                let line = NSMenuItem(title: text, action: nil, keyEquivalent: "")
                line.isEnabled = false
                menu.addItem(line)
            case let .item(title, action, enabled, checked):
                let entry = NSMenuItem(title: title, action: #selector(menuItemChosen(_:)), keyEquivalent: "")
                entry.target = self
                entry.isEnabled = enabled
                entry.state = checked ? .on : .off
                entry.representedObject = ActionBox(action)
                menu.addItem(entry)
            case .separator:
                menu.addItem(.separator())
            }
        }
        return menu
    }

    @objc private func menuItemChosen(_ sender: NSMenuItem) {
        guard let box = sender.representedObject as? ActionBox else { return }
        perform(box.action)
    }

    /// `representedObject` takes a class.
    private final class ActionBox {
        let action: MenuItemSpec.Action
        init(_ action: MenuItemSpec.Action) { self.action = action }
    }
}
