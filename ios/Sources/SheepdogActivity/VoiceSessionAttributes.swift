import ActivityKit
import Foundation
import SheepdogKit

/// The voice session's Live Activity. Compiled into the app (which starts,
/// updates and ends it, `VoiceLiveActivity`) and into the widget extension
/// (which only renders it). The content state is SheepdogKit's
/// `LiveActivityContent`: ActivityKit decodes it with a default
/// `JSONDecoder` in the widget's process, which links SheepdogKit for it.
/// The type name is shipped-forever.
struct VoiceSessionAttributes: ActivityAttributes {
    typealias ContentState = LiveActivityContent

    /// The dog's name as the daemon gives it ("Sheepdog").
    var voiceName: String
}
