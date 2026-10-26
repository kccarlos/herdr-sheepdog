import SheepdogKit
import SwiftUI

/// The one-time screen before the first real voice session or typed line:
/// what goes where, what the app's developer gets (never your voice, text or screens), and how to
/// take it back. After a withdrawal the same screen is the way back.
/// Agree is the only thing that turns sending on.
struct AIConsentView: View {
    private let consent: AIConsentModel
    let model: SessionViewModel

    init(model: SessionViewModel) {
        self.model = model
        consent = model.consent
    }

    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    private var withdrawn: Bool { consent.decision == .withdrawn }

    var body: some View {
        VStack(spacing: 0) {
            ScrollView {
                VStack(spacing: 14) {
                    MascotCard(name: "MascotListening", width: 100)
                        .frame(maxHeight: 110)
                        .padding(.top, 8)
                    Text(withdrawn ? "Sending to AI services is off" : "Your voice goes to AI services")
                        .font(.title.weight(.bold))
                        .fontDesign(.rounded)
                        .foregroundStyle(Theme.ink)
                        .multilineTextAlignment(.center)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityAddTraits(.isHeader)
                        .accessibilityIdentifier("consent.title")
                    Text(lead)
                        .font(.body)
                        .foregroundStyle(Theme.muted)
                        .multilineTextAlignment(.center)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("consent.lead")
                    VStack(alignment: .leading, spacing: 16) {
                        fact("arrow.up.right", "What is sent",
                             "What you say and type, and what your agents' screens show. A photo you attach goes to your computer, and the coding agent you send it to passes it to its own AI service.")
                        fact("desktopcomputer", "Where it goes",
                             "To your own computer, then to the AI services you set up on it. They have their own terms.")
                        fact("lock.shield", "What we get",
                             "Sheepdog's developer never sees your voice, text, photos or screens. The one service we run, a push relay, only forwards notifications to Apple. It sees your phone's notification token, when one is sent and how many agents are waiting, working and done, and cannot read what a notification says.")
                    }
                    .padding(16)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(RoundedRectangle(cornerRadius: Theme.Radius.card).fill(Theme.card))
                    .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card).strokeBorder(Theme.line, lineWidth: 1))
                    .padding(.top, 4)
                    Text("You can turn this off any time in Settings > Privacy.")
                        .font(.footnote)
                        .foregroundStyle(Theme.muted)
                        .multilineTextAlignment(.center)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .padding(.horizontal, 24)
                .padding(.bottom, 12)
                // At the accessibility sizes the buttons scroll with the
                // text: pinned, they would leave a few lines to read.
                if dynamicTypeSize.isAccessibilitySize { buttons.padding(.top, 8) }
            }
            .scrollBounceBehavior(.basedOnSize)
            if !dynamicTypeSize.isAccessibilitySize { buttons.padding(.top, 8) }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.surface.ignoresSafeArea())
        .interactiveDismissDisabled()
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("consent.screen")
    }

    private var buttons: some View {
            VStack(spacing: 4) {
                Button { model.agreeToAIServices() } label: {
                    Text(withdrawn ? "Turn it back on" : "Agree")
                        .font(.body.weight(.semibold))
                        .foregroundStyle(Theme.onAccent)
                        .frame(maxWidth: .infinity, minHeight: 50)
                        .background(Capsule().fill(Color.accentColor))
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("consent.agree")
                let secondary = dynamicTypeSize.isAccessibilitySize ? AnyLayout(VStackLayout(spacing: 0)) : AnyLayout(HStackLayout(spacing: 8))
                secondary {
                    Link(destination: SetupHelp.privacyURL) {
                        Text("Learn more")
                            .font(.body.weight(.medium))
                            .foregroundStyle(Theme.primaryText)
                            .frame(maxWidth: .infinity, minHeight: 44)
                    }
                    .accessibilityIdentifier("consent.learnMore")
                    Button { model.declineAIServices() } label: {
                        Text("Not now")
                            .font(.body.weight(.medium))
                            .foregroundStyle(Theme.muted)
                            .frame(maxWidth: .infinity, minHeight: 44)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("consent.notNow")
                }
            }
            .padding(.horizontal, 24)
            .padding(.bottom, 12)
    }

    private var lead: String {
        if withdrawn {
            return "While this is off, Sheepdog starts no voice session and sends no typed lines. Your agents keep working, and the demo still works."
        }
        return "Sheepdog passes what you say and type on to \(model.aiServicesSummary)."
    }

    private func fact(_ symbol: String, _ title: String, _ text: String) -> some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: symbol)
                .font(.body)
                .foregroundStyle(Theme.primaryText)
                .frame(width: 28)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(Theme.ink)
                Text(text)
                    .font(.subheadline)
                    .foregroundStyle(Theme.muted)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .accessibilityElement(children: .combine)
    }
}

/// Settings > Privacy: the switch that sends the voice and typed lines to
/// AI services, and where to read more. Off blocks both, with the consent
/// screen as the way back.
struct PrivacyView: View {
    @EnvironmentObject private var model: SessionViewModel
    let consent: AIConsentModel

    var body: some View {
        Form {
            Section {
                Toggle("Send to AI services", isOn: Binding(
                    get: { consent.isAllowed },
                    set: { on in
                        if on { model.agreeToAIServices() } else { model.withdrawFromAIServices() }
                    }
                ))
                .tint(Color.accentColor)
                .accessibilityIdentifier("privacy.aiSwitch")
            } footer: {
                Text(footer)
                    .accessibilityIdentifier("privacy.aiFooter")
            }
            Section {
                Link(destination: SetupHelp.privacyURL) {
                    Label("Privacy policy", systemImage: "hand.raised")
                }
                .tint(Theme.primaryText)
                .accessibilityIdentifier("privacy.policy")
            }
        }
        .navigationTitle("Privacy")
        .navigationBarTitleDisplayMode(.inline)
        .accessibilityIdentifier("privacy.list")
    }

    private var footer: String {
        let where_ = "Your voice and typed lines go to your own computer and from there to \(model.aiServicesSummary). Sheepdog's developer never sees them."
        if consent.isAllowed { return where_ }
        return "Off: no voice session starts and no typed line is sent. Your agents keep working. " + where_
    }
}
