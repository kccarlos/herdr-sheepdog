#if DEBUG
import SheepdogKit
import SwiftUI

/// Every capsule mode on one screen (`-sheepdogCapsuleGallery`), for the
/// redesign screenshots and a quick look in light, dark and the large text
/// sizes. Debug builds only.
struct CapsuleGallery: View {
    @State private var meter = VoiceMeter()

    private struct Tile: Identifiable {
        let title: String
        let state: SessionState
        var talkMode: TalkMode = .openMic
        var holding = false
        var question = false
        var muted = false
        var unreachable = false
        var elsewhere = false
        var route: AudioRoute?
        let status: String
        var id: String { title }
    }

    private let tiles: [Tile] = [
        Tile(title: "Listening", state: .listening, status: "Listening — just talk"),
        Tile(title: "Question open", state: .listening, question: true, status: "Listening"),
        Tile(title: "Speaking", state: .speaking, status: "Sheepdog is talking"),
        Tile(title: "Thinking", state: .thinking, status: "Thinking…"),
        Tile(title: "Stalled", state: .stalled, status: "Still waiting on Sheepdog…"),
        Tile(title: "Muted", state: .muted, muted: true, status: "Muted — tap to unmute"),
        Tile(title: "Audio paused", state: .audioPaused, status: "Paused by a call — tap to resume"),
        Tile(title: "No microphone", state: .noMicrophone, status: "Can’t use the microphone"),
        Tile(title: "Reconnecting", state: .reconnecting, status: "Reconnecting…"),
        Tile(title: "Stopped", state: .disconnected, status: "Not talking · tap the orb"),
        Tile(title: "Stopped, question open", state: .disconnected, question: true, status: "Not talking · tap the orb"),
        Tile(title: "In another window", state: .listening, elsewhere: true, status: "In another window"),
        Tile(title: "Offline", state: .disconnected, unreachable: true, status: "Tap the orb to connect"),
        Tile(title: "Voice off", state: .voiceOff, status: "Voice is off on this computer"),
        Tile(title: "On the speaker", state: .listening, route: .speaker, status: "Listening — just talk"),
        Tile(title: "On headphones", state: .listening, route: .headphones, status: "Listening — just talk"),
        Tile(title: "In the car", state: .listening, route: .car, status: "Listening — just talk"),
        Tile(title: "Hold to talk", state: .listening, talkMode: .holdToTalk, status: "Hold the orb to talk"),
        Tile(title: "Holding", state: .listening, talkMode: .holdToTalk, holding: true, status: "Talking — release to send"),
    ]

    var body: some View {
        // `-sheepdogCapsuleGallery banners`: the banners instead.
        if ProcessInfo.processInfo.arguments.contains("banners") {
            BannerGallery()
        } else if ProcessInfo.processInfo.arguments.contains("timeline") {
            TimelineGallery()
        } else if ProcessInfo.processInfo.arguments.contains("daysPinned") {
            DaysGallery(manyYesterday: true)
        } else if ProcessInfo.processInfo.arguments.contains("daysRest") {
            DaysGallery(manyYesterday: false)
        } else if ProcessInfo.processInfo.arguments.contains("hour") {
            HourGallery(technical: false)
        } else if ProcessInfo.processInfo.arguments.contains("hourTech") {
            HourGallery(technical: true)
        } else if ProcessInfo.processInfo.arguments.contains("replies") {
            SuggestedRepliesGallery()
        } else {
            capsules
        }
    }

    private var capsules: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 10) {
                ForEach(tiles) { tile in
                    Text(tile.title)
                        .font(.footnote.weight(.semibold))
                        .foregroundStyle(Theme.muted)
                        .padding(.leading, 8)
                    VoiceCapsule(
                        state: tile.state, meter: meter, muted: tile.muted, name: "Sheepdog",
                        talkMode: tile.talkMode, holding: tile.holding, questionOpen: tile.question,
                        statusText: tile.status, route: tile.route,
                        computerUnreachable: tile.unreachable, audioElsewhere: tile.elsewhere,
                        onConnect: {}, onStop: {}, onMute: {},
                        menuValue: 0,
                        menu: { _ in Button("End voice session") {} }
                    )
                }
            }
            .padding(12)
        }
        .background(Theme.surface)
        .accessibilityIdentifier("capsule.gallery")
    }
}

/// The error banners and the update notice on one screen, for screenshots.
private struct BannerGallery: View {
    var body: some View {
        ScrollView {
            VStack(spacing: 12) {
                BannerView(banner: .init(tone: .error, message: "Can't reach your computer.",
                                         hint: "Is the Mac awake and sheepdog running? The phone and the computer need the same network, or a VPN between them.",
                                         retryTitle: "Try again", help: URL(string: "https://example.com")), retry: {}, dismiss: {})
                BannerView(banner: .init(tone: .warning, message: "This app needs a newer sheepdog.",
                                         hint: "Run `sheepdog update` on your computer.", retryTitle: nil), retry: {}, dismiss: {})
                BannerView(banner: .init(tone: .error, message: "The voice provider refused the session.",
                                         hint: "Its key may be out of quota. Typing still works.", retryTitle: "Try again"), retry: {}, dismiss: {})
                BannerView(banner: .init(tone: .warning, message: "Couldn't send your answer.",
                                         hint: "The question is still open; tap an option again.", retryTitle: nil), retry: {}, dismiss: {})
                // The audio notices that need a tap (`LocalNotice.banner`).
                ForEach(
                    [LocalNotice.audioNotGivenBack, .audioFailedToResume("x"), .voiceOff],
                    id: \.text
                ) { notice in
                    if let words = notice.banner {
                        BannerView(banner: .init(tone: notice == .voiceOff ? .info : .warning, message: words.message, hint: words.hint, retryTitle: nil),
                                   retry: {}, dismiss: {})
                    }
                }
                UpdateBanner(notice: UpdateInfo(current: "0.9.1", latest: "0.9.2", available: true, how: "brew upgrade sheepdog").notice!, dismiss: {})
            }
            .padding(16)
        }
        .background(Theme.surface)
        .accessibilityIdentifier("capsule.gallery")
    }
}

/// The orchestrator's suggested replies in each state on one screen
/// (`-sheepdogCapsuleGallery replies`): live, one on its way, sent, a
/// failed send, answered and stale.
private struct SuggestedRepliesGallery: View {
    @State private var sender = SuggestedReplySender(sendTyped: { _ in false })

    private static let options = ["Yes, start the logging", "Not yet"]

    private struct Sample: Identifiable {
        let id: Int64
        let title: String
        let status: SuggestedReplies.Status
        var phase: SuggestedReplyTap.Phase?
    }

    private let samples: [Sample] = [
        Sample(id: 1, title: "Live", status: .live),
        Sample(id: 2, title: "Sending", status: .live, phase: .sending),
        Sample(id: 3, title: "Sent", status: .live, phase: .sent),
        Sample(id: 4, title: "Failed", status: .live, phase: .failed(message: "Couldn't reach Work Mac. Check that it is awake.")),
        Sample(id: 5, title: "Answered", status: .stale(chosen: "Not yet")),
        Sample(id: 6, title: "Stale", status: .stale(chosen: nil)),
    ]

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                ForEach(samples) { sample in
                    VStack(alignment: .leading, spacing: 4) {
                        Text(sample.title).font(.caption.weight(.semibold)).foregroundStyle(Theme.muted)
                        BrainReplyCard(
                            reply: .init(id: sample.id, ts: Date(), spoken: "Shall I tell it to go ahead and start the echo logging?",
                                         options: Self.options, raw: "{}"),
                            compact: false, voiceName: "Sheepdog"
                        )
                        .environment(\.suggestedReplies, context(sample))
                    }
                }
            }
            .padding(16)
        }
        .background(Theme.surface)
        .accessibilityIdentifier("capsule.gallery")
    }

    private func context(_ sample: Sample) -> SuggestedReplyContext {
        let tap = sample.phase.map { SuggestedReplyTap(replyID: sample.id, option: Self.options[0], computer: "gallery", phase: $0) }
        return SuggestedReplyContext(
            replies: [sample.id: SuggestedReplies(replyID: sample.id, options: Self.options, status: sample.status)],
            tap: tap, busy: false, computer: "gallery", sender: sender
        )
    }
}

/// The conversation's phase-2 rows on one screen, for screenshots: a
/// missed run, a settled question with its folded push, a voice bubble with
/// its Details, and the dividers.
private struct TimelineGallery: View {
    private static let base = Date(timeIntervalSince1970: 1_790_000_000)

    private static func item(_ id: Int64, _ kind: ConversationItem.Kind, _ text: String, agent: String? = nil, urgency: String? = nil,
                             resolution: ConversationItem.Resolution? = nil, answer: String? = nil) -> TimelineRow {
        .item(ConversationItem(id: id, ts: base.addingTimeInterval(TimeInterval(id) * 20), kind: kind, text: text, via: "tap", agent: agent,
                               urgency: urgency, pendingID: kind == .approval ? "q\(id)" : nil, answer: answer, resolution: resolution))
    }

    private static func missed(_ id: Int64, _ text: String) -> TimelineRow {
        .held(.init(key: "k\(id)", id: id, ts: base.addingTimeInterval(TimeInterval(id) * 20), text: text, fate: .dropped(ageMilliseconds: 300_000)))
    }

    private static let summary = "Codex is asking allow Computer Use to use Finder."

    private static var rows: [TimelineRow] {
        [
            missed(1, "forumind finished the design doc and is waiting for your OK to start."),
            missed(2, "The daemon fix built and is now waiting on you to approve the release."),
            missed(3, "Claude finished the forumind work and has published the branch."),
            .live(.init(id: 1, speaker: .system, kind: .notice, text: "Talking to Work Mac", isFinal: true, at: base.addingTimeInterval(100))),
            missed(6, "slingshot stage Codex finished."),
            item(7, .push, summary, agent: "Codex", urgency: "interrupt"),
            item(8, .approval, summary, agent: "Codex", resolution: .cancelled),
            item(9, .approval, "Claude wants to run npm test", agent: "Claude", resolution: .confirmed, answer: "Yes, proceed"),
            item(10, .shepherd, "What did I miss?"),
            .brain(.init(id: 11, ts: base.addingTimeInterval(210), spoken: "Two agents finished.", detail: "forumind and slingshot are done.", raw: "{}"), compact: false),
            item(12, .dog, "Two agents finished. Want the details?"),
        ]
    }

    var body: some View {
        ConversationView(rows: Self.rows, voiceName: "Sheepdog", ask: { _ in })
            .background(Theme.surface)
            .accessibilityIdentifier("capsule.gallery")
    }
}

/// A busy hour on one screen (`-sheepdogCapsuleGallery hour`, or `hourTech`
/// for the technical view), the visual spec's "ChatHour": every kind of row
/// the conversation can show, for screenshots in light, dark and the
/// largest text, on the iPhone and the iPad.
private struct HourGallery: View {
    let technical: Bool

    private static let base = Calendar.current.startOfDay(for: Date()).addingTimeInterval(12 * 3600)

    private static func at(_ minutes: Double, _ seconds: Double = 0) -> Date { base.addingTimeInterval(minutes * 60 + seconds) }

    private static func item(_ id: Int64, _ kind: ConversationItem.Kind, _ text: String, _ time: Date, via: String? = nil, agent: String? = nil,
                             urgency: String? = nil, pending: String? = nil, resolution: ConversationItem.Resolution? = nil, answer: String? = nil) -> TimelineRow {
        .item(ConversationItem(id: id, ts: time, kind: kind, text: text, via: via, agent: agent, urgency: urgency, pendingID: pending,
                               answer: answer, resolution: resolution))
    }

    private static func missed(_ id: Int64, _ text: String, _ time: Date, dropped: Bool = true) -> TimelineRow {
        .held(.init(key: "k\(id)", id: id, ts: time, text: text, fate: dropped ? .dropped(ageMilliseconds: 300_000) : .held))
    }

    private static let finder = "Codex is asking allow Computer Use to use Finder."

    private static var rows: [TimelineRow] {
        let day = TimelineRow.day(Calendar.current.startOfDay(for: base))
        var rows: [TimelineRow] = [day]
        let news = [
            "forumind finished the design doc and is waiting for your OK to start.",
            "Both releases are underway. The daemon fix is waiting on CI.",
            "Claude finished the forumind work and has published it.",
            "The two stuck waiting scripts are cleared.",
            "slingshot stage Claude finished.",
            "The WordPress version of forumind is on TestFlight.",
            "The sheepdog agent has finished. The slash command is in.",
            "The local daemon is now the signed v0.1.9.",
        ]
        for (i, text) in news.enumerated() { rows.append(missed(Int64(i + 1), text, at(Double(i * 5 + 2)))) }
        rows += [
            .live(.init(id: 1, speaker: .system, kind: .notice, text: "Talking to Work Mac", isFinal: true, at: at(62))),
            item(20, .shepherd, "What did I miss?", at(62, 40)),
            item(21, .dog, "Eight things finished while you were away. The forumind design doc is waiting for your OK. Want the details?", at(62, 47)),
            .brain(.init(id: 22, ts: at(62, 48), spoken: "Eight things finished while you were away. forumind's design doc waits for your OK.",
                         detail: "Both releases are out (app 0.2.1 to TestFlight, daemon v0.1.9 signed). The two stuck waiting scripts were cleared after the restart. forumind wants an OK before it starts on the design.",
                         options: ["Read the design doc", "Not now"], raw: "{ \"name\": \"to_supervisor\" }"), compact: false),
            item(23, .shepherd, "Is anyone stuck?", at(62, 55), via: "typed"),
            item(24, .dog, "Codex wants to use Finder. Its question is below.", at(63)),
            item(25, .approval, finder, at(63, 5), agent: "Codex", pending: "q25", resolution: .open),
            item(26, .approval, "Claude is asking to run npm test in webapp.", at(63, 20), via: "tap", agent: "Claude", pending: "q26", resolution: .confirmed, answer: "Yes, proceed"),
            item(27, .approval, "Claude is asking to run rm -rf .build in sheepdog.", at(63, 40), agent: "Claude", pending: "q27", resolution: .cancelled),
            item(28, .push, "slingshot stage Codex finished.", at(64)),
            .card(ScreenCard(cardID: "c1", kind: .card, at: at(64, 30), title: "Test results", markdown: "- 41 passed")),
            .live(.init(id: 2, speaker: .shepherd, kind: .speech, text: "Tell Codex it can use Finder but nothing", isFinal: false, at: at(65))),
        ]
        return rows
    }

    private static var technicalRows: [TimelineRow] {
        [
            .day(Calendar.current.startOfDay(for: base)),
            item(1, .notice, "Voice context lost.", at(0, 4)),
            item(2, .notice, "Voice reconnected with a fresh session; earlier context was lost.", at(0, 11)),
            missed(3, "The WordPress version of forumind is on TestFlight.", at(5, 40)),
            missed(4, "The sheepdog agent has finished. The slash command is in.", at(9, 13), dropped: false),
            item(5, .notice, "Voice session closed: shutdown.", at(51)),
            item(6, .notice, "Sheepdog started.", at(51, 3)),
            .live(.init(id: 1, speaker: .system, kind: .notice, text: "Talking to Work Mac", isFinal: true, at: at(62, 10))),
            .live(.init(id: 2, speaker: .system, kind: .notice, text: "Audio moved to Speaker.", isFinal: true, at: at(62, 30))),
            item(20, .shepherd, "What did I miss?", at(62, 40)),
            item(21, .dog, "Eight things finished while you were away. The forumind design doc is waiting for your OK.", at(62, 47)),
            .brain(.init(id: 22, ts: at(62, 48), spoken: "Eight things finished while you were away.", detail: "Both releases are out.", options: ["Read the design doc", "Not now"], raw: "{}"), compact: false),
            item(25, .approval, finder, at(63, 2), agent: "Codex", pending: "q25", resolution: .open),
            item(26, .approval, "Claude is asking to run npm test in webapp.", at(63, 21), via: "tap", agent: "Claude", pending: "q26", resolution: .confirmed, answer: "Yes, proceed"),
        ]
    }

    var body: some View {
        ConversationView(
            rows: technical ? Self.technicalRows : Self.rows,
            voiceName: "Sheepdog",
            canLoadOlder: !technical,
            openPendingIDs: ["q25"],
            pointerIDs: technical ? [] : ["item.25"],
            ask: { _ in },
            showTechnical: technical
        )
        .background(Theme.surface)
        .accessibilityIdentifier("capsule.gallery")
    }
}

/// Two days under pinned day headings (`-sheepdogCapsuleGallery daysPinned`
/// or `daysRest`), with the flag button over the corner as in the real
/// screen: where the heading sits pinned, at rest, and what it covers.
private struct DaysGallery: View {
    let manyYesterday: Bool

    private static let today = Calendar.current.startOfDay(for: Date())

    private static func item(_ id: Int64, _ kind: ConversationItem.Kind, _ text: String, _ time: Date) -> TimelineRow {
        .item(ConversationItem(id: id, ts: time, kind: kind, text: text))
    }

    private var rows: [TimelineRow] {
        let yesterday = Self.today.addingTimeInterval(-86_400 + 12 * 3600)
        let now = Self.today.addingTimeInterval(10 * 3600 + 31 * 60)
        var rows: [TimelineRow] = []
        if manyYesterday {
            rows.append(.day(Self.today.addingTimeInterval(-86_400)))
            for i in 0..<5 {
                let t = yesterday.addingTimeInterval(Double(i) * 120)
                rows.append(Self.item(Int64(10 + i * 2), .shepherd, "What's the current status \(i + 1)?", t))
                rows.append(Self.item(Int64(11 + i * 2), .dog, "Both agents are idle. Codex has installed the updated menu bar app, and Slingshot stage Codex has finished its investigation into the issue. Would you like me to share what it found?", t.addingTimeInterval(20)))
            }
            // A context divider right above the next day's heading: the
            // heading's band must not touch it.
            rows.append(.live(.init(id: 9, speaker: .system, kind: .notice, text: "Talking to Work Mac", isFinal: true, at: yesterday.addingTimeInterval(3600))))
        }
        rows.append(.day(Self.today))
        rows += [
            .live(.init(id: 1, speaker: .system, kind: .notice, text: "Talking to Work Mac", isFinal: true, at: now)),
            Self.item(100, .shepherd, "Hey, what is the status of my agents?", now.addingTimeInterval(20)),
            Self.item(101, .dog, "Codex is currently idle and ready for input, while the slingshot stage Codex has exited.", now.addingTimeInterval(30)),
        ]
        if !manyYesterday {
            // Fills the screen so the heading rests at the very top.
            let args = ProcessInfo.processInfo.arguments
            let pairs = args.firstIndex(of: "pairs").flatMap { Int(args[$0 + 1]) } ?? 0
            for i in 0..<pairs {
                let t = now.addingTimeInterval(Double(60 + i * 60))
                rows.append(Self.item(Int64(200 + i * 2), .shepherd, "Status \(i + 1)?", t))
                rows.append(Self.item(Int64(201 + i * 2), .dog, "All idle.", t.addingTimeInterval(10)))
            }
        }
        return rows
    }

    var body: some View {
        ConversationView(
            rows: rows, voiceName: "Sheepdog", canLoadOlder: false, openPendingIDs: [], pointerIDs: [],
            ask: { _ in }, showTechnical: false
        )
        .background(Theme.surface)
        .overlay(alignment: .topTrailing) {
            FeedbackButton { _, _, _ in }
                .padding(.top, 4)
                .padding(.trailing, 10)
        }
        .accessibilityIdentifier("capsule.gallery")
    }
}
#endif
