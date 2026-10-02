import Foundation
import SheepdogKit

#if DEBUG
/// Every `-sheepdog*` launch argument the debug app and the UI tests read,
/// gathered in one place instead of a `ProcessInfo.processInfo.arguments`
/// scattered through `SheepdogApp` and `SessionViewModel`. Debug builds only:
/// nothing here compiles into a release.
enum DebugLaunch {
    private static var arguments: [String] { ProcessInfo.processInfo.arguments }

    /// `-sheepdogResetState`: the walkthrough and screenshots start from a
    /// clean slate rather than whatever the last run saved.
    static var resetState: Bool { arguments.contains("-sheepdogResetState") }
    /// `-sheepdogResetHints`: clears the one-time hints (also implied by
    /// `resetState`).
    static var resetHints: Bool { arguments.contains("-sheepdogResetHints") }
    /// `-sheepdogFakeVoice`: the voice states without a live session or a
    /// microphone.
    static var fakeVoice: Bool { arguments.contains("-sheepdogFakeVoice") }
    /// `-sheepdogFakeProviders`: the contract's example provider list,
    /// against a `--voice=off` daemon that reports none.
    static var fakeProviders: Bool { arguments.contains("-sheepdogFakeProviders") }
    /// `-sheepdogShowStreamErrors`: `/events` failures become notices in the
    /// conversation, instead of only a log line.
    static var showStreamErrors: Bool { arguments.contains("-sheepdogShowStreamErrors") }
    /// `-sheepdogAskPush`: ask for notification permission after connecting
    /// even when started with `-sheepdogAddress` (the UI tests otherwise
    /// skip it, so no system alert covers what they tap).
    static var askPush: Bool { arguments.contains("-sheepdogAskPush") }
    /// `-sheepdogDemoMarkdown`: adds a finished agent whose task and last
    /// message are markdown, for screenshots (the fixture daemon cannot
    /// produce a worker transcript).
    static var demoMarkdown: Bool { arguments.contains("-sheepdogDemoMarkdown") }
    /// The agent `-sheepdogDemoMarkdown` adds.
    static let markdownAgent = Agent(
        paneID: "demo-md", alias: "docs", kind: "claude", state: .done, stateSince: Date().addingTimeInterval(-240),
        lastTask: "Write the **install** section of the README and check `make install` on a clean machine",
        journal: [Agent.JournalEntry(
            at: Date().addingTimeInterval(-240), event: "done",
            lastAssistant: "## Summary\nDone. The README now has an *Install* section:\n\n- `make install` copies the binary to `~/.local/bin`\n- a note on **unlinking first** on macOS\n1. build\n2. install\n\n```\nmake build && make install\n```\nSee [the docs](https://github.com/kccarlos/herdr-sheepdog).",
            filesTouched: ["README.md"], tools: ["Edit": 3, "Bash": 2]
        )]
    )
    /// `-sheepdogAddress 127.0.0.1:7799`: start configured, for the history
    /// walkthrough and screenshots.
    static var address: String? { value(after: "-sheepdogAddress") }
    /// `-sheepdogToken dev`: the daemon token to store with `-sheepdogAddress`.
    static var token: String? { value(after: "-sheepdogToken") }
    /// `-sheepdogTransport livekit|websocket`: Settings ▸ Voice transport.
    static var transport: VoiceTransportKind? { value(after: "-sheepdogTransport").flatMap(VoiceTransportKind.init(rawValue:)) }
    /// `-sheepdogBridge 127.0.0.1:7778`: Settings ▸ Bridge address.
    static var bridge: String? { value(after: "-sheepdogBridge") }
    /// `-sheepdogAutoConnect`: tap Connect at launch (the LiveKit smoke run).
    static var autoConnect: Bool { arguments.contains("-sheepdogAutoConnect") }
    /// `-sheepdogSmokeText hello`: send this as a `text` control message
    /// over the voice transport once it opens, to see it round-trip.
    static var smokeText: String? { value(after: "-sheepdogSmokeText") }

    private static func value(after flag: String) -> String? {
        guard let i = arguments.firstIndex(of: flag), i + 1 < arguments.count else { return nil }
        return arguments[i + 1]
    }
}

/// Drives the voice states without a live session or a microphone
/// (`-sheepdogFakeVoice`): a slow sine for the mic level and matching gate
/// diagnostics, so screenshots and UI tests against a `--voice=off` daemon
/// show something moving.
struct FakeVoiceDriver {
    private var phase = 0.0

    mutating func tick(talkMode: TalkMode, holding: Bool, isMuted: Bool) -> (level: Float, diagnostics: CaptureGate.Diagnostics) {
        phase += 0.35
        let speakingNow = !isMuted && (talkMode == .openMic || holding)
        let level: Float = speakingNow ? Float(0.25 + 0.2 * sin(phase)) : 0
        let diagnostics = CaptureGate.Diagnostics(
            levelDBFS: speakingNow ? -31 + 4 * sin(phase) : -58,
            floorDBFS: -62,
            marginDB: 6,
            isOpen: speakingNow,
            opens: 4,
            spuriousOpens: 0,
            gatedFrames: 812,
            peak: speakingNow ? 9_400 : 30,
            manual: talkMode == .holdToTalk ? holding : nil
        )
        return (level, diagnostics)
    }
}
#endif
