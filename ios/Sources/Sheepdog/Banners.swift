import SheepdogKit
import SwiftUI

/// Errors in plain words, with the one button that helps (app-redesign.md
/// 8.6): a card on the warm surface with a hairline border,
/// the tone's glyph in a soft disc, the message, a quiet hint, and pill
/// actions. The accent stays with the screen's question or orb: the
/// banner's buttons are outlined. Error is a red disc, warning an ink
/// triangle on a Fill disc, info and updates blue; the border is always a
/// hairline.
struct BannerView: View {
    let banner: SessionViewModel.Banner
    let retry: () -> Void
    let dismiss: () -> Void

    var body: some View {
        NoticeCard(
            tint: tint,
            disc: banner.tone == .warning ? Theme.fill : nil,
            symbol: symbol,
            message: banner.message,
            hint: banner.hint.map { LocalizedStringKey($0) },
            dismiss: dismiss
        ) {
            if let retryTitle = banner.retryTitle {
                Button(retryTitle, action: retry)
                    .buttonStyle(NoticeButtonStyle())
            }
            if let help = banner.help {
                Link(destination: help) {
                    Label("How to fix this", systemImage: "questionmark.circle")
                }
                .buttonStyle(NoticeButtonStyle(quiet: true))
                .accessibilityIdentifier("banner.help")
            }
        }
        .accessibilityIdentifier("banner")
    }

    private var tint: Color {
        switch banner.tone {
        case .error: return Theme.danger
        // Amber stays with the question that needs you; a warning is ink.
        case .warning: return Theme.ink
        case .info: return Theme.working
        }
    }

    private var symbol: String {
        switch banner.tone {
        case .error: return "exclamationmark"
        case .warning: return "exclamationmark.triangle"
        case .info: return "info"
        }
    }
}

/// The daemon's update notice on the main screen, once per version: what
/// is new and the one command to run on the computer, copyable. Later
/// hides it until the next version; Settings keeps it.
struct UpdateBanner: View {
    let notice: UpdateNotice
    let dismiss: () -> Void
    @State private var copied = false

    var body: some View {
        NoticeCard(tint: Theme.working, disc: nil, symbol: "arrow.down", message: notice.text, hint: notice.after.map { LocalizedStringKey($0) }, dismiss: dismiss) {
            if let command = notice.command {
                Button {
                    UIPasteboard.general.string = command
                    copied = true
                    Haptics.tap()
                    Task {
                        try? await Task.sleep(for: .seconds(1.6))
                        copied = false
                    }
                } label: {
                    HStack(spacing: 6) {
                        Text(command).font(.footnote.monospaced())
                        Image(systemName: copied ? "checkmark" : "doc.on.doc")
                            .font(.caption)
                            .contentTransition(.symbolEffect(.replace))
                    }
                }
                .buttonStyle(NoticeButtonStyle())
                .accessibilityLabel(copied ? "Copied" : "Copy the command: \(command)")
                .accessibilityIdentifier("updateNotice.copy")
            }
        }
        .accessibilityIdentifier("updateNotice")
    }
}

/// The shared shape of a banner.
private struct NoticeCard<Actions: View>: View {
    let tint: Color
    /// The disc's fill when it is not the tint's wash.
    let disc: Color?
    let symbol: String
    let message: String
    let hint: LocalizedStringKey?
    let dismiss: () -> Void
    @ViewBuilder let actions: () -> Actions
    @ScaledMetric(relativeTo: .subheadline) private var discSize: CGFloat = 28

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: symbol)
                .font(.footnote.weight(.bold))
                .foregroundStyle(tint)
                .frame(width: min(discSize, 36), height: min(discSize, 36))
                .background(Circle().fill(disc ?? tint.opacity(0.14)))
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 4) {
                Text(message)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(Theme.ink)
                    .fixedSize(horizontal: false, vertical: true)
                if let hint {
                    Text(hint)
                        .font(.footnote)
                        .foregroundStyle(Theme.muted)
                        .tint(Theme.primaryText)
                        .fixedSize(horizontal: false, vertical: true)
                }
                // Side by side while they fit, stacked when they do not.
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: 8) { actions() }
                    VStack(alignment: .leading, spacing: 8) { actions() }
                }
                .padding(.top, 4)
            }
            .padding(.top, 3)
            Spacer(minLength: 0)
            Button(action: dismiss) {
                Image(systemName: "xmark")
                    .font(.caption.weight(.bold))
                    .foregroundStyle(Theme.muted)
                    .frame(width: 44, height: 44)
                    .contentShape(Rectangle())
            }
            .padding(-8)
            .accessibilityLabel("Dismiss")
        }
        .padding(12)
        .background(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).fill(Theme.card))
        .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).strokeBorder(Theme.line, lineWidth: 1))
        .shadow(color: .black.opacity(0.06), radius: 8, y: 3)
        .accessibilityElement(children: .contain)
    }
}

/// A banner's pill: ink on the card with a hairline; `quiet` is a text
/// link in the brand's text colour. At least 44 pt tall to touch.
private struct NoticeButtonStyle: ButtonStyle {
    var quiet = false

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.footnote.weight(.semibold))
            .foregroundStyle(quiet ? Theme.primaryText : Theme.ink)
            .padding(.horizontal, quiet ? 4 : 14)
            .frame(minHeight: 34)
            .background { if !quiet { Capsule().fill(Theme.fill) } }
            .overlay { if !quiet { Capsule().strokeBorder(Theme.line, lineWidth: 1) } }
            .padding(.vertical, 5)
            .contentShape(Rectangle())
            .opacity(configuration.isPressed ? 0.6 : 1)
    }
}
