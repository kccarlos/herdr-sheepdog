import Foundation

/// Starts a shell script that outlives the app (its own session, no pipe to
/// the app). The relaunch after an update uses it.
public protocol UpdateDetachedLaunching: Sendable {
    func launch(script: String) throws
}

/// What the menu offers, or nil when there is nothing newer.
public struct MacUpdateOffer: Equatable, Sendable {
    /// The newest release, as the daemon reports it ("v0.2.6").
    public var latest: String

    /// The daemon says a newer sheepdog exists, or one is installed and not
    /// running yet. Nothing once the daemon runs the newest release.
    public init?(info: UpdateInfo?) {
        guard let info, info.available || info.restartPending,
              let latest = (info.restartTo ?? info.latest)?.trimmingCharacters(in: .whitespacesAndNewlines),
              latest.first.map({ $0 == "v" || $0.isNumber }) == true
        else { return nil }
        self.latest = latest.hasPrefix("v") ? latest : "v" + latest
    }

    /// The release page: shown in the browser when the daemon cannot be reached.
    public var releaseURL: URL? {
        URL(string: "https://github.com/kccarlos/herdr-sheepdog/releases/tag/" + latest)
    }
}

/// The update row's state: idle, upgrading, failed.
public enum MacUpdateState: Equatable, Sendable {
    case idle
    case upgrading
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
        guard let offer else {
            status = nil
            itemTitle = nil
            accessibilityLabel = ""
            return
        }
        switch state {
        case .idle:
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

/// The menu's Update: the local daemon updates itself (`POST /api/update`,
/// what `sheepdog update` does, Sheepdog.app's cask included when Homebrew
/// installed it) and restarts. The progress comes from `/api/state`. When the
/// daemon comes back on another version and the bundle on disk changed, the
/// app relaunches itself (Homebrew reopens it only when its quit worked).
@MainActor
public final class MacUpdater {
    public private(set) var state: MacUpdateState = .idle {
        didSet { if state != oldValue { onChange?() } }
    }
    public var onChange: (@MainActor () -> Void)?

    private let install: @MainActor () async throws -> UpdateInfo?
    private let launcher: UpdateDetachedLaunching
    private let openURL: @MainActor (URL) -> Void
    private let bundleURL: URL, appVersion: String?, quit: @MainActor () -> Void
    private var daemonVersion: String?

    public init(
        install: @escaping @MainActor () async throws -> UpdateInfo?,
        launcher: UpdateDetachedLaunching,
        openURL: @escaping @MainActor (URL) -> Void,
        bundleURL: URL = Bundle.main.bundleURL, appVersion: String? = nil, quit: @escaping @MainActor () -> Void = {}
    ) {
        self.install = install
        self.launcher = launcher
        self.openURL = openURL
        self.bundleURL = bundleURL; self.appVersion = appVersion ?? Self.bundleVersion(at: bundleURL); self.quit = quit
    }

    /// The row was chosen. A daemon that cannot be reached, or that is too
    /// old to update itself, opens the release page instead.
    public func start(offer: MacUpdateOffer) async {
        guard state != .upgrading else { return }
        state = .upgrading
        do {
            if try await install() == nil {
                state = .idle
                if let url = offer.releaseURL { openURL(url) }
            }
        } catch {
            if case .unreachable? = error as? DaemonError {
                state = .idle
                if let url = offer.releaseURL { openURL(url) }
            } else {
                state = .failed((error as? DaemonError)?.message ?? error.localizedDescription)
            }
        }
    }

    /// Every `/api/state` read (nil: the daemon did not answer, as while it
    /// restarts): the daemon's progress wins over the app's own.
    public func follow(_ daemon: DaemonState?) {
        switch daemon?.update?.state {
        case "installing": state = .upgrading
        case "failed": state = .failed(daemon?.update?.error ?? "the daemon gave no reason")
        default: if daemon != nil, state == .upgrading { state = .idle }
        }
        guard let version = daemon?.version else { return }
        if let last = daemonVersion, last != version { relaunchIfReplaced() }
        daemonVersion = version
    }

    private func relaunchIfReplaced() {
        guard let appVersion, let disk = Self.bundleVersion(at: bundleURL), disk != appVersion else { return }
        do { try launcher.launch(script: Self.relaunchScript(bundlePath: bundleURL.path)); quit() }
        catch { state = .failed("Could not restart Sheepdog: \(error.localizedDescription)") }
    }

    public static func relaunchScript(bundlePath: String, pid: Int32 = ProcessInfo.processInfo.processIdentifier) -> String {
        "while kill -0 \(pid) 2>/dev/null; do sleep 0.1; done; sleep 0.2; /usr/bin/open '\(bundlePath.replacingOccurrences(of: "'", with: "'\\''"))' >/dev/null 2>&1"
    }

    static func bundleVersion(at url: URL) -> String? {
        (try? NSDictionary(contentsOf: url.appendingPathComponent("Contents/Info.plist"), error: ()))?["CFBundleShortVersionString"] as? String
    }
}
