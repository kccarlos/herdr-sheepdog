#if DEBUG
import SheepdogKit
import SwiftUI

/// Every capsule mode on one screen (`-sheepdogCapsuleGallery`), for the
/// redesign screenshots and a quick look in light, dark and the large text
/// sizes. Debug builds only.
struct CapsuleGallery: View {
    @StateObject private var meter = VoiceMeter()

    private struct Tile: Identifiable {
        let title: String
        let state: SessionState
        var talkMode: TalkMode = .openMic
        var holding = false
        var question = false
        var muted = false
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
        Tile(title: "Offline", state: .disconnected, status: "Offline — tap the orb to connect"),
        Tile(title: "Voice off", state: .voiceOff, status: "Voice is off on this computer"),
        Tile(title: "Hold to talk", state: .listening, talkMode: .holdToTalk, status: "Hold the orb to talk"),
        Tile(title: "Holding", state: .listening, talkMode: .holdToTalk, holding: true, status: "Talking — release to send"),
    ]

    var body: some View {
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
                        statusText: tile.status,
                        onConnect: {}, onStop: {}, onMute: {},
                        menu: { Button("End voice session") {} }
                    )
                }
            }
            .padding(12)
        }
        .background(Theme.surface)
        .accessibilityIdentifier("capsule.gallery")
    }
}
#endif
