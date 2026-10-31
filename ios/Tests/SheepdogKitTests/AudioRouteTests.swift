import XCTest
@testable import SheepdogKit

/// The voice bar's route glyph and its VoiceOver words (`AudioRoute`), which
/// build on macOS too. The iOS port mapping is in `AudioRoutePortTests`.
final class AudioRouteTests: XCTestCase {
    private let all: [AudioRoute] = [.speaker, .receiver, .headphones, .bluetooth, .car, .airPlay]

    func testEveryRouteHasItsOwnGlyphAndWords() {
        XCTAssertEqual(Set(all.map(\.symbol)).count, all.count, "one glyph per route")
        XCTAssertEqual(Set(all.map(\.title)).count, all.count)
    }

    func testTheWordsNameTheRoute() {
        XCTAssertEqual(AudioRoute.speaker.title, "the speaker")
        XCTAssertEqual(AudioRoute.receiver.title, "the earpiece")
        XCTAssertEqual(AudioRoute.car.symbol, "car")
        XCTAssertEqual(AudioRoute.airPlay.title, "AirPlay")
    }

    func testARouteChangeNoticeKeepsTheOutputName() {
        XCTAssertEqual(LocalNotice.routeChanged(output: "Speaker").text, "Audio moved to Speaker.")
        XCTAssertEqual(LocalNotice.routeChanged(output: nil).text, "The audio route changed.")
    }
}
