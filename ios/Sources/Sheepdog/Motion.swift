import SheepdogKit
import SwiftUI

// The motion pass (app-redesign.md 1.2 "Motion"): one easing,
// cubic-bezier(.2, .8, .2, 1) over 200 ms (`Theme.ease`), and under Reduce
// Motion every movement becomes a fade.

// MARK: - Shared namespace

private struct MotionNamespaceKey: EnvironmentKey {
    static let defaultValue: Namespace.ID? = nil
}

extension EnvironmentValues {
    /// The main screen's namespace: a floating question collapses into its
    /// conversation row, and an agent window zooms out of the chip or card
    /// it was opened from.
    var motionNamespace: Namespace.ID? {
        get { self[MotionNamespaceKey.self] }
        set { self[MotionNamespaceKey.self] = newValue }
    }
}

extension View {
    /// `matchedGeometryEffect` in the main screen's namespace, when there
    /// is one and Reduce Motion is off.
    func matchedInMotion(id: String, isSource: Bool = true) -> some View {
        modifier(MatchedInMotion(id: id, isSource: isSource))
    }

    /// A zoom source for the agent window (`navigationTransition(.zoom)`).
    func zoomSource(_ paneID: String) -> some View {
        modifier(ZoomSource(id: paneID))
    }

    /// One check flash when the agent turns done; no loop.
    func doneFlash(_ state: Agent.State) -> some View {
        modifier(DoneFlash(state: state))
    }
}

private struct MatchedInMotion: ViewModifier {
    let id: String
    let isSource: Bool
    @Environment(\.motionNamespace) private var namespace
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    func body(content: Content) -> some View {
        if let namespace, !reduceMotion {
            content.matchedGeometryEffect(id: "question." + id, in: namespace, isSource: isSource)
        } else {
            content
        }
    }
}

private struct ZoomSource: ViewModifier {
    let id: String
    @Environment(\.motionNamespace) private var namespace

    func body(content: Content) -> some View {
        if let namespace {
            content.matchedTransitionSource(id: "window." + id, in: namespace)
        } else {
            content
        }
    }
}

/// A check that pops in over the view and fades once when `state` turns
/// `.done` (a fade only under Reduce Motion).
private struct DoneFlash: ViewModifier {
    let state: Agent.State
    @State private var flashing = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    func body(content: Content) -> some View {
        content
            .overlay {
                if flashing {
                    Image(systemName: "checkmark.circle.fill")
                        .font(.footnote.weight(.semibold))
                        .foregroundStyle(Theme.done)
                        .background(Circle().fill(Theme.card).padding(1))
                        .transition(reduceMotion ? .opacity : .scale(scale: 0.4).combined(with: .opacity))
                        .accessibilityHidden(true)
                }
            }
            .onChange(of: state) { old, new in
                guard new == .done, old != .done else { return }
                withAnimation(Theme.ease(reduceMotion: reduceMotion)) { flashing = true }
                Task { @MainActor in
                    try? await Task.sleep(for: .milliseconds(900))
                    withAnimation(Theme.ease(reduceMotion: reduceMotion)) { flashing = false }
                }
            }
    }
}
