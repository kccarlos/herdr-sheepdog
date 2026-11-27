import XCTest
@testable import SheepdogKit

/// Parked PR 11c unit test moved out of `ios/Tests/SheepdogKitTests/FactorySettingsTests.swift`.
/// Requires PR 11c (`pr11c-on-11b.patch`: `FactoryModelCandidate`).
extension FactorySettingsTests {
    func testModelCandidateParseAndFormat() {
        let parsed = FactoryModelCandidate.parse("agy/gemini-4-neon/high, claude/sonnet/medium, codex/openai/gpt-6.2/high")
        XCTAssertEqual(parsed, [
            FactoryModelCandidate(cli: "agy", model: "gemini-4-neon", effort: "high"),
            FactoryModelCandidate(cli: "claude", model: "sonnet", effort: "medium"),
            FactoryModelCandidate(cli: "codex", model: "openai/gpt-6.2", effort: "high"),
        ])
        XCTAssertEqual(FactoryModelCandidate.format(parsed), "agy/gemini-4-neon/high, claude/sonnet/medium, codex/openai/gpt-6.2/high")
        XCTAssertEqual(FactoryModelCandidate.parse("   "), [])
    }
}
