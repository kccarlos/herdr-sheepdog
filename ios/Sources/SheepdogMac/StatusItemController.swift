import AppKit
import SheepdogKit

/// One row of the status menu. The menu is built from these, so the layout
/// and what is enabled are tested without an `NSMenu`.
public struct MenuItemSpec: Equatable {
    public enum Action: Equatable {
        case openPanel
        case toggleLoginItem
        case quit
        /// Start the session here, or unmute the one that runs
        /// (`AppRoute.talk`).
        case talk
        case toggleMute
        case stop
        /// Take the voice from the phone: connect `/ws`.
        case useHere
        case selectHotkey(HotkeyShortcut)
        case selectHerdr(HerdrBackendSwitch.Kind)
        case openMicrophoneSettings
        /// The daemon updates itself (`MacUpdater`, `POST /api/update`).
        case updateApp
    }

    public enum Row: Equatable {
        case status(String)
        case item(title: String, action: Action, enabled: Bool, checked: Bool)
        case separator
    }
}

/// The menu-bar item: a template icon (with the "needs you" count beside it)
/// and the menu (docs/dev/design/desktop-client.md section 5.4). No window,
/// no Dock icon. Talk, Mute, Stop and Use here drive the Mac's voice session
/// (`VoiceControlling`); the daemon must answer for the ones that connect.
@MainActor
public final class StatusItemController: NSObject {
    public static func rows(
        for state: MenuBarState,
        loginItemEnabled: Bool,
        voice: VoiceMenuState = VoiceMenuState(),
        hotkey: HotkeyShortcut = .optionSpace,
        update: MacUpdatePresentation? = nil
    ) -> [MenuItemSpec.Row] {
        let daemonUp = state.kind != .daemonDown
        var rows: [MenuItemSpec.Row] = [.status(state.line)]
        if let problem = voice.problem { rows.append(.status(problem)) }
        if let update {
            if let line = update.status { rows.append(.status(line)) }
            if let title = update.itemTitle {
                rows.append(.item(title: title, action: .updateApp, enabled: true, checked: false))
            }
        }
        rows += [
            .separator,
            .item(title: "Open panel", action: .openPanel, enabled: true, checked: false),
            .separator,
            .item(title: "Talk", action: .talk, enabled: daemonUp && (!voice.heldHere || voice.muted), checked: false),
            .item(title: "Mute", action: .toggleMute, enabled: voice.heldHere, checked: voice.muted),
            .item(title: "Stop", action: .stop, enabled: voice.heldHere, checked: false),
            .item(title: "Use here", action: .useHere, enabled: daemonUp && !voice.heldHere, checked: false),
        ]
        if voice.micDenied {
            rows.append(.item(title: "Open Microphone Settings…", action: .openMicrophoneSettings, enabled: true, checked: false))
        }
        rows += [
            .separator,
            .status("Hotkey"),
        ]
        rows += HotkeyShortcut.allCases.map {
            .item(title: $0.title, action: .selectHotkey($0), enabled: true, checked: $0 == hotkey)
        }
        if let herdr = state.herdr {
            rows.append(.separator)
            if let reason = herdr.readOnlyReason {
                rows.append(.status("herdr: \(herdr.currentLabel) (\(reason))"))
            } else {
                rows.append(.status("herdr: \(herdr.currentLabel)"))
                rows += herdr.options.map {
                    .item(title: $0.title, action: .selectHerdr($0.kind), enabled: $0.canSelect, checked: $0.current)
                }
            }
        }
        rows += [
            .separator,
            .item(title: "Launch at login", action: .toggleLoginItem, enabled: true, checked: loginItemEnabled),
            .item(title: "Quit Sheepdog", action: .quit, enabled: true, checked: false),
        ]
        return rows
    }

    private let source: DaemonStatusSource
    private let voice: VoiceControlling
    private let panel: PanelOpening
    private let hotkey: HotkeyServing
    private let loginItem: LoginItemControlling
    private let log: AppLogging
    private let quit: () -> Void
    private let updater: MacUpdater
    private var statusItem: NSStatusItem?

    /// The state the menu shows; the daemon is "down" until it answers.
    public private(set) var state = MenuBarState(state: nil)
    /// A newer release than this app and daemon, from `/api/state`.
    public private(set) var offer: MacUpdateOffer?
    public var confirmHerdrSwitch: (HerdrBackendSwitch.Kind) -> Bool = { kind in
        let alert = NSAlert()
        alert.messageText = "Switch to \(kind.fullName)?"
        alert.informativeText = HerdrBackendSwitch.confirmMessage
        alert.addButton(withTitle: "Switch")
        alert.addButton(withTitle: "Cancel")
        return alert.runModal() == .alertFirstButtonReturn
    }
    public var switchHerdr: (HerdrBackendSwitch.Kind) async throws -> Void = { kind in
        let client = DaemonClient(endpoint: DaemonLocator().endpoint)
        defer { client.invalidate() }
        _ = try await client.setHerdrBackend(kind)
    }

    public init(
        source: DaemonStatusSource,
        voice: VoiceControlling,
        panel: PanelOpening,
        hotkey: HotkeyServing,
        loginItem: LoginItemControlling,
        log: AppLogging,
        updater: MacUpdater? = nil,
        quit: @escaping () -> Void = { NSApp.terminate(nil) }
    ) {
        self.updater = updater ?? MacUpdater(
            install: { try await source.installUpdate() },
            launcher: DetachedScriptLauncher(),
            openURL: { NSWorkspace.shared.open($0) }, quit: { voice.handle(route: .end); source.stop(); hotkey.stop(); quit() }
        )
        self.source = source
        self.voice = voice
        self.panel = panel
        self.hotkey = hotkey
        self.loginItem = loginItem
        self.log = log
        self.quit = quit
        super.init()
        self.updater.onChange = { [weak self] in
            self?.render()
        }
        hotkey.onChange = { [weak self] in
            self?.render()
        }
        hotkey.onAction = { [weak self] action in
            switch action {
            case let .pushToTalk(on):
                self?.voice.pushToTalk(on: on)
            case .toggleMute:
                self?.voice.handle(route: .toggleMute)
            }
        }
        hotkey.onError = { [weak self] message in
            self?.log.log(message)
        }
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
        voice.onChange = { [weak self] in
            self?.render()
        }
        hotkey.start()
        source.start()
    }

    public func update(_ daemon: DaemonState?) {
        state = MenuBarState(state: daemon)
        offer = MacUpdateOffer(info: daemon?.update)
        updater.follow(daemon)
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
            voice.handle(route: .end)
            source.stop()
            hotkey.stop()
            quit()
        case .talk:
            voice.handle(route: .talk)
        case .toggleMute:
            voice.handle(route: .toggleMute)
        case .stop:
            voice.handle(route: .stop)
        case .useHere:
            voice.useHere()
        case let .selectHotkey(shortcut):
            hotkey.select(shortcut)
            render()
        case let .selectHerdr(kind):
            guard state.herdr?.options.first(where: { $0.kind == kind })?.canSelect == true,
                  confirmHerdrSwitch(kind) else { return }
            let switchHerdr = switchHerdr
            let log = log
            Task {
                do {
                    try await switchHerdr(kind)
                } catch {
                    log.log("could not switch herdr: \(error.localizedDescription)")
                    let alert = NSAlert()
                    alert.messageText = "Could not switch to \(kind.fullName)"
                    alert.informativeText = RequestFailure(error, linkUsable: true).message
                    alert.runModal()
                }
            }
        case .openMicrophoneSettings:
            voice.openMicrophoneSettings()
        case .updateApp:
            guard let offer else { return }
            let updater = updater
            Task { await updater.start(offer: offer) }
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
        for row in Self.rows(for: state, loginItemEnabled: loginItem.isEnabled, voice: voice.state, hotkey: hotkey.shortcut, update: MacUpdatePresentation(offer: offer, state: updater.state)) {
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
                if action == .updateApp {
                    entry.setAccessibilityLabel(MacUpdatePresentation(offer: offer, state: updater.state).accessibilityLabel)
                }
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
