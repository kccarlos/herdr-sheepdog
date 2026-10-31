import XCTest
@testable import SheepdogKit

/// Runs `ConversationTimeline.classify` over the cases shared with the
/// herdr-app panel (`ios/Tests/Fixtures/timeline-classification.json`): an
/// item in, its category and style out. The panel's `model/classify.js`
/// runs the same file under `node --test`, so a change to either classifier
/// lands with a change to the fixture (docs/dev/design/desktop-client.md,
/// section 6.1).
final class TimelineClassificationFixtureTests: XCTestCase {
    private final class BundleToken {}

    private struct Style: Decodable, Equatable {
        var shownByDefault: Bool
        var folds: Bool
        var bridgesRuns: Bool
        var ledger: Bool
    }

    private struct Case: Decodable {
        var name: String
        var item: ConversationItem
        var category: String
        var style: Style
        var line: String?
    }

    private struct Fixture: Decodable {
        var cases: [Case]
    }

    private func load() throws -> Fixture {
        let url = try XCTUnwrap(
            Bundle(for: BundleToken.self).url(forResource: "timeline-classification", withExtension: "json"),
            "missing fixture timeline-classification.json in the test bundle"
        )
        return try DaemonClient.decoder.decode(Fixture.self, from: Data(contentsOf: url))
    }

    func testEveryCaseClassifiesAsTheFixtureSays() throws {
        let fixture = try load()
        XCTAssertFalse(fixture.cases.isEmpty)
        for c in fixture.cases {
            let row = ConversationTimeline.classify(c.item)
            XCTAssertEqual(row.category.rawValue, c.category, c.name)
            let style = Style(shownByDefault: row.shownByDefault, folds: row.folds, bridgesRuns: row.bridgesRuns, ledger: row.ledger)
            XCTAssertEqual(style, c.style, c.name)
            XCTAssertEqual(c.item.settledLine, c.line, c.name)
        }
    }

    func testTheFixtureCoversEveryCategoryAnItemCanHave() throws {
        let covered = Set(try load().cases.map(\.category))
        // `screen` is the folded group, made by the screen from many items.
        let expected = Set(TimelineCategory.allCases.filter { $0 != .screen }.map(\.rawValue))
        XCTAssertEqual(covered, expected)
    }

    func testTheFixtureCoversEveryItemKindAndSettledEnding() throws {
        let cases = try load().cases
        for kind in [ConversationItem.Kind.shepherd, .dog, .push, .approval, .card, .notice, .unknown] {
            XCTAssertTrue(cases.contains { $0.item.kind == kind }, "no case for \(kind)")
        }
        for resolution in [ConversationItem.Resolution.confirmed, .expired, .cancelled, .replaced, .unknown] {
            XCTAssertTrue(cases.contains { $0.item.resolution == resolution }, "no case for \(resolution)")
        }
    }
}
