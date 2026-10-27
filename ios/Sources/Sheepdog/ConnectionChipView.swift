import SheepdogKit
import SwiftUI

/// The chip's sheet: everything about the connection that does not fit in
/// three words up top.
struct ConnectionDetailView: View {
    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.dismiss) private var dismiss
    let showSettings: () -> Void
    let showModels: () -> Void

    @State private var roundTrip: TimeInterval?
    @State private var hasPinged = false
    @State private var pinging = false

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    LabeledContent("Status", value: model.linkModel.presentation.chip)
                        .accessibilityIdentifier("chip.status")
                    if let inline = model.linkModel.presentation.inline {
                        Text(inline.message)
                            .font(.footnote)
                            .foregroundStyle(Theme.muted)
                            .accessibilityIdentifier("chip.statusDetail")
                    }
                    LabeledContent("Address", value: model.endpointDisplay)
                        .accessibilityIdentifier("chip.address")
                    LabeledContent("Round trip") {
                        if pinging {
                            ProgressView()
                        } else if let roundTrip {
                            Text("\(Int(roundTrip * 1000)) ms")
                        } else {
                            Text(hasPinged ? "No answer" : "—")
                        }
                    }
                    .accessibilityIdentifier("chip.rtt")
                }

                Section {
                    LabeledContent("Daemon", value: model.daemonVersion ?? "—")
                        .accessibilityIdentifier("chip.daemonVersion")
                    LabeledContent("Protocol", value: model.linkModel.protocolDisplay)
                        .accessibilityIdentifier("chip.protocol")
                    LabeledContent("Voice name", value: model.voiceName)
                        .accessibilityIdentifier("chip.voiceName")
                    LabeledContent("Voice status", value: model.state.title)
                        .accessibilityIdentifier("chip.voiceStatus")
                    if let voice = model.voiceProviderDisplay {
                        LabeledContent("Voice provider", value: voice)
                            .accessibilityIdentifier("chip.voiceProvider")
                    }
                    if let brain = model.brain?.display {
                        LabeledContent("Orchestrator", value: brain)
                            .accessibilityIdentifier("chip.brain")
                    }
                }

                Section {
                    NavigationLink {
                        HealthView(meter: model.meter, changeModels: showModels)
                    } label: {
                        Text("Health")
                    }
                    .accessibilityIdentifier("chip.health")
                } footer: {
                    Text("Daemon, models, audio path and recent problems.")
                }

                Section {
                    Button("Reconnect") {
                        model.retryLink()
                        dismiss()
                    }
                    .accessibilityIdentifier("chip.reconnect")
                    Button("Open Settings", action: showSettings)
                        .accessibilityIdentifier("chip.openSettings")
                }
            }
            .navigationTitle("Connection")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                        .tint(Theme.primaryText)
                }
            }
            .task {
                async let providers: Void = model.settingsModel.loadVoiceProviders()
                await ping()
                await providers
            }
        }
    }

    private func ping() async {
        pinging = true
        roundTrip = await model.pingRoundTrip()
        hasPinged = true
        pinging = false
    }
}

extension LinkPresentation.Tint {
    /// The status colour: green connected, amber on its way, red needs
    /// something, grey off.
    var color: Color {
        switch self {
        case .green: return Theme.done
        case .amber: return Theme.blocked
        case .red: return Theme.danger
        case .grey: return Theme.unknown
        }
    }

    /// The symbol beside the status word in menus.
    var symbol: String {
        switch self {
        case .green: return "checkmark.circle"
        case .amber: return "arrow.triangle.2.circlepath"
        case .red: return "wifi.slash"
        case .grey: return "circle.dashed"
        }
    }
}
