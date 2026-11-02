import SheepdogKit
import SwiftUI

/// The floating voice capsule (canvas "Dock"): the mic (mute) button, the
/// orb, the name and status — a button that opens the voice menu — and the
/// keyboard, which morphs the capsule into the composer.
///
/// Every control sits in a fixed 44 pt slot, so a hidden one never shifts
/// the others. The capsule grows with the text size up to a cap. At the
/// accessibility sizes it stacks: the controls on one row, the name and
/// the status (which wraps at word boundaries) full width below. It is Liquid
/// Glass, or a solid card under Reduce Transparency. The look comes from
/// `CapsuleMode`; what the orb does from `VoiceBarModel`.
struct VoiceCapsule<MenuValue: Equatable, VoiceMenu: View>: View {
    let state: SessionState
    /// Read only by the orb and the mic ring (`MeteredOrb`,
    /// `MeteredMicButton`): the capsule does not re-render per level tick.
    let meter: VoiceMeter
    let muted: Bool
    let name: String
    var talkMode: TalkMode = .openMic
    var holding = false
    var noSignal = false
    /// A question is open: the orb goes quiet so the answer has the accent.
    var questionOpen = false
    var statusText = ""
    /// Where the sound goes while audio runs (speaker, headphones, car):
    /// a glyph after the status, which VoiceOver announces when it changes.
    var route: AudioRoute?
    var canType = true
    /// The link says the computer cannot be reached: "no session" then
    /// reads as offline (retry), not as stopped.
    var computerUnreachable = false
    /// Another window holds the audio (iPad): this capsule offers "Use
    /// here" in place of the keyboard.
    var audioElsewhere = false
    /// "In another window", or who holds the daemon's voice ("Voice on Mac").
    var elsewhereLabel = "In another window"
    let onConnect: () -> Void
    let onStop: () -> Void
    let onMute: () -> Void
    var onResume: () -> Void = {}
    var onPress: () -> Void = {}
    var onRelease: () -> Void = {}
    var onType: () -> Void = {}
    /// "Use here": move the voice session to this window.
    var onUseHere: () -> Void = {}
    /// What the voice menu shows, and its items drawn from it: the menu
    /// re-renders only when this (or the name and status) changes
    /// (`StableMenu`).
    let menuValue: MenuValue
    @ViewBuilder let menu: (MenuValue) -> VoiceMenu

    @ScaledMetric(relativeTo: .subheadline) private var scaledHeight: CGFloat = 60
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    /// The row's own height, measured.
    @State private var rowHeight: CGFloat = 0

    private var mode: CapsuleMode {
        CapsuleMode.resolve(
            state: state, talkMode: talkMode, holding: holding, questionOpen: questionOpen,
            computerUnreachable: computerUnreachable, audioElsewhere: audioElsewhere
        )
    }
    private var model: VoiceBarModel { VoiceBarModel(state: state, talkMode: talkMode, audioElsewhere: audioElsewhere) }
    /// The status the capsule shows: the mode's own words where the state
    /// cannot say them (stopped, in another window).
    private var shownStatus: String {
        if mode == .elsewhere { return elsewhereLabel }
        return mode.statusOverride ?? statusText
    }
    private var stacked: Bool { dynamicTypeSize.isAccessibilitySize }
    private var height: CGFloat { min(max(scaledHeight, 60), cap) }
    /// Past this the capsule would cover the conversation it serves: the
    /// name and status scroll inside it instead. The stacked layout has
    /// two rows, so it gets more room.
    private var cap: CGFloat { stacked ? 240 : 150 }

    var body: some View {
        // As tall as its text up to the cap, then the row scrolls inside.
        // Scrolling is off while it fits, so nothing delays the orb's
        // press (hold to talk).
        ScrollView {
            row
                .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { h in
                    let rounded = h.rounded(.up)
                    if rounded != rowHeight { rowHeight = rounded }
                }
        }
        .scrollDisabled(rowHeight <= cap)
        .scrollBounceBehavior(.basedOnSize)
        .scrollIndicators(rowHeight > cap ? .visible : .hidden)
        .frame(height: min(max(rowHeight, height), cap))
        .padding(.horizontal, 7)
        .capsuleSurface()
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("voice.capsule")
    }

    private var row: some View {
        Group {
            if stacked {
                VStack(spacing: 2) {
                    HStack(spacing: 8) {
                        micSlot
                        orb
                        Spacer(minLength: 0)
                        trailingButton
                    }
                    voiceMenu
                }
                // Clear of the capsule's rounded ends.
                .padding(.horizontal, 16)
                .padding(.vertical, 12)
            } else {
                HStack(spacing: 8) {
                    micSlot
                    orb
                    voiceMenu
                    trailingButton
                }
            }
        }
        .frame(minHeight: height)
    }

    private var voiceMenu: some View {
        // The status is drawn over the menu button rather than inside
        // it, so VoiceOver (and the UI tests) read it as its own text;
        // touches pass through it to the menu.
        stableMenu(
            CapsuleMenuLabel(items: menuValue, name: name, status: shownStatus, mode: mode, noSignal: noSignal, size: dynamicTypeSize),
            probe: "voice",
            content: { menu($0.items) },
            label: { _ in nameAndStatus(showsName: true) }
        )
            .menuOrder(.fixed)
            .overlay { nameAndStatus(showsName: false).allowsHitTesting(false) }
            .accessibilityLabel("Voice: \(name)")
            .accessibilityHint("Shows the connection and End voice session")
            .accessibilityIdentifier("voice.menu")
    }

    /// The keyboard, or "Use here" in a window that does not hold the audio.
    @ViewBuilder
    private var trailingButton: some View {
        if mode.showsUseHere {
            Button(action: onUseHere) {
                Text("Use here")
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(Theme.ink)
                    .padding(.horizontal, 12)
                    .frame(minWidth: 44, minHeight: 44)
                    .contentShape(Capsule())
            }
            .buttonStyle(.plain)
            .accessibilityHint("Moves the voice session to this window")
            .accessibilityIdentifier("controls.useHere")
        } else {
            typeButton
        }
    }

    private var typeButton: some View {
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

    // MARK: - Mic

    @ViewBuilder
    private var micSlot: some View {
        let visible = mode.showsMic && model.muteAvailability != .hidden
        MeteredMicButton(meter: meter, muted: muted, noSignal: noSignal, enabled: model.muteAvailability == .enabled, action: onMute)
            .opacity(visible ? (mode.micDimmed ? 0.35 : 1) : 0)
            .allowsHitTesting(visible && model.muteAvailability == .enabled)
            .accessibilityHidden(!visible)
            .accessibilityIdentifier("controls.mute")
    }

    // MARK: - Orb

    @ViewBuilder
    private var orb: some View {
        if model.orbAction == .holdToTalk {
            MeteredOrb(meter: meter, mode: mode)
                .holdToTalk(holding: holding, onPress: onPress, onRelease: onRelease)
                .accessibilityIdentifier("controls.orb")
        } else {
            Button(action: tapOrb) {
                MeteredOrb(meter: meter, mode: mode)
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
        case .stopped, .stoppedQuiet: return "Start talking"
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
                    .lineLimit(dynamicTypeSize.isAccessibilitySize ? 2 : 1)
                    .minimumScaleFactor(dynamicTypeSize.isAccessibilitySize ? 0.7 : 1)
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
                Text(shownStatus)
                    .font(.caption)
                    .foregroundStyle(statusColor)
                    // Wraps at the accessibility sizes; never cut there.
                    .lineLimit(dynamicTypeSize.isAccessibilitySize ? nil : 1)
                    .fixedSize(horizontal: false, vertical: dynamicTypeSize.isAccessibilitySize)
                    .minimumScaleFactor(dynamicTypeSize.isAccessibilitySize ? 1 : 0.85)
                    .accessibilityIdentifier("controls.statusLine")
                if let route {
                    Image(systemName: route.symbol)
                        .font(.caption)
                        .foregroundStyle(Theme.muted)
                        .accessibilityLabel("Audio plays through \(route.title)")
                        .accessibilityIdentifier("controls.route")
                }
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
        case .offline, .voiceOff, .hold, .stopped, .stoppedQuiet, .elsewhere: return Theme.unknown
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
/// What the capsule's menu button shows, with the menu's own value: the
/// label's inputs (`StableMenu`).
struct CapsuleMenuLabel<Items: Equatable>: Equatable {
    var items: Items
    var name: String
    var status: String
    var mode: CapsuleMode
    var noSignal: Bool
    var size: DynamicTypeSize
}

/// The orb, reading the voice's output level itself: the one view that
/// re-renders on each level tick.
struct MeteredOrb: View {
    let meter: VoiceMeter
    let mode: CapsuleMode
    var size: CGFloat = 44

    var body: some View {
        CapsuleOrb(mode: mode, size: size, outputLevel: meter.outputLevel)
    }
}

/// The mic button, reading the input level itself (its ring).
struct MeteredMicButton: View {
    let meter: VoiceMeter
    let muted: Bool
    var noSignal = false
    let enabled: Bool
    let action: () -> Void

    var body: some View {
        CapsuleMicButton(muted: muted, level: meter.micLevel, noSignal: noSignal, enabled: enabled, action: action)
    }
}

struct CapsuleOrb: View {
    let mode: CapsuleMode
    var size: CGFloat = 44
    /// The voice's output level while it speaks: the waveform follows it.
    var outputLevel: Float = 0

    @State private var breathe = false
    @State private var spin = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var fill: Color {
        switch mode {
        case .live, .stopped: return .accentColor
        case .speaking: return Theme.workingSoft
        case .thinking: return Theme.thinkingSoft
        case .stalled, .paused, .reconnecting: return Theme.accentSoft
        case .holding: return Theme.doneSoft
        case .quiet, .muted, .offline, .voiceOff, .hold, .noMicrophone, .stoppedQuiet, .elsewhere: return Theme.fill
        }
    }

    private var glyphColor: Color {
        switch mode {
        case .live, .stopped: return Theme.onAccent
        case .stoppedQuiet: return .accentColor
        case .speaking: return Theme.working
        case .thinking: return Theme.thinking
        case .stalled, .paused, .reconnecting: return Theme.blockedText
        case .holding: return Theme.done
        case .muted, .voiceOff, .noMicrophone, .elsewhere: return Theme.muted
        case .quiet, .offline, .hold: return Theme.ink
        }
    }

    private var stroke: (Color, CGFloat)? {
        switch mode {
        case .live, .stopped: return nil
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
        case .stopped, .stoppedQuiet, .elsewhere: return "mic.fill"
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
            if mode == .speaking, !reduceMotion {
                SpeakingWave(level: outputLevel, color: glyphColor, height: size * 0.44)
                    .transition(.opacity)
            } else {
                Image(systemName: symbol)
                    .font(.system(size: size * 0.44, weight: .semibold))
                    .foregroundStyle(glyphColor)
                    .rotationEffect(.degrees(mode == .reconnecting && spin ? 360 : 0))
                    .contentTransition(.symbolEffect(.replace))
                    .transition(.opacity)
            }
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
        guard !reduceMotion, !debugNoAnimations else {
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
        .accessibilityHint(VoiceEngine.muteHint)
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
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

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
                    .lineLimit(1 ... (dynamicTypeSize.isAccessibilitySize ? 2 : 4))
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

/// The speaking orb's glyph: four bars that follow the voice's output
/// level (one easing, 200 ms). Under Reduce Motion the orb keeps the still
/// waveform symbol instead.
private struct SpeakingWave: View {
    let level: Float
    let color: Color
    let height: CGFloat

    private static let shape: [CGFloat] = [0.55, 1, 0.8, 0.45]

    var body: some View {
        HStack(spacing: height * 0.14) {
            ForEach(Array(Self.shape.enumerated()), id: \.offset) { _, factor in
                Capsule()
                    .fill(color)
                    .frame(width: height * 0.13, height: max(height * 0.18, height * factor * CGFloat(0.3 + 0.7 * min(1, level * 2.2))))
            }
        }
        .frame(height: height)
        .animation(Theme.ease, value: level)
        .accessibilityHidden(true)
    }
}
