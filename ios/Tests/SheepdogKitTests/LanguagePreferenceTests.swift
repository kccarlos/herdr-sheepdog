import XCTest
@testable import SheepdogKit

/// Settings ▸ Languages: decoding the daemon's settings with and without
/// the language fields, the summary and lists the screen shows, the
/// ordered-list edits, and the PUT that sends only the languages.
final class LanguagePreferenceTests: XCTestCase {
    override func setUp() {
        super.setUp()
        StubProtocol.seen = []
    }

    static let settingsJSON = #"""
    {"chattiness":"normal","levels":["quiet","normal","chatty"],"languages":["zh-Hans","en"],
     "language_options":[
       {"code":"en","name":"English","native":"English","providers":["gemini","openai"]},
       {"code":"zh-Hans","name":"Chinese (Simplified)","native":"简体中文","providers":["gemini","openai"]},
       {"code":"zh-Hant","name":"Chinese (Traditional)","native":"繁體中文","providers":["gemini","openai"]},
       {"code":"ja","name":"Japanese","native":"日本語","providers":["gemini"]}]}
    """#

    func testDecodesTheLanguagesAndTheCatalog() throws {
        let s = try DaemonClient.decoder.decode(DaemonSettings.self, from: Data(Self.settingsJSON.utf8))
        XCTAssertTrue(s.supportsLanguages)
        XCTAssertEqual(s.languages, ["zh-Hans", "en"])
        XCTAssertEqual(s.chosenLanguages.map(\.native), ["简体中文", "English"], "in preference order, not catalog order")
        XCTAssertEqual(s.otherLanguages.map(\.code), ["zh-Hant", "ja"], "the rest in catalog order")
        XCTAssertEqual(s.languagesSummary, "简体中文, English")
        XCTAssertEqual(s.languageOptions?[0].providersSummary, "Gemini Live and GPT-Live")
        XCTAssertEqual(s.languageOptions?[3].providersSummary, "Gemini Live only")
    }

    func testAnOlderDaemonHasNoLanguageSetting() throws {
        let s = try DaemonClient.decoder.decode(DaemonSettings.self, from: Data(#"{"chattiness":"normal","levels":["quiet","normal","chatty"]}"#.utf8))
        XCTAssertFalse(s.supportsLanguages)
        XCTAssertEqual(s, DaemonSettings(chattiness: "normal", levels: ["quiet", "normal", "chatty"]))
        XCTAssertEqual(s.languagesSummary, "Not set")
        XCTAssertEqual(s.chosenLanguages, [])
    }

    func testAStaleCodeIsSkipped() {
        let s = DaemonSettings(chattiness: "normal", levels: [], languages: ["xx", "en"],
                               languageOptions: [LanguageOption(code: "en", name: "English", native: "English", providers: ["gemini", "openai"])])
        XCTAssertEqual(s.chosenLanguages.map(\.code), ["en"])
    }

    func testOrderedListEdits() {
        let list = ["zh-Hans", "en", "ja"]
        XCTAssertEqual(LanguagePreference.adding("zh-Hant", to: list), ["zh-Hans", "en", "ja", "zh-Hant"])
        XCTAssertEqual(LanguagePreference.adding("en", to: list), list, "already there")
        XCTAssertEqual(LanguagePreference.removing(atOffsets: IndexSet(integer: 1), from: list), ["zh-Hans", "ja"])
        // SwiftUI's onMove: drag "ja" to the top, "zh-Hans" to the end.
        XCTAssertEqual(LanguagePreference.moving(fromOffsets: IndexSet(integer: 2), toOffset: 0, in: list), ["ja", "zh-Hans", "en"])
        XCTAssertEqual(LanguagePreference.moving(fromOffsets: IndexSet(integer: 0), toOffset: 3, in: list), ["en", "ja", "zh-Hans"])
        XCTAssertEqual(LanguagePreference.moving(fromOffsets: IndexSet(integer: 0), toOffset: 2, in: list), ["en", "zh-Hans", "ja"])
        XCTAssertEqual(LanguagePreference.moving(fromOffsets: IndexSet(integer: 1), toOffset: 1, in: list), list, "dropped where it was")
    }

    func testPutSendsOnlyTheLanguages() async throws {
        StubProtocol.handler = { _ in (200, Data(Self.settingsJSON.utf8)) }
        let s = try await StubProtocol.client(token: "t").setLanguages(["zh-Hans", "en"])
        XCTAssertEqual(s.languages, ["zh-Hans", "en"])
        let request = try XCTUnwrap(StubProtocol.seen.last)
        XCTAssertEqual(request.httpMethod, "PUT")
        XCTAssertEqual(request.url?.path, "/api/settings")
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: request.httpBody ?? Data()) as? [String: Any])
        XCTAssertEqual(body.keys.sorted(), ["languages"], "the chattiness is left alone")
        XCTAssertEqual(body["languages"] as? [String], ["zh-Hans", "en"])
    }

    func testARefusedCodeCarriesTheDaemonsWords() async throws {
        StubProtocol.handler = { _ in (400, Data(#"{"error":"supervisor: language \"xx\" is not in the catalog"}"#.utf8)) }
        do {
            _ = try await StubProtocol.client().setLanguages(["xx"])
            XCTFail("expected the daemon's refusal")
        } catch let error as DaemonError {
            XCTAssertEqual(error, .rejected(status: 400, message: #"supervisor: language "xx" is not in the catalog"#))
        }
    }
}
