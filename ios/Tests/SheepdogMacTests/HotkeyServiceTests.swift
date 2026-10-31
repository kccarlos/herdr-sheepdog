import Carbon
import XCTest
import SheepdogKit
@testable import SheepdogMac

final class HotkeyServiceTests: XCTestCase {
    @MainActor
    private final class FakeRegistrar: HotkeyRegistering {
        var onEvent: (@MainActor (HotkeyEvent) -> Void)?
        private(set) var registration: (keyCode: UInt32, modifiers: UInt32)?
        private(set) var unregistered = 0

        func register(keyCode: UInt32, modifiers: UInt32) throws {
            registration = (keyCode, modifiers)
        }

        func unregister() {
            unregistered += 1
            registration = nil
        }

        func emit(_ event: HotkeyEvent) {
            onEvent?(event)
        }
    }

    @MainActor
    func testATapUnderThreeHundredMillisecondsTogglesMute() {
        let registrar = FakeRegistrar()
        var now: TimeInterval = 100
        var actions: [HotkeyAction] = []
        let service = HotkeyService(defaults: defaults(), registrar: registrar, clock: { now })
        service.onAction = { actions.append($0) }
        service.start()
        registrar.emit(.pressed)
        now += 0.299
        registrar.emit(.released)
        XCTAssertEqual(actions, [.pushToTalk(true), .pushToTalk(false), .toggleMute])
    }

    @MainActor
    func testAHoldAtThreeHundredMillisecondsDoesNotToggleMute() {
        let registrar = FakeRegistrar()
        var now: TimeInterval = 100
        var actions: [HotkeyAction] = []
        let service = HotkeyService(defaults: defaults(), registrar: registrar, clock: { now })
        service.onAction = { actions.append($0) }
        service.start()
        registrar.emit(.pressed)
        now += 0.3
        registrar.emit(.released)
        XCTAssertEqual(actions, [.pushToTalk(true), .pushToTalk(false)])
    }

    @MainActor
    func testTheDefaultAndChoiceAreKeptInUserDefaults() {
        let defaults = defaults()
        let registrar = FakeRegistrar()
        let service = HotkeyService(defaults: defaults, registrar: registrar)
        XCTAssertEqual(service.shortcut, .optionSpace)
        service.start()
        XCTAssertEqual(registrar.registration?.keyCode, UInt32(kVK_Space))
        service.select(.controlSpace)
        XCTAssertEqual(HotkeyShortcut.stored(in: defaults), .controlSpace)
        XCTAssertEqual(registrar.registration?.modifiers, UInt32(controlKey))
    }

    private func defaults() -> UserDefaults {
        let suite = "sheepdog-hotkey-tests-\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        return defaults
    }
}
