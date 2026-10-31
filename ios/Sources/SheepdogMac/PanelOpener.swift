import Foundation

/// Starts a program and does not wait for it.
public protocol ProcessLaunching {
    func launch(_ executable: String, arguments: [String], environment: [String: String]) throws
}

public struct ProcessLauncher: ProcessLaunching {
    public init() {}

    public func launch(_ executable: String, arguments: [String], environment: [String: String]) throws {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: executable)
        process.arguments = arguments
        process.environment = environment
        try process.run()
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

    public init(launcher: ProcessLaunching = ProcessLauncher(), environment: [String: String] = ProcessInfo.processInfo.environment) {
        self.launcher = launcher
        self.environment = environment
    }

    public func openPanel() throws {
        var env = environment
        env["PATH"] = Self.searchPath(inherited: environment["PATH"])
        try launcher.launch("/usr/bin/env", arguments: Self.arguments, environment: env)
    }
}
