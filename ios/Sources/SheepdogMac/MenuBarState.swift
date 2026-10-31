import Foundation
import SheepdogKit

/// What the menu-bar icon and the menu's first line say: one value from
/// `/api/state` (nil when the daemon did not answer), so the icon can never
/// disagree with the menu. Pure; `MenuBarStateTests` is the table.
///
/// The Live Activity's states, minus the ones that need this Mac's own
/// session (thinking, stalled, audio paused): this shell does not hold the
/// voice yet (PR 9), so "listening", "muted" and "speaking" are what the
/// daemon reports for a Mac holder (`voice.client.platform == "macos"`).
public struct MenuBarState: Equatable, Sendable {
    public enum Kind: Equatable, Sendable {
        /// The daemon did not answer.
        case daemonDown
        /// The daemon runs with `--voice=off`, or no client holds the voice.
        case off
        /// Another device holds the voice; the name is its `voice.client.name`
        /// (nil from a daemon that does not report the holder).
        case voiceElsewhere(holder: String?)
        case muted
        case speaking
        case listening
    }

    public var kind: Kind
    /// Open questions: the "needs you" badge.
    public var needsYou: Int

    public init(kind: Kind, needsYou: Int = 0) {
        self.kind = kind
        self.needsYou = needsYou
    }

    /// `voice.client.platform` of this app.
    public static let platform = "macos"

    public init(state: DaemonState?) {
        guard let state else {
            self.init(kind: .daemonDown)
            return
        }
        let badge = state.questions?.count ?? (state.pendingAction == nil ? 0 : 1)
        let voice = state.voice
        if voice.isOff {
            self.init(kind: .off, needsYou: badge)
        } else if let client = voice.client {
            if client.platform != Self.platform {
                self.init(kind: .voiceElsewhere(holder: client.name), needsYou: badge)
            } else if voice.muted {
                self.init(kind: .muted, needsYou: badge)
            } else {
                self.init(kind: voice.speaking ? .speaking : .listening, needsYou: badge)
            }
        } else if voice.clientConnected {
            self.init(kind: .voiceElsewhere(holder: nil), needsYou: badge)
        } else {
            self.init(kind: .off, needsYou: badge)
        }
    }

    /// The menu's first line.
    public var line: String {
        let base: String
        switch kind {
        case .daemonDown: return "Sheepdog isn't running"
        case .off: base = "Voice off"
        case let .voiceElsewhere(holder): base = holder.map { "Voice on \($0)" } ?? "Voice on another device"
        case .muted: base = "Muted"
        case .speaking: base = "Speaking"
        case .listening: base = "Listening"
        }
        guard needsYou > 0 else { return base }
        return base + (needsYou == 1 ? " · 1 needs you" : " · \(needsYou) need you")
    }

    /// The icon is dimmed: nothing is listening or the daemon is away.
    public var isDimmed: Bool {
        switch kind {
        case .daemonDown, .off, .voiceElsewhere: return true
        case .muted, .speaking, .listening: return false
        }
    }

    /// VoiceOver's name for the status item.
    public var accessibilityLabel: String { "Sheepdog: " + line }
}
