@testable import SheepdogKit
import XCTest

final class MarkdownRendererTests: XCTestCase {
    private func intents(_ s: AttributedString, of substring: String) -> InlinePresentationIntent? {
        guard let range = s.range(of: substring) else { return nil }
        return s[range].runs.first?.inlinePresentationIntent
    }

    func testInlineStyles() {
        let s = MarkdownRenderer.render("Run **this** and *that* with `make test`, see [docs](https://example.com).")
        XCTAssertEqual(String(s.characters), "Run this and that with make test, see docs.")
        XCTAssertEqual(intents(s, of: "this"), .stronglyEmphasized)
        XCTAssertEqual(intents(s, of: "that"), .emphasized)
        XCTAssertEqual(intents(s, of: "make test"), .code)
        let docs = s.range(of: "docs")!
        XCTAssertEqual(s[docs].link, URL(string: "https://example.com"))
    }

    func testHeadingIsBoldBodyTextNotATitle() {
        XCTAssertEqual(MarkdownRenderer.blocks("## Summary\nAll done."), [.heading("Summary"), .paragraph("All done.")])
        let s = MarkdownRenderer.render("# Big *news* #\n\nbody")
        XCTAssertEqual(String(s.characters), "Big news\n\nbody")
        XCTAssertTrue(intents(s, of: "Big")?.contains(.stronglyEmphasized) ?? false)
        XCTAssertEqual(intents(s, of: "news"), [.stronglyEmphasized, .emphasized])
        // No presentation intent survives that a Text would draw large.
        for run in s.runs { XCTAssertNil(run.presentationIntent) }
        // "#hashtag" is not a heading.
        XCTAssertEqual(MarkdownRenderer.blocks("#1 priority"), [.paragraph("#1 priority")])
    }

    func testBulletAndNumberedListsWithNesting() {
        let text = """
        Changes:
        - added tests
          and fixtures
        - fixed **auth**
            - nested item
        1. build
        2) ship
        """
        XCTAssertEqual(MarkdownRenderer.blocks(text), [
            .paragraph("Changes:"),
            .list([
                .init(marker: "•", depth: 0, text: "added tests and fixtures"),
                .init(marker: "•", depth: 0, text: "fixed **auth**"),
                .init(marker: "•", depth: 2, text: "nested item"),
                .init(marker: "1.", depth: 0, text: "build"),
                .init(marker: "2.", depth: 0, text: "ship"),
            ]),
        ])
        let s = MarkdownRenderer.render(text)
        XCTAssertEqual(String(s.characters), "Changes:\n\n• added tests and fixtures\n• fixed auth\n        • nested item\n1. build\n2. ship")
        XCTAssertEqual(intents(s, of: "auth"), .stronglyEmphasized)
    }

    func testFencedCodeIsAMonospacedBlockKeptVerbatim() {
        let text = "Try:\n```sh\nls *.go | grep -v _test\n  indented **not bold**\n```\nthen push."
        XCTAssertEqual(MarkdownRenderer.blocks(text), [
            .paragraph("Try:"),
            .code("ls *.go | grep -v _test\n  indented **not bold**"),
            .paragraph("then push."),
        ])
        let s = MarkdownRenderer.render(text)
        XCTAssertTrue(String(s.characters).contains("indented **not bold**"))
        XCTAssertEqual(intents(s, of: "ls *.go"), .code)
    }

    func testAnUnclosedFenceStillShowsItsCode() {
        XCTAssertEqual(MarkdownRenderer.blocks("```\nmake build"), [.code("make build")])
    }

    func testABareAsteriskInAShellCommandDoesNotVanish() {
        let s = MarkdownRenderer.render("rm -rf build/* now")
        XCTAssertEqual(String(s.characters), "rm -rf build/* now")
        XCTAssertEqual(MarkdownRenderer.plain("2 * 3 = 6"), "2 * 3 = 6")
    }

    func testParagraphsKeepTheirLineBreaksAndPlainTextPassesThrough() {
        XCTAssertEqual(MarkdownRenderer.plain("one\ntwo\n\nthree"), "one\ntwo\n\nthree")
        XCTAssertEqual(MarkdownRenderer.plain("Nothing special here."), "Nothing special here.")
        XCTAssertEqual(MarkdownRenderer.plain(""), "")
    }

    func testTaskListItems() {
        XCTAssertEqual(MarkdownRenderer.plain("- [ ] write\n- [x] test"), "☐ write\n☑ test")
    }
}
