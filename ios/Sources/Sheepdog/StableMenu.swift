import SwiftUI

/// A `Menu` drawn from one value.
///
/// SwiftUI rebuilds an open menu's items whenever the view that holds it
/// re-renders, and the open menu flickers (or closes). As an `Equatable`
/// view whose `==` compares only `value` (the closures are left out), it
/// re-renders only when what it shows changes, however often its parent
/// does. Everything the items and the label show must come from `value`;
/// the actions may read live state when tapped.
struct StableMenu<Value: Equatable, Content: View, MenuLabel: View>: View, Equatable {
    let value: Value
    /// The `RenderProbe` name, for the UI tests that count re-renders.
    var probe: String
    @ViewBuilder let content: (Value) -> Content
    @ViewBuilder let label: (Value) -> MenuLabel

    static func == (a: Self, b: Self) -> Bool { a.value == b.value }

    var body: some View {
        #if DEBUG
        let _ = RenderProbe.hit("menu.\(probe)")
        #endif
        Menu { content(value) } label: { label(value) }
    }
}

/// A `StableMenu`, compared by its value (`.equatable()`).
func stableMenu<Value: Equatable, Content: View, MenuLabel: View>(
    _ value: Value,
    probe: String,
    @ViewBuilder content: @escaping (Value) -> Content,
    @ViewBuilder label: @escaping (Value) -> MenuLabel
) -> some View {
    StableMenu(value: value, probe: probe, content: content, label: label).equatable()
}

#if DEBUG
/// Counts what re-renders, for the UI tests that check a stream or a
/// re-render storm leaves the rest of the app alone
/// (`-sheepdogCountRenders`): the session model's and the agent windows'
/// change announcements, terminal frames, and body evaluations of the
/// menus and window chrome. `RenderProbeReadout` shows the counts.
@MainActor
enum RenderProbe {
    static let enabled = DebugLaunch.countRenders
    private(set) static var counts: [String: Int] = [:]

    static func hit(_ name: String) {
        guard enabled else { return }
        counts[name, default: 0] += 1
    }

    /// "frames=40 model=2 …", sorted by name.
    static var summary: String {
        counts.sorted { $0.key < $1.key }.map { "\($0.key)=\($0.value)" }.joined(separator: " ")
    }
}

/// The counts, twice a second, in a hidden element (`debug.renders`) the
/// UI tests read. Only with `-sheepdogCountRenders`.
struct RenderProbeReadout: View {
    var body: some View {
        if RenderProbe.enabled {
            TimelineView(.periodic(from: .now, by: 0.5)) { _ in
                Text(RenderProbe.summary.isEmpty ? "none" : RenderProbe.summary)
                    .font(.system(size: 1))
                    .opacity(0.02)
                    .frame(width: 2, height: 2)
                    .allowsHitTesting(false)
                    .accessibilityIdentifier("debug.renders")
            }
        }
    }
}
#endif
