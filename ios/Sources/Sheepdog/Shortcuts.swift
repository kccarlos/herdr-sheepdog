import SheepdogKit
import SwiftUI

/// What the hardware-keyboard shortcuts act on in this window.
struct SessionCommands {
    var newAgent: (() -> Void)?
    var toggleMute: (() -> Void)?
    var muted = false
    /// The agents ⌘1…9 jump to, in sidebar order.
    var agents: [Agent] = []
    var openAgent: (String) -> Void = { _ in }
    /// ⌘K: talk to the next computer; nil with one computer.
    var talkToNext: (() -> Void)?
}

/// ⌘N new agent, ⌘⇧M mute, ⌘1…9 jump to an agent, and ⌘K (Talk to the next
/// computer) only with several computers: with one there is nothing to switch to.
///
/// Invisible buttons in the window itself rather than app `Commands`: the
/// offline demo is a cover with its own session, and a window's own
/// shortcuts reach whatever is presented in it, the terminal included.
/// They show in the ⌘ list a long press of Command opens.
struct KeyboardShortcuts: View {
    let commands: SessionCommands

    var body: some View {
        ZStack {
            Button("New Agent") { commands.newAgent?() }
                .keyboardShortcut("n", modifiers: .command)
                .disabled(commands.newAgent == nil)
            Button(commands.muted ? "Unmute" : "Mute") { commands.toggleMute?() }
                .keyboardShortcut("m", modifiers: [.command, .shift])
                .disabled(commands.toggleMute == nil)
            if let talkToNext = commands.talkToNext {
                Button("Talk to the Next Computer", action: talkToNext)
                    .keyboardShortcut("k", modifiers: .command)
            }
            ForEach(Array(commands.agents.prefix(9).enumerated()), id: \.element.paneID) { index, agent in
                Button(agent.alias) { commands.openAgent(agent.paneID) }
                    .keyboardShortcut(KeyEquivalent(Character(String(index + 1))), modifiers: .command)
            }
        }
        .opacity(0)
        .frame(width: 0, height: 0)
        .accessibilityHidden(true)
        .allowsHitTesting(false)
    }
}
