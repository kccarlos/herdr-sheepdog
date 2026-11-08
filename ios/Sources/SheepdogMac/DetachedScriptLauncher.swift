import Foundation
import SheepdogKit

/// Starts a script in its own session (perl's setsid, then sh), stdin and
/// output /dev/null: it outlives the app, which the relaunch needs.
public struct DetachedScriptLauncher: UpdateDetachedLaunching {
    public init() {}

    public func launch(script: String) throws {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/perl")
        process.arguments = ["-MPOSIX", "-e", "POSIX::setsid(); exec @ARGV", "/bin/sh", "-c", script]
        process.standardInput = FileHandle.nullDevice
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        try process.run()
    }
}
