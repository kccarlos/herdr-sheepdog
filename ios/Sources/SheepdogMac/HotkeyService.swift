import Carbon
import Foundation

/// The fixed shortcuts the menu offers. Carbon sees these without
/// Accessibility or Input Monitoring permission.
public enum HotkeyShortcut: String, CaseIterable, Sendable {
    case optionSpace
    case controlSpace
    case controlOptionSpace

    static let defaultsKey = "hotkey.shortcut"

    var keyCode: UInt32 { UInt32(kVK_Space) }

    var modifiers: UInt32 {
        switch self {
        case .optionSpace: return UInt32(optionKey)
        case .controlSpace: return UInt32(controlKey)
        case .controlOptionSpace: return UInt32(controlKey | optionKey)
        }
    }

    var title: String {
        switch self {
        case .optionSpace: return "⌥ Space"
        case .controlSpace: return "⌃ Space"
        case .controlOptionSpace: return "⌃⌥ Space"
        }
    }

    static func stored(in defaults: UserDefaults) -> HotkeyShortcut {
        defaults.string(forKey: defaultsKey).flatMap(HotkeyShortcut.init(rawValue:)) ?? .optionSpace
    }
}

public enum HotkeyAction: Equatable, Sendable {
    case pushToTalk(Bool)
    case toggleMute
}

/// The menu and `SheepdogMacApp` use this seam; `HotkeyService` owns the
/// Carbon registration and a testable press/release state machine.
@MainActor
public protocol HotkeyServing: AnyObject {
    var shortcut: HotkeyShortcut { get }
    var onChange: (@MainActor () -> Void)? { get set }
    var onAction: (@MainActor (HotkeyAction) -> Void)? { get set }
    var onError: (@MainActor (String) -> Void)? { get set }
    func start()
    func stop()
    func select(_ shortcut: HotkeyShortcut)
}

enum HotkeyEvent: Equatable, Sendable {
    case pressed
    case released
}

@MainActor
protocol HotkeyRegistering: AnyObject {
    var onEvent: (@MainActor (HotkeyEvent) -> Void)? { get set }
    func register(keyCode: UInt32, modifiers: UInt32) throws
    func unregister()
}

@MainActor
final class HotkeyService: HotkeyServing {
    static let holdThreshold: TimeInterval = 0.3

    private(set) var shortcut: HotkeyShortcut
    var onChange: (@MainActor () -> Void)?
    var onAction: (@MainActor (HotkeyAction) -> Void)?
    var onError: (@MainActor (String) -> Void)?

    private let defaults: UserDefaults
    private let registrar: HotkeyRegistering
    private let clock: () -> TimeInterval
    private var started = false
    private var pressedAt: TimeInterval?

    init(
        defaults: UserDefaults = .standard,
        registrar: HotkeyRegistering? = nil,
        clock: @escaping () -> TimeInterval = { ProcessInfo.processInfo.systemUptime }
    ) {
        self.defaults = defaults
        self.registrar = registrar ?? CarbonHotkeyRegistrar()
        self.clock = clock
        shortcut = HotkeyShortcut.stored(in: defaults)
        self.registrar.onEvent = { [weak self] event in
            self?.receive(event)
        }
    }

    func start() {
        guard !started else { return }
        started = true
        register()
    }

    func stop() {
        guard started else { return }
        started = false
        pressedAt = nil
        registrar.unregister()
    }

    func select(_ shortcut: HotkeyShortcut) {
        guard shortcut != self.shortcut else { return }
        self.shortcut = shortcut
        defaults.set(shortcut.rawValue, forKey: HotkeyShortcut.defaultsKey)
        onChange?()
        if started { register() }
    }

    private func register() {
        do {
            try registrar.register(keyCode: shortcut.keyCode, modifiers: shortcut.modifiers)
        } catch {
            onError?(error.localizedDescription)
        }
    }

    private func receive(_ event: HotkeyEvent) {
        switch event {
        case .pressed:
            guard pressedAt == nil else { return }
            pressedAt = clock()
            onAction?(.pushToTalk(true))
        case .released:
            guard let started = pressedAt else { return }
            pressedAt = nil
            onAction?(.pushToTalk(false))
            let heldMilliseconds = Int((((clock() - started) * 1_000)).rounded())
            if heldMilliseconds < Int(Self.holdThreshold * 1_000) {
                onAction?(.toggleMute)
            }
        }
    }
}

/// Carbon's global hotkey registration. It reports key-down and key-up on
/// the application event target, so it needs no Accessibility permission.
@MainActor
final class CarbonHotkeyRegistrar: HotkeyRegistering {
    private static let signature: OSType = 0x5348_4447 // SHDG
    private static let identifier: UInt32 = 1

    var onEvent: (@MainActor (HotkeyEvent) -> Void)?
    private var handler: EventHandlerRef?
    private var hotKey: EventHotKeyRef?

    deinit {
        if let hotKey { UnregisterEventHotKey(hotKey) }
        if let handler { RemoveEventHandler(handler) }
    }

    func register(keyCode: UInt32, modifiers: UInt32) throws {
        unregister()
        if handler == nil {
            var eventTypes = [
                EventTypeSpec(eventClass: OSType(kEventClassKeyboard), eventKind: UInt32(kEventHotKeyPressed)),
                EventTypeSpec(eventClass: OSType(kEventClassKeyboard), eventKind: UInt32(kEventHotKeyReleased)),
            ]
            let status = InstallEventHandler(
                GetApplicationEventTarget(),
                carbonHotkeyHandler,
                eventTypes.count,
                &eventTypes,
                Unmanaged.passUnretained(self).toOpaque(),
                &handler
            )
            guard status == noErr else { throw HotkeyRegistrationError.install(status) }
        }
        let identifier = EventHotKeyID(signature: Self.signature, id: Self.identifier)
        let status = RegisterEventHotKey(
            keyCode,
            modifiers,
            identifier,
            GetApplicationEventTarget(),
            0,
            &hotKey
        )
        guard status == noErr else { throw HotkeyRegistrationError.register(status) }
    }

    func unregister() {
        if let hotKey {
            UnregisterEventHotKey(hotKey)
            self.hotKey = nil
        }
    }

    fileprivate func receive(kind: UInt32) {
        if kind == UInt32(kEventHotKeyPressed) {
            onEvent?(.pressed)
        } else if kind == UInt32(kEventHotKeyReleased) {
            onEvent?(.released)
        }
    }
}

private enum HotkeyRegistrationError: LocalizedError {
    case install(OSStatus)
    case register(OSStatus)

    var errorDescription: String? {
        switch self {
        case let .install(status): return "Could not install the global hotkey handler (Carbon error \(status))."
        case let .register(status): return "Could not register the global hotkey (Carbon error \(status))."
        }
    }
}

private func carbonHotkeyHandler(
    _ handler: EventHandlerCallRef?,
    _ event: EventRef?,
    _ userData: UnsafeMutableRawPointer?
) -> OSStatus {
    guard let event, let userData else { return OSStatus(eventNotHandledErr) }
    let kind = GetEventKind(event)
    let registrar = Unmanaged<CarbonHotkeyRegistrar>.fromOpaque(userData).takeUnretainedValue()
    DispatchQueue.main.async {
        MainActor.assumeIsolated {
            registrar.receive(kind: kind)
        }
    }
    return noErr
}
