import Foundation

/// Pure state and validation for the New factory project sheet (`docs/dev/design/app-factory.md` §8).
public struct FactoryNewProjectForm: Equatable, Sendable {
    public struct FolderOption: Identifiable, Equatable, Sendable {
        public let path: String
        public let git: Bool
        public let isExistingProject: Bool
        public var id: String { path }
        public var isSelectable: Bool { !isExistingProject }
        public var accessibilityLabel: String { "\(path), \(mark)" }
        public var mark: String {
            isExistingProject ? "already a project: open it to give it a new goal"
                : (git ? "git repository" : "not a git repository")
        }
    }

    public static let notGitText = "This folder is not a git repository. Pick another folder, or make it one."
    public static let goalHint = "What you want built, in your own words. The coordinator turns it into tasks."
    public static let startedMessage = "Started. The coordinator is planning the tasks."
    public static let firstProjectIntro =
        "A project is one folder with a git repository and one goal. The coordinator plans the tasks, "
        + "workers build them, reviewers check them, and you are asked only for decisions."
    public static let checkFailureMessage =
        "Can't run the start checks: sheepdog did not answer in time. Check that sheepdog is running, then check again."

    public let folders: [FolderOption]
    public let selectedFolder: String?
    public let checks: [FactoryStartCheck]?
    public let showsFirstProjectIntro: Bool
    private let hasGoal: Bool

    public init(spaces: SpacesView? = nil, heads: [FactoryProjectHead], selectedFolder: String?,
                goal: String, checks: [FactoryStartCheck]? = nil, computerName _: String = "") {
        let existing = Set(heads.compactMap(\.folder).map(Self.normalizePath))
        var seen: Set<String> = []
        let candidates = (spaces?.spaces.map { ($0.path, $0.git) } ?? [])
            + (spaces?.folders.map { ($0.path, $0.git) } ?? [])
            + heads.compactMap { $0.folder.map { ($0, true) } }
        folders = candidates.compactMap { path, git in
            let clean = Self.normalizePath(path)
            guard !clean.isEmpty, seen.insert(clean).inserted else { return nil }
            return FolderOption(path: clean, git: git, isExistingProject: existing.contains(clean))
        }
        self.selectedFolder = selectedFolder.map(Self.normalizePath)?.nonEmpty
        self.checks = checks
        showsFirstProjectIntro = heads.isEmpty
        hasGoal = !goal.trimmed.isEmpty
    }

    private var selected: FolderOption? { selectedFolder.flatMap { path in folders.first { $0.path == path } } }
    public var isEmptyFolders: Bool { folders.isEmpty }
    public var hasFailedChecks: Bool { checks?.contains { $0.status == "fail" } == true }
    public var hasPassedChecks: Bool { checks?.isEmpty == false && !hasFailedChecks }
    public var canCheck: Bool { selected?.isSelectable == true && selected?.git == true }
    public var canStart: Bool { canCheck && hasGoal && hasPassedChecks }
    public var checkButtonLabel: String { checks == nil ? "Check this folder" : "Check again" }
    public var notGitMessage: String? {
        selected?.isSelectable == true && selected?.git == false ? Self.notGitText : nil
    }
    public var startHint: String {
        if hasPassedChecks, !hasGoal {
            return "Write a goal first."
        }
        return canStart
            ? "Starting runs agents on your model accounts. They use your quota."
            : "Start turns on when no check fails."
    }
    public var checksSummary: String? { Self.summarize(checks) }

    public static func summarize(_ checks: [FactoryStartCheck]?) -> String? {
        guard let checks, !checks.isEmpty else { return nil }
        let counts = Dictionary(grouping: checks, by: \.status).mapValues(\.count)
        let failed = counts["fail", default: 0]
        if failed > 0 {
            let noun = failed == 1 ? "check failed. Fix it" : "checks failed. Fix them"
            return "\(failed) \(noun), then check again."
        }
        let warns = counts["warn", default: 0]
        if warns > 0 {
            return "\(counts["ok", default: 0]) pass, \(warns) \(warns == 1 ? "warning" : "warnings"). You can start."
        }
        return "All \(checks.count) checks pass."
    }

    public static func normalizePath(_ path: String) -> String {
        var value = path.trimmed
        while value.count > 1, value.hasSuffix("/") {
            value.removeLast()
        }
        return value
    }

    public static func spacesFailureMessage(
        _ result: Result<SpacesView, Error>? = .failure(DaemonError.rejected(status: 502, message: "")),
        computerName: String, linkUsable: Bool = true
    ) -> String {
        guard case let .failure(error) = result else {
            return "No folders to pick yet. Open a workspace in herdr on \(computerName), then try again."
        }
        if case .rejected(status: 502, _) = error as? DaemonError {
            return "No folders to pick: herdr cannot be reached on \(computerName). "
                + "Open herdr-app on \(computerName), then try again."
        }
        return RequestFailure(error, linkUsable: linkUsable, name: computerName).message
    }

    public static func startErrorMessage(_ error: Error, computerName: String, linkUsable: Bool = true) -> String {
        factoryStartError(error, action: "Start", computerName: computerName, linkUsable: linkUsable)
    }

    public static func isFactoryUnavailable(_ error: Error) -> Bool {
        guard case .rejected(status: 503, _) = error as? DaemonError else { return false }
        return true
    }
}
