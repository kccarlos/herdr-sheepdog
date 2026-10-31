import AppKit
import Foundation

/// Starts a program without blocking the caller, and says how it ended.
public protocol ProcessLaunching {
    /// `onExit` gets the exit status and everything the program printed
    /// (standard output and error together), on some background queue.
    func launch(_ executable: String, arguments: [String], environment: [String: String], onExit: @escaping @Sendable (Int32, String) -> Void) throws
}

public struct ProcessLauncher: ProcessLaunching {
    public init() {}

    public func launch(_ executable: String, arguments: [String], environment: [String: String], onExit: @escaping @Sendable (Int32, String) -> Void) throws {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: executable)
        process.arguments = arguments
        process.environment = environment
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe
        try process.run()
        // Read to the end before waiting: a program that fills the pipe
        // would otherwise never exit.
        DispatchQueue.global(qos: .userInitiated).async {
            let data = pipe.fileHandleForReading.readDataToEndOfFile()
            process.waitUntilExit()
            onExit(process.terminationStatus, String(decoding: data, as: UTF8.self))
        }
    }
}

/// Why "Open panel" did not open the panel, in words for the user.
public enum PanelOpenProblem: Equatable, Sendable {
    /// `herdr-app` is not on the search path.
    case herdrAppMissing
    /// herdr-app runs, but the sheepdog mod is not in its mods folder.
    case panelMissing
    /// Anything else herdr-app said.
    case failed(String)

    /// `/usr/bin/env` exits 127 when it cannot find the program.
    static let notFoundStatus: Int32 = 127

    /// What a finished `herdr-app do mod.open` means; nil when it worked.
    public init?(status: Int32, output: String) {
        guard status != 0 else { return nil }
        if status == Self.notFoundStatus {
            self = .herdrAppMissing
            return
        }
        // herdr-app answers `{"error": "…"}`; anything else is shown as is.
        let said: String
        if let data = output.data(using: .utf8),
           let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
           let error = object["error"] as? String {
            said = error
        } else {
            said = output.trimmingCharacters(in: .whitespacesAndNewlines)
        }
        if said.contains("no mod folder") {
            self = .panelMissing
        } else {
            self = .failed(said.isEmpty ? "herdr-app stopped with status \(status)." : said)
        }
    }

    public var title: String {
        switch self {
        case .herdrAppMissing: return "herdr-app isn't installed"
        case .panelMissing: return "The Sheepdog panel isn't installed in herdr-app"
        case .failed: return "herdr-app could not open the panel"
        }
    }

    public var message: String {
        switch self {
        case .herdrAppMissing:
            return "The panel lives in herdr-app, and Sheepdog could not find the herdr-app command. Install herdr-app, then choose Open panel again."
        case .panelMissing:
            return "Copy the panel into herdr-app's mods folder from a copy of the sheepdog repository:\n\nmkdir -p ~/.config/herdr-app/mods\ncp -R herdr-app/mods/sheepdog ~/.config/herdr-app/mods/\n\nThen choose Open panel again. The guide has the details."
        case let .failed(said):
            return "herdr-app said: \(said)"
        }
    }

    /// Where the guide explains the fix.
    public var guide: URL {
        switch self {
        case .herdrAppMissing: return URL(string: "https://github.com/kccarlos/herdr-sheepdog/blob/main/docs/desktop.md#requirements")!
        case .panelMissing, .failed: return URL(string: "https://github.com/kccarlos/herdr-sheepdog/blob/main/docs/desktop.md#3-install-the-panel")!
        }
    }
}

/// Shows a `PanelOpenProblem` to the user.
@MainActor
public protocol PanelProblemReporting {
    func report(_ problem: PanelOpenProblem)
}

/// An alert, with the guide one click away. The app has no Dock icon, so
/// it comes to the front first or the alert opens behind other windows.
public struct PanelProblemAlert: PanelProblemReporting {
    nonisolated public init() {}

    public func report(_ problem: PanelOpenProblem) {
        NSApp.activate(ignoringOtherApps: true)
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = problem.title
        alert.informativeText = problem.message
        alert.addButton(withTitle: "OK")
        alert.addButton(withTitle: "Open the Guide")
        if alert.runModal() == .alertSecondButtonReturn {
            NSWorkspace.shared.open(problem.guide)
        }
    }
}

/// "Open panel": the herdr-app panel mod, `herdr-app do mod.open --name
/// sheepdog` (docs/dev/design/desktop-client.md section 2).
public protocol PanelOpening {
    func openPanel() throws
}

public struct PanelOpener: PanelOpening {
    public static let arguments = ["herdr-app", "do", "mod.open", "--name", "sheepdog"]

    /// Where `herdr-app` lives. A menu-bar app starts with launchd's
    /// minimal PATH, not the user's shell's, so the usual install
    /// directories are added.
    public static func searchPath(home: String = NSHomeDirectory(), inherited: String? = ProcessInfo.processInfo.environment["PATH"]) -> String {
        let dirs = ["/opt/homebrew/bin", "/usr/local/bin", home + "/.local/bin", home + "/.cargo/bin"]
        return (dirs + (inherited.map { [$0] } ?? [])).joined(separator: ":")
    }

    private let launcher: ProcessLaunching
    private let environment: [String: String]
    private let reporter: PanelProblemReporting
    private let log: AppLogging?

    public init(
        launcher: ProcessLaunching = ProcessLauncher(),
        environment: [String: String] = ProcessInfo.processInfo.environment,
        reporter: PanelProblemReporting = PanelProblemAlert(),
        log: AppLogging? = nil
    ) {
        self.launcher = launcher
        self.environment = environment
        self.reporter = reporter
        self.log = log
    }

    /// Throws only when the command could not start at all; what herdr-app
    /// answers is reported to the user when it ends.
    public func openPanel() throws {
        var env = environment
        env["PATH"] = Self.searchPath(inherited: environment["PATH"])
        let reporter = reporter
        let log = log
        try launcher.launch("/usr/bin/env", arguments: Self.arguments, environment: env) { status, output in
            guard let problem = PanelOpenProblem(status: status, output: output) else { return }
            DispatchQueue.main.async {
                MainActor.assumeIsolated {
                    log?.log("could not open the panel (status \(status)): \(output.trimmingCharacters(in: .whitespacesAndNewlines))")
                    reporter.report(problem)
                }
            }
        }
    }
}
