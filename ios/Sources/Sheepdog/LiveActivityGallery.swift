#if DEBUG
import SheepdogKit
import SwiftUI

/// Sample Live Activity content, for the gallery and for
/// `-sheepdogDemoLiveActivity` (a real activity without a session).
enum LiveActivitySamples {
    static let authFix = LiveActivityContent.Row(pane: "w1:p1", agent: "auth-fix", question: "Run npm test in webapp?")
    static let blog = LiveActivityContent.Row(pane: "w2:p1", agent: "blog", question: "Publish the draft to staging?")
    static let migrate = LiveActivityContent.Row(pane: "w3:p1", agent: "db-migrate", question: "Drop the old sessions table?")

    static let one = LiveActivityContent(phase: .listening, rows: [authFix, blog], working: 3, done: 1)
    static let several = LiveActivityContent(
        phase: .listening, computer: "Work Mac",
        rows: [authFix, LiveActivityContent.Row(pane: "w2:p1", agent: "blog", question: "Publish the draft to staging?", computer: "Home Mac", computerID: "home")],
        working: 2
    )
    static let many = LiveActivityContent(phase: .speaking, rows: [authFix, blog, migrate], waiting: 5, working: 1)
    static let quiet = LiveActivityContent(phase: .muted, muted: true, working: 2, done: 3)
    /// What the daemon pushes while the app is away, its rows opened.
    static let away = LiveActivityContent(phase: .away, rows: [authFix, blog], working: 3, done: 1)
    /// The same push when the widget cannot open the rows: counts only.
    static let awayCounts = LiveActivityContent(phase: .away, waiting: 2, working: 3, done: 1)

    static func named(_ name: String) -> LiveActivityContent {
        switch name {
        case "several": return several
        case "many": return many
        case "quiet": return quiet
        case "away": return away
        case "away-counts": return awayCounts
        default: return one
        }
    }

    /// `-sheepdogDemoLiveActivity <session state>`.
    static func demo(_ state: SessionState) -> LiveActivityContent? {
        var content = one
        content.phase = LiveActivityContent.phase(state: state, suspendedInBackground: false) ?? .listening
        content.muted = state == .muted
        return content
    }
}

/// `-sheepdogLiveActivityGallery [one|several|many|quiet|away|away-counts|stale]`: the
/// Live Activity's lock-screen banner, the widget's own view, drawn on a
/// stand-in lock screen for screenshots (a simulator cannot be locked
/// from a script).
struct LiveActivityGallery: View {
    private var variant: String {
        let args = ProcessInfo.processInfo.arguments
        guard let i = args.firstIndex(of: "-sheepdogLiveActivityGallery"), i + 1 < args.count, !args[i + 1].hasPrefix("-") else { return "one" }
        return args[i + 1]
    }

    @Environment(\.colorScheme) private var scheme

    var body: some View {
        let stale = variant == "stale"
        VStack(spacing: 0) {
            VStack(spacing: 0) {
                Text("Tuesday 20 October")
                    .font(.system(size: 20, weight: .semibold))
                Text("9:41")
                    .font(.system(size: 96, weight: .semibold, design: .rounded))
                    .tracking(-2)
            }
            .foregroundStyle(scheme == .dark ? Color(white: 0.95) : Color(red: 0.16, green: 0.17, blue: 0.19))
            .dynamicTypeSize(.large)
            .padding(.top, 70)
            Spacer()
            VoiceSessionLockScreenView(state: LiveActivitySamples.named(variant), voiceName: "Sheepdog", isStale: stale)
                .background(ActivityPalette.card, in: RoundedRectangle(cornerRadius: 24, style: .continuous))
                .padding(.horizontal, 12)
                .padding(.bottom, 110)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(scheme == .dark ? Color(red: 0.09, green: 0.09, blue: 0.11) : Color(red: 0.906, green: 0.882, blue: 0.831))
        .ignoresSafeArea()
        .statusBarHidden()
    }
}
#endif
