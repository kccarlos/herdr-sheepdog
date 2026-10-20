import XCTest
@testable import SheepdogKit

/// `/api/state`'s `update` object (Go `update.Status`, omitempty fields)
/// and the one line Settings shows for it.
final class UpdateNoticeTests: XCTestCase {
    private func state(_ update: String?) throws -> DaemonState {
        let tail = update.map { #","update":"# + $0 } ?? ""
        let json = #"{"agents":[],"voice":{"mode":"gemini","status":"connected","muted":false,"stalled":false,"client_connected":true},"version":"v0.1.4""# + tail + "}"
        return try DaemonClient.decoder.decode(DaemonState.self, from: Data(json.utf8))
    }

    func testAnOlderDaemonHasNoUpdateAndNoLine() throws {
        let s = try state(nil)
        XCTAssertNil(s.update)
        XCTAssertNil(s.update?.notice)
    }

    func testUpToDateShowsNothing() throws {
        // What the Go side writes with no newer release: the false and
        // empty fields are omitted.
        let s = try state(#"{"current":"v0.1.4","latest":"v0.1.4","available":false,"how":"brew upgrade sheepdog","auto_restart":true}"#)
        XCTAssertEqual(s.update?.available, false)
        XCTAssertNil(s.update?.notice)
        XCTAssertNil(try state(#"{"current":"dev","available":false}"#).update?.notice)
    }

    func testAvailableWithAutoRestart() throws {
        let s = try state(#"{"current":"v0.1.4","latest":"v0.1.5","available":true,"how":"brew upgrade sheepdog","url":"https://example.invalid/r","auto_restart":true}"#)
        let n = try XCTUnwrap(s.update?.notice)
        XCTAssertEqual(n.text, "sheepdog v0.1.5 is available. To update, run this on your computer:")
        XCTAssertEqual(n.command, "brew upgrade sheepdog")
        XCTAssertEqual(n.after, "It then restarts itself when idle.")
        XCTAssertEqual(n.spoken, "sheepdog v0.1.5 is available. To update, run this on your computer: brew upgrade sheepdog It then restarts itself when idle.")
    }

    func testAvailableWithoutAutoRestartOrCommand() throws {
        let n = try XCTUnwrap(try state(#"{"current":"v0.1.4","latest":"v0.1.5","available":true}"#).update?.notice)
        XCTAssertEqual(n.text, "sheepdog v0.1.5 is available. Update it on your computer.")
        XCTAssertNil(n.command)
        XCTAssertNil(n.after)
    }

    func testInstalledRestartsItselfWhenIdle() throws {
        let n = try XCTUnwrap(try state(#"{"current":"v0.1.4","latest":"v0.1.5","available":true,"restart_pending":true,"restart_to":"v0.1.5","auto_restart":true}"#).update?.notice)
        XCTAssertEqual(n.text, "sheepdog v0.1.5 is installed and restarts itself when no one is using it.")
        XCTAssertNil(n.command)
    }

    func testInstalledWithoutAutoRestartNamesTheCommand() throws {
        let n = try XCTUnwrap(try state(#"{"current":"v0.1.4","available":false,"restart_pending":true}"#).update?.notice)
        XCTAssertEqual(n.text, "A new sheepdog is installed. To finish, run this on your computer:")
        XCTAssertEqual(n.command, "sheepdog restart")
    }
}
