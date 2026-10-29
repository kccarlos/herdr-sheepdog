import SheepdogKit
import SwiftUI

/// What floats at the foot of a screen, the same on iPhone and iPad: the
/// open questions, the first-run coach mark and the voice capsule, which
/// the keyboard button turns into the composer (`CapsuleComposer`). One
/// implementation: the iPhone puts it in a bottom safe-area inset across
/// the screen, the iPad in the detail's, at the capsule's own width
/// (`IPadDock`).
struct FloatingDock: View {
    let menus: SessionMenus
    /// The screen's height above the keyboard (`FloatingQuestions.area`).
    let questionArea: CGFloat
    /// The open questions float above the capsule. Off where each sits on
    /// its agent's card instead (the Agents pane).
    let showsQuestions: Bool

    @EnvironmentObject private var model: SessionViewModel
    @Environment(SceneUI.self) private var ui
    private let scenes = SceneCoordinator.shared
    @Environment(\.sceneID) private var sceneID
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.motionNamespace) private var motionNamespace
    @Namespace private var fallbackNamespace

    private var motion: Namespace.ID { motionNamespace ?? fallbackNamespace }

    var body: some View {
        VStack(spacing: 8) {
            // Every open question floats above the capsule.
            let cards = showsQuestions ? model.floatingQuestions : []
            if !cards.isEmpty {
                FloatingQuestions(
                    cards: cards,
                    voiceQuestionID: model.approval?.pending.id,
                    time: { question in
                        model.flock.all.first { $0.paneID == question.paneID }?.timeInState(at: DemoClock.now())
                    },
                    area: questionArea,
                    remote: { model.remoteQuestion($0) },
                    talkTo: { model.talk(to: $0) }
                ) { id, index in
                    model.answer(question: id, option: index)
                }
                .transition(reduceMotion ? .opacity : .move(edge: .bottom).combined(with: .opacity))
            }

            // A window that does not hold the audio shows no coach mark and
            // no composer: its capsule says "In another window".
            if scenes.isOwner(sceneID), model.voice.showCoachMark {
                CoachMarkView(text: VoiceBarModel.coachMark(voiceName: model.voiceName)) {
                    model.voice.dismissCoachMark()
                }
                .padding(.horizontal, 4)
                .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                .transition(reduceMotion ? .opacity : .move(edge: .bottom).combined(with: .opacity))
                .onAppear {
                    AccessibilityNotification.Announcement(VoiceBarModel.coachMark(voiceName: model.voiceName)).post()
                }
            }

            ZStack {
                if ui.typing, scenes.isOwner(sceneID) {
                    CapsuleComposer(
                        voiceName: model.voiceName,
                        sending: model.conversationModel.sendingTyped,
                        error: model.conversationModel.typedError,
                        send: { await model.sendTyped($0) },
                        close: {
                            withAnimation(Theme.ease(reduceMotion: reduceMotion)) { ui.typing = false }
                            model.conversationModel.typedError = nil
                        }
                    )
                    .modifier(MorphSurface(namespace: motion, reduceMotion: reduceMotion))
                    .transition(.opacity)
                } else {
                    // Grows with the text at every size; the capsule caps
                    // its own height and scrolls inside past it.
                    SessionCapsule(menus: menus)
                        .modifier(MorphSurface(namespace: motion, reduceMotion: reduceMotion))
                        .transition(.opacity)
                }
            }
        }
    }
}

/// The dock on iPad: one capsule per window, `CapsuleFrame.width` wide,
/// centred in the content area and so never over the sidebar. It slides
/// to the new centre when the sidebar shows or hides or the window
/// resizes (the brand ease; under Reduce Motion it jumps).
struct IPadDock: View {
    let menus: SessionMenus
    let questionArea: CGFloat
    let showsQuestions: Bool
    /// The sidebar lies over this detail (portrait, shown). Beside it, the
    /// detail already excludes the sidebar's width.
    let sidebarOverlays: Bool

    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    /// The detail's width, measured.
    @State private var detailWidth: CGFloat = 0

    var body: some View {
        let content = CapsuleFrame.content(window: detailWidth, sidebarShown: sidebarOverlays)
        // Before the first measure the capsule has its usual width, not 0.
        let width = detailWidth > 0 ? CapsuleFrame.width(content: content) : CapsuleFrame.width
        let shift = CapsuleFrame.centerX(window: detailWidth, sidebarShown: sidebarOverlays) - detailWidth / 2
        FloatingDock(menus: menus, questionArea: questionArea, showsQuestions: showsQuestions)
            .frame(width: width)
            .frame(maxWidth: .infinity)
            .onGeometryChange(for: CGFloat.self) { $0.size.width.rounded() } action: { detailWidth = $0 }
            .offset(x: shift)
            .animation(reduceMotion ? nil : Theme.ease, value: shift)
            .animation(reduceMotion ? nil : Theme.ease, value: width)
            .padding(.bottom, 4)
    }
}
