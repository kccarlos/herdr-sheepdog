import SwiftUI

/// What the app asks before a system permission prompt: why, in one line,
/// and the choice to say no. The system prompt comes only after Continue.
enum PermissionPrimer: String, Identifiable {
    case microphone
    case notifications
    var id: String { rawValue }
}

/// The primer's screen (canvas "Before the microphone prompt" and "Before
/// the notification prompt").
struct PermissionPrimerView: View {
    let primer: PermissionPrimer
    let answer: (Bool) -> Void

    var body: some View {
        VStack(spacing: 14) {
            Spacer(minLength: 20)
            MascotCard(name: primer == .microphone ? "MascotListening" : "MascotAlert", width: 150)
                .frame(maxHeight: 170)
            Text(title)
                .font(.title.weight(.bold))
                .fontDesign(.rounded)
                .foregroundStyle(Theme.ink)
                .multilineTextAlignment(.center)
                .padding(.top, 6)
            Text(message)
                .font(.body)
                .foregroundStyle(Theme.muted)
                .multilineTextAlignment(.center)
                .fixedSize(horizontal: false, vertical: true)
            if primer == .notifications { sample }
            Spacer(minLength: 20)
            Button { answer(true) } label: {
                Text(primary)
                    .font(.body.weight(.semibold))
                    .foregroundStyle(Theme.onAccent)
                    .frame(maxWidth: .infinity, minHeight: 50)
                    .background(Capsule().fill(Color.accentColor))
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("primer.allow")
            Button { answer(false) } label: {
                Text(secondary)
                    .font(.body.weight(.medium))
                    .foregroundStyle(Theme.muted)
                    .frame(maxWidth: .infinity, minHeight: 44)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("primer.notNow")
        }
        .padding(.horizontal, 28)
        .padding(.bottom, 12)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.surface.ignoresSafeArea())
        .interactiveDismissDisabled()
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("primer.\(primer.rawValue)")
    }

    private var title: String {
        primer == .microphone ? "Let Sheepdog hear you" : "Know when an agent needs you"
    }

    private var message: String {
        primer == .microphone
            ? "It listens only while a voice session is on. Tap the mic in the voice capsule to mute it any time."
            : "When an agent asks a question or finishes its work, Sheepdog taps you, even with the app closed."
    }

    private var primary: String { primer == .microphone ? "Allow the microphone" : "Turn on notifications" }
    private var secondary: String { primer == .microphone ? "Not now — I'll type" : "Not now" }

    /// What a notification looks like.
    private var sample: some View {
        HStack(alignment: .top, spacing: 10) {
            Image("LaunchLogo")
                .resizable()
                .frame(width: 34, height: 34)
                .clipShape(RoundedRectangle(cornerRadius: 8))
            VStack(alignment: .leading, spacing: 2) {
                Text("auth-fix needs you").font(.subheadline.weight(.semibold)).foregroundStyle(Theme.ink)
                Text("Run npm test in webapp?").font(.subheadline).foregroundStyle(Theme.ink)
                Text("Yes · Yes, don't ask again · No").font(.caption).foregroundStyle(Theme.muted)
            }
            Spacer(minLength: 0)
        }
        .padding(12)
        .background(RoundedRectangle(cornerRadius: Theme.Radius.card).fill(Theme.card))
        .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card).strokeBorder(Theme.line, lineWidth: 1))
        .padding(.top, 8)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("Example notification: auth-fix needs you. Run npm test in webapp?")
    }
}
