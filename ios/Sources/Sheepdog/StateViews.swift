import SheepdogKit
import SwiftUI

/// The Agents pane with nothing to list: one `ContentUnavailableView` per
/// reason, each with the one action that helps.
struct FlockUnavailableView: View {
    let state: FlockPaneState
    @EnvironmentObject private var model: SessionViewModel

    var body: some View {
        Group {
            switch state {
            case .loading, .reconnecting:
                // Words and a spinner: the chip says the same thing.
                ContentUnavailableView {
                    Label {
                        Text(state == .loading ? "Connecting…" : "Reconnecting…")
                    } icon: {
                        ProgressView()
                    }
                } description: {
                    Text("Your agents appear here as soon as the daemon answers.")
                }
            case .empty:
                ScrollView {
                    VStack(spacing: 4) {
                        NoAgentsView(onNewAgent: model.agentActionsAvailable ? { model.presentation.present(.newAgent) } : nil)
                        Button("Refresh") { Task { _ = await model.refreshState(quiet: false) } }
                            .font(.subheadline.weight(.medium))
                            .foregroundStyle(Theme.muted)
                            .frame(minHeight: 44)
                            .accessibilityIdentifier("flock.empty.refresh")
                    }
                    .padding(.top, 80)
                }
            case .notConnected:
                ContentUnavailableView {
                    Label("Not connected", systemImage: "bolt.horizontal")
                } description: {
                    Text("Connect to see your agents.")
                } actions: {
                    Button("Connect") { model.connect() }
                        .accessibilityIdentifier("flock.notConnected.connect")
                }
            case .unreachable:
                // The link's words (`LinkPresentation`): when it was last
                // seen, and what to check.
                let title = "Can't reach \(model.showsComputerLabels ? model.currentComputerName : "your computer")"
                ContentUnavailableView {
                    Label(title, systemImage: "wifi.slash")
                } description: {
                    // The rest of the link's words: the title already says
                    // the first sentence.
                    Text(Self.dropping("\(title).", from: model.linkModel.presentation.inline?.message ?? SetupHelp.unreachableFix))
                } actions: {
                    Button("Try now") { model.retryLink() }
                        .accessibilityIdentifier("flock.unreachable.retry")
                    Link("How to fix this", destination: SetupHelp.remoteAccessURL)
                        .tint(Theme.primaryText)
                        .accessibilityIdentifier("flock.unreachable.help")
                }
            case .noNetwork:
                ContentUnavailableView {
                    Label("No network", systemImage: "wifi.exclamationmark")
                } description: {
                    Text(model.linkModel.presentation.inline?.message ?? "")
                }
                .accessibilityIdentifier("flock.noNetwork")
            case .untrusted:
                ContentUnavailableView {
                    Label("Not verified", systemImage: "exclamationmark.shield")
                } description: {
                    Text(model.linkModel.presentation.inline?.message ?? "")
                } actions: {
                    Button("Try again") { model.retryLink() }
                        .accessibilityIdentifier("flock.untrusted.retry")
                    Button("Use another address") {
                        model.navigation.settingsRoute = .daemon
                        model.presentation.present(.settings)
                    }
                    .accessibilityIdentifier("flock.untrusted.addresses")
                }
            case .tokenRefused:
                ContentUnavailableView {
                    Label((model.notices.pairingProblem ?? .revoked).title, systemImage: "qrcode.viewfinder")
                } description: {
                    Text((model.notices.pairingProblem ?? .revoked).message)
                } actions: {
                    Button("Scan again") {
                        model.needsNewScan(model.notices.pairingProblem ?? .revoked)
                    }
                    .accessibilityIdentifier("flock.tokenRefused.settings")
                }
            case .updateNeeded:
                ContentUnavailableView {
                    Label("Update needed", systemImage: "arrow.down.app")
                } description: {
                    Text("This app and the daemon speak different protocol versions. Update the older one.")
                } actions: {
                    Button("Try again") { model.retryLink() }
                }
            case .content:
                EmptyView()
            }
        }
        .tint(Theme.primaryText)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    /// `text` without a leading `prefix` (and the space after it).
    static func dropping(_ prefix: String, from text: String) -> String {
        guard text.hasPrefix(prefix) else { return text }
        return String(text.dropFirst(prefix.count)).trimmingCharacters(in: .whitespaces)
    }
}

/// A brand mascot pose from the asset catalog: transparent, with the cream
/// sticker outline baked in, the same art in light and dark. Decorative:
/// VoiceOver skips it.
///
/// - sleeping: first run, offline, no agents;
/// - listening: the empty conversation, the microphone primer;
/// - alert: the notification primer;
/// - celebrating: the "Paired" screen only.
struct MascotCard: View {
    let name: String
    var width: CGFloat = 160

    var body: some View {
        Image(decorative: name)
            .resizable()
            .scaledToFit()
            .frame(maxWidth: width)
            .accessibilityHidden(true)
    }
}

/// The splash: the launch screen's logo on `LaunchBackground`, with the
/// lowercase wordmark under it. The launch screen (Info.plist) shows the
/// same logo at the same size and place, so the hand-off does not jump;
/// the wordmark fades in, then the whole splash fades out.
struct SplashView: View {
    var body: some View {
        ZStack {
            Color("LaunchBackground").ignoresSafeArea()
            Image(decorative: "LaunchLogo")
                .overlay(alignment: .bottom) {
                    Text("sheepdog")
                        .font(.system(size: 36, weight: .bold, design: .rounded))
                        .tracking(-0.4)
                        .foregroundStyle(Theme.cream)
                        .fixedSize()
                        .offset(y: 58)
                }
        }
        .accessibilityElement()
        .accessibilityLabel("Sheepdog")
        .accessibilityIdentifier("splash")
    }
}

/// An agent window whose agent is no longer listed.
struct AgentGoneView: View {
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        ContentUnavailableView {
            Label("That agent is gone", systemImage: "terminal")
        } description: {
            Text("Its pane closed or herdr no longer reports it.")
        } actions: {
            Button("Back to the agents") { dismiss() }
                .accessibilityIdentifier("agentGone.back")
        }
    }
}
