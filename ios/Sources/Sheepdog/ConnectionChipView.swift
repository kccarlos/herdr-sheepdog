import SheepdogKit
import SwiftUI

/// The top-left chip: a dot and one word for the daemon connection itself
/// (never the voice state — see `ConnectionChip`). Tapping it opens a sheet
/// with the address, round-trip time, daemon and voice info, and a way to
/// reconnect or jump to Settings.
struct ConnectionChipView: View {
    @EnvironmentObject private var model: SessionViewModel
    @State private var showDetail = false

    var body: some View {
        Button {
            showDetail = true
        } label: {
            HStack(spacing: 6) {
                Circle()
                    .fill(color)
                    .frame(width: 8, height: 8)
                Text(model.connectionChip.word)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
        }
        .accessibilityIdentifier("chip.connection")
        .accessibilityLabel("Connection: \(model.connectionChip.word)")
        .sheet(isPresented: $showDetail) {
            ConnectionDetailView(showSettings: {
                showDetail = false
                model.showSettings = true
            })
            .environmentObject(model)
            // A single detent only: swiping on the Form inside a
            // multi-detent sheet is ambiguous between "scroll" and "resize
            // the sheet", and that ambiguity is what triggered a SwiftUI
            // "onChange(of: Bool) … multiple times per frame" main-thread
            // hang during manual testing. One detent removes the ambiguity
            // and leaves room for everything without scrolling.
            .presentationDetents([.large])
        }
    }

    private var color: Color {
        switch model.connectionChip.tint {
        case .green: return Theme.done
        case .amber: return Theme.blocked
        case .red: return Color(.systemRed)
        case .grey: return Theme.unknown
        }
    }
}

/// The chip's sheet: everything about the connection that does not fit in
/// three words up top.
private struct ConnectionDetailView: View {
    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.dismiss) private var dismiss
    let showSettings: () -> Void

    @State private var roundTrip: TimeInterval?
    @State private var hasPinged = false
    @State private var pinging = false

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    LabeledContent("Status", value: model.connectionChip.word)
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
                    LabeledContent("Voice name", value: model.voiceName)
                        .accessibilityIdentifier("chip.voiceName")
                    LabeledContent("Voice status", value: model.state.title)
                        .accessibilityIdentifier("chip.voiceStatus")
                    if let brain = model.brain?.display {
                        LabeledContent("Brain", value: brain)
                            .accessibilityIdentifier("chip.brain")
                    }
                }

                Section {
                    Button("Reconnect") {
                        model.reconnect()
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
                }
            }
            .task { await ping() }
        }
    }

    private func ping() async {
        pinging = true
        roundTrip = await model.pingRoundTrip()
        hasPinged = true
        pinging = false
    }
}
