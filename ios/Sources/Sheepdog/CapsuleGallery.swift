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
        // `-sheepdogCapsuleGallery banners`: the banners instead.
        if ProcessInfo.processInfo.arguments.contains("banners") {
            BannerGallery()
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
                        statusText: tile.status,
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
                UpdateBanner(notice: UpdateInfo(current: "0.9.1", latest: "0.9.2", available: true, how: "brew upgrade sheepdog").notice!, dismiss: {})
            }
            .padding(16)
        }
        .background(Theme.surface)
        .accessibilityIdentifier("capsule.gallery")
    }
}
#endif
