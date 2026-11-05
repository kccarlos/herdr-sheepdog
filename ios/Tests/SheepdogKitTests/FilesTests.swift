@testable import SheepdogKit
import ImageIO
import XCTest

private let utc: Calendar = {
    var calendar = Calendar(identifier: .gregorian)
    calendar.timeZone = TimeZone(identifier: "UTC")!
    return calendar
}()

/// 2026-11-04 12:00 UTC, a Wednesday.
private let now = Date(timeIntervalSince1970: 1_793_793_600)

private func ago(_ seconds: TimeInterval) -> Date { now.addingTimeInterval(-seconds) }

final class FileKindTests: XCTestCase {
    func testKindsByName() {
        XCTAssertEqual(FileKind.of(name: "README.md"), .markdown)
        XCTAssertEqual(FileKind.of(name: "notes.MARKDOWN"), .markdown)
        XCTAssertEqual(FileKind.of(name: "login-flow.PNG"), .image)
        XCTAssertEqual(FileKind.of(name: "server.go"), .code)
        XCTAssertEqual(FileKind.of(name: "Makefile"), .code)
        XCTAssertEqual(FileKind.of(name: "build.log"), .text)
        XCTAssertEqual(FileKind.of(name: "LICENSE"), .text)
        XCTAssertEqual(FileKind.of(name: "data.sqlite"), .other)
        XCTAssertEqual(FileKind.of(name: ".gitignore"), .other, "a dot file has no extension")
        XCTAssertEqual(FileKind.of(name: "archive"), .other)
    }

    func testAFolderAndALinkAreNotJudgedByName() {
        XCTAssertEqual(FileKind(FileEntry(name: "notes.md", kind: .dir)), .folder)
        XCTAssertEqual(FileKind(FileEntry(name: "latest.md", kind: .link)), .link)
        XCTAssertEqual(FileKind(FileEntry(name: "a.md")), .markdown)
    }

    func testEveryKindHasASymbolAndAWord() {
        for kind in [FileKind.folder, .link, .markdown, .image, .code, .text, .other] {
            XCTAssertFalse(kind.symbol.isEmpty)
            XCTAssertFalse(kind.word.isEmpty)
        }
    }
}

final class FileTextTests: XCTestCase {
    func testSizes() {
        XCTAssertEqual(FileSizeText.text(0), "0 B")
        XCTAssertEqual(FileSizeText.text(812), "812 B")
        XCTAssertEqual(FileSizeText.text(1000), "1 KB")
        XCTAssertEqual(FileSizeText.text(2400), "2.4 KB")
        XCTAssertEqual(FileSizeText.text(12_300), "12 KB")
        XCTAssertEqual(FileSizeText.text(184_000), "184 KB")
        XCTAssertEqual(FileSizeText.text(999_999), "1 MB")
        XCTAssertEqual(FileSizeText.text(4_200_000), "4.2 MB")
        XCTAssertEqual(FileSizeText.text(38_000_000), "38 MB")
        XCTAssertEqual(FileSizeText.text(16 << 20), "17 MB")
    }

    func testRelativeTimes() {
        func text(_ seconds: TimeInterval) -> String { FileTimeText.relative(ago(seconds), now: now, calendar: utc) }
        XCTAssertEqual(text(20), "just now")
        XCTAssertEqual(text(-30), "just now")
        XCTAssertEqual(text(3 * 60), "3 min ago")
        XCTAssertEqual(text(59 * 60), "59 min ago")
        XCTAssertEqual(text(3 * 3600), "3 hr ago")
        // 12:00 now: 13 hours ago is yesterday at 23:00.
        XCTAssertEqual(text(13 * 3600), "yesterday")
        XCTAssertEqual(text(3 * 86400), "3 days ago")
        XCTAssertEqual(text(8 * 86400), "last week")
        XCTAssertEqual(text(30 * 86400), "Oct 5")
        XCTAssertEqual(text(400 * 86400), "Sep 30, 2025")
    }

    func testRowDetail() {
        let file = FileEntry(name: "install.md", kind: .file, size: 12_300, modified: ago(3 * 60))
        XCTAssertEqual(FileRowText.detail(file, now: now, calendar: utc), "12 KB · 3 min ago")
        let folder = FileEntry(name: "reports", kind: .dir, modified: ago(2 * 86400))
        XCTAssertEqual(FileRowText.detail(folder, now: now, calendar: utc), "2 days ago")
        let link = FileEntry(name: "latest", kind: .link, modified: ago(60 * 60 * 5))
        XCTAssertEqual(FileRowText.detail(link, now: now, calendar: utc), "Link · 5 hr ago")
        let blocked = FileEntry(name: "notes.md", kind: .link, blocked: true)
        XCTAssertEqual(FileRowText.detail(blocked, now: now, calendar: utc), "Link out of this folder")
        let bare = FileEntry(name: "a")
        XCTAssertEqual(FileRowText.detail(bare, now: now, calendar: utc), "0 B")
    }

    func testYesterdayReadsTheSameInEveryOrder() {
        // One word for one file wherever it shows: the day word above a
        // group does not change what the row says.
        let file = FileEntry(name: "api.md", kind: .file, size: 28_000, modified: now.addingTimeInterval(-18 * 3600)) // yesterday 18:00
        XCTAssertEqual(FileRowText.detail(file, now: now, calendar: utc), "28 KB · yesterday")
        XCTAssertEqual(FileGroup.bucket(for: file.modified, now: now, calendar: utc), .yesterday)
    }

    func testSpokenRow() {
        let entry = FileEntry(name: "install.md", kind: .file, size: 12_300, modified: ago(180))
        XCTAssertEqual(FileRowText.spoken(entry, detail: "12 KB · 3 min ago", changed: true), "Markdown document install.md, changed, 12 KB · 3 min ago")
        let blocked = FileEntry(name: "notes.md", kind: .link, blocked: true)
        XCTAssertEqual(FileRowText.spoken(blocked, detail: "", changed: false), "Link notes.md, outside this folder")
    }

    func testSearchAndFooterWords() {
        let one = FileListing(rootName: "shop", entries: [FileEntry(name: "a")])
        XCTAssertEqual(FileRowText.searchSummary(one, query: "a", scope: "shop"), "1 file named “a” in shop")
        let mixed = FileListing(rootName: "shop", entries: [FileEntry(name: "a"), FileEntry(name: "ab", kind: .dir)])
        XCTAssertEqual(FileRowText.searchSummary(mixed, query: "a", scope: "docs"), "2 files named “a” in docs", "one word, whatever is found")
        XCTAssertNil(FileRowText.searchSummary(FileListing(rootName: "shop"), query: "ledger", scope: "shop"), "no results: the empty state says it")
        XCTAssertNil(FileRowText.hiddenNote(0))
        XCTAssertEqual(FileRowText.hiddenNote(1), "1 file hidden for safety, like .env and keys.")
        XCTAssertEqual(FileRowText.hiddenNote(2), "2 files hidden for safety, like .env and keys.")
        XCTAssertEqual(FileRowText.truncatedNote(searching: false, count: 2000), "Showing the first 2,000 items. Search by name to find the rest.")
        XCTAssertEqual(FileRowText.truncatedNote(searching: true, count: 200), "Showing the first 200 matches. Type more of the name to narrow it down.")
        XCTAssertEqual(FileRowText.location(FileEntry(name: "a.md", path: "docs/reports/a.md"), rootName: "shop"), "docs/reports")
        XCTAssertEqual(FileRowText.location(FileEntry(name: "a.md"), rootName: "shop"), "shop")
    }
}

final class FileSortTests: XCTestCase {
    private let entries = [
        FileEntry(name: "docs", kind: .dir, modified: ago(5 * 86400)),
        FileEntry(name: "a.md", modified: ago(3 * 60)),
        FileEntry(name: "b.md", modified: ago(26 * 3600)),
        FileEntry(name: "c.md", modified: ago(30 * 86400)),
        FileEntry(name: "d.md", modified: nil),
        FileEntry(name: "e.md", modified: ago(3 * 60)),
    ]

    func testNameKeepsTheDaemonsOrder() {
        XCTAssertEqual(FileSort.name.ordered(entries).map(\.name), entries.map(\.name))
    }

    func testRecentIsNewestFirstWithTiesInDaemonOrderAndUndatedLast() {
        XCTAssertEqual(FileSort.recent.ordered(entries).map(\.name), ["a.md", "e.md", "b.md", "docs", "c.md", "d.md"])
    }

    func testGroupsUnderDayWords() {
        let groups = FileGroup.groups(FileSort.recent.ordered(entries), now: now, calendar: utc)
        XCTAssertEqual(groups.map(\.bucket), [.today, .yesterday, .lastWeek, .older])
        XCTAssertEqual(groups.map(\.bucket.title), ["Today", "Yesterday", "Last 7 days", "Older"])
        XCTAssertEqual(groups[0].entries.map(\.name), ["a.md", "e.md"])
        XCTAssertEqual(groups[3].entries.map(\.name), ["c.md", "d.md"])
    }

    func testBuckets() {
        func bucket(_ seconds: TimeInterval) -> FileGroup.Bucket { FileGroup.bucket(for: ago(seconds), now: now, calendar: utc) }
        XCTAssertEqual(bucket(11 * 3600), .today)
        XCTAssertEqual(bucket(13 * 3600), .yesterday)
        XCTAssertEqual(bucket(2 * 86400), .lastWeek)
        XCTAssertEqual(bucket(6 * 86400), .lastWeek)
        XCTAssertEqual(bucket(7 * 86400), .older)
        XCTAssertEqual(FileGroup.bucket(for: nil, now: now, calendar: utc), .older)
    }
}

final class FilePathTests: XCTestCase {
    func testParentAndName() {
        XCTAssertEqual(FilePath.parent(of: "docs/reports/a.md"), "docs/reports")
        XCTAssertEqual(FilePath.parent(of: "a.md"), "")
        XCTAssertEqual(FilePath.name(of: "docs/reports/a.md"), "a.md")
    }

    func testLinksInADocumentResolveFromItsFolder() {
        let file = "docs/reports/login-loop.md"
        XCTAssertEqual(FilePath.resolve("../images/login-flow.png", from: file), "docs/images/login-flow.png")
        XCTAssertEqual(FilePath.resolve("./other.md", from: file), "docs/reports/other.md")
        XCTAssertEqual(FilePath.resolve("../../internal/auth/token.go", from: file), "internal/auth/token.go")
        XCTAssertEqual(FilePath.resolve("sub/a%20b.md#top", from: file), "docs/reports/sub/a b.md")
        XCTAssertEqual(FilePath.resolve("a.md?plain=1", from: "README.md"), "a.md")
    }

    func testLinksThatAreNotFilesOfTheFolderResolveToNothing() {
        let file = "docs/a.md"
        XCTAssertNil(FilePath.resolve("../../outside.md", from: file), "climbs above the folder")
        XCTAssertNil(FilePath.resolve("/etc/passwd", from: file))
        XCTAssertNil(FilePath.resolve("https://example.com/a.md", from: file))
        XCTAssertNil(FilePath.resolve("mailto:me@example.com", from: file))
        XCTAssertNil(FilePath.resolve("#section", from: file))
        XCTAssertNil(FilePath.resolve("", from: file))
        XCTAssertNil(FilePath.resolve("..", from: "a.md"))
    }

    func testCrumbs() {
        XCTAssertEqual(FileCrumb.crumbs(rootName: "shop", path: ""), [FileCrumb(name: "shop", path: "")])
        XCTAssertEqual(FileCrumb.crumbs(rootName: "shop", path: "docs/reports"), [
            FileCrumb(name: "shop", path: ""), FileCrumb(name: "docs", path: "docs"), FileCrumb(name: "reports", path: "docs/reports"),
        ])
    }
}

final class CodeLinesTests: XCTestCase {
    func testLinesAreNumberedAndTabsBecomeSpaces() {
        XCTAssertEqual(CodeLines.split("a\n\tb\n\nc\n"), [
            CodeLine(number: 1, text: "a"), CodeLine(number: 2, text: "    b"), CodeLine(number: 3, text: ""), CodeLine(number: 4, text: "c"),
        ])
    }

    func testEmptyAndLineEndings() {
        XCTAssertTrue(CodeLines.split("").isEmpty)
        XCTAssertEqual(CodeLines.split("\n").map(\.text), [""])
        XCTAssertEqual(CodeLines.split("a\r\nb\rc").map(\.text), ["a", "b", "c"])
        XCTAssertEqual(CodeLines.split("no newline").count, 1)
        XCTAssertEqual(CodeLines.split("a\n\nb\r\n\r\nc\n").map(\.text), ["a", "", "b", "", "c"], "a blank line is a line, in either line ending")
    }

    func testAPlainLineIsOnePiece() {
        XCTAssertEqual(CodeLine(number: 1, text: "plain").pieces, [CodeLine.Piece(text: "plain", kind: nil)])
        XCTAssertEqual(CodeLine(number: 1, text: "").pieces, [CodeLine.Piece(text: "", kind: nil)])
    }

    func testVeryLongLineUsesBoundedChunksAndOneLineNumber() {
        let text = String(repeating: "a🙂", count: 1000)
        let chunks = CodeLines.split(text)
        XCTAssertGreaterThan(chunks.count, 1)
        XCTAssertTrue(chunks.first?.showsNumber == true)
        XCTAssertTrue(chunks.dropFirst().allSatisfy { !$0.showsNumber && $0.number == 1 })
        XCTAssertTrue(chunks.allSatisfy { $0.text.utf8.count <= CodeLines.maximumChunkBytes + 3 })
        XCTAssertEqual(chunks.map(\.text).joined(), text)
        XCTAssertEqual(Set(chunks.map(\.id)).count, chunks.count)
    }

    func testPiecesCutTheLineAtItsColours() {
        let line = CodeLines.split("func run() { // go", language: SyntaxLanguage.of(name: "main.go")).first
        XCTAssertEqual(line?.pieces, [
            .init(text: "func", kind: .keyword), .init(text: " ", kind: nil), .init(text: "run", kind: .call),
            .init(text: "() { ", kind: nil), .init(text: "// go", kind: .comment),
        ])
        XCTAssertEqual(line?.pieces.map(\.text).joined(), "func run() { // go", "the pieces are the line")
    }

    func testNonASCIITextKeepsItsColoursAligned() {
        let line = CodeLines.split("let café = \"naïve\" // ünï", language: SyntaxLanguage.of(name: "a.swift")).first
        XCTAssertEqual(line?.pieces.map(\.text).joined(), "let café = \"naïve\" // ünï")
        XCTAssertEqual(line?.pieces.filter { $0.kind == .string }.map(\.text), ["\"naïve\""])
        XCTAssertEqual(line?.pieces.filter { $0.kind == .comment }.map(\.text), ["// ünï"])
    }

    func testAMegabyteIsSplitAndColouredQuickly() {
        let line = "func handle(w http.ResponseWriter, r *http.Request) { // answers /login with 200 and \"ok\"\n"
        let text = String(repeating: line, count: (1 << 20) / line.utf8.count)
        let started = Date()
        let lines = CodeLines.split(text, language: SyntaxLanguage.of(name: "server.go"))
        let seconds = Date().timeIntervalSince(started)
        XCTAssertGreaterThan(lines.count, 10_000)
        XCTAssertFalse(lines[100].spans.isEmpty)
        // A debug build, on a slow runner: the bound is for a slip to
        // something quadratic, not for the speed of a release build.
        XCTAssertLessThan(seconds, 6, "split and colour 1 MB in \(seconds) s")
    }
}

final class SyntaxHighlighterTests: XCTestCase {
    private func kinds(_ source: String, _ name: String) -> [[(String, SyntaxKind)]] {
        CodeLines.split(source, language: SyntaxLanguage.of(name: name)).map { line in
            line.pieces.compactMap { piece in piece.kind.map { (piece.text, $0) } }
        }
    }

    private func flat(_ source: String, _ name: String) -> [String] {
        kinds(source, name).flatMap { $0 }.map { "\($0.0)=\($0.1)" }
    }

    func testLanguagesByNameAndExtension() {
        XCTAssertNotNil(SyntaxLanguage.of(name: "main.GO"))
        XCTAssertNotNil(SyntaxLanguage.of(name: "Makefile"))
        XCTAssertNotNil(SyntaxLanguage.of(name: "config.yaml"))
        XCTAssertNil(SyntaxLanguage.of(name: "build.log"), "a log has no colours")
        XCTAssertNil(SyntaxLanguage.of(name: "LICENSE"))
        XCTAssertNil(SyntaxLanguage.of(name: ".gitignore"))
    }

    func testKeywordsStringsNumbersCallsAndComments() {
        XCTAssertEqual(
            flat("return add(1, \"two\") // done", "a.go"),
            ["return=keyword", "add=call", "1=number", "\"two\"=string", "// done=comment"]
        )
    }

    func testAKeywordInsideAWordIsNotAKeyword() {
        XCTAssertEqual(flat("format := ifelse + for2", "a.go"), [])
        XCTAssertEqual(flat("for i := 0; i < 10; i++ {", "a.go"), ["for=keyword", "0=number", "10=number"])
    }

    func testAStringKeepsItsEscapedQuoteAndHidesWhatIsInIt() {
        XCTAssertEqual(flat(#"x = "a \" // not a comment" + 'b'"#, "a.py"), [#""a \" // not a comment"=string"#, "'b'=string"])
    }

    func testABlockCommentRunsOverLines() {
        XCTAssertEqual(
            kinds("a /* one\ntwo\nthree */ b\nc", "a.c").map { $0.map(\.1) },
            [[.comment], [.comment], [.comment], []]
        )
        XCTAssertEqual(flat("/* x */ if", "a.c"), ["/* x */=comment", "if=keyword"], "a block comment closed on its line")
    }

    func testAMultilineStringRunsOverLines() {
        let source = "x = \"\"\"\nreturn 1\n\"\"\"\nreturn 2"
        XCTAssertEqual(kinds(source, "a.py").map { $0.map(\.1) }, [[.string], [.string], [.string], [.keyword, .number]])
    }

    func testAGoRawStringTakesNoEscapes() {
        // The backslash before the closing backtick is not an escape.
        XCTAssertEqual(flat("re := `^\\d+\\`\nreturn", "a.go"), ["`^\\d+\\`=string", "return=keyword"])
    }

    func testHashCommentsAndConfigFiles() {
        XCTAssertEqual(flat("# note\nkey: true", "a.yaml"), ["# note=comment", "true=keyword"])
        XCTAssertEqual(flat("echo hi # x", "run.sh"), ["echo=keyword", "# x=comment"])
    }

    func testAnUnclosedQuoteEndsWithItsLine() {
        XCTAssertEqual(kinds("s = 'open\nreturn", "a.py").map { $0.map(\.1) }, [[.string], [.keyword]])
    }

    func testAVeryLongLineIsLeftPlain() {
        let long = "return " + String(repeating: "x", count: SyntaxLanguage.longestLine)
        XCTAssertEqual(CodeLines.split(long, language: SyntaxLanguage.of(name: "a.go")).first?.spans, [])
    }

    func testNumbersAreNotTheEndOfAName() {
        XCTAssertEqual(flat("x1 = 0x1F + 3.5", "a.py"), ["0x1F=number", "3.5=number"])
    }
}

final class ChangeIndexTests: XCTestCase {
    func testTheLineUnderAChangedFile() {
        XCTAssertEqual(ChangedFile(path: "internal/auth/token.go", status: "modified").detailLine, "internal/auth · modified")
        XCTAssertEqual(ChangedFile(path: "login.md", status: "untracked").detailLine, "new")
        XCTAssertEqual(ChangedFile(path: "web/login.tsx", oldPath: "web/signin.tsx", status: "renamed").detailLine, "from web/signin.tsx")
        XCTAssertEqual(ChangedFile(path: "a/b.go", status: "conflicted").statusWord, "conflict")
        XCTAssertEqual(ChangedFile(path: "a/b.go", status: "deleted").statusWord, "deleted")
    }

    private func changes(root: String?) -> AgentChanges {
        AgentChanges(repo: true, root: root, branch: "main", files: [
            ChangedFile(path: "internal/auth/token.go", status: "modified", added: 9, deleted: 3),
            ChangedFile(path: "docs/a.md", status: "untracked", added: 4),
        ])
    }

    func testTheSamePathsWhenTheFolderIsTheRoot() {
        let index = ChangeIndex(changes(root: "/Users/me/shop"), folder: "/Users/me/shop")
        XCTAssertEqual(index.change(at: "internal/auth/token.go")?.added, 9)
        XCTAssertTrue(index.isChanged("docs/a.md"))
        XCTAssertFalse(index.isChanged("README.md"))
    }

    func testBelowTheRootTheFoldersPathIsCutFromTheFront() {
        let index = ChangeIndex(changes(root: "/Users/me/shop/"), folder: "/Users/me/shop/internal")
        XCTAssertEqual(index.change(at: "auth/token.go")?.path, "internal/auth/token.go", "the daemon's own path comes back, to open the diff")
        XCTAssertFalse(index.isChanged("docs/a.md"), "that file is not under this folder")
        XCTAssertFalse(index.isChanged("internal/auth/token.go"))
    }

    func testWhenTheyCannotBeRelatedTheyAreAssumedToAgree() {
        XCTAssertTrue(ChangeIndex(changes(root: "/Users/demo/code/shop"), folder: "~/code/shop").isChanged("docs/a.md"))
        XCTAssertTrue(ChangeIndex(changes(root: nil), folder: nil).isChanged("docs/a.md"))
    }

    func testTildeFolderInsideRepositoryUsesRelativeChangedPaths() {
        let changes = AgentChanges(repo: true, root: "/Users/demo/code/shop", files: [ChangedFile(path: "sub/docs/a.md", status: "modified")])
        let index = ChangeIndex(changes, folder: "~/code/shop/sub")
        XCTAssertEqual(index.prefix, "sub/")
        XCTAssertTrue(index.isChanged("docs/a.md"))
    }

    func testNothingWithoutARepository() {
        XCTAssertFalse(ChangeIndex(AgentChanges(repo: false), folder: nil).isChanged("a"))
        XCTAssertFalse(ChangeIndex(nil, folder: nil).isChanged("a"))
    }

    func testAFolderWithAChangeBelowIsMarkedToo() {
        let index = ChangeIndex(changes(root: "/Users/me/shop"), folder: "/Users/me/shop")
        XCTAssertTrue(index.isChanged("internal"))
        XCTAssertTrue(index.isChanged("internal/auth"))
        XCTAssertTrue(index.isChanged("docs"))
        XCTAssertFalse(index.isChanged("web"))
        XCTAssertNil(index.change(at: "docs"), "a folder is marked, but it has no diff of its own")
        XCTAssertFalse(index.isChanged("intern"), "a name that merely starts alike")
    }

    func testFoldersAreMarkedFromTheBrowsedFoldersView() {
        let index = ChangeIndex(changes(root: "/Users/me/shop"), folder: "/Users/me/shop/internal")
        XCTAssertTrue(index.isChanged("auth"))
        XCTAssertFalse(index.isChanged("internal"), "the browsed folder is not one of its own entries")
        XCTAssertFalse(index.isChanged("docs"))
    }
}

final class LineCountTextTests: XCTestCase {
    func testASideThatIsNothingIsLeftOut() {
        XCTAssertEqual(LineCountText.parts(added: 41, deleted: 0).map(\.text), ["+41"])
        XCTAssertEqual(LineCountText.parts(added: 0, deleted: 13).map(\.text), ["−13"])
        XCTAssertEqual(LineCountText.parts(added: 49, deleted: 4).map(\.text), ["+49", "−4"])
        XCTAssertTrue(LineCountText.parts(added: 0, deleted: 0).isEmpty)
        XCTAssertEqual(LineCountText.parts(added: 3, deleted: 1).map(\.isAddition), [true, false])
    }

    func testSpokenCounts() {
        XCTAssertEqual(LineCountText.spoken(added: 41, deleted: 0), "41 added")
        XCTAssertEqual(LineCountText.spoken(added: 3, deleted: 3), "3 added, 3 removed")
        XCTAssertEqual(LineCountText.spoken(added: 0, deleted: 13), "13 removed")
        XCTAssertEqual(LineCountText.spoken(added: 0, deleted: 0), "no line changes")
    }

    func testTheImageHintStaysMeaningfulWhenZoomed() {
        XCTAssertEqual(ImageZoomText.hint(zoom: 0.4, fitting: true), "Pinch or double-tap to zoom")
        XCTAssertEqual(ImageZoomText.hint(zoom: 1, fitting: false), "100% · double-tap to fit")
        XCTAssertEqual(ImageZoomText.hint(zoom: 2.504, fitting: false), "250% · double-tap to fit")
    }
}

final class MarkdownPathTests: XCTestCase {
    private let joiner = "\u{2060}"

    func testAPathIsGluedAfterItsSlashes() {
        XCTAssertEqual(MarkdownRenderer.gluingPaths("/login"), "/\(joiner)login")
        XCTAssertEqual(MarkdownRenderer.gluingPaths("see docs/reports/a.md now"), "see docs/\(joiner)reports/\(joiner)a.md now")
    }

    func testNothingIsAddedWhereThereIsNoMore() {
        XCTAssertEqual(MarkdownRenderer.gluingPaths("no slash here"), "no slash here")
        XCTAssertEqual(MarkdownRenderer.gluingPaths("docs/ then"), "docs/ then", "a slash that ends its word")
        XCTAssertEqual(MarkdownRenderer.gluingPaths("a / b"), "a / b")
        XCTAssertEqual(MarkdownRenderer.gluingPaths("end/"), "end/")
        XCTAssertEqual(MarkdownRenderer.gluingPaths("/\(joiner)login"), "/\(joiner)login", "gluing twice changes nothing")
    }

    func testStyledTextKeepsItsStyleAndGluesAcrossRuns() {
        let text = MarkdownRenderer.inline("open `/login` and **/logout** or /a **/b**", keepingPaths: true)
        XCTAssertEqual(String(text.characters), "open /\(joiner)login and /\(joiner)logout or /\(joiner)a /\(joiner)b")
        let code = text.runs.filter { $0.inlinePresentationIntent?.contains(.code) == true }.map { String(text[$0.range].characters) }
        XCTAssertEqual(code, ["/\(joiner)login"], "the code style stays on its run")
        let strong = text.runs.filter { $0.inlinePresentationIntent?.contains(.stronglyEmphasized) == true }.map { String(text[$0.range].characters) }
        XCTAssertEqual(strong, ["/\(joiner)logout", "/\(joiner)b"])
    }

    func testTheDefaultLeavesTextAsWritten() {
        XCTAssertEqual(String(MarkdownRenderer.inline("open /login").characters), "open /login")
        XCTAssertEqual(MarkdownRenderer.plain("open /login"), "open /login", "VoiceOver text has no marks")
    }
}

final class FileProblemTests: XCTestCase {
    func testReasonsMapAndAnUnknownOneFails() {
        XCTAssertEqual(FileProblem(reason: "sensitive"), .sensitive)
        XCTAssertEqual(FileProblem(reason: "home_root"), .homeRoot)
        XCTAssertEqual(FileProblem(reason: "private_root"), .privateRoot)
        XCTAssertEqual(FileProblem(reason: "files_off"), .filesOff)
        XCTAssertEqual(FileProblem(reason: "not_file"), .notFile)
        XCTAssertEqual(FileProblem(reason: "too_large"), .tooLarge)
        XCTAssertEqual(FileProblem(reason: "something_new"), .failed)
        XCTAssertEqual(FileProblem(reason: nil), .failed)
    }

    func testWordsNameTheFolderAndTheSize() {
        let outside = FileProblem.outside.words(subject: .file, root: "shop")
        XCTAssertEqual(outside.title, "This file is outside shop")
        XCTAssertEqual(FileProblem.notFound.words(subject: .folder).title, "That folder is gone")
        XCTAssertEqual(FileProblem.notFound.words(subject: .file).title, "That file is gone")
        let sensitiveFile = FileProblem.sensitive.words(subject: .file)
        XCTAssertEqual(sensitiveFile.title, "Hidden for safety")
        XCTAssertTrue(sensitiveFile.message.contains(".env"))
        XCTAssertFalse(sensitiveFile.message.contains("home folder"))
        XCTAssertFalse(sensitiveFile.message.contains("data folder"))
        let sensitiveFolder = FileProblem.sensitive.words(subject: .folder)
        XCTAssertEqual(sensitiveFolder.title, "Hidden for safety")
        XCTAssertTrue(sensitiveFolder.message.contains(".git"))
        XCTAssertFalse(sensitiveFolder.message.contains("home folder"))
        let home = FileProblem.homeRoot.words()
        XCTAssertEqual(home.title, "Can't open your home folder")
        XCTAssertTrue(home.message.contains("project folder"))
        let privateRoot = FileProblem.privateRoot.words()
        XCTAssertEqual(privateRoot.title, "Can't open sheepdog's data folder")
        XCTAssertTrue(privateRoot.message.contains("project folder"))
        let filesOff = FileProblem.filesOff.words()
        XCTAssertEqual(filesOff.title, "File browsing is off")
        XCTAssertTrue(filesOff.message.contains("web.files = false"))
        let big = FileProblem.tooLarge.words(subject: .file, name: "photo.png", size: 38_000_000)
        XCTAssertEqual(big.message, "photo.png is 38 MB. The phone shows images up to 16 MB, so open it on your computer.")
        XCTAssertTrue(FileProblem.denied.words().message.contains("Files and Folders"))
    }

    func testAPictureTooBigShowsThePictureSymbol() {
        XCTAssertEqual(FileProblem.tooLarge.words(subject: .image, name: "p.png", size: 38_000_000).symbol, "photo")
        XCTAssertEqual(FileProblem.tooLarge.words(subject: .file, name: "a.bin", size: 38_000_000).symbol, "doc")
        XCTAssertEqual(FileProblem.notFound.words(subject: .image).symbol, "photo")
        XCTAssertEqual(FileProblem.notFound.words(subject: .image).title, "That file is gone")
        XCTAssertEqual(FileProblem.tooLarge.words(subject: .image).title, "Too big to show here")
    }

    func testEveryProblemHasWords() {
        let all: [FileProblem] = [
            .noAgent, .noSpace, .noCwd, .notFound, .outside, .sensitive, .homeRoot, .privateRoot,
            .filesOff, .notFile, .denied, .tooLarge, .notImage, .badPath, .failed, .olderDaemon,
        ]
        for problem in all {
            let words = problem.words()
            XCTAssertFalse(words.title.isEmpty, "\(problem)")
            XCTAssertFalse(words.message.isEmpty, "\(problem)")
            XCTAssertFalse(words.symbol.isEmpty, "\(problem)")
        }
    }

    func testOnlyWhatMightChangeCanBeRetried() {
        XCTAssertTrue(FileProblem.denied.canRetry)
        XCTAssertTrue(FileProblem.failed.canRetry)
        XCTAssertFalse(FileProblem.sensitive.canRetry)
        XCTAssertFalse(FileProblem.homeRoot.canRetry)
        XCTAssertFalse(FileProblem.privateRoot.canRetry)
        XCTAssertFalse(FileProblem.filesOff.canRetry)
        XCTAssertFalse(FileProblem.outside.canRetry)
    }
}

final class FilesWireTests: XCTestCase {
    func testAnUnknownKindIsAFileAndAnUnknownContentKindIsBinary() throws {
        let entry = try DaemonClient.decoder.decode(FileEntry.self, from: Data(#"{"name":"a","path":"a","kind":"socket","size":1,"modified":"2026-10-21T09:00:00Z"}"#.utf8))
        XCTAssertEqual(entry.kind, .file)
        let content = try DaemonClient.decoder.decode(FileContent.self, from: Data(#"{"path":"a","size":1,"kind":"video"}"#.utf8))
        XCTAssertEqual(content.kind, .binary)
        XCTAssertFalse(content.truncated)
    }

    func testAListingWithoutOptionalFieldsDecodes() throws {
        let listing = try DaemonClient.decoder.decode(FileListing.self, from: Data(#"{"root_name":"shop","entries":[]}"#.utf8))
        XCTAssertEqual(listing.path, "")
        XCTAssertEqual(listing.hidden, 0)
        XCTAssertFalse(listing.truncated)
    }

    func testPathsAreEscapedPerSegment() {
        XCTAssertEqual(DaemonClient.escapedFilePath("docs/my report #1.md"), "docs/my%20report%20%231.md")
        XCTAssertEqual(DaemonClient.escapedFilePath("a%b/c?d"), "a%25b/c%3Fd")
    }

    func testQueryValuesAreEscapedSoPlusAndAmpersandSurvive() throws {
        let client = DaemonClient(endpoint: DaemonEndpoint(host: "127.0.0.1", port: 7777))
        let request = try client.filesRequest(encodedPath: FileScope.agent(paneID: "w1:p2").basePath, query: [("path", "c++ & more"), ("q", "a+b")])
        XCTAssertEqual(request.url?.absoluteString, "http://127.0.0.1:7777/api/agents/w1:p2/files?path=c%2B%2B%20%26%20more&q=a%2Bb")
    }

    func testRoutes() {
        XCTAssertEqual(FileScope.agent(paneID: "w1:p2").basePath, "/api/agents/w1:p2/files")
        XCTAssertEqual(FileScope.agent(paneID: "%12").basePath, "/api/agents/%2512/files")
        XCTAssertEqual(FileScope.space(id: "w1").basePath, "/api/spaces/w1/files")
    }
}

/// The client against the offline demo, which answers as the daemon does:
/// the same routes, shapes and refusals.
final class FilesClientTests: XCTestCase {
    private var daemon: DemoDaemon!
    private var client: DaemonClient!
    private let agent = FileScope.agent(paneID: DemoWorld.authPane)

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

    private func unwrapped<T>(_ value: T?, file: StaticString = #filePath, line: UInt = #line) throws -> T {
        try XCTUnwrap(value, file: file, line: line)
    }

    private func refusal<T>(_ body: () async throws -> T) async -> FilesRefusal? {
        do {
            _ = try await body()
            return nil
        } catch {
            return error as? FilesRefusal
        }
    }

    func testTheRootListsFoldersFirstAndCountsWhatIsHidden() async throws {
        let listing = try unwrapped(try await client.files(agent))
        XCTAssertEqual(listing.rootName, "shop")
        XCTAssertEqual(listing.hidden, 2)
        let kinds = listing.entries.map(\.kind)
        XCTAssertEqual(kinds.prefix { $0 == .dir }.count, kinds.filter { $0 == .dir }.count, "folders come first")
        XCTAssertFalse(listing.entries.contains { $0.name == ".env" }, "a sensitive name is not listed")
        XCTAssertTrue(listing.entries.contains { $0.name == "notes.md" && $0.isBlocked })
    }

    func testAFolderOfASpace() async throws {
        let listing = try unwrapped(try await client.files(.space(id: "w1"), path: "docs"))
        XCTAssertEqual(listing.path, "docs")
        XCTAssertEqual(listing.entries.map(\.name).prefix(2), ["images", "reports"])
        XCTAssertEqual(listing.entries.first { $0.name == "api.md" }?.path, "docs/api.md")
    }

    func testSearchFindsNamesUnderTheFolderShallowestFirst() async throws {
        let listing = try unwrapped(try await client.files(agent, query: "LOGIN"))
        XCTAssertEqual(listing.entries.map(\.path), ["web/login.tsx", "docs/images/login-flow.png", "docs/reports/login-loop.md"])
        let none = try unwrapped(try await client.files(agent, query: "ledger"))
        XCTAssertTrue(none.entries.isEmpty)
        let inFolder = try unwrapped(try await client.files(agent, path: "docs", query: "login"))
        XCTAssertEqual(inFolder.entries.count, 2)
    }

    func testSearchNeverMatchesAHiddenNameButCountsIt() async throws {
        let listing = try unwrapped(try await client.files(agent, query: ".env"))
        XCTAssertTrue(listing.entries.isEmpty)
        XCTAssertEqual(listing.hidden, 1)
    }

    func testReadingTextAndTheKindsOfTheOthers() async throws {
        let text = try unwrapped(try await client.file(agent, path: "docs/reports/login-loop.md"))
        XCTAssertEqual(text.kind, .text)
        XCTAssertTrue(text.text?.hasPrefix("# Login redirect loop") == true)
        XCTAssertEqual(text.size, text.text?.utf8.count)
        let log = try unwrapped(try await client.file(agent, path: "build.log"))
        XCTAssertTrue(log.truncated)
        let binary = try unwrapped(try await client.file(agent, path: "data.sqlite"))
        XCTAssertEqual(binary.kind, .binary)
        XCTAssertNil(binary.text)
        let image = try unwrapped(try await client.file(agent, path: "docs/images/login-flow.png"))
        XCTAssertEqual(image.kind, .image)
    }

    func testAnImageComesAsBytes() async throws {
        let bytes = try await client.fileImage(agent, path: "docs/images/login-flow.png")
        XCTAssertEqual(Array(bytes.prefix(4)), [0x89, 0x50, 0x4E, 0x47], "a PNG")
        let content = try unwrapped(try await client.file(agent, path: "docs/images/login-flow.png"))
        XCTAssertEqual(content.size, bytes.count)
    }

    func testALinkInsideIsFollowed() async throws {
        let content = try unwrapped(try await client.file(agent, path: "latest"))
        XCTAssertEqual(content.path, "latest")
        XCTAssertEqual(content.kind, .text)
    }

    func testRefusalsCarryTheDaemonsReason() async throws {
        let outside = await refusal { try await client.file(agent, path: "notes.md") }
        XCTAssertEqual(outside?.problem, .outside)
        XCTAssertEqual(outside?.status, 403)
        let hidden = await refusal { try await client.file(agent, path: ".env") }
        XCTAssertEqual(hidden?.problem, .sensitive)
        let folder = await refusal { try await client.file(agent, path: "docs") }
        XCTAssertEqual(folder?.problem, .notFile)
        let gone = await refusal { try await client.file(agent, path: "nothing.md") }
        XCTAssertEqual(gone?.problem, .notFound)
        XCTAssertEqual(gone?.status, 404)
        let notAFolder = await refusal { try await client.files(agent, path: "README.md") }
        XCTAssertEqual(notAFolder?.problem, .notFound)
        let tooBig = await refusal { try await client.fileImage(agent, path: "big-photo.png") }
        XCTAssertEqual(tooBig?.problem, .tooLarge)
        XCTAssertEqual(tooBig?.status, 413)
        let notImage = await refusal { try await client.fileImage(agent, path: "README.md") }
        XCTAssertEqual(notImage?.problem, .notImage)
        let noAgent = await refusal { try await client.files(.agent(paneID: "nope")) }
        XCTAssertEqual(noAgent?.problem, .noAgent)
        let noSpace = await refusal { try await client.files(.space(id: "nope")) }
        XCTAssertEqual(noSpace?.problem, .noSpace)
    }

    func testAnotherFolderIsASmallOne() async throws {
        let listing = try unwrapped(try await client.files(.space(id: "w2")))
        XCTAssertEqual(listing.rootName, "shop-fix-login")
        XCTAssertEqual(listing.entries.map(\.name), ["ideas.txt", "README.md"])
    }
}

@MainActor
final class FileBrowserModelTests: XCTestCase {
    private func listing(_ path: String = "", _ names: [String] = ["a.md"], root: String = "shop") -> FileListing {
        FileListing(rootName: root, path: path, entries: names.map { FileEntry(name: $0, path: path.isEmpty ? $0 : path + "/" + $0, modified: now) })
    }

    private func model(debounce: Duration = .zero, list: @escaping FileBrowserModel.List) -> FileBrowserModel {
        FileBrowserModel(list: list, describe: { RequestFailure($0, linkUsable: true) }, debounce: debounce, clock: { now })
    }

    func testStartLoadsTheRoot() async {
        let model = model { path, query in
            XCTAssertEqual(path, "")
            XCTAssertNil(query)
            return self.listing()
        }
        XCTAssertEqual(model.folder, .loading)
        await model.start()
        XCTAssertEqual(model.folder.value?.entries.map(\.name), ["a.md"])
        XCTAssertEqual(model.rootName, "shop")
        XCTAssertEqual(model.crumbs.map(\.name), ["shop"])
    }

    func testOpeningAFolderMovesTheCrumbs() async {
        let model = model { path, _ in self.listing(path, ["x.md"]) }
        await model.start()
        await model.open(folder: "docs/reports")
        XCTAssertEqual(model.path, "docs/reports")
        XCTAssertEqual(model.crumbs.map(\.path), ["", "docs", "docs/reports"])
        XCTAssertEqual(model.scopeName, "reports")
        XCTAssertEqual(model.folder.value?.entries.first?.path, "docs/reports/x.md")
        await model.open(folder: "")
        XCTAssertEqual(model.scopeName, "shop")
    }

    func testAnOlderDaemonAndARefusalAndAnOfflineComputer() async {
        let older = model { _, _ in nil }
        await older.start()
        XCTAssertEqual(older.folder, .failed(.problem(.olderDaemon)))
        let refused = model { _, _ in throw FilesRefusal(status: 404, problem: .notFound, message: "not found") }
        await refused.start()
        XCTAssertEqual(refused.folder, .failed(.problem(.notFound)))
        let offline = model { _, _ in throw DaemonError.unreachable(endpoint: "x:1", detail: "") }
        await offline.start()
        guard case let .failed(failure) = offline.folder else { return XCTFail("\(offline.folder)") }
        XCTAssertTrue(failure.isOffline)
        XCTAssertTrue(failure.canRetry)
    }

    func testACancelledReadIsNotAFailure() async {
        let model = model { _, _ in throw CancellationError() }
        await model.start()
        XCTAssertEqual(model.folder, .loading)
    }

    func testSearchWaitsThenReplacesTheListAndClearingGoesBack() async throws {
        var asked: [String?] = []
        let model = model { path, query in
            asked.append(query)
            return query == nil ? self.listing() : self.listing("", ["login.md", "login.png"])
        }
        await model.start()
        model.query = "  login "
        XCTAssertTrue(model.isSearching)
        XCTAssertEqual(model.results, .loading)
        try await Task.sleep(for: .milliseconds(150))
        XCTAssertEqual(model.entries.map(\.name), ["login.md", "login.png"])
        XCTAssertEqual(asked, [nil, "login"], "the query is trimmed")
        XCTAssertEqual(model.searchSummary, "2 files named “login” in shop")
        model.clearSearch()
        XCTAssertNil(model.results)
        XCTAssertEqual(model.entries.map(\.name), ["a.md"])
        XCTAssertNil(model.searchSummary)
    }

    func testNoResultsHaveNoSummary() async throws {
        let model = model { _, query in query == nil ? self.listing() : self.listing("", []) }
        await model.start()
        model.query = "ledger"
        try await Task.sleep(for: .milliseconds(150))
        XCTAssertEqual(model.results?.value?.entries.count, 0, "the search ran and found nothing")
        XCTAssertNil(model.searchSummary, "the empty state says it, so the summary does not repeat it")
    }

    func testOnlyTheLastKeystrokeAsks() async throws {
        var asked: [String?] = []
        let model = model(debounce: .milliseconds(80)) { _, query in
            asked.append(query)
            return self.listing()
        }
        model.query = "l"
        model.query = "lo"
        model.query = "log"
        try await Task.sleep(for: .milliseconds(250))
        XCTAssertEqual(asked, ["log"])
    }

    func testAnOldAnswerDoesNotOverwriteANewerOne() async throws {
        var calls = 0
        let model = model { _, query in
            guard query != nil else { return self.listing() }
            calls += 1
            if calls == 1 { try await Task.sleep(for: .milliseconds(200)) }
            return self.listing("", [calls == 1 ? "old.md" : "new.md"])
        }
        model.query = "a"
        try await Task.sleep(for: .milliseconds(40))
        model.query = "ab"
        try await Task.sleep(for: .milliseconds(400))
        XCTAssertEqual(model.entries.map(\.name), ["new.md"])
    }

    func testAnOpenFolderIgnoresTheAnswerForTheOneItLeft() async throws {
        let model = model { path, _ in
            if path == "slow" { try await Task.sleep(for: .milliseconds(200)) }
            return self.listing(path, [path.isEmpty ? "root.md" : "\(path).md"])
        }
        let slow = Task { await model.open(folder: "slow") }
        try await Task.sleep(for: .milliseconds(30))
        await model.open(folder: "")
        await slow.value
        XCTAssertEqual(model.path, "")
        XCTAssertEqual(model.folder.value?.entries.map(\.name), ["root.md"])
    }

    func testRecentOrderAndGroups() async {
        let list = FileListing(rootName: "shop", entries: [
            FileEntry(name: "old.md", modified: ago(20 * 86400)),
            FileEntry(name: "new.md", modified: ago(60)),
        ])
        let model = model { _, _ in list }
        await model.start()
        XCTAssertNil(model.groups)
        XCTAssertEqual(model.entries.map(\.name), ["old.md", "new.md"])
        model.sort = .recent
        XCTAssertEqual(model.entries.map(\.name), ["new.md", "old.md"])
        XCTAssertEqual(model.groups?.map(\.bucket), [.today, .older])
    }

    func testALinkToAFolderListsAndALinkToAFileDoesNot() async {
        let model = model { path, _ in
            if path == "latest" { return self.listing(path) }
            throw FilesRefusal(status: 404, problem: .notFound, message: "not found")
        }
        let folder = await model.resolve(link: FileEntry(name: "latest", kind: .link))
        XCTAssertEqual(folder, .folder("latest"))
        let file = await model.resolve(link: FileEntry(name: "other", kind: .link))
        XCTAssertEqual(file, .file)
        let blocked = await model.resolve(link: FileEntry(name: "latest", kind: .link, blocked: true))
        XCTAssertEqual(blocked, .file, "a blocked link is not even asked")
    }

    func testFailedLinkProbesLeaveTheLoadedListAlone() async {
        let failures: [Error] = [
            FilesRefusal(status: 403, problem: .sensitive, message: "hidden"),
            FilesRefusal(status: 403, problem: .outside, message: "outside"),
            DaemonError.unreachable(endpoint: "x:1", detail: ""),
        ]
        for failure in failures {
            let model = model { path, _ in
                if path.isEmpty { return self.listing() }
                throw failure
            }
            await model.start()
            let original = model.folder
            let target = await model.resolve(link: FileEntry(name: "link", kind: .link))
            XCTAssertEqual(target, .file)
            XCTAssertEqual(model.folder, original, "probe failed: \(failure)")
            XCTAssertEqual(model.path, "")
        }
    }

    func testReloadKeepsWhatIsShownUntilTheAnswer() async {
        var names = ["a.md"]
        let model = model { _, _ in self.listing("", names) }
        await model.start()
        names = ["a.md", "b.md"]
        await model.reload()
        XCTAssertEqual(model.entries.map(\.name), ["a.md", "b.md"])
    }
}

@MainActor
final class FileViewerModelTests: XCTestCase {
    private func model(_ path: String, content: FileContent?, image: Data = Data(), failure: Error? = nil) -> FileViewerModel {
        FileViewerModel(
            path: path,
            read: { _ in
                if let failure { throw failure }
                return content
            },
            image: { _ in image },
            describe: { RequestFailure($0, linkUsable: true) }
        )
    }

    func testMarkdownOpensRenderedAndCodeOpensAsSource() async {
        let md = model("docs/a.md", content: FileContent(path: "docs/a.md", size: 5, kind: .text, text: "# Hi\n"))
        XCTAssertEqual(md.mode, .rendered)
        XCTAssertEqual(md.name, "a.md")
        XCTAssertEqual(md.folder, "docs")
        await md.load()
        XCTAssertEqual(md.phase, .text(truncated: false))
        XCTAssertEqual(md.blocks, [.heading("Hi")])
        XCTAssertTrue(md.lines.isEmpty, "the lines are made when Raw is asked for")
        let code = model("main.go", content: FileContent(path: "main.go", size: 1, kind: .text, text: "x"))
        XCTAssertEqual(code.mode, .raw)
        await code.load()
        XCTAssertEqual(code.lines.map(\.text), ["x"])
        XCTAssertTrue(code.blocks.isEmpty, "a code file has no document")
    }

    func testTheOtherViewOfADocumentIsMadeBeforeItShows() async {
        let md = model("a.md", content: FileContent(path: "a.md", size: 9, kind: .text, text: "# Hi\n\nline\n"))
        await md.load()
        await md.select(.raw)
        XCTAssertEqual(md.mode, .raw)
        XCTAssertEqual(md.lines.map(\.text), ["# Hi", "", "line"])
        XCTAssertEqual(md.blocks.count, 2, "the document is kept for the way back")
        await md.select(.rendered)
        XCTAssertEqual(md.mode, .rendered)
        await md.select(.rendered)
        XCTAssertEqual(md.mode, .rendered)
    }

    func testAFileThatIsNotMarkdownHasNoOtherView() async {
        let code = model("main.go", content: FileContent(path: "main.go", size: 1, kind: .text, text: "x"))
        await code.load()
        await code.select(.rendered)
        XCTAssertEqual(code.mode, .raw)
    }

    func testCodeCarriesItsColoursAndAPlainTextDoesNot() async {
        let code = model("main.go", content: FileContent(path: "main.go", size: 8, kind: .text, text: "return 1\n"))
        await code.load()
        XCTAssertEqual(code.lines.first?.spans.map(\.kind), [.keyword, .number])
        let log = model("build.log", content: FileContent(path: "build.log", size: 8, kind: .text, text: "return 1\n"))
        await log.load()
        XCTAssertEqual(log.lines.first?.spans, [])
    }

    func testAnEmptyTextHasNoLines() async {
        let empty = model("empty.txt", content: FileContent(path: "empty.txt", kind: .text, text: nil))
        await empty.load()
        XCTAssertEqual(empty.phase, .text(truncated: false))
        XCTAssertTrue(empty.lines.isEmpty)
    }

    func testATruncatedTextSaysSo() async {
        let big = model("a.log", content: FileContent(path: "a.log", size: 2 << 20, kind: .text, text: "x", truncated: true))
        await big.load()
        XCTAssertEqual(big.phase, .text(truncated: true))
    }

    func testAnImageFetchesItsBytesAfterTheKindIsKnownAndKeepsOnlyThePicture() async {
        var fetched = 0
        let bytes = FileImageTests.png(width: 40, height: 20)
        let image = FileViewerModel(
            path: "a.png",
            read: { _ in FileContent(path: "a.png", size: bytes.count, kind: .image) },
            image: { _ in fetched += 1; return bytes },
            describe: { RequestFailure($0, linkUsable: true) }
        )
        await image.load()
        XCTAssertEqual(fetched, 1)
        guard case let .image(picture) = image.phase else { return XCTFail("\(image.phase)") }
        XCTAssertEqual(picture.sizeText, "40 × 20")
        XCTAssertEqual(image.content?.size, bytes.count)
    }

    func testBytesThatAreNotAPictureSaySo() async {
        let image = model("a.png", content: FileContent(path: "a.png", size: 3, kind: .image), image: Data([1, 2, 3]))
        await image.load()
        XCTAssertEqual(image.phase, .unreadableImage)
    }

    func testARefusalNamesAPictureWithThePictureSymbol() async {
        let png = model("big.png", content: FileContent(path: "big.png", size: FileBrowser.imageLimit + 1, kind: .image))
        XCTAssertEqual(png.failureSubject, .image, "by its name before it is read")
        await png.load()
        XCTAssertEqual(png.failureSubject, .image)
        let text = model("a.md", content: FileContent(path: "a.md", kind: .text, text: ""))
        await text.load()
        XCTAssertEqual(text.failureSubject, .file)
    }

    func testATooLargeImageIsNotFetched() async {
        var fetched = false
        let viewer = FileViewerModel(
            path: "big.png",
            read: { _ in FileContent(path: "big.png", size: FileBrowser.imageLimit + 1, kind: .image) },
            image: { _ in fetched = true; return Data() },
            describe: { RequestFailure($0, linkUsable: true) }
        )
        await viewer.load()
        XCTAssertEqual(viewer.phase, .failed(.problem(.tooLarge)))
        XCTAssertFalse(fetched)
        XCTAssertEqual(viewer.content?.size, FileBrowser.imageLimit + 1, "the size is there for the words")
    }

    func testBinaryAndRefusalsAndAnOlderDaemon() async {
        let binary = model("a.sqlite", content: FileContent(path: "a.sqlite", size: 9, kind: .binary))
        await binary.load()
        XCTAssertEqual(binary.phase, .binary)
        let hidden = model(".env", content: nil, failure: FilesRefusal(status: 403, problem: .sensitive, message: "hidden for safety"))
        await hidden.load()
        XCTAssertEqual(hidden.phase, .failed(.problem(.sensitive)))
        let older = model("a.md", content: nil)
        await older.load()
        XCTAssertEqual(older.phase, .failed(.problem(.olderDaemon)))
        let offline = model("a.md", content: nil, failure: DaemonError.timedOut(endpoint: "x:1"))
        await offline.load()
        guard case let .failed(failure) = offline.phase else { return XCTFail("\(offline.phase)") }
        XCTAssertTrue(failure.canRetry)
    }

    func testLoadingAgainReplacesWhatWasThere() async {
        var text = "one"
        let viewer = FileViewerModel(
            path: "a.txt",
            read: { _ in FileContent(path: "a.txt", size: 3, kind: .text, text: text) },
            image: { _ in Data() },
            describe: { RequestFailure($0, linkUsable: true) }
        )
        await viewer.load()
        text = "two"
        await viewer.load()
        XCTAssertEqual(viewer.lines.map(\.text), ["two"])
    }

    func testLoadingAgainKeepsTheOtherViewOfADocumentCurrent() async {
        var text = "# one"
        let viewer = FileViewerModel(
            path: "a.md",
            read: { _ in FileContent(path: "a.md", size: 5, kind: .text, text: text) },
            image: { _ in Data() },
            describe: { RequestFailure($0, linkUsable: true) }
        )
        await viewer.load()
        await viewer.select(.raw)
        text = "# two"
        await viewer.load()
        XCTAssertEqual(viewer.lines.map(\.text), ["# two"])
        await viewer.select(.rendered)
        XCTAssertEqual(viewer.blocks, [.heading("two")], "not the document of the first read")
    }
}

final class FileImageTests: XCTestCase {
    /// A solid-colour PNG of the given size, made with ImageIO.
    static func png(width: Int, height: Int) -> Data {
        let context = CGContext(
            data: nil, width: width, height: height, bitsPerComponent: 8, bytesPerRow: 0,
            space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue
        )!
        context.setFillColor(red: 0.8, green: 0.2, blue: 0.1, alpha: 1)
        context.fill(CGRect(x: 0, y: 0, width: width, height: height))
        let data = NSMutableData()
        let destination = CGImageDestinationCreateWithData(data, "public.png" as CFString, 1, nil)!
        CGImageDestinationAddImage(destination, context.makeImage()!, nil)
        CGImageDestinationFinalize(destination)
        return data as Data
    }

    /// Decodes and unwraps: a picture, or the test fails.
    private func decode(_ data: Data, maxPixel: Int, file: StaticString = #filePath, line: UInt = #line) async throws -> FileImage {
        let image = await FileImage.decode(data, maxPixel: maxPixel)
        return try XCTUnwrap(image, "not a picture", file: file, line: line)
    }

    func testASmallPictureIsKeptWhole() async throws {
        let image = try await decode(Self.png(width: 40, height: 20), maxPixel: 100)
        XCTAssertEqual([image.cgImage.width, image.cgImage.height], [40, 20])
        XCTAssertEqual([image.originalWidth, image.originalHeight], [40, 20])
        XCTAssertEqual(image.scale, 1, accuracy: 0.0001)
        XCTAssertEqual(image.sizeText, "40 × 20")
    }

    func testALargePictureIsCutDownAndRemembersItsRealSize() async throws {
        let image = try await decode(Self.png(width: 400, height: 200), maxPixel: 100)
        XCTAssertEqual([image.cgImage.width, image.cgImage.height], [100, 50])
        XCTAssertEqual([image.originalWidth, image.originalHeight], [400, 200])
        XCTAssertEqual(image.scale, 0.25, accuracy: 0.0001, "drawn at this scale it is 400 points wide, so 100% is the real picture")
        XCTAssertEqual(image.sizeText, "400 × 200")
    }

    func testATallPictureIsCutByItsLongSide() async throws {
        let image = try await decode(Self.png(width: 50, height: 500), maxPixel: 100)
        XCTAssertEqual([image.cgImage.width, image.cgImage.height], [10, 100])
    }

    func testTheCapsAreWhatThePhoneCanHold() {
        XCTAssertEqual(FileImage.viewerMaxPixel, 4_096)
        XCTAssertLessThan(FileImage.inlineMaxPixel, FileImage.viewerMaxPixel)
    }

    func testBytesThatAreNotAPictureGiveNothing() async {
        let none = await FileImage.decode(Data([1, 2, 3, 4]), maxPixel: 100)
        XCTAssertNil(none)
        let empty = await FileImage.decode(Data(), maxPixel: 100)
        XCTAssertNil(empty)
    }

    func testTwoDecodesOfOneFileAreTwoPictures() async throws {
        let bytes = Self.png(width: 8, height: 8)
        let a = try await decode(bytes, maxPixel: 100)
        let b = try await decode(bytes, maxPixel: 100)
        XCTAssertEqual(a, a)
        XCTAssertNotEqual(a, b, "a picture is itself, not its pixels")
    }
}

final class MarkdownImageBlockTests: XCTestCase {
    func testALoneImageIsABlockAndTheRestIsText() {
        XCTAssertEqual(MarkdownRenderer.blocks("![Login flow](../images/login-flow.png)"), [.image(alt: "Login flow", source: "../images/login-flow.png")])
        XCTAssertEqual(MarkdownRenderer.blocks("before\n\n![a](b.png \"title\")\n\nafter"), [.paragraph("before"), .image(alt: "a", source: "b.png"), .paragraph("after")])
        XCTAssertEqual(MarkdownRenderer.blocks("see ![a](b.png) here"), [.paragraph("see ![a](b.png) here")])
        XCTAssertEqual(MarkdownRenderer.blocks("![a]()"), [.paragraph("![a]()")])
    }

    func testTheOneTextRenderingKeepsWhatItShowedBefore() {
        XCTAssertEqual(MarkdownRenderer.plain("![Login flow](a.png)"), "Login flow")
    }
}
