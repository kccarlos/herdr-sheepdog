import AppIntents

/// "Talk to Sheepdog" in Siri, Spotlight and Shortcuts, and so on the
/// Action Button (Settings ▸ Action Button ▸ Shortcut).
struct SheepdogShortcuts: AppShortcutsProvider {
    static var appShortcuts: [AppShortcut] {
        AppShortcut(
            intent: TalkToSheepdogIntent(),
            phrases: [
                "Talk to \(.applicationName)",
                "Start \(.applicationName)",
                "Open \(.applicationName) and listen",
            ],
            shortTitle: "Talk to Sheepdog",
            systemImageName: "waveform"
        )
    }

    static let shortcutTileColor: ShortcutTileColor = .teal
}
