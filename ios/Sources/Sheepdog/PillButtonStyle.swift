import SwiftUI

/// The app's pill button: the accent fill with ink words for the one
/// primary action on a screen, else the card with a hairline. At least
/// 44 pt tall; the label wraps rather than truncates.
struct PillButtonStyle: ButtonStyle {
    var prominent = false
    var fullWidth = true
    @Environment(\.isEnabled) private var isEnabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.body.weight(.semibold))
            .foregroundStyle(prominent ? Theme.onAccent : Theme.ink)
            .multilineTextAlignment(.center)
            .padding(.horizontal, 18)
            .padding(.vertical, 11)
            .frame(maxWidth: fullWidth ? .infinity : nil, minHeight: 44)
            .background(Capsule().fill(prominent ? Color.accentColor : Theme.card))
            .overlay { if !prominent { Capsule().strokeBorder(Theme.line, lineWidth: 1) } }
            .contentShape(Capsule())
            .opacity(configuration.isPressed ? 0.7 : (isEnabled ? 1 : 0.45))
    }
}
