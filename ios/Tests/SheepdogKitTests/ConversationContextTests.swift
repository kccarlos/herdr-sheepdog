import XCTest
@testable import SheepdogKit

/// The rows the neighbours decide (docs/dev/design/conversation-timeline.md,
/// sections 5.1, 5.3, 5.5 and 10): reconnects that matter, replies folded
/// into the voice's bubble, settled questions as one line, missed pushes,
/// and this phone's own notices.
final class ConversationContextTests: XCTestCase {
    private let base = Date(timeIntervalSince1970: 1_790_000_000)

    private func at(_ seconds: TimeInterval) -> Date { base.addingTimeInterval(seconds) }

    private func item(
        _ id: Int64, _ kind: ConversationItem.Kind, _ text: String = "t", at seconds: TimeInterval? = nil,
        agent: String? = nil, urgency: String? = nil, resolution: ConversationItem.Resolution? = nil, answer: String? = nil, via: String? = nil
    ) -> TimelineRow {
        .item(ConversationItem(
            id: id, ts: at(seconds ?? TimeInterval(id)), kind: kind, text: text, via: via, agent: agent, urgency: urgency,
            pendingID: kind == .approval ? "q\(id)" : nil, answer: answer, resolution: resolution
        ))
    }

    private func reply(_ id: Int64, at seconds: TimeInterval, pending: Bool = false) -> TimelineRow {
        .brain(.init(id: id, ts: at(seconds), spoken: "Two agents are done.", detail: "More.", pending: pending), compact: false)
    }

    private let lost = "Voice context lost."
    private let fresh = "Voice reconnected with a fresh session; earlier context was lost."
    private let resumed = "Voice reconnected; the conversation continues."

    private func ids(_ rows: [TimelineRow]) -> [String] { rows.map(\.id) }

    // MARK: - Reconnects

    func testAReconnectRightAfterDialogueIsOneRow() {
        let rows: [TimelineRow] = [
            item(1, .shepherd, "what is going on", at: 0),
            item(2, .dog, "billing is working", at: 5),
            item(3, .notice, lost, at: 100),
            item(4, .notice, fresh, at: 102),
        ]
        let shown = ConversationTimeline.visibleRows(rows, showTechnical: false)
        XCTAssertEqual(ids(shown), ["item.1", "item.2", "item.4"], "context lost is paired with the reconnect: one row")
        guard case let .divider(restarted, title) = shown[2] else { return XCTFail("a divider") }
        XCTAssertTrue(title.hasPrefix("Voice restarted — it may not remember the last minute · "), title)
        XCTAssertEqual(DaemonNotice(restarted), .voiceRestarted)
        XCTAssertEqual(ConversationTimeline.classify(shown[2]).category, .context)
        XCTAssertTrue(ConversationTimeline.classify(shown[2]).shownByDefault)
    }

    func testAReconnectLongAfterDialogueOrBeforeAnyIsHidden() {
        let rows: [TimelineRow] = [
            item(1, .notice, lost, at: 0), item(2, .notice, fresh, at: 2), // nobody has spoken yet
            item(3, .shepherd, "hi", at: 10),
            item(4, .notice, lost, at: 10 + 301), item(5, .notice, fresh, at: 10 + 303), // just over 5 minutes
        ]
        XCTAssertEqual(ids(ConversationTimeline.visibleRows(rows, showTechnical: false)), ["item.3"])
    }

    func testAResumedReconnectIsNeverShown() {
        let rows: [TimelineRow] = [item(1, .shepherd, "hi", at: 0), item(2, .notice, resumed, at: 5), item(3, .notice, "Voice session closed: shutdown.", at: 6)]
        XCTAssertEqual(ids(ConversationTimeline.visibleRows(rows, showTechnical: false)), ["item.1"])
    }

    func testTheVoiceSpeakingCountsAsAConversation() {
        let rows: [TimelineRow] = [item(1, .dog, "done", at: 0), item(2, .notice, fresh, at: 60)]
        XCTAssertEqual(ids(ConversationTimeline.visibleRows(rows, showTechnical: false)), ["item.1", "item.2"])
    }

    func testTheSwitchStillShowsEveryReconnectUnchanged() {
        let rows: [TimelineRow] = [item(1, .shepherd, "hi", at: 0), item(2, .notice, lost, at: 5), item(3, .notice, fresh, at: 6)]
        XCTAssertEqual(ConversationTimeline.visibleRows(rows, showTechnical: true), rows)
    }

    func testShutdownAndRestartAreOneDivider() {
        // "Voice session closed: shutdown." is plumbing; "Sheepdog restarted" is the divider.
        let rows: [TimelineRow] = [
            item(1, .shepherd, "hi", at: 0),
            item(2, .notice, "Voice session closed: shutdown.", at: 50),
            item(3, .notice, "Sheepdog started.", at: 51),
        ]
        XCTAssertEqual(ids(ConversationTimeline.visibleRows(rows, showTechnical: false)), ["item.1", "item.3"])
    }

    // MARK: - Orchestrator replies

    func testAReplyFoldsIntoTheBubbleThatSaysIt() {
        let rows: [TimelineRow] = [item(1, .shepherd, "status?", at: 0), reply(2, at: 3), item(3, .dog, "Two agents are done.", at: 4)]
        let layout = ConversationTimeline.layout(rows, showTechnical: false)
        XCTAssertEqual(layout.rows.map(\.id), ["item.1", "item.3"])
        XCTAssertEqual(layout.attached["item.3"]?.map(\.id), ["brain.2"])
    }

    func testAReplyFoldsIntoTheBubbleBeforeItWhenNothingFollows() {
        let rows: [TimelineRow] = [item(1, .dog, "Let me check.", at: 0), reply(2, at: 3), item(3, .shepherd, "thanks", at: 20)]
        let layout = ConversationTimeline.layout(rows, showTechnical: false)
        XCTAssertEqual(layout.attached["item.1"]?.map(\.id), ["brain.2"])
        XCTAssertEqual(layout.rows.map(\.id), ["item.1", "item.3"])
    }

    func testAPendingOrUnvoicedReplyStaysACard() {
        let pending: [TimelineRow] = [item(1, .shepherd, "status?", at: 0), reply(2, at: 3, pending: true), item(3, .dog, "Still working.", at: 4)]
        XCTAssertEqual(ConversationTimeline.layout(pending, showTechnical: false).rows.map(\.id), ["item.1", "brain.2", "item.3"])
        let neverVoiced: [TimelineRow] = [item(1, .shepherd, "status?", at: 0), reply(2, at: 3), item(3, .shepherd, "hello?", at: 9)]
        XCTAssertEqual(ConversationTimeline.layout(neverVoiced, showTechnical: false).rows.map(\.id), ["item.1", "brain.2", "item.3"])
        let far: [TimelineRow] = [reply(1, at: 0), item(2, .dog, "Much later.", at: 500)]
        XCTAssertEqual(ConversationTimeline.layout(far, showTechnical: false).rows.map(\.id), ["brain.1", "item.2"])
    }

    func testTheTechnicalViewKeepsTheReplyCard() {
        let rows: [TimelineRow] = [reply(1, at: 0), item(2, .dog, "Two agents are done.", at: 1)]
        let layout = ConversationTimeline.layout(rows, showTechnical: true)
        XCTAssertEqual(layout.rows.map(\.id), ["brain.1", "item.2"])
        XCTAssertTrue(layout.attached.isEmpty)
    }

    // MARK: - Settled questions

    private let summary = "Codex is asking allow Computer Use to use Finder."

    func testASettledQuestionFoldsItsPushAndTheUnansweredRestatement() {
        let rows: [TimelineRow] = [
            item(1, .push, summary, at: 9, agent: "Codex", urgency: "interrupt"),
            item(2, .approval, summary, at: 10, agent: "Codex", resolution: .cancelled),
            item(3, .dog, "Codex is asking to allow Computer Use to use Finder. Allow it?", at: 14),
            item(4, .push, "docs finished", at: 60, agent: "docs"),
        ]
        let layout = ConversationTimeline.layout(rows, showTechnical: false)
        XCTAssertEqual(layout.rows.map(\.id), ["item.2", "item.4"])
        XCTAssertEqual(layout.attached["item.2"]?.map(\.id), ["item.1", "item.3"])
    }

    func testARestatementTheUserRepliedToStaysDialogue() {
        let rows: [TimelineRow] = [
            item(2, .approval, summary, at: 10, agent: "Codex", resolution: .confirmed, answer: "Yes", via: "voice"),
            item(3, .dog, "Codex is asking to allow Computer Use to use Finder. Allow it?", at: 14),
            item(4, .shepherd, "yes", at: 20),
        ]
        let layout = ConversationTimeline.layout(rows, showTechnical: false)
        XCTAssertEqual(layout.rows.map(\.id), ["item.2", "item.3", "item.4"])
        XCTAssertNil(layout.attached["item.2"])
    }

    func testNewsAboutTheSameAgentIsNotFoldedIntoTheQuestion() {
        let rows: [TimelineRow] = [
            item(1, .approval, summary, at: 10, agent: "Codex", resolution: .expired),
            item(2, .push, "Codex finished the refactor.", at: 20, agent: "Codex"),
        ]
        XCTAssertEqual(ConversationTimeline.layout(rows, showTechnical: false).rows.map(\.id), ["item.1", "item.2"])
    }

    func testNothingIsFoldedAfterTheWindowOrIntoAnOpenQuestion() {
        let late: [TimelineRow] = [
            item(1, .approval, summary, at: 0, agent: "Codex", resolution: .cancelled),
            item(2, .dog, "Codex is asking to allow Finder.", at: 121),
        ]
        XCTAssertEqual(ConversationTimeline.layout(late, showTechnical: false).rows.map(\.id), ["item.1", "item.2"])
        let open: [TimelineRow] = [item(1, .approval, summary, at: 0, agent: "Codex"), item(2, .dog, "Codex is asking to allow Finder.", at: 4)]
        XCTAssertEqual(ConversationTimeline.layout(open, showTechnical: false).rows.map(\.id), ["item.1", "item.2"], "the pointer rule handles an open one")
    }

    func testTheSettledLine() {
        func line(_ resolution: ConversationItem.Resolution, answer: String? = nil, agent: String? = "Codex", text: String? = nil) -> String? {
            guard case let .item(i) = item(1, .approval, text ?? summary, agent: agent, resolution: resolution, answer: answer, via: "tap") else { return nil }
            return i.settledLine
        }
        XCTAssertEqual(line(.cancelled), "Codex's question (allow Computer Use to use Finder) — closed")
        XCTAssertEqual(line(.expired), "Codex's question (allow Computer Use to use Finder) — expired")
        XCTAssertEqual(line(.replaced), "Codex's question (allow Computer Use to use Finder) — replaced")
        XCTAssertEqual(line(.unknown), "Codex's question (allow Computer Use to use Finder) — closed")
        XCTAssertEqual(line(.confirmed, answer: "Yes, proceed"), "You approved — Yes, proceed · by tap")
        XCTAssertEqual(line(.cancelled, agent: nil, text: "Allow it?"), "A question (Allow it) — closed")
        XCTAssertEqual(
            line(.cancelled, text: "Codex wants to run a very long command that goes on and on and on"),
            "Codex's question (run a very long command that goes…) — closed"
        )
        XCTAssertNil(line(.open))
    }

    // MARK: - Missed pushes

    private func held(_ id: Int64, _ fate: BrainActivity.HeldPush.Fate, _ text: String = "forumind finished the design doc") -> TimelineRow {
        .held(.init(key: "k\(id)", id: id, ts: at(TimeInterval(id)), text: text, fate: fate))
    }

    func testMissedPushesAreAgentOutcomesThatFold() {
        let rows: [TimelineRow] = [held(1, .dropped(ageMilliseconds: 300_000)), held(2, .held), held(3, .dropped(ageMilliseconds: 300_000))]
        for row in rows {
            XCTAssertEqual(ConversationTimeline.classify(row), RowClass(category: .outcome, folds: true, ledger: true))
            XCTAssertTrue(row.isMissed)
        }
        let layout = ConversationTimeline.layout(rows, showTechnical: false)
        XCTAssertEqual(layout.rows.count, 1)
        guard case let .updates(_, members) = layout.rows[0] else { return XCTFail("one group") }
        XCTAssertEqual(ConversationTimeline.updatesTitle(members), "3 updates while you were away")
        XCTAssertEqual(ConversationTimeline.previewLines(members), ["forumind finished the design doc", "forumind finished the design doc"])
    }

    func testAMixedRunSaysBoth() {
        let rows: [TimelineRow] = [held(1, .dropped(ageMilliseconds: 1)), item(2, .push, "docs done"), item(3, .push, "auth done"), item(4, .push, "ci done")]
        XCTAssertEqual(ConversationTimeline.updatesTitle(rows), "4 agent updates · 1 while you were away")
        XCTAssertEqual(ConversationTimeline.updatesTitle(Array(rows[1...])), "3 agent updates")
    }

    func testARestartDoesNotSplitMissedPushes() {
        let restart = TimelineRow.divider(
            ConversationItem(id: 5, ts: at(5), kind: .notice, text: "Sheepdog started."), title: "Sheepdog restarted · 5:00 PM"
        )
        let rows: [TimelineRow] = [held(1, .held), held(2, .held), restart, held(6, .dropped(ageMilliseconds: 1))]
        let layout = ConversationTimeline.layout(rows, showTechnical: false)
        XCTAssertEqual(layout.rows.count, 1, "one group, the restart inside")
    }

    func testTheTechnicalViewDoesNotFoldMissedPushes() {
        let rows: [TimelineRow] = [held(1, .held), held(2, .held), held(3, .held)]
        XCTAssertEqual(ConversationTimeline.layout(rows, showTechnical: true).rows.count, 3)
    }

    // MARK: - This phone's own notices

    func testEveryLocalNoticeKeepsItsWordsThroughTheRoundTrip() {
        let notices: [LocalNotice] = [
            .talkingTo("Work Mac"), .routeChanged(output: "Speaker"), .routeChanged(output: nil), .pausedForInterruption, .listeningAgain,
            .audioNotGivenBack, .audioFailedToResume("The operation couldn’t be completed."), .mediaServicesReset,
            .audioStopped(LocalNotice.microphoneWentAway), .audioStopped(LocalNotice.audioStoppedAfterRouteChange("error 1")),
            .voiceOff, .voiceClosed(reason: "shutdown"),
        ]
        for notice in notices {
            XCTAssertEqual(LocalNotice(text: notice.text), notice, notice.text)
        }
        XCTAssertEqual(LocalNotice(text: "Something new"), .unrecognised("Something new"))
    }

    func testWhereEachLocalNoticeGoes() {
        XCTAssertEqual(LocalNotice.talkingTo("A").placement, .timeline)
        XCTAssertEqual(LocalNotice.unrecognised("x").placement, .timeline)
        for notice in [LocalNotice.routeChanged(output: "Speaker"), .pausedForInterruption, .listeningAgain] {
            XCTAssertEqual(notice.placement, .voiceBar)
            XCTAssertNil(notice.banner)
        }
        for notice in [LocalNotice.audioNotGivenBack, .audioFailedToResume("x"), .mediaServicesReset, .audioStopped("x"), .voiceOff, .voiceClosed(reason: "x")] {
            XCTAssertEqual(notice.placement, .banner)
            XCTAssertNotNil(notice.banner, notice.text)
        }
        XCTAssertEqual(LocalNotice.audioNotGivenBack.banner?.hint, "Tap the orb to resume listening.")
    }

    func testTheTimelineKeepsOnlyContextAndUnknownLocalNotices() {
        func row(_ notice: LocalNotice) -> TimelineRow {
            .live(.init(id: 1, speaker: .system, kind: .notice, text: notice.text, isFinal: true))
        }
        XCTAssertTrue(ConversationTimeline.classify(row(.talkingTo("A"))).shownByDefault)
        XCTAssertTrue(ConversationTimeline.classify(row(.unrecognised("A notice this build has never seen"))).shownByDefault)
        for notice in [LocalNotice.routeChanged(output: "Speaker"), .pausedForInterruption, .listeningAgain, .audioNotGivenBack, .voiceOff, .audioStopped(LocalNotice.microphoneWentAway)] {
            XCTAssertFalse(ConversationTimeline.classify(row(notice)).shownByDefault, notice.text)
            XCTAssertEqual(ConversationTimeline.classify(row(notice)).category, .system)
        }
    }

    func testABannerClearsWhenItsReasonIsGone() {
        XCTAssertTrue(LocalNotice.audioNotGivenBack.bannerStillApplies(in: .audioPaused))
        XCTAssertFalse(LocalNotice.audioNotGivenBack.bannerStillApplies(in: .listening))
        XCTAssertTrue(LocalNotice.voiceOff.bannerStillApplies(in: .voiceOff))
        XCTAssertFalse(LocalNotice.voiceOff.bannerStillApplies(in: .idle))
        XCTAssertTrue(LocalNotice.mediaServicesReset.bannerStillApplies(in: .listening), "it stays until dismissed or reconnected")
    }

    // MARK: - Computer names

    func testAMachineIDIsRecognisedAndAHumanNameIsNot() {
        for name in ["M944QD4VY", "C02X1234", "MBP-2", "ab12"] {
            XCTAssertTrue(ComputerNaming.looksMachineMade(name), name)
            XCTAssertEqual(ComputerNaming.suggestion(for: name), "Work Mac")
        }
        for name in ["Work Mac", "Studio", "MacBook Pro 14", "", "  ", "12345", "kitchen", "Studio Mac 2"] {
            XCTAssertFalse(ComputerNaming.looksMachineMade(name), name)
            XCTAssertNil(ComputerNaming.suggestion(for: name))
        }
    }
}
