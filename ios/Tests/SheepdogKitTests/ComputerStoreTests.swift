import Foundation
import XCTest
@testable import SheepdogKit

/// Several computers (docs/dev/design/app-redesign.md sections 3, 4 and
/// 10): each keyed by its daemon id with its own record, pins and ticket;
/// one current computer the app never changes on its own; the old single
/// pairing moved over so an existing user stays paired.
final class ComputerStoreTests: XCTestCase {
    private func fresh(_ secrets: InMemorySecretStore = InMemorySecretStore(), defaults: UserDefaults? = nil) -> (ComputerStore, InMemorySecretStore, UserDefaults) {
        let defaults = defaults ?? UserDefaults(suiteName: "sheepdog.tests.\(UUID().uuidString)")!
        return (ComputerStore(defaults: defaults, secrets: secrets), secrets, defaults)
    }

    private func computer(_ id: String, name: String = "Mac", pin: String = "pin-\(UUID().uuidString)", addresses: [String] = ["192.168.1.20:7777"]) -> PairedComputer {
        PairedComputer(daemonID: id, name: name, addresses: addresses, pin: pin, deviceID: "dev-\(id)", pairedAt: Date(timeIntervalSince1970: 1_800_000_000))
    }

    func testEachComputerHasItsOwnRecordInTheSecretStore() {
        let (store, secrets, defaults) = fresh()
        XCTAssertEqual(store.count, 0)
        XCTAssertNil(store.currentID)
        let work = computer("d1", name: "Work Mac", pin: "pin123456")
        XCTAssertTrue(store.save(work, address: "192.168.1.20:7777"))
        XCTAssertTrue(store.save(computer("d2", name: "Home Mac"), address: "10.0.0.2:7777"))
        XCTAssertEqual(Set(secrets.values.keys), [DeviceCredentials.account(for: "d1"), DeviceCredentials.account(for: "d2")])
        XCTAssertEqual(DeviceCredentials(daemonID: "d1", secrets: secrets).computer, work)
        XCTAssertEqual(work.shortPin, "pin12345")
        XCTAssertFalse(defaults.dictionaryRepresentation().values.contains { "\($0)".contains("pin123456") }, "no pin in defaults")
        XCTAssertEqual(store.ids, ["d1", "d2"])
        XCTAssertTrue(store.credentials(for: "d1") === store.credentials(for: "d1"), "one instance per computer")
        XCTAssertFalse(store.credentials(for: "d1") === store.credentials(for: "d2"))
    }

    func testThePinsAndTicketsNeverMix() {
        let (store, _, _) = fresh()
        store.save(computer("d1", pin: "a"), address: "192.168.1.20:7777")
        store.save(computer("d2", pin: "b"), address: "10.0.0.2:7777")
        store.credentials(for: "d1").adopt(SessionTicket(ticket: "t1", expires: Date().addingTimeInterval(3600)))
        XCTAssertEqual(store.credentials(for: "d1").pins, ["a"])
        XCTAssertEqual(store.credentials(for: "d2").pins, ["b"])
        XCTAssertNil(store.credentials(for: "d2").header())
        let one = store.endpoint(for: "d1")
        let two = store.endpoint(for: "d2")
        XCTAssertEqual(one?.computerID, "d1")
        XCTAssertEqual(two?.display, "10.0.0.2:7777")
        XCTAssertNotEqual(one, two)
        var upgrade = URLRequest(url: URL(string: "wss://192.168.1.20:7777/ws")!)
        one?.authorize(&upgrade)
        XCTAssertEqual(upgrade.value(forHTTPHeaderField: "Authorization"), "Bearer sds_t1")
        var other = URLRequest(url: URL(string: "wss://10.0.0.2:7777/ws")!)
        two?.authorize(&other)
        XCTAssertNil(other.value(forHTTPHeaderField: "Authorization"))
    }

    func testTheFirstIsCurrentAndPairingAnotherNeverSwitches() {
        let (store, _, _) = fresh()
        store.save(computer("d1"), address: "192.168.1.20:7777")
        XCTAssertEqual(store.currentID, "d1", "the computer paired first")
        store.save(computer("d2"), address: "10.0.0.2:7777")
        XCTAssertEqual(store.currentID, "d1", "another computer is added, not switched to")
        store.currentID = "d2"
        XCTAssertEqual(store.current?.daemonID, "d2")
        store.currentID = "gone"
        XCTAssertEqual(store.currentID, "d1", "an unknown id falls back to the first")
    }

    func testARepairUpdatesTheComputerInPlace() {
        let (store, _, _) = fresh()
        store.save(computer("d1", pin: "old"), address: "192.168.1.20:7777")
        store.save(computer("d2"), address: "10.0.0.2:7777")
        store.rename("d1", to: "  Studio  ")
        XCTAssertFalse(store.save(computer("d1", name: "Mac mini", pin: "new"), address: "192.168.1.30:7777"), "same daemon id: not a second computer")
        XCTAssertEqual(store.ids, ["d1", "d2"])
        XCTAssertEqual(store.computer("d1")?.pin, "new")
        XCTAssertEqual(store.computer("d1")?.displayName, "Studio", "the local name survives a re-pair")
        XCTAssertEqual(store.address(for: "d1"), "192.168.1.30:7777")
    }

    func testRenameIsLocalAndBlankGoesBack() {
        let (store, _, _) = fresh()
        store.save(computer("d1", name: "Mac mini"), address: "192.168.1.20:7777")
        XCTAssertEqual(store.computer("d1")?.displayName, "Mac mini")
        store.rename("d1", to: "Home Mac")
        XCTAssertEqual(store.computer("d1")?.displayName, "Home Mac")
        XCTAssertEqual(store.computer("d1")?.name, "Mac mini", "the daemon's own name is kept")
        store.rename("d1", to: "   ")
        XCTAssertEqual(store.computer("d1")?.displayName, "Mac mini")
        XCTAssertEqual(PairedComputer(daemonID: "x", name: "", addresses: [], pin: "p", deviceID: "", pairedAt: Date()).displayName, "Your computer")
    }

    func testRemovingTheCurrentMovesToTheFirstLeft() {
        let (store, secrets, _) = fresh()
        store.save(computer("d1"), address: "192.168.1.20:7777")
        store.save(computer("d2"), address: "10.0.0.2:7777")
        store.save(computer("d3"), address: "10.0.0.3:7777")
        store.currentID = "d2"
        store.remove("d2")
        XCTAssertEqual(store.ids, ["d1", "d3"])
        XCTAssertEqual(store.currentID, "d1")
        XCTAssertNil(secrets.values[DeviceCredentials.account(for: "d2")])
        store.remove("d1")
        store.remove("d3")
        XCTAssertEqual(store.count, 0)
        XCTAssertNil(store.currentID)
        XCTAssertTrue(secrets.values.isEmpty)
    }

    func testMoveReorders() {
        let (store, _, _) = fresh()
        for id in ["a", "b", "c"] { store.save(computer(id), address: "10.0.0.1:7777") }
        store.move(fromOffsets: IndexSet(integer: 2), toOffset: 0)
        XCTAssertEqual(store.ids, ["c", "a", "b"])
        store.move(fromOffsets: IndexSet(integer: 0), toOffset: 3)
        XCTAssertEqual(store.ids, ["a", "b", "c"])
    }

    func testLabelsShowOnlyWithASecondComputer() {
        let (store, _, _) = fresh()
        XCTAssertFalse(store.showsComputerLabels)
        store.save(computer("d1"), address: "192.168.1.20:7777")
        XCTAssertFalse(store.showsComputerLabels, "one computer looks exactly as it always did")
        store.save(computer("d2"), address: "10.0.0.2:7777")
        XCTAssertTrue(store.showsComputerLabels)
        store.remove("d2")
        XCTAssertFalse(store.showsComputerLabels)
    }

    func testALoopbackFixtureIsPlainHTTP() {
        let (store, _, _) = fresh()
        store.save(computer("fixture", pin: "", addresses: ["127.0.0.1:7799"]), address: "127.0.0.1:7799")
        let endpoint = store.endpoint(for: "fixture")
        XCTAssertEqual(endpoint?.secure, false)
        XCTAssertEqual(endpoint?.url(path: "/api/hello")?.scheme, "http")
        XCTAssertEqual(endpoint?.computerID, "fixture")
    }

    func testNothingToMoveLeavesAFixtureAddressAlone() {
        let defaults = UserDefaults(suiteName: "sheepdog.tests.\(UUID().uuidString)")!
        defaults.set("127.0.0.1:7799", forKey: "daemon.address")
        let (store, _, _) = fresh(defaults: defaults)
        XCTAssertEqual(store.count, 0)
        XCTAssertEqual(store.looseAddress, "127.0.0.1:7799")
    }
}

final class SettingsStoreTests: XCTestCase {
    private func store() -> SettingsStore {
        let defaults = UserDefaults(suiteName: "sheepdog.tests.\(UUID().uuidString)")!
        return SettingsStore(defaults: defaults, computers: ComputerStore(defaults: defaults, secrets: InMemorySecretStore()))
    }

    func testTheSessionTalksToTheCurrentComputer() {
        let settings = store()
        XCTAssertNil(settings.endpoint)
        XCTAssertNil(settings.credentials)
        settings.address = "127.0.0.1:7799"
        XCTAssertEqual(settings.endpoint?.display, "127.0.0.1:7799")
        XCTAssertEqual(settings.endpoint?.secure, false)
        XCTAssertNil(settings.endpoint?.computerID)

        let computers = settings.computers
        computers.save(PairedComputer(daemonID: "d1", name: "Work", addresses: ["192.168.1.20:7777"], pin: "p1", deviceID: "x", pairedAt: Date()), address: "192.168.1.20:7777")
        computers.save(PairedComputer(daemonID: "d2", name: "Home", addresses: ["10.0.0.2:7777"], pin: "p2", deviceID: "y", pairedAt: Date()), address: "10.0.0.2:7777")
        XCTAssertEqual(settings.endpoint?.secure, true)
        XCTAssertEqual(settings.endpoint?.url(path: "/api/hello")?.scheme, "https")
        XCTAssertEqual(settings.computer?.daemonID, "d1")
        XCTAssertEqual(settings.credentials?.daemonID, "d1")
        settings.address = "100.101.1.1:7777"
        XCTAssertEqual(computers.address(for: "d1"), "100.101.1.1:7777", "an address switch is the current computer's")
        XCTAssertEqual(computers.address(for: "d2"), "10.0.0.2:7777")

        computers.currentID = "d2"
        XCTAssertEqual(settings.endpoint?.display, "10.0.0.2:7777")
        XCTAssertEqual(settings.endpoint?.computerID, "d2")
    }

    func testFirstLaunchIsUnconfiguredAndBackgroundListeningIsOnByDefault() {
        let settings = store()
        XCTAssertFalse(settings.hasBeenConfigured)
        XCTAssertTrue(settings.keepListeningWhenLocked)
        settings.hasBeenConfigured = true
        settings.keepListeningWhenLocked = false
        XCTAssertTrue(settings.hasBeenConfigured)
        XCTAssertFalse(settings.keepListeningWhenLocked)
    }
}
