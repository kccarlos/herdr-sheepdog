import Foundation

/// Pure state and validation for the New factory project sheet (`docs/dev/design/app-factory.md` §8).
public struct FactoryNewProjectForm: Equatable, Sendable {
    public struct FolderOption: Identifiable, Equatable, Sendable {
        public let path: String
        public let git, isExistingProject: Bool
        public var id: String { path }
        public var isSelectable: Bool { !isExistingProject }
        public var accessibilityLabel: String { "\(path), \(mark)" }
        public var mark: String {
            isExistingProject ? "already a project: open it to give it a new goal"
                : (git ? "git repository" : "not a git repository")
        }
    }

    public let folders: [FolderOption]
    public let selectedFolder: String?
    public let checks: [FactoryStartCheck]?
    public let showsFirstProjectIntro: Bool
    private let hasGoal: Bool

    public init(spaces: SpacesView, heads: [FactoryProjectHead], selectedFolder: String?,
                goal: String, checks: [FactoryStartCheck]? = nil, computerName _: String = "") {
        let existing = Set(heads.compactMap(\.folder).map(Self.normalizePath))
        var seen: Set<String> = []
        folders = (spaces.spaces.map { ($0.path, $0.git) } + spaces.folders.map { ($0.path, $0.git) }
            + heads.compactMap { $0.folder.map { ($0, true) } }).compactMap { path, git in
            let clean = Self.normalizePath(path)
            return !clean.isEmpty && seen.insert(clean).inserted
                ? FolderOption(path: clean, git: git, isExistingProject: existing.contains(clean)) : nil
        }
        self.selectedFolder = selectedFolder.map(Self.normalizePath)?.nonEmpty
        self.checks = checks
        showsFirstProjectIntro = heads.isEmpty
        hasGoal = !goal.trimmed.isEmpty
    }

    private var selected: FolderOption? { selectedFolder.flatMap { path in folders.first { $0.path == path } } }
    public var isEmptyFolders: Bool { folders.isEmpty }
    public var hasFailedChecks: Bool { checks?.contains { $0.status == "fail" } == true }
    public var canCheck: Bool { selected?.isSelectable == true && selected?.git == true }
    public var canStart: Bool { canCheck && hasGoal && !hasFailedChecks }
    public var checkButtonLabel: String { checks == nil ? "Check this folder" : "Check again" }
    public var notGitMessage: String? {
        selected?.isSelectable == true && selected?.git == false
            ? "This folder is not a git repository. Pick another folder, or make it one." : nil
    }
    public var startHint: String {
        hasFailedChecks ? "Start turns on when no check fails."
            : "Starting runs agents on your model accounts. They use your quota."
    }
    public var checksSummary: String? {
        guard let checks else { return nil }
        let failed = checks.filter { $0.status == "fail" }.count
        if failed > 0 {
            return "\(failed) \(failed == 1 ? "check failed. Fix it" : "checks failed. Fix them"), then check again."
        }
        let passed = checks.filter { $0.status == "ok" }.count
        let warns = checks.filter { $0.status == "warn" }.count
        return warns > 0 ? "\(passed) pass, \(warns) \(warns == 1 ? "warning" : "warnings"). You can start."
            : "All \(passed) checks pass."
    }

    public static func normalizePath(_ path: String) -> String {
        var value = path.trimmed
        while value.count > 1, value.hasSuffix("/") { value.removeLast() }
        return value
    }
    public static func spacesFailureMessage(computerName: String) -> String {
        "No folders to pick: herdr cannot be reached on \(computerName). "
            + "Open herdr-app on \(computerName), then try again."
    }
    public static func startErrorMessage(_ error: Error, computerName: String, linkUsable: Bool = true) -> String {
        factoryStartError(error, action: "Start", computerName: computerName, linkUsable: linkUsable)
    }
    public static func isFactoryUnavailable(_ error: Error) -> Bool {
        if case .rejected(status: 503, _) = error as? DaemonError { true } else { false }
    }
}
