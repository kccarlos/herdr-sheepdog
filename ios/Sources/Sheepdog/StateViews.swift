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
                ContentUnavailableView {
                    Label("No agents yet", systemImage: "pawprint")
                } description: {
                    Text("Start a coding agent in herdr on your computer and it shows up here.")
                } actions: {
                    Button("Refresh") { Task { _ = await model.refreshState(quiet: false) } }
                        .accessibilityIdentifier("flock.empty.refresh")
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
                ContentUnavailableView {
                    Label("Daemon unreachable", systemImage: "wifi.slash")
                } description: {
                    Text(LocalizedStringKey(SetupHelp.unreachableFix))
                } actions: {
                    Button("Try again") { model.reconnect() }
                        .accessibilityIdentifier("flock.unreachable.retry")
                    Link("How to fix this", destination: SetupHelp.remoteAccessURL)
                        .accessibilityIdentifier("flock.unreachable.help")
                }
            case .tokenRefused:
                ContentUnavailableView {
                    Label("Token refused", systemImage: "key.slash")
                } description: {
                    Text(LocalizedStringKey(SetupHelp.tokenFix))
                } actions: {
                    Button("Scan the code again") {
                        model.settingsRoute = .daemon
                        model.showSettings = true
                    }
                    .accessibilityIdentifier("flock.tokenRefused.settings")
                }
            case .updateNeeded:
                ContentUnavailableView {
                    Label("Update needed", systemImage: "arrow.down.app")
                } description: {
                    Text("This app and the daemon speak different protocol versions. Update the older one.")
                } actions: {
                    Button("Try again") { model.reconnect() }
                }
            case .content:
                EmptyView()
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
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
