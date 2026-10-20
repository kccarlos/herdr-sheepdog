@testable import SheepdogKit
import XCTest

final class DiffParserTests: XCTestCase {
    private let diff = """
    diff --git a/a.go b/a.go
    index 1111111..2222222 100644
    --- a/a.go
    +++ b/a.go
    @@ -88,4 +88,5 @@ func handle() {
     func handle(w http.ResponseWriter) {
    -\tlog(tok)
    +\tlog(token)
    +\treturn errNoToken
     }
    \\ No newline at end of file

    """

    func testParsesLinesAndNumbers() {
        let lines = DiffParser.parse(diff)
        XCTAssertEqual(lines.map(\.kind), [.hunk, .context, .removed, .added, .added, .context, .meta])
        XCTAssertEqual(lines[1].oldNumber, 88)
        XCTAssertEqual(lines[1].newNumber, 88)
        XCTAssertEqual(lines[2].oldNumber, 89)
        XCTAssertEqual(lines[3].newNumber, 89)
        XCTAssertEqual(lines[4].newNumber, 90)
        XCTAssertEqual(lines[5].oldNumber, 90)
        XCTAssertEqual(lines[5].newNumber, 91)
        XCTAssertEqual(lines[2].text, "\tlog(tok)")
        XCTAssertEqual(lines[6].text, "No newline at end of file")
        XCTAssertEqual(Set(lines.map(\.id)).count, lines.count)
    }

    /// The paired removed and added line highlight only the changed word;
    /// the unpaired added line stays plain.
    func testWordHighlights() {
        let lines = DiffParser.parse(diff)
        XCTAssertEqual(lines[2].segments, [.init("\tlog("), .init("tok", changed: true), .init(")")])
        XCTAssertEqual(lines[3].segments, [.init("\tlog("), .init("token", changed: true), .init(")")])
        XCTAssertEqual(lines[4].segments, [.init("\treturn errNoToken")])
    }

    func testUnrelatedLinesStayPlain() {
        let (old, new) = DiffParser.wordHighlights("alpha", "beta gamma")
        XCTAssertEqual(old, [.init("alpha")])
        XCTAssertEqual(new, [.init("beta gamma")])
        let (a, b) = DiffParser.wordHighlights("x := 1", "x := 1 + y")
        XCTAssertEqual(a, [.init("x := 1")])
        XCTAssertEqual(b, [.init("x := 1"), .init(" + y", changed: true)])
    }

    func testEmptyAndBinary() {
        XCTAssertTrue(DiffParser.parse("").isEmpty)
        XCTAssertTrue(DiffParser.parse("diff --git a/x.png b/x.png\nBinary files a/x.png and b/x.png differ\n").isEmpty)
    }
}

final class AttachmentsTests: XCTestCase {
    func testInsertingPath() {
        XCTAssertEqual(Attachments.inserting("/tmp/a.png", into: ""), "/tmp/a.png ")
        XCTAssertEqual(Attachments.inserting("/tmp/a.png", into: "look at"), "look at /tmp/a.png ")
        XCTAssertEqual(Attachments.inserting("/tmp/b.png", into: "look at /tmp/a.png "), "look at /tmp/a.png /tmp/b.png ")
    }

    func testMultipartBody() {
        let body = Attachments.multipartBody(data: Data("hi".utf8), fileName: "a\"b\r\n.txt", mimeType: "text/plain", boundary: "B")
        let text = String(decoding: body, as: UTF8.self)
        XCTAssertEqual(text, "--B\r\nContent-Disposition: form-data; name=\"file\"; filename=\"ab.txt\"\r\nContent-Type: text/plain\r\n\r\nhi\r\n--B--\r\n")
    }

    func testDecodesWireShapes() throws {
        let json = #"{"repo":true,"root":"/r","branch":"main","files":[{"path":"a/b.go","old_path":"a/c.go","status":"renamed","added":1,"deleted":2,"binary":false}],"added":1,"deleted":2}"#
        let changes = try DaemonClient.decoder.decode(AgentChanges.self, from: Data(json.utf8))
        XCTAssertEqual(changes.files.first?.oldPath, "a/c.go")
        XCTAssertEqual(changes.files.first?.fileName, "b.go")
        XCTAssertEqual(changes.files.first?.folder, "a")
        XCTAssertEqual(changes.summary, "main · 1 file")
        let none = try DaemonClient.decoder.decode(AgentChanges.self, from: Data(#"{"repo":false,"reason":"not_repo","files":[],"added":0,"deleted":0}"#.utf8))
        XCTAssertEqual(none.noRepoMessage, "This folder is not a git repository.")
    }
}

/// The demo serves the changes view and accepts uploads, through the
/// app's own client.
final class DemoChangesTests: XCTestCase {
    private var daemon: DemoDaemon!
    private var client: DaemonClient!

    override func setUp() {
        super.setUp()
        daemon = DemoDaemon()
        DemoDaemon.current = daemon
        client = DaemonClient(endpoint: DemoDaemon.endpoint, session: DemoDaemon.session())
    }

    override func tearDown() {
        daemon.stop()
        super.tearDown()
    }

    func testChangesDiffAndUpload() async throws {
        let pane = DemoWorld.authPane
        let fetched = try await client.agentChanges(paneID: pane)
        let changes = try XCTUnwrap(fetched)
        XCTAssertTrue(changes.repo)
        XCTAssertEqual(changes.files.count, 3)
        let fetchedDiff = try await client.agentDiff(paneID: pane, path: "internal/auth/token.go")
        let diff = try XCTUnwrap(fetchedDiff)
        XCTAssertFalse(DiffParser.parse(diff.diff).isEmpty)
        do {
            _ = try await client.agentDiff(paneID: pane, path: "../etc/passwd")
            XCTFail("an unlisted path was served")
        } catch DaemonError.rejected(404, _) {}
        let reply = try await client.uploadAttachment(paneID: pane, data: Data("x".utf8), fileName: "a.txt", mimeType: "text/plain")
        XCTAssertTrue(reply.path.hasPrefix("/"))
        do {
            _ = try await client.uploadAttachment(paneID: pane, data: Data(count: Attachments.maxBytes + 1), fileName: "big.png", mimeType: "image/png")
            XCTFail("an oversize file was sent")
        } catch let DaemonError.rejected(status, message) {
            XCTAssertEqual(status, 413)
            XCTAssertEqual(message, Attachments.tooBigMessage)
        }
    }
}
