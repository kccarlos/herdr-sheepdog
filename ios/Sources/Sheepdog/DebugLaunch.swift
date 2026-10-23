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
    /// When the process started: an agent window that appears within a few
    /// seconds of it was restored from the last run, which a reset closes.
    static let launchedAt = Date()
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
    /// `-sheepdogDemoSilent`: the demo without speech synthesis or speech
    /// recognition, so no audio unit starts and no permission is asked.
    /// For the UI tests: on Xcode Cloud's simulators the audio server hangs
    /// and the app aborts in `AURemoteIO::Initialize` (an RPC timeout).
    static var demoSilent: Bool { arguments.contains("-sheepdogDemoSilent") }
    /// `-sheepdogAskPrimers`: show the permission primers even when the
    /// UI tests start configured.
    static var askPrimers: Bool { arguments.contains("-sheepdogAskPrimers") }
    /// `-sheepdogOpenDemo`: open the offline demo at launch, for the
    /// redesign screenshots (`RedesignShotsUITests`).
    static var openDemo: Bool { arguments.contains("-sheepdogOpenDemo") }
    /// `-sheepdogDemoPaused`: the demo's script never moves on (its clock
    /// stays at the start), so no agent finishes or asks: a UI test sees
    /// the opening state for as long as it needs, on a Mac of any speed.
    static var demoPaused: Bool { arguments.contains("-sheepdogDemoPaused") }
    /// `-sheepdogNoAnimations`: no UIKit or SwiftUI animations
    /// (`AppDelegate`, `View.debugAnimationsOff()`), for the UI tests that
    /// tap through screens: on a slow Mac a tap landed on a sheet or menu
    /// still moving in and was lost. The recordings and screenshots keep
    /// the motion.
    static var noAnimations: Bool { arguments.contains("-sheepdogNoAnimations") }
    /// `-sheepdogDemoUpdate`: the demo's daemon reports a newer release,
    /// so the update notice can be filmed without an older daemon
    /// (docs/dev/launch-video.md, shot 10b).
    static var demoUpdate: UpdateInfo? {
        guard arguments.contains("-sheepdogDemoUpdate") else { return nil }
        return UpdateInfo(current: "v0.1.16", latest: "v0.1.17", available: true, how: "brew upgrade sheepdog", autoRestart: true)
    }
    /// `-sheepdogDemoRecording`: a prompt sent to a demo agent is answered
    /// as a daemon answers it, for the launch video's shot 8.
    static var demoRecording: Bool { arguments.contains("-sheepdogDemoRecording") }
    /// `-sheepdogSplashHold`: keep the splash up, for its screenshot.
    static var splashHold: Bool { arguments.contains("-sheepdogSplashHold") }
    /// `-sheepdogDemoMarkdown`: adds a finished agent whose task and last
    /// message are markdown, for screenshots (the fixture daemon cannot
    /// produce a worker transcript).
    static var demoMarkdown: Bool { arguments.contains("-sheepdogDemoMarkdown") }
    /// `-sheepdogDemoCard`: the demo asks "Show me the agents" by itself
    /// once the script has news (about 18 s in), so its card opens for screenshots.
    static var demoCard: Bool { arguments.contains("-sheepdogDemoCard") }
    /// `-sheepdogDemoLiveActivity speaking|muted|listening`: show the voice
    /// session's Live Activity with two agents waiting, without a session,
    /// for the lock-screen and Dynamic Island screenshots.
    static var demoLiveActivity: SessionState? {
        guard let i = arguments.firstIndex(of: "-sheepdogDemoLiveActivity"), i + 1 < arguments.count else { return nil }
        return SessionState(rawValue: arguments[i + 1])
    }
    /// `-sheepdogDemoTerm busy|gone|herdr`: the demo's live window answers
    /// every open that way, for screenshots and UI tests of the observe
    /// banner and the error states.
    static var demoTerm: DemoTermTransport.Behavior {
        guard let i = arguments.firstIndex(of: "-sheepdogDemoTerm"), i + 1 < arguments.count else { return .normal }
        return DemoTermTransport.Behavior(rawValue: arguments[i + 1]) ?? .normal
    }
    /// `-sheepdogDemoSecondQuestion`: a second agent waits too, so two
    /// questions float at once (screenshots; its answer goes nowhere).
    static var demoSecondQuestion: Bool { arguments.contains("-sheepdogDemoSecondQuestion") }
    static let secondQuestionAgent = Agent(
        paneID: "demo-blog", alias: "blog", kind: "claude", state: .blocked, stateSince: Date().addingTimeInterval(-60)
    )
    static let secondQuestion = PendingAction(
        id: "demo-blog-q", paneID: "demo-blog", kind: "answer_prompt",
        options: [PendingOption(index: 1, label: "Publish", isDefault: true), PendingOption(index: 2, label: "Not yet")],
        agent: "blog", summary: "Publish the draft to the staging site?"
    )
    /// The agent `-sheepdogDemoMarkdown` adds.
    static let markdownAgent = Agent(
        paneID: "demo-md", alias: "docs", kind: "claude", state: .done, stateSince: Date().addingTimeInterval(-240),
        lastTask: "Write the **install** section of the README and check `make install` on a clean machine",
        journal: [
            Agent.JournalEntry(at: Date().addingTimeInterval(-1500), event: "stop", lastAssistant: "Read the Makefile; install needs a PREFIX."),
            Agent.JournalEntry(at: Date().addingTimeInterval(-1080), event: "stop", lastAssistant: "Tried make install on a clean user: it needs ~/.local/bin on PATH."),
            Agent.JournalEntry(at: Date().addingTimeInterval(-600), event: "stop", lastAssistant: "Drafted the Install section; checking the macOS note."),
            Agent.JournalEntry(
            at: Date().addingTimeInterval(-240), event: "done",
            lastAssistant: "## Summary\nDone. The README now has an *Install* section:\n\n- `make install` copies the binary to `~/.local/bin`\n- a note on **unlinking first** on macOS\n1. build\n2. install\n\n```\nmake build && make install\n```\nSee [the docs](https://github.com/kccarlos/herdr-sheepdog).",
            filesTouched: ["README.md"], tools: ["Edit": 3, "Bash": 2]
        ),
        ]
    )
    /// `-sheepdogAddress 127.0.0.1:7799`: start configured, for the history
    /// walkthrough and screenshots.
    static var address: String? {
        guard let i = arguments.firstIndex(of: "-sheepdogAddress"), i + 1 < arguments.count else { return nil }
        return arguments[i + 1]
    }
    /// `-sheepdogComputers "Work Mac=127.0.0.1:7801;Home Mac=127.0.0.1:7802"`:
    /// start with these loopback fixture daemons as paired computers (plain
    /// HTTP, no pin), ids `fixture-1`, `fixture-2`, …, the first one
    /// current. For the several-computers tests and screenshots.
    static var computers: [PairedComputer]? {
        guard let i = arguments.firstIndex(of: "-sheepdogComputers"), i + 1 < arguments.count else { return nil }
        return arguments[i + 1].split(separator: ";").enumerated().compactMap { n, entry in
            let parts = entry.split(separator: "=", maxSplits: 1).map(String.init)
            guard parts.count == 2 else { return nil }
            return PairedComputer(daemonID: "fixture-\(n + 1)", name: parts[0], addresses: [parts[1]], pin: "",
                                  deviceID: "", pairedAt: Date(timeIntervalSince1970: 1_800_000_000 + Double(n)))
        }
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
