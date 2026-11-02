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
    public static let offExplanation = "The shell is off on this computer. Turn on Shell from your phone on its setup page."

    /// The ended window's description, with its plain fix.
    public static func endedDescription(_ notice: AgentWindowState.Notice?) -> String {
        switch notice {
        case .shellOff: return offExplanation
        case .shellFolder: return "Open the shell from an agent that works there, or add the folder to supervisor.project_roots on your computer."
        default: return "Open a new one from an agent's ⋯ menu."
        }
    }
}

/// The phone shell offer changes with the daemon binary, not just the
/// current `/term` connection. A reconnect that finds a new `/api/hello`
/// version forgets the old answer, so New shell is tried again.
public struct PhoneShellOffer: Equatable, Sendable {
    public private(set) var offered: Bool?
    public private(set) var daemonVersion: String?

    public init(offered: Bool? = nil, daemonVersion: String? = nil) {
        self.offered = offered
        self.daemonVersion = daemonVersion
    }

    /// Record `/api/hello`. A changed non-nil version resets the offer;
    /// a first hello or the same daemon version leaves it alone.
    public mutating func hello(version: String) {
        guard daemonVersion != version else { return }
        if daemonVersion != nil { offered = nil }
        daemonVersion = version
    }

    /// Record `term-ready.shell`.
    public mutating func ready(shell: Bool) {
        if offered != shell { offered = shell }
    }

    /// This app is now looking at another computer.
    public mutating func reset() {
        offered = nil
        daemonVersion = nil
    }
}
