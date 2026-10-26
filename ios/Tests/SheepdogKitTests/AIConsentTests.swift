import XCTest
@testable import SheepdogKit

final class AIConsentTests: XCTestCase {
    private func store() -> (AIConsent, UserDefaults) {
        let suite = "sheepdog.test.consent.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        addTeardownBlock { defaults.removePersistentDomain(forName: suite) }
        return (AIConsent(defaults: defaults), defaults)
    }

    func testNeverAskedNeedsConsent() {
        let (consent, _) = store()
        XCTAssertEqual(consent.decision, .needsConsent)
        XCTAssertFalse(consent.isAllowed)
        XCTAssertNil(consent.agreedVersion)
    }

    func testAgreeAllowsAndPersists() {
        let (consent, defaults) = store()
        consent.agree()
        XCTAssertEqual(consent.decision, .allowed)
        XCTAssertEqual(consent.agreedVersion, AIConsent.currentVersion)
        // A new instance on the same defaults (the next launch) agrees too.
        XCTAssertEqual(AIConsent(defaults: defaults).decision, .allowed)
    }

    func testWithdrawBlocksAndAgreeBringsItBack() {
        let (consent, _) = store()
        consent.agree()
        consent.withdraw()
        XCTAssertEqual(consent.decision, .withdrawn)
        XCTAssertFalse(consent.isAllowed)
        consent.agree()
        XCTAssertEqual(consent.decision, .allowed)
    }

    func testWithdrawBeforeAgreeingStaysUnasked() {
        let (consent, _) = store()
        consent.withdraw()
        XCTAssertEqual(consent.decision, .needsConsent, "never agreed is never allowed")
    }

    func testAnOlderVersionIsAskedAgain() {
        let (consent, defaults) = store()
        defaults.set(AIConsent.currentVersion - 1, forKey: "privacy.aiConsentVersion")
        XCTAssertEqual(consent.decision, .needsConsent)
        consent.agree()
        XCTAssertEqual(consent.decision, .allowed)
    }

    func testForgetStartsOver() {
        let (consent, _) = store()
        consent.agree()
        consent.forget()
        XCTAssertEqual(consent.decision, .needsConsent)
    }

    func testServicesAreNamedAsAPersonKnowsThem() {
        XCTAssertEqual(AIServices.summary(voice: "gemini", brain: "deepseek"), "Google Gemini (voice) and DeepSeek (answers)")
        XCTAssertEqual(AIServices.summary(voice: "openai", brain: "openai"), "OpenAI (voice and answers)")
        XCTAssertEqual(AIServices.summary(voice: "gemini", brain: nil), "Google Gemini (voice)")
        XCTAssertEqual(AIServices.summary(voice: "off", brain: "anthropic"), "Anthropic Claude (answers)")
    }

    func testUnknownDaemonFallsBackToTheGenericWords() {
        let generic = AIServices.summary(voice: nil, brain: nil)
        XCTAssertTrue(generic.contains("Google Gemini"))
        XCTAssertEqual(AIServices.summary(voice: "off", brain: ""), generic)
    }
}
