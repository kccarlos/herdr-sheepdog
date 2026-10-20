import Foundation

/// The phone shell's words (docs/dev/design/phone-terminal.md section 5,
/// "Shell"): a plain shell in an agent's folder, over `/term`. The window
/// is the agent window's (`AgentWindowState` with `isShell`), without the
/// composer.
public enum PhoneShell {
    /// "Shell · app" for a folder, "Shell" for the first project folder.
    public static func title(cwd: String?) -> String {
        guard let cwd = cwd?.trimmingCharacters(in: .whitespaces), !cwd.isEmpty else { return "Shell" }
        let name = (cwd as NSString).lastPathComponent
        return name.isEmpty || name == "/" || name == "~" ? "Shell" : "Shell · \(name)"
    }

    /// Why the shell is not offered, with the fix (Settings and the
    /// window's ended state).
    public static let offExplanation = "Shell is available over Tailscale, or turn it on for this network on your computer's setup page."

    /// The ended window's description, with its plain fix.
    public static func endedDescription(_ notice: AgentWindowState.Notice?) -> String {
        switch notice {
        case .shellOff: return offExplanation
        case .shellFolder: return "Open the shell from an agent that works there, or add the folder to supervisor.project_roots on your computer."
        default: return "Open a new one from an agent's ⋯ menu."
        }
    }
}
