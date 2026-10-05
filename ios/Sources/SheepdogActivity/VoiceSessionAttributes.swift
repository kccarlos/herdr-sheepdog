import ActivityKit
import Foundation

/// The voice session's Live Activity. Compiled into the app (which starts,
/// updates and ends it, `VoiceLiveActivity`) and into the widget extension
/// (which only renders it). Every field is a primitive: ActivityKit decodes
/// the content state with a default `JSONDecoder` and drops the whole
/// update on any mismatch. The type name is shipped-forever.
struct VoiceSessionAttributes: ActivityAttributes {
    struct ContentState: Codable, Hashable {
        /// `VoiceActivityState.Phase` raw value: connecting, listening,
        /// speaking, thinking, muted, paused. Unknown values render as
        /// listening.
        var phase: String
        var muted: Bool
        /// Agents waiting for an answer.
        var waiting: Int
        /// "Listening", "Speaking", "Reconnecting…".
        var title: String
        /// "2 agents waiting for an answer", or nil.
        var waitingText: String?
        /// VoiceOver: the banner and the expanded island.
        var accessibilityLabel: String
        /// VoiceOver: the compact and minimal island.
        var compactAccessibilityLabel: String
    }

    /// The dog's name as the daemon gives it ("Sheepdog").
    var voiceName: String
}
