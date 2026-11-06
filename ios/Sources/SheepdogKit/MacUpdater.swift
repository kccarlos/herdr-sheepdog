import Foundation

/// How a finished command ended.
public struct UpdateRunResult: Equatable, Sendable {
    public var status: Int32
    public var output: String
    public var timedOut: Bool

    public init(status: Int32, output: String = "", timedOut: Bool = false) {
        self.status = status
        self.output = output
        self.timedOut = timedOut
    }
}

/// Runs a program to its end without blocking the caller. The Mac app's
/// implementation uses `Process`; tests inject a fake. Never run through sudo.
public protocol UpdateCommandRunning: Sendable {
    func run(_ executable: String, arguments: [String], timeout: TimeInterval) async -> UpdateRunResult
}

/// The file checks the install-method detection needs.
public protocol UpdateFileChecking: Sendable {
    func exists(_ path: String) -> Bool
}

/// Starts a shell script that outlives the app: its own session, stdin from
/// /dev/null, stdout and stderr appended to the log file, never a pipe to
/// the app (the cask quits the app mid-upgrade; a pipe would SIGPIPE brew).
public protocol UpdateDetachedLaunching: Sendable {
    func launch(script: String) throws
}

/// The upgrade's log file: the detached script writes it, the app reads it
/// (also after the cask reopened the app).
public protocol UpdateLogStoring: Sendable {
    func read() -> String?
    /// When the log was last written.
    func modified() -> Date?
    func clear()
}

/// How Sheepdog.app and the daemon were installed.
public enum MacInstallMethod: Equatable, Sendable {
    /// Homebrew owns at least one of them: `brew` is the program, and these
    /// are the packages to upgrade.
    case homebrew(brew: String, formula: Bool, cask: Bool)
    case manual
}

/// Finds out how Sheepdog was installed. Pure over the injected runner and
/// file checks; `MacUpdaterTests` is the table.
public struct MacInstallDetector: Sendable {
    public static let brewPaths = ["/opt/homebrew/bin/brew", "/usr/local/bin/brew"]
    public static let caskroomPaths = ["/opt/homebrew/Caskroom/sheepdog-app", "/usr/local/Caskroom/sheepdog-app"]
    public static let cask = "sheepdog-app"
    public static let formula = "sheepdog"

    let runner: UpdateCommandRunning
    let files: UpdateFileChecking

    public init(runner: UpdateCommandRunning, files: UpdateFileChecking) {
        self.runner = runner
        self.files = files
    }

    public func detect() async -> MacInstallMethod {
        guard let brew = Self.brewPaths.first(where: { files.exists($0) }) else { return .manual }
        let caskList = await runner.run(brew, arguments: ["list", "--cask", Self.cask], timeout: 60)
        let cask = (caskList.status == 0 && !caskList.timedOut) || Self.caskroomPaths.contains(where: { files.exists($0) })
        let formulaList = await runner.run(brew, arguments: ["list", "--formula", Self.formula], timeout: 60)
        let formula = formulaList.status == 0 && !formulaList.timedOut
        guard cask || formula else { return .manual }
        return .homebrew(brew: brew, formula: formula, cask: cask)
    }
}

/// What the menu offers, or nil when there is nothing newer.
public struct MacUpdateOffer: Equatable, Sendable {
    /// The newest release, as the daemon reports it ("v0.2.6").
    public var latest: String

    /// The daemon says a newer sheepdog exists, or this app's own version is
    /// older than the newest release (daemon and app ship in one v* release).
    public init?(info: UpdateInfo?, appVersion: String?) {
        guard let info, let latest = info.latest?.trimmingCharacters(in: .whitespacesAndNewlines),
              let newest = MacUpdateOffer.semver(latest)
        else { return nil }
        let appOlder = appVersion.flatMap(MacUpdateOffer.semver).map { $0.lexicographicallyPrecedes(newest) } ?? false
        guard info.available || appOlder else { return nil }
        self.latest = latest.hasPrefix("v") ? latest : "v" + latest
    }

    /// "v1.2.3" or "1.2.3" (a suffix after - or + ignored); nil for anything else.
    static func semver(_ text: String) -> [Int]? {
        var v = text.trimmingCharacters(in: .whitespaces)
        if v.hasPrefix("v") { v.removeFirst() }
        if let i = v.firstIndex(where: { $0 == "-" || $0 == "+" }) { v = String(v[..<i]) }
        let parts = v.split(separator: ".", omittingEmptySubsequences: false).map { Int($0) }
        guard parts.count == 3, parts.allSatisfy({ ($0 ?? -1) >= 0 }) else { return nil }
        return parts.compactMap { $0 }
    }

    /// The release page: shown in the browser when Homebrew does not own the app.
    public var releaseURL: URL? {
        URL(string: "https://github.com/kccarlos/herdr-sheepdog/releases/tag/" + latest)
    }
}

/// The update row's state machine: idle, upgrading, failed.
public enum MacUpdateState: Equatable, Sendable {
    case idle
    case upgrading
    case restartNeeded
    case failed(String)
}

/// Words and enabled-ness of the update rows, for the menu.
public struct MacUpdatePresentation: Equatable, Sendable {
    /// A disabled line above the item (progress or the failure), or nil.
    public var status: String?
    /// The clickable item's title, or nil while an upgrade runs.
    public var itemTitle: String?
    public var accessibilityLabel: String

    public init(offer: MacUpdateOffer?, state: MacUpdateState) {
        if state == .restartNeeded { status = nil; itemTitle = "Restart to finish updating"; accessibilityLabel = "Update installed. Choose to restart Sheepdog."; return }
        guard let offer else {
            status = nil
            itemTitle = nil
            accessibilityLabel = ""
            return
        }
        switch state {
        case .idle, .restartNeeded:
            status = nil
            itemTitle = "Update available — \(offer.latest)"
            accessibilityLabel = "Update available, version \(offer.latest.dropFirst()). Choose to install it."
        case .upgrading:
            status = "Updating Sheepdog to \(offer.latest)…"
            itemTitle = nil
            accessibilityLabel = "Updating Sheepdog"
        case let .failed(message):
            status = "Update failed: \(message)"
            itemTitle = "Try the update again — \(offer.latest)"
            accessibilityLabel = "Update failed. \(message) Choose to try again."
        }
    }
}

/// Runs the update the user chose. Homebrew-installed: `brew upgrade` in a
/// detached shell, progress read from its log file. Otherwise: the release page in the
/// browser. No main-thread work.
@MainActor
public final class MacUpdater {
    public static let timeout: TimeInterval = 600

    public private(set) var state: MacUpdateState = .idle {
        didSet { if state != oldValue { onChange?() } }
    }
    public var onChange: (@MainActor () -> Void)?

    private let detector: MacInstallDetector
    private let launcher: UpdateDetachedLaunching
    private let log: UpdateLogStoring
    private let openURL: @MainActor (URL) -> Void
    private let timeout: TimeInterval
    private let pollInterval: TimeInterval
    private let bundleURL: URL, appVersion: String?, voiceActive: @MainActor () -> Bool, quit: @MainActor () -> Void

    public init(
        runner: UpdateCommandRunning,
        files: UpdateFileChecking,
        launcher: UpdateDetachedLaunching,
        log: UpdateLogStoring,
        openURL: @escaping @MainActor (URL) -> Void,
        timeout: TimeInterval = MacUpdater.timeout,
        pollInterval: TimeInterval = 2, bundleURL: URL = Bundle.main.bundleURL, appVersion: String? = nil, voiceActive: @escaping @MainActor () -> Bool = { false }, quit: @escaping @MainActor () -> Void = {}
    ) {
        detector = MacInstallDetector(runner: runner, files: files)
        self.launcher = launcher
        self.log = log
        self.openURL = openURL
        self.timeout = timeout
        self.pollInterval = pollInterval
        self.bundleURL = bundleURL; self.appVersion = appVersion ?? Self.bundleVersion(at: bundleURL); self.voiceActive = voiceActive; self.quit = quit
    }

    /// The brew arguments for what is installed. Never sudo.
    public static func upgradeArguments(formula: Bool, cask: Bool) -> [String] {
        var args = ["upgrade"]
        if formula && cask { return args + [MacInstallDetector.formula, MacInstallDetector.cask] }
        args += cask ? ["--cask", MacInstallDetector.cask] : ["--formula", MacInstallDetector.formula]
        return args
    }

    /// The shell script: brew, then its exit status on a line of its own.
    public static func script(brew: String, arguments: [String]) -> String {
        func quote(_ s: String) -> String { "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'" }
        return ([brew] + arguments).map(quote).joined(separator: " ") + "; echo \"exit=$?\""
    }

    /// The row was chosen. Returns when the upgrade ended (or the browser opened).
    public func start(offer: MacUpdateOffer) async {
        guard state != .upgrading else { return }
        state = .upgrading
        switch await detector.detect() {
        case .manual:
            state = .idle
            if let url = offer.releaseURL { openURL(url) }
        case let .homebrew(brew, formula, cask):
            log.clear()
            do {
                try launcher.launch(script: Self.script(brew: brew, arguments: Self.upgradeArguments(formula: formula, cask: cask)))
            } catch {
                state = .failed("Could not start Homebrew: \(error.localizedDescription)")
                return
            }
            await poll(since: Date())
        }
    }

    /// On launch: read what a previous
    /// detached upgrade left. Finished: show a failure once, or clear a
    /// success. Still running: follow it.
    public func resume() async {
        guard state != .upgrading, let text = log.read() else { return }
        if let result = Self.finished(text) {
            log.clear()
            finish(result)
        } else if let at = log.modified(), Date().timeIntervalSince(at) < timeout {
            state = .upgrading
            await poll(since: at)
        } else {
            log.clear()
            state = .failed("The last update did not finish. Run brew upgrade in Terminal.")
        }
    }

    private func poll(since started: Date) async {
        while true {
            if let text = log.read(), let result = Self.finished(text) {
                finish(result)
                return
            }
            if Date().timeIntervalSince(started) >= timeout {
                state = .failed("Homebrew took longer than 10 minutes. Run brew upgrade in Terminal.")
                return
            }
            try? await Task.sleep(nanoseconds: UInt64(pollInterval * 1_000_000_000))
        }
    }

    public static func relaunchScript(bundlePath: String, pid: Int32 = ProcessInfo.processInfo.processIdentifier) -> String {
        "while kill -0 \(pid) 2>/dev/null; do sleep 0.1; done; sleep 0.2; echo \"exit=0\"; /usr/bin/open '\(bundlePath.replacingOccurrences(of: "'", with: "'\\''"))' >/dev/null 2>&1"
    }
    public func relaunch() {
        do { try launcher.launch(script: Self.relaunchScript(bundlePath: bundleURL.path)); state = .idle; quit() }
        catch { state = .failed("Could not restart Sheepdog: \(error.localizedDescription)") }
    }
    static func bundleVersion(at url: URL) -> String? {
        (try? NSDictionary(contentsOf: url.appendingPathComponent("Contents/Info.plist"), error: ()))?["CFBundleShortVersionString"] as? String
    }
    private func finish(_ result: UpdateRunResult) {
        let next = Self.outcome(result)
        guard next == .idle, let appVersion, let disk = Self.bundleVersion(at: bundleURL), disk != appVersion else { state = next; return }
        if voiceActive() { state = .restartNeeded } else { relaunch() }
    }

    /// The log's result once its last line is `exit=N`.
    static func finished(_ text: String) -> UpdateRunResult? {
        let lines = text.split(whereSeparator: \.isNewline).map { $0.trimmingCharacters(in: .whitespaces) }
        guard let last = lines.last, last.hasPrefix("exit="), let code = Int32(last.dropFirst(5)) else { return nil }
        return UpdateRunResult(status: code, output: lines.dropLast().joined(separator: "\n"))
    }

    static func outcome(_ result: UpdateRunResult) -> MacUpdateState {
        if result.timedOut { return .failed("Homebrew took longer than 10 minutes. Run brew upgrade in Terminal.") }
        if result.status == 0 { return .idle }
        let lines = result.output.split(whereSeparator: \.isNewline).map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
        let tail = lines.last(where: { $0.lowercased().contains("error") }) ?? lines.last
        return .failed(String((tail ?? "Homebrew stopped with code \(result.status).").prefix(200)))
    }
}
