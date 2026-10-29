import SheepdogKit
import SwiftUI

/// A computer's section header on the Agents page, All agents and the iPad
/// sidebar (several computers only): its name, status and one voice marker.
struct ComputerSectionHeader: View {
    enum Style: Equatable {
        case section
        case sidebar
    }

    let id: String
    let name: String
    let detail: String?
    let marker: ComputerVoiceMarkerState
    let style: Style
    let isExpanded: Bool
    let toggleExpanded: () -> Void
    let moveVoice: () -> Void
    let retry: () -> Void

    init(
        id: String,
        name: String,
        detail: String?,
        marker: ComputerVoiceMarkerState,
        style: Style = .section,
        isExpanded: Bool,
        toggleExpanded: @escaping () -> Void,
        moveVoice: @escaping () -> Void,
        retry: @escaping () -> Void
    ) {
        self.id = id
        self.name = name
        self.detail = detail
        self.marker = marker
        self.style = style
        self.isExpanded = isExpanded
        self.toggleExpanded = toggleExpanded
        self.moveVoice = moveVoice
        self.retry = retry
    }

    var body: some View {
        HStack(alignment: .center, spacing: 8) {
            Button(action: toggleExpanded) {
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    titles
                    Image(systemName: "chevron.right")
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(Theme.muted)
                        .rotationEffect(.degrees(isExpanded ? 90 : 0))
                        .accessibilityHidden(true)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityAddTraits(.isHeader)
            .accessibilityValue(isExpanded ? "Expanded" : "Collapsed")
            .accessibilityHint(isExpanded ? "Hide its agents" : "Show its agents")
            .accessibilityIdentifier(style == .sidebar ? "computerLabel.sidebar.\(id)" : "computerLabel.section.\(id)")
            Spacer(minLength: 8)
            markerView
        }
        // Every header is leading-aligned, including when a long name wraps.
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.top, style == .section ? 4 : 12)
        .padding(.bottom, 2)
    }

    /// Unique per surface: the iPad shows the sidebar and All agents together.
    private var markerIdentifier: String {
        style == .sidebar ? "computer.sidebarVoiceMarker.\(id)" : "computer.voiceMarker.\(id)"
    }

    private var subtitle: String? {
        marker == .voice ? "Voice is here" : detail
    }

    private var titles: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(name)
                .font(style == .section ? .title3.weight(.semibold) : .subheadline.weight(.semibold))
                .fontDesign(.rounded)
                .foregroundStyle(Theme.ink)
                .fixedSize(horizontal: false, vertical: true)
            if let subtitle {
                Text(subtitle)
                    .font(style == .section ? .footnote : .caption)
                    .foregroundStyle(Theme.muted)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    @ViewBuilder private var markerView: some View {
        switch marker {
        case .hidden:
            EmptyView()
        case .voice:
            markerCircle {
                Circle().fill(Theme.ink)
                Image(systemName: "waveform")
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundStyle(style == .sidebar ? Theme.fill : Theme.surface)
            }
            .accessibilityElement()
            .accessibilityAddTraits(.isSelected)
            .accessibilityLabel("Voice is on \(name)")
            .accessibilityIdentifier(markerIdentifier)
        case .available:
            Button(action: moveVoice) {
                markerCircle {
                    Circle().strokeBorder(Theme.dash, lineWidth: 1.5)
                }
                .contentShape(Circle())
            }
            .buttonStyle(.plain)
            .hoverEffect()
            .accessibilityLabel("Move the voice to \(name)")
            .accessibilityIdentifier(markerIdentifier)
        case .offline:
            Button(action: retry) {
                markerCircle {
                    Circle().strokeBorder(Theme.dash, style: StrokeStyle(lineWidth: 1.5, dash: [4, 3]))
                    Image(systemName: "arrow.clockwise")
                        .font(.system(size: 11, weight: .semibold))
                        .foregroundStyle(Theme.ink)
                }
                .contentShape(Circle())
            }
            .buttonStyle(.plain)
            .hoverEffect()
            .accessibilityLabel("Retry \(name)")
            .accessibilityIdentifier(markerIdentifier)
        }
    }

    private func markerCircle<Content: View>(@ViewBuilder content: () -> Content) -> some View {
        ZStack { content() }
            .frame(width: 28, height: 28)
            .frame(width: 44, height: 44)
    }
}

/// A computer that does not answer: one dashed row with Retry. Its
/// last-known agents are not shown as if they were live.
struct OfflineComputerRow: View {
    let name: String
    /// "Last seen 9:12.", nil when it never answered.
    let lastSeen: String?
    let retry: () -> Void

    private var message: String {
        ["Can't reach \(name).", lastSeen, "Is it awake and on the same network?"].compactMap { $0 }.joined(separator: " ")
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(systemName: "wifi.slash")
                    .foregroundStyle(Theme.muted)
                    .accessibilityHidden(true)
                Text(message)
                    .font(.subheadline)
                    .foregroundStyle(Theme.ink)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Button(action: retry) {
                Text("Retry")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(Theme.ink)
                    .padding(.horizontal, 16)
                    .frame(minHeight: 36)
                    .overlay(Capsule().strokeBorder(Theme.ink, lineWidth: 1.5))
                    .frame(minHeight: 44)
                    .contentShape(Capsule())
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Retry \(name)")
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .overlay(
            RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
                .strokeBorder(Theme.line, style: StrokeStyle(lineWidth: 1.5, dash: [5, 4]))
        )
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("computerLabel.offline.\(name)")
    }
}
