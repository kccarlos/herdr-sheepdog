import Foundation
import SheepdogKit

/// `UpdateCommandRunning` over `Process`: the program runs on a background
/// queue with stdin closed, its output captured, and is terminated when the
/// timeout passes. Runs the program as given; never through sudo.
public struct UpdateProcessRunner: UpdateCommandRunning {
    public init() {}

    public func run(_ executable: String, arguments: [String], timeout: TimeInterval) async -> UpdateRunResult {
        await withCheckedContinuation { continuation in
            DispatchQueue.global(qos: .utility).async {
                continuation.resume(returning: Self.runBlocking(executable, arguments: arguments, timeout: timeout))
            }
        }
    }

    private static func runBlocking(_ executable: String, arguments: [String], timeout: TimeInterval) -> UpdateRunResult {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: executable)
        process.arguments = arguments
        var env = ProcessInfo.processInfo.environment
        env["PATH"] = "/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
        env["NONINTERACTIVE"] = "1"
        env["HOMEBREW_NO_ENV_HINTS"] = "1"
        process.environment = env
        process.standardInput = FileHandle.nullDevice
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe
        do {
            try process.run()
        } catch {
            return UpdateRunResult(status: 127, output: "Could not run \(executable): \(error.localizedDescription)")
        }
        let timedOut = TimeoutFlag()
        let killer = DispatchWorkItem {
            timedOut.set()
            process.terminate()
        }
        DispatchQueue.global().asyncAfter(deadline: .now() + timeout, execute: killer)
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        killer.cancel()
        let text = String(decoding: data.suffix(64 * 1024), as: UTF8.self)
        return UpdateRunResult(status: process.terminationStatus, output: text, timedOut: timedOut.value)
    }
}

private final class TimeoutFlag: @unchecked Sendable {
    private let lock = NSLock()
    private var flag = false
    func set() { lock.lock(); flag = true; lock.unlock() }
    var value: Bool { lock.lock(); defer { lock.unlock() }; return flag }
}

/// File checks on the real file system.
public struct DiskFileChecker: UpdateFileChecking {
    public init() {}
    public func exists(_ path: String) -> Bool { FileManager.default.fileExists(atPath: path) }
}

/// The upgrade's log under ~/Library/Logs/Sheepdog. The detached shell
/// writes it; the app reads it, also after the cask reopened the app.
public struct UpdateLogFile: UpdateLogStoring {
    public let url: URL

    public init(url: URL = FileManager.default.homeDirectoryForCurrentUser
        .appendingPathComponent("Library/Logs/Sheepdog/update.log")) {
        self.url = url
    }

    public func read() -> String? { try? String(contentsOf: url, encoding: .utf8) }
    public func modified() -> Date? {
        (try? FileManager.default.attributesOfItem(atPath: url.path))?[.modificationDate] as? Date
    }
    public func clear() { try? FileManager.default.removeItem(at: url) }
}

/// Starts the upgrade script in its own session (perl's setsid, then sh),
/// stdin /dev/null, output appended to the log file: no pipe to the app, so
/// the app quitting cannot SIGPIPE brew.
public struct DetachedScriptLauncher: UpdateDetachedLaunching {
    let log: UpdateLogFile

    public init(log: UpdateLogFile) { self.log = log }

    public func launch(script: String) throws {
        let fm = FileManager.default
        try fm.createDirectory(at: log.url.deletingLastPathComponent(), withIntermediateDirectories: true)
        fm.createFile(atPath: log.url.path, contents: nil)
        let out = try FileHandle(forWritingTo: log.url)
        try out.seekToEnd()
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/perl")
        process.arguments = ["-MPOSIX", "-e", "POSIX::setsid(); exec @ARGV", "/bin/sh", "-c", script]
        var env = ProcessInfo.processInfo.environment
        env["PATH"] = "/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
        env["NONINTERACTIVE"] = "1"
        env["HOMEBREW_NO_ENV_HINTS"] = "1"
        process.environment = env
        process.standardInput = FileHandle.nullDevice
        process.standardOutput = out
        process.standardError = out
        try process.run()
        try? out.close()
    }
}
