import XCTest
@testable import SheepdogKit

/// "Add address" (Settings ▸ Connection ▸ Addresses) for a router's port
/// forward: what a typed address becomes, what is refused before anything
/// is sent, and how the paired computer's record keeps it.
final class ComputerAddressTests: XCTestCase {
    func testATypedAddressIsNormalized() {
        XCTAssertEqual(try ComputerAddress.normalize("100.101.1.1:7777").get(), "100.101.1.1:7777")
        XCTAssertEqual(try ComputerAddress.normalize("  100.101.1.1  ").get(), "100.101.1.1:7777", "port 7777 when none is given")
        XCTAssertEqual(try ComputerAddress.normalize("https://Router.Example:8443/voice").get(), "router.example:8443")
        XCTAssertEqual(try ComputerAddress.normalize("[fd7a:115c::1]:7777").get(), "[fd7a:115c::1]:7777")
    }

    func testRefusalsSayWhy() {
        XCTAssertEqual(ComputerAddress.normalize("   "), .failure(.empty))
        XCTAssertEqual(ComputerAddress.normalize("host:99999"), .failure(.unreadable))
        XCTAssertEqual(ComputerAddress.normalize("studio.tail1234.ts.net"), .failure(.tailscaleName))
        XCTAssertEqual(ComputerAddress.normalize("STUDIO.tail1234.TS.NET:7777"), .failure(.tailscaleName))
        XCTAssertEqual(ComputerAddress.normalize("127.0.0.1:7777"), .failure(.thisPhone))
        XCTAssertEqual(ComputerAddress.normalize("localhost"), .failure(.thisPhone))
        XCTAssertEqual(ComputerAddress.normalize("100.101.1.1", known: ["100.101.1.1:7777"]), .failure(.known))
        for problem: ComputerAddress.Problem in [.empty, .unreadable, .tailscaleName, .thisPhone, .known] {
            XCTAssertFalse(problem.message.isEmpty)
        }
        XCTAssertTrue(ComputerAddress.Problem.tailscaleName.message.contains("IP address"))
    }

    func testTheRecordKeepsAddedAddressesAndTheLastOne() {
        let secrets = InMemorySecretStore()
        let creds = DeviceCredentials(daemonID: "d", secrets: secrets)
        creds.computer = PairedComputer(daemonID: "d", name: "Studio", addresses: ["192.168.1.20:7777"], pin: "p", deviceID: "dev", pairedAt: Date())
        creds.adopt(SessionTicket(ticket: "t", expires: Date().addingTimeInterval(3600)))
        creds.addAddress("100.101.1.1:7777")
        creds.addAddress("100.101.1.1:7777")
        XCTAssertEqual(creds.computer?.addresses, ["192.168.1.20:7777", "100.101.1.1:7777"])
        XCTAssertEqual(DeviceCredentials(daemonID: "d", secrets: secrets).computer?.addresses.count, 2, "saved")
        XCTAssertEqual(creds.header(), "Bearer sds_t", "adding an address keeps the ticket")
        creds.removeAddress("192.168.1.20:7777")
        XCTAssertEqual(creds.computer?.addresses, ["100.101.1.1:7777"])
        creds.removeAddress("100.101.1.1:7777")
        XCTAssertEqual(creds.computer?.addresses, ["100.101.1.1:7777"], "the last address stays")
    }
}
