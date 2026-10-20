@testable import SheepdogKit
import XCTest

final class KeyEncodingTests: XCTestCase {
    private func s(_ bytes: [UInt8]) -> String { String(decoding: bytes, as: UTF8.self) }

    func testSequences() {
        XCTAssertEqual(TermKey.esc.bytes, [0x1b])
        XCTAssertEqual(TermKey.tab.bytes, [0x09])
        XCTAssertEqual(s(TermKey.shiftTab.bytes), "\u{1b}[Z")
        XCTAssertEqual(TermKey.enter.bytes, [0x0d])
        XCTAssertEqual(TermKey.backspace.bytes, [0x7f])
        XCTAssertEqual(s(TermKey.up.bytes), "\u{1b}[A")
        XCTAssertEqual(s(TermKey.left.bytes), "\u{1b}[D")
        XCTAssertEqual(s(TermKey.pageUp.bytes), "\u{1b}[5~")
        XCTAssertEqual(s(TermKey.pageDown.bytes), "\u{1b}[6~")
        XCTAssertEqual(s(TermKey.home.bytes), "\u{1b}[H")
        XCTAssertEqual(s(TermKey.end.bytes), "\u{1b}[F")
        XCTAssertEqual(s(TermKey.function(1).bytes), "\u{1b}OP")
        XCTAssertEqual(s(TermKey.function(4).bytes), "\u{1b}OS")
        XCTAssertEqual(s(TermKey.function(5).bytes), "\u{1b}[15~")
        XCTAssertEqual(s(TermKey.function(6).bytes), "\u{1b}[17~")
        XCTAssertEqual(s(TermKey.function(10).bytes), "\u{1b}[21~")
        XCTAssertEqual(s(TermKey.function(11).bytes), "\u{1b}[23~")
        XCTAssertEqual(s(TermKey.function(12).bytes), "\u{1b}[24~")
        XCTAssertEqual(TermKey.ctrl("c").bytes, [0x03])
        XCTAssertEqual(TermKey.ctrl("d").bytes, [0x04])
        XCTAssertEqual(TermKey.text("y").bytes, [0x79])
        XCTAssertEqual(KeyEncoding.functions.count, 12)
    }

    func testControlCodes() {
        XCTAssertEqual(KeyEncoding.control("a"), 1)
        XCTAssertEqual(KeyEncoding.control("Z"), 26)
        XCTAssertEqual(KeyEncoding.control("["), 0x1b)
        XCTAssertEqual(KeyEncoding.control("@"), 0)
        XCTAssertEqual(KeyEncoding.control(" "), 0)
        XCTAssertEqual(KeyEncoding.control("_"), 0x1f)
        XCTAssertEqual(KeyEncoding.control("?"), 0x7f)
        XCTAssertNil(KeyEncoding.control("1"))
        XCTAssertNil(KeyEncoding.control("界"))
    }

    func testOneShotModifiers() {
        var m = KeyModifiers(ctrl: true)
        XCTAssertEqual(m.apply(to: Array("r".utf8)), [0x12])
        XCTAssertTrue(m.isEmpty, "one shot")
        XCTAssertEqual(m.apply(to: Array("r".utf8)), Array("r".utf8))
        m.alt = true
        XCTAssertEqual(m.apply(to: Array("b".utf8)), [0x1b, 0x62])
        m = KeyModifiers(ctrl: true, alt: true)
        XCTAssertEqual(m.apply(to: Array("x".utf8)), [0x1b, 0x18])
        m.ctrl = true
        XCTAssertEqual(m.apply(to: Array("ab".utf8)), Array("ab".utf8), "ctrl applies to one character only")
        m.alt = true
        XCTAssertEqual(m.apply(to: TermKey.left.bytes), [0x1b] + TermKey.left.bytes, "alt+arrow")
    }

    func testAllowListForTheSnapshotWindow() {
        XCTAssertEqual(TermKey.esc.allowListed, .esc)
        XCTAssertEqual(TermKey.ctrl("c").allowListed, .ctrlC)
        XCTAssertEqual(TermKey.text("2").allowListed, .two)
        XCTAssertNil(TermKey.shiftTab.allowListed)
        XCTAssertNil(TermKey.function(1).allowListed)
        XCTAssertTrue(TermKey.ctrl("c").needsConfirmation)
        XCTAssertFalse(TermKey.ctrl("d").needsConfirmation)
    }

    func testSnippets() {
        var store = SnippetStore()
        XCTAssertFalse(store.add("   "))
        XCTAssertTrue(store.add("run the tests"))
        XCTAssertTrue(store.add("git status"))
        XCTAssertTrue(store.add(" run the tests "))
        XCTAssertEqual(store.items, ["run the tests", "git status"], "a duplicate moves to the top")
        for i in 0 ..< 30 { store.add("s\(i)") }
        XCTAssertEqual(store.items.count, SnippetStore.maxCount)
        store.remove(atOffsets: IndexSet(integer: 0))
        XCTAssertEqual(store.items.first, "s28")
        let defaults = UserDefaults(suiteName: "KeyEncodingTests")!
        defaults.removePersistentDomain(forName: "KeyEncodingTests")
        store.save(defaults)
        XCTAssertEqual(SnippetStore.load(defaults), store)
    }
}
