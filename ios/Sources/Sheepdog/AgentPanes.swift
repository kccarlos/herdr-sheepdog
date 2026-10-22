import CoreTransferable
import SheepdogKit
import SwiftUI
import UniformTypeIdentifiers

extension UTType {
    /// An agent dragged from the iPad sidebar (declared in project.yml).
    static let sheepdogAgent = UTType(exportedAs: "com.github.kccarlos.herdr-sheepdog-ios.agent")
}

/// An agent's window as a value: what Open in New Window opens (the
/// "agent" `WindowGroup`) and what a sidebar row drags.
extension AgentWindowRoute: Transferable {
    static var transferRepresentation: some TransferRepresentation {
        CodableRepresentation(contentType: .sheepdogAgent)
    }
}

/// The iPad detail's agent windows (canvas "iPad — two agents side by
/// side"): one pane, or two with a divider that drags. Two sit side by side
/// in landscape and stack in portrait; the focused one gets a 3 pt ink
/// line. Dragging an agent from the sidebar shows a drop target: dropped,
/// it opens beside the current pane (or replaces the other one of two).
struct AgentPanes: View {
    let panes: [String]
    let leading: AnyView?
    @EnvironmentObject private var model: SessionViewModel
    @EnvironmentObject private var ui: SceneUI
    @Environment(\.supportsMultipleWindows) private var supportsMultipleWindows
    @Environment(\.openWindow) private var openWindow
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var dropTargeted = false

    var body: some View {
        GeometryReader { proxy in
            let side = proxy.size.width > proxy.size.height
            Group {
                if panes.count == 2 {
                    let total = (side ? proxy.size.width : proxy.size.height) - PaneDivider.thickness
                    let first = (total * ui.splitFraction).rounded()
                    if side {
                        HStack(spacing: 0) {
                            pane(0).frame(width: first)
                            PaneDivider(fraction: $ui.splitFraction, total: total, horizontal: true)
                            pane(1)
                        }
                    } else {
                        VStack(spacing: 0) {
                            pane(0).frame(height: first)
                            PaneDivider(fraction: $ui.splitFraction, total: total, horizontal: false)
                            pane(1)
                        }
                    }
                } else if let only = panes.first {
                    pane(0)
                        .id(only)
                }
            }
            .overlay { if dropTargeted { dropTarget(side: side) } }
        }
        .dropDestination(for: AgentWindowRoute.self) { routes, _ in
            guard let route = routes.first, model.flock.all.contains(where: { $0.paneID == route.paneID }) else { return false }
            withAnimation(Theme.ease(reduceMotion: reduceMotion)) { ui.beside(route.paneID) }
            return true
        } isTargeted: { targeted in
            withAnimation(Theme.ease(reduceMotion: reduceMotion)) { dropTargeted = targeted }
        }
    }

    @ViewBuilder
    private func pane(_ index: Int) -> some View {
        let id = panes[index]
        if let agent = model.flock.all.first(where: { $0.paneID == id }) {
            let two = panes.count == 2
            AgentScreenView(agent: agent, pane: PaneChrome(
                // Show sidebar and the mini capsule sit in the first pane.
                leading: index == 0 ? leading : nil,
                onClose: two ? { withAnimation(Theme.ease(reduceMotion: reduceMotion)) { ui.close(pane: index) } } : nil,
                splitCandidates: two ? [] : model.flock.running.filter { $0.paneID != id },
                onSplit: two ? nil : { other in withAnimation(Theme.ease(reduceMotion: reduceMotion)) { ui.beside(other) } },
                focused: two && ui.focusedPane == index,
                onGone: { ui.close(pane: index) },
                onSwitch: { ui.replace(pane: index, with: $0) },
                extraMenu: supportsMultipleWindows ? AnyView(
                    Button {
                        openWindow(value: AgentWindowRoute(paneID: id))
                    } label: {
                        Label("Open in New Window", systemImage: "macwindow.badge.plus")
                    }
                    .accessibilityIdentifier("agentScreen.menu.newWindow")
                ) : nil,
                onInteract: two ? { if ui.focusedPane != index { ui.focusedPane = index } } : nil
            ))
            .id(id)
        } else {
            IPadAgentGone(leading: index == 0 ? leading : nil) { ui.close(pane: index) }
        }
    }

    /// Where a dragged agent lands: the other half with one pane, the
    /// pane that is not focused with two.
    private func dropTarget(side: Bool) -> some View {
        let words = panes.count == 2 ? "Drop to show it here" : "Drop to show it beside"
        let shape = RoundedRectangle(cornerRadius: Theme.Radius.large, style: .continuous)
        let label = Label(words, systemImage: "rectangle.split.2x1")
            .font(.headline)
            .foregroundStyle(Theme.ink)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(shape.fill(Color.accentColor.opacity(0.12)))
            .overlay(shape.strokeBorder(Color.accentColor, style: StrokeStyle(lineWidth: 2, dash: [8, 6])))
            .padding(12)
        let otherFirst = panes.count == 2 && ui.focusedPane == 1
        return Group {
            if side {
                HStack(spacing: 0) {
                    if !otherFirst { Color.clear }
                    label
                    if otherFirst { Color.clear }
                }
            } else {
                VStack(spacing: 0) {
                    if !otherFirst { Color.clear }
                    label
                    if otherFirst { Color.clear }
                }
            }
        }
        .allowsHitTesting(false)
        .accessibilityIdentifier("panes.dropTarget")
    }
}

/// The line between two panes, with a grip. Drag it (or, with VoiceOver,
/// swipe up and down) to share the room; each pane keeps at least 30 %.
struct PaneDivider: View {
    @Binding var fraction: CGFloat
    let total: CGFloat
    let horizontal: Bool

    static let thickness: CGFloat = 2
    static let range: ClosedRange<CGFloat> = 0.3 ... 0.7

    @State private var start: CGFloat?

    var body: some View {
        Rectangle()
            .fill(Theme.line)
            .frame(width: horizontal ? Self.thickness : nil, height: horizontal ? nil : Self.thickness)
            .overlay {
                Capsule()
                    .fill(Theme.muted.opacity(0.45))
                    .frame(width: horizontal ? 6 : 40, height: horizontal ? 40 : 6)
            }
            .overlay {
                // A 24 pt grab area around the 2 pt line.
                Color.clear
                    .frame(width: horizontal ? 24 : nil, height: horizontal ? nil : 24)
                    .contentShape(Rectangle())
                    .gesture(
                        DragGesture(minimumDistance: 1, coordinateSpace: .global)
                            .onChanged { value in
                                let begin = start ?? fraction
                                if start == nil { start = fraction }
                                let delta = horizontal ? value.translation.width : value.translation.height
                                fraction = min(max(begin + delta / max(total, 1), Self.range.lowerBound), Self.range.upperBound)
                            }
                            .onEnded { _ in start = nil }
                    )
                    .hoverEffect(.highlight)
            }
            .zIndex(1)
            .accessibilityElement()
            .accessibilityLabel("Drag to resize")
            .accessibilityValue("\(Int((fraction * 100).rounded())) percent")
            .accessibilityAdjustableAction { direction in
                switch direction {
                case .increment: fraction = min(fraction + 0.1, Self.range.upperBound)
                case .decrement: fraction = max(fraction - 0.1, Self.range.lowerBound)
                @unknown default: break
                }
            }
            .accessibilityIdentifier("panes.divider")
    }
}

/// An agent opened in its own window (Open in New Window, or a sidebar row
/// dragged out): the pane alone, with the mini capsule. It never owns the
/// voice.
struct AgentWindowScene: View {
    let route: AgentWindowRoute?
    @EnvironmentObject private var app: SessionViewModel
    @ObservedObject private var scenes = SceneCoordinator.shared

    var body: some View {
        AgentWindowContent(route: route)
            .environmentObject(scenes.demoModel ?? app)
    }
}

private struct AgentWindowContent: View {
    let route: AgentWindowRoute?
    @EnvironmentObject private var model: SessionViewModel
    @StateObject private var ui = SceneUI()
    @ObservedObject private var scenes = SceneCoordinator.shared
    @Environment(\.dismissWindow) private var dismissWindow

    var body: some View {
        Group {
            if let route, let agent = model.flock.all.first(where: { $0.paneID == route.paneID }) {
                AgentScreenView(agent: agent, pane: PaneChrome(
                    leading: AnyView(MiniCapsule(menus: SessionMenus(model: model, ui: ui, scenes: scenes, sceneID: nil))),
                    onGone: { dismissWindow() },
                    extraMenu: AnyView(
                        Button {
                            dismissWindow()
                        } label: {
                            Label("Close Window", systemImage: "xmark.rectangle")
                        }
                        .accessibilityIdentifier("agentWindow.closeWindow")
                    )
                ))
                .id(route.paneID)
            } else {
                ContentUnavailableView {
                    Label("That agent is gone", systemImage: "terminal")
                } description: {
                    Text("Its pane closed or herdr no longer reports it.")
                } actions: {
                    Button("Close this window") { dismissWindow() }
                        .accessibilityIdentifier("agentWindow.close")
                }
            }
        }
        .background(Theme.surface)
        .environmentObject(ui)
        // A window restored on its own, before any main window.
        .onAppear { model.start() }
    }
}
