@testable import SheepdogKit
import XCTest

/// When the floating questions go compact (docs/dev/design/conversation-timeline.md,
/// "When a question goes compact"). Heights are what SwiftUI measures;
/// the areas are an iPhone in portrait (720), the same with the keyboard
/// up (400), in landscape (330), and an iPad in portrait (1100).
final class QuestionPresentationTests: XCTestCase {
    private typealias P = QuestionPresentation

    func testTable() {
        // budget = 0.55 × area: 396 on the phone, 220 with the keyboard,
        // 181.5 in landscape, 605 on the iPad.
        let cases: [(name: String, input: P.Input, current: P?, want: P)] = [
            ("yes/no approval, phone", .init(cardHeights: [230], area: 720), nil, .inline),
            ("three-option command approval, phone", .init(cardHeights: [380], area: 720), nil, .inline),
            ("three long options with descriptions, phone", .init(cardHeights: [520], area: 720), nil, .compact),
            ("the same question on an iPad", .init(cardHeights: [400], area: 1100), nil, .inline),
            ("free-text question (no options)", .init(cardHeights: [110], area: 720), nil, .inline),
            ("yes/no with the keyboard up", .init(cardHeights: [190], area: 400), nil, .inline),
            ("command approval with the keyboard up", .init(cardHeights: [380], area: 400), nil, .compact),
            ("yes/no in landscape", .init(cardHeights: [190], area: 330), nil, .compact),
            ("two short questions fit together", .init(cardHeights: [130, 140], area: 720), nil, .inline),
            ("two command approvals do not", .init(cardHeights: [280, 280], area: 720), nil, .compact),
            ("largest text sizes, even a short one", .init(cardHeights: [100], area: 1100, largestText: true), nil, .compact),
            ("largest text wins over held", .init(cardHeights: [100], area: 1100, largestText: true, held: true), .inline, .compact),
            ("not measured yet: inline", .init(cardHeights: [0], area: 720), nil, .inline),
            ("no area yet: keeps what shows", .init(cardHeights: [520], area: 0), .compact, .compact),
            ("a new card not measured yet: keeps what shows", .init(cardHeights: [190, 0], area: 720), .inline, .inline),
            ("held (sheet open, answer in flight): keeps compact", .init(cardHeights: [100], area: 720, held: true), .compact, .compact),
            ("held: keeps inline", .init(cardHeights: [900], area: 720, held: true), .inline, .inline),
            ("no questions", .init(cardHeights: [], area: 720), nil, .inline),
        ]
        for c in cases {
            XCTAssertEqual(P.decide(c.input, current: c.current), c.want, c.name)
        }
    }

    /// Back from compact only well inside the budget: the keyboard going
    /// down by a little, or a card a few points shorter, does not flip it.
    func testHysteresis() {
        let area = 720.0 // budget 396, back to inline at or under 336.6
        XCTAssertEqual(P.decide(.init(cardHeights: [380], area: area), current: .inline), .inline)
        XCTAssertEqual(P.decide(.init(cardHeights: [380], area: area), current: .compact), .compact, "near the line stays compact")
        XCTAssertEqual(P.decide(.init(cardHeights: [330], area: area), current: .compact), .inline, "well inside goes back")
        XCTAssertEqual(P.decide(.init(cardHeights: [400], area: area), current: .inline), .compact, "over the budget goes compact")
    }

    /// Rotation, the keyboard and Dynamic Type change the inputs, never
    /// the cards' own expansion: the decision follows them both ways.
    func testChangesWhileVisible() {
        var current = P.decide(.init(cardHeights: [200], area: 720), current: nil)
        XCTAssertEqual(current, .inline)
        current = P.decide(.init(cardHeights: [200], area: 330), current: current) // rotated
        XCTAssertEqual(current, .compact)
        current = P.decide(.init(cardHeights: [200], area: 720), current: current) // back
        XCTAssertEqual(current, .inline)
        current = P.decide(.init(cardHeights: [200, 200], area: 720), current: current) // a second question
        XCTAssertEqual(current, .compact)
    }

    func testNaturalHeightAddsTheGaps() {
        XCTAssertEqual(P.Input(cardHeights: [100, 100, 100], spacing: 8, area: 720).natural, 316)
        XCTAssertEqual(P.budget(area: 1000), 550)
    }
}
