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
    /// `-sheepdogAddress 127.0.0.1:7799`: start configured, for the history
    /// walkthrough and screenshots.
    static var address: String? {
        guard let i = arguments.firstIndex(of: "-sheepdogAddress"), i + 1 < arguments.count else { return nil }
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
