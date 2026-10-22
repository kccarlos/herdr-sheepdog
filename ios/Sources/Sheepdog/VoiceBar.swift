import SheepdogKit
import SwiftUI

/// The floating voice capsule (canvas "Dock"): the mic (mute) button, the
/// orb, the name and status — a button that opens the voice menu — and the
/// keyboard, which morphs the capsule into the composer.
///
/// Every control sits in a fixed 44 pt slot, so a hidden one never shifts
/// the others. The capsule grows with the text size up to a cap (the
/// status wraps to two lines at the accessibility sizes). It is Liquid
/// Glass, or a solid card under Reduce Transparency. The look comes from
/// `CapsuleMode`; what the orb does from `VoiceBarModel`.
struct VoiceCapsule<VoiceMenu: View>: View {
    let state: SessionState
    @ObservedObject var meter: VoiceMeter
    let muted: Bool
    let name: String
    var talkMode: TalkMode = .openMic
    var holding = false
    var noSignal = false
    /// A question is open: the orb goes quiet so the answer has the accent.
    var questionOpen = false
    var statusText = ""
    var canType = true
    let onConnect: () -> Void
    let onStop: () -> Void
    let onMute: () -> Void
    var onResume: () -> Void = {}
    var onPress: () -> Void = {}
    var onRelease: () -> Void = {}
    var onType: () -> Void = {}
    /// The voice menu's items.
    @ViewBuilder let menu: () -> VoiceMenu

    @ScaledMetric(relativeTo: .subheadline) private var scaledHeight: CGFloat = 60
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    private var mode: CapsuleMode {
        CapsuleMode.resolve(state: state, talkMode: talkMode, holding: holding, questionOpen: questionOpen)
    }
    private var model: VoiceBarModel { VoiceBarModel(state: state, talkMode: talkMode) }
    private var height: CGFloat { min(max(scaledHeight, 60), 110) }

    var body: some View {
        HStack(spacing: 8) {
            micSlot
            orb
            // The status is drawn over the menu button rather than inside
            // it, so VoiceOver (and the UI tests) read it as its own text;
            // touches pass through it to the menu.
            Menu(content: menu) { nameAndStatus(showsName: true) }
                .menuOrder(.fixed)
                .overlay { nameAndStatus(showsName: false).allowsHitTesting(false) }
                .accessibilityLabel("Voice: \(name)")
                .accessibilityHint("Shows the connection and End voice session")
                .accessibilityIdentifier("voice.menu")
            Button(action: onType) {
                Image(systemName: "keyboard")
                    .font(.system(size: 19, weight: .regular))
                    .foregroundStyle(canType ? Theme.muted : Theme.muted.opacity(0.4))
                    .frame(width: 44, height: 44)
                    .contentShape(Circle())
            }
            .buttonStyle(.plain)
            .disabled(!canType)
            .accessibilityLabel("Type instead")
            .accessibilityIdentifier("controls.type")
        }
        .padding(.horizontal, 7)
        .frame(minHeight: height)
        .capsuleSurface()
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("voice.capsule")
    }

    // MARK: - Mic

    @ViewBuilder
    private var micSlot: some View {
        let visible = mode.showsMic && model.muteAvailability != .hidden
        CapsuleMicButton(muted: muted, level: meter.micLevel, noSignal: noSignal, enabled: model.muteAvailability == .enabled, action: onMute)
            .opacity(visible ? (mode.micDimmed ? 0.35 : 1) : 0)
            .allowsHitTesting(visible && model.muteAvailability == .enabled)
            .accessibilityHidden(!visible)
            .accessibilityIdentifier("controls.mute")
    }

    // MARK: - Orb

    @ViewBuilder
    private var orb: some View {
        if model.orbAction == .holdToTalk {
            CapsuleOrb(mode: mode)
                .holdToTalk(holding: holding, onPress: onPress, onRelease: onRelease)
                .accessibilityIdentifier("controls.orb")
        } else {
            Button(action: tapOrb) {
                CapsuleOrb(mode: mode)
            }
            // Disabled while it has nothing to do, but never dimmed: the
            // orb's colour is the state.
            .buttonStyle(UndimmedButtonStyle())
            .disabled(model.orbAction == .none)
            .accessibilityLabel(orbLabel)
            .accessibilityIdentifier("controls.orb")
        }
    }

    private func tapOrb() {
        switch model.orbAction {
        case .connect: onConnect()
        case .stop: onStop()
        case .resume: onResume()
        case .holdToTalk, .none: break
        }
    }

    private var orbLabel: String {
        switch mode {
        case .offline: return "Connect"
        case .speaking, .thinking, .stalled: return "Stop \(name)"
        case .paused: return "Resume audio"
        default: return state.accessibilityLabel(voiceName: name)
        }
    }

    // MARK: - Name and status

    /// The name row and the status row. Drawn twice: once as the menu's
    /// label with the status invisible, once over it with only the status.
    private func nameAndStatus(showsName: Bool) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            HStack(spacing: 4) {
                Text(name)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(Theme.ink)
                    .lineLimit(1)
                Image(systemName: "chevron.up.chevron.down")
                    .font(.system(size: 10, weight: .bold))
                    .foregroundStyle(Theme.ink)
                    .accessibilityHidden(true)
            }
            .opacity(showsName ? 1 : 0)
            .accessibilityHidden(!showsName)
            HStack(alignment: .firstTextBaseline, spacing: 5) {
                Circle()
                    .fill(dotColor)
                    .frame(width: 6, height: 6)
                    .alignmentGuide(.firstTextBaseline) { $0[.bottom] - 1 }
                Text(statusText)
                    .font(.caption)
                    .foregroundStyle(statusColor)
                    .lineLimit(dynamicTypeSize.isAccessibilitySize ? 2 : 1)
                    .minimumScaleFactor(0.85)
                    .accessibilityIdentifier("controls.statusLine")
            }
            .opacity(showsName ? 0 : 1)
            .accessibilityHidden(showsName)
        }
        .padding(.horizontal, 8)
        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
        .contentShape(RoundedRectangle(cornerRadius: 12))
    }

    private var dotColor: Color {
        if noSignal, mode == .live || mode == .quiet { return Theme.blocked }
        switch mode {
        case .live, .quiet, .holding: return Theme.done
        case .muted: return Theme.danger
        case .speaking: return Theme.working
        case .thinking: return Theme.thinking
        case .stalled, .paused, .noMicrophone, .reconnecting: return Theme.blocked
        case .offline, .voiceOff, .hold: return Theme.unknown
        }
    }

    private var statusColor: Color {
        if noSignal, mode == .live || mode == .quiet { return Theme.blockedText }
        switch mode {
        case .muted: return Theme.danger
        case .stalled, .paused, .noMicrophone, .reconnecting: return Theme.blockedText
        case .holding: return Theme.done
        default: return Theme.muted
        }
    }
}

/// A button that only dims its label while pressed, never when disabled.
struct UndimmedButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label.opacity(configuration.isPressed ? 0.7 : 1)
    }
}

/// The capsule's surface: Liquid Glass, or a solid card with a hairline
/// under Reduce Transparency. The composer uses the same one.
private struct CapsuleSurface: ViewModifier {
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency

    func body(content: Content) -> some View {
        if reduceTransparency {
            content
                .background(Capsule().fill(Theme.card))
                .overlay(Capsule().strokeBorder(Theme.line, lineWidth: 1))
                .shadow(color: .black.opacity(0.12), radius: 12, y: 8)
        } else {
            content
                .glassEffect(.regular, in: .capsule)
        }
    }
}

extension View {
    func capsuleSurface() -> some View { modifier(CapsuleSurface()) }
}

/// The capsule's orb, 44 pt: colour, glyph and badge per `CapsuleMode`.
/// The listening orb breathes (1 to 1.04, 2 s) unless Reduce Motion is on.
struct CapsuleOrb: View {
    let mode: CapsuleMode
    var size: CGFloat = 44

    @State private var breathe = false
    @State private var spin = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var fill: Color {
        switch mode {
        case .live: return .accentColor
        case .speaking: return Theme.workingSoft
        case .thinking: return Theme.thinkingSoft
        case .stalled, .paused, .reconnecting: return Theme.accentSoft
        case .holding: return Theme.doneSoft
        case .quiet, .muted, .offline, .voiceOff, .hold, .noMicrophone: return Theme.fill
        }
    }

    private var glyphColor: Color {
        switch mode {
        case .live: return Theme.onAccent
        case .speaking: return Theme.working
        case .thinking: return Theme.thinking
        case .stalled, .paused, .reconnecting: return Theme.blockedText
        case .holding: return Theme.done
        case .muted, .voiceOff, .noMicrophone: return Theme.muted
        case .quiet, .offline, .hold: return Theme.ink
        }
    }

    private var stroke: (Color, CGFloat)? {
        switch mode {
        case .live: return nil
        case .speaking: return (Theme.working, 1.5)
        case .thinking: return (Theme.thinking, 1.5)
        case .stalled, .paused, .reconnecting: return (Theme.blocked, 1.5)
        case .holding: return nil
        default: return (Theme.line, 1)
        }
    }

    private var symbol: String {
        switch mode {
        case .live, .quiet, .speaking, .muted, .hold, .holding: return "waveform"
        case .thinking: return "ellipsis"
        case .stalled: return "hourglass"
        case .paused: return "play.fill"
        case .noMicrophone, .voiceOff: return "mic.slash"
        case .reconnecting: return "arrow.clockwise"
        case .offline: return "wifi.slash"
        }
    }

    var body: some View {
        ZStack {
            Circle().fill(fill)
            if let stroke {
                Circle().strokeBorder(stroke.0, lineWidth: stroke.1)
            }
            Image(systemName: symbol)
                .font(.system(size: size * 0.44, weight: .semibold))
                .foregroundStyle(glyphColor)
                .rotationEffect(.degrees(mode == .reconnecting && spin ? 360 : 0))
                .contentTransition(.symbolEffect(.replace))
        }
        .frame(width: size, height: size)
        // The cream sticker ring, with a hairline outside it.
        .overlay {
            if mode == .live {
                Circle().stroke(Theme.cream, lineWidth: 3).padding(-1.5)
                Circle().stroke(Theme.line, lineWidth: 1).padding(-3.5)
            }
            if mode == .holding {
                Circle().stroke(Theme.done, lineWidth: 3).padding(-1.5)
            }
        }
        .overlay(alignment: .bottomTrailing) {
            if mode.showsStop {
                ZStack {
                    Circle().fill(Theme.ink)
                    RoundedRectangle(cornerRadius: 1.5).fill(Theme.card).frame(width: 7, height: 7)
                }
                .frame(width: 22, height: 22)
                .overlay(Circle().stroke(Theme.card, lineWidth: 2))
                .offset(x: 5, y: 5)
                .accessibilityHidden(true)
            }
        }
        .overlay(alignment: .topTrailing) {
            if mode == .noMicrophone {
                Text("!")
                    .font(.system(size: 12, weight: .heavy))
                    .foregroundStyle(Theme.onAccent)
                    .frame(width: 20, height: 20)
                    .background(Circle().fill(Theme.blocked))
                    .overlay(Circle().stroke(Theme.card, lineWidth: 2))
                    .offset(x: 5, y: -5)
                    .accessibilityHidden(true)
            }
        }
        .scaleEffect(mode == .holding ? 1.08 : (mode == .live && breathe ? 1.04 : 1))
        .animation(Theme.ease(reduceMotion: reduceMotion), value: mode)
        .onAppear(perform: animate)
        .onChange(of: mode) { _, _ in animate() }
    }

    private func animate() {
        guard !reduceMotion else {
            breathe = false
            spin = false
            return
        }
        if mode == .live {
            breathe = false
            withAnimation(.easeInOut(duration: 2).repeatForever(autoreverses: true)) { breathe = true }
        } else {
            withAnimation(nil) { breathe = false }
        }
        if mode == .reconnecting {
            spin = false
            withAnimation(.linear(duration: 1.2).repeatForever(autoreverses: false)) { spin = true }
        } else {
            withAnimation(nil) { spin = false }
        }
    }
}

private extension View {
    /// Press and hold to talk, release to send; VoiceOver gets a toggle.
    func holdToTalk(holding: Bool, onPress: @escaping () -> Void, onRelease: @escaping () -> Void) -> some View {
        modifier(HoldToTalk(holding: holding, onPress: onPress, onRelease: onRelease))
    }
}

private struct HoldToTalk: ViewModifier {
    let holding: Bool
    let onPress: () -> Void
    let onRelease: () -> Void
    @GestureState private var pressed = false

    func body(content: Content) -> some View {
        content
            .contentShape(Circle())
            .gesture(
                DragGesture(minimumDistance: 0)
                    .updating($pressed) { _, pressed, _ in pressed = true }
            )
            .onChange(of: pressed) { _, isPressed in
                isPressed ? onPress() : onRelease()
            }
            .accessibilityElement()
            .accessibilityAddTraits(.isButton)
            .accessibilityLabel(holding ? "Talking. Double-tap to send" : "Hold to talk")
            .accessibilityAction { holding ? onRelease() : onPress() }
    }
}

/// The mic button, 44 pt. Live: the mic in a well with a green ring that
/// follows the input level. Muted: the one loud red control. No signal: an
/// amber ring.
struct CapsuleMicButton: View {
    let muted: Bool
    let level: Float
    var noSignal = false
    let enabled: Bool
    let action: () -> Void

    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var ringFraction: CGFloat {
        guard enabled, !muted, !noSignal else { return 1 }
        return CGFloat(max(0.25, min(1, sqrt(max(0, level)) * 1.8)))
    }

    var body: some View {
        Button(action: action) {
            ZStack {
                Circle().fill(muted ? Theme.danger : Theme.fill)
                if !muted {
                    Circle()
                        .trim(from: 0, to: ringFraction)
                        .stroke(noSignal ? Theme.blocked : Theme.done, style: StrokeStyle(lineWidth: 2.5, lineCap: .round))
                        .rotationEffect(.degrees(-90))
                        .padding(1.25)
                        .animation(reduceMotion ? nil : .easeOut(duration: 0.12), value: ringFraction)
                }
                Image(systemName: muted ? "mic.slash.fill" : "mic.fill")
                    .font(.system(size: 18, weight: .medium))
                    .foregroundStyle(muted ? Color.white : Theme.ink)
            }
            .frame(width: 44, height: 44)
        }
        .buttonStyle(.plain)
        .disabled(!enabled)
        .accessibilityLabel(VoiceBarModel.muteLabel(muted: muted))
        .accessibilityValue(muted ? "Muted" : (noSignal ? "No microphone signal" : "Listening"))
        .accessibilityHint(SessionViewModel.muteHint)
    }
}

/// The capsule as the composer: the quiet orb (back to voice), the text
/// field and the ink send button. What was typed goes to the daemon as if
/// spoken (`POST /api/utterance`).
struct CapsuleComposer: View {
    let voiceName: String
    let sending: Bool
    let error: String?
    let send: (String) async -> Bool
    let close: () -> Void

    @State private var text = ""
    @FocusState private var focused: Bool
    @ScaledMetric(relativeTo: .body) private var scaledHeight: CGFloat = 60

    private var canSend: Bool { !sending && !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if let error {
                Text(error)
                    .font(.caption)
                    .foregroundStyle(Theme.danger)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.horizontal, 16)
                    .accessibilityIdentifier("type.error")
            }
            HStack(spacing: 8) {
                Button(action: close) {
                    CapsuleOrb(mode: .quiet)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Back to voice")
                .accessibilityIdentifier("type.close")
                TextField("Message to \(voiceName)", text: $text, axis: .vertical)
                    .font(.body)
                    .foregroundStyle(Theme.ink)
                    .lineLimit(1 ... 4)
                    .textInputAutocapitalization(.sentences)
                    .submitLabel(.send)
                    .focused($focused)
                    .onSubmit(submit)
                    .accessibilityLabel("Message to \(voiceName)")
                    .accessibilityIdentifier("type.field")
                Button(action: submit) {
                    ZStack {
                        Circle().fill(canSend ? Theme.ink : Theme.line)
                        if sending {
                            ProgressView().tint(Theme.surface)
                        } else {
                            Image(systemName: "arrow.up")
                                .font(.system(size: 17, weight: .bold))
                                .foregroundStyle(Theme.surface)
                        }
                    }
                    .frame(width: 44, height: 44)
                }
                .buttonStyle(.plain)
                .disabled(!canSend)
                .accessibilityLabel("Send")
                .accessibilityIdentifier("type.send")
            }
            .padding(.horizontal, 7)
            .padding(.vertical, 8)
            .frame(minHeight: min(max(scaledHeight, 60), 100))
            .capsuleSurface()
        }
        .onAppear { focused = true }
    }

    private func submit() {
        guard canSend else { return }
        let sent = text
        Task {
            if await send(sent) {
                text = ""
                focused = false
            }
        }
    }
}
