import Foundation
import XCTest
@testable import SheepdogKit

/// Failing unit tests for app-factory PR 12 (`docs/dev/design/app-factory.md` §8, §10 row 12;
/// `PlusMenu`, `FactoryNewProject`, `FactoryNewProjectChecks`, `slice14-wording.md`;
/// `internal/web/factory.go`, `internal/onboard`). Does not depend on PR 11 settings types.
final class FactoryNewProjectTests: XCTestCase {
    private func head(id: String, name: String, state: FactoryProjectState, folder: String) throws -> FactoryProjectHead {
        let obj: [String: Any] = ["id": id, "name": name, "goal": "Goal", "state": state.rawValue, "counts": [String: Int](), "folder": folder]
        return try DaemonClient.decoder.decode(FactoryProjectHead.self, from: JSONSerialization.data(withJSONObject: obj))
    }

    private func sampleSpaces() throws -> SpacesView {
        let json = #"""
        {"spaces":[
          {"id":"w1","name":"notes","path":"~/code/notes","git":true,"worktree":false,"agents":[],"shells":[],"commands":[]},
          {"id":"w2","name":"photos","path":"~/code/photos/","git":true,"worktree":false,"agents":[],"shells":[],"commands":[]}
        ],"folders":[
          {"name":"webapp","path":"~/code/webapp","git":true},
          {"name":"drafts","path":"~/Documents/drafts","git":false}
        ],"kinds":[{"kind":"claude","name":"Claude"}],"worktrees":true,"needs_access":[]}
        """#
        return try DaemonClient.decoder.decode(SpacesView.self, from: Data(json.utf8))
    }

    /// Reuses `SpacesView` + existing project folders, deduplicates trailing slashes, and marks
    /// existing running/stopped/finished projects non-pickable (`app.np.known`).
    func testFolderOptionsReuseSpacesDeduplicateAndDisableExistingProjects() throws {
        let spaces = try sampleSpaces()
        let heads = [
            try head(id: "p1", name: "photos", state: .stopped, folder: "~/code/photos"),
            try head(id: "p2", name: "recipes", state: .finished, folder: "~/code/recipes"),
        ]
        let form = FactoryNewProjectForm(spaces: spaces, heads: heads, selectedFolder: nil, goal: "", computerName: "Work Mac")
        XCTAssertFalse(form.showsFirstProjectIntro)
        XCTAssertFalse(form.isEmptyFolders)
        XCTAssertEqual(form.folders.map(\.path), ["~/code/notes", "~/code/photos", "~/code/webapp", "~/Documents/drafts", "~/code/recipes"])

        XCTAssertEqual(form.folders[0].mark, "git repository")
        XCTAssertTrue(form.folders[0].isSelectable && form.folders[0].git)
        XCTAssertEqual(form.folders[0].accessibilityLabel, "~/code/notes, git repository")

        XCTAssertTrue(form.folders[1].isExistingProject && !form.folders[1].isSelectable)
        XCTAssertEqual(form.folders[1].mark, "already a project: open it to give it a new goal")
        XCTAssertEqual(form.folders[1].accessibilityLabel, "~/code/photos, already a project: open it to give it a new goal")

        XCTAssertFalse(form.folders[3].git)
        XCTAssertTrue(form.folders[3].isSelectable)
        XCTAssertEqual(form.folders[3].mark, "not a git repository")
        XCTAssertEqual(form.folders[3].accessibilityLabel, "~/Documents/drafts, not a git repository")

        XCTAssertTrue(form.folders[4].isExistingProject && !form.folders[4].isSelectable)
        XCTAssertEqual(form.folders[4].mark, "already a project: open it to give it a new goal")
    }

    /// Empty/non-git validation, first-project intro, start-check status words & `run:` fix cuts,
    /// and disabling Start while any check fails until retried without failures.
    func testValidationCheckSummariesAndCommandFixParsing() throws {
        let spaces = try sampleSpaces()
        let existing = [try head(id: "p1", name: "photos", state: .running, folder: "~/code/photos")]
        let emptySpaces = SpacesView(spaces: [], folders: [], kinds: [], worktrees: true)
        XCTAssertTrue(FactoryNewProjectForm(spaces: emptySpaces, heads: [], selectedFolder: nil, goal: "").showsFirstProjectIntro)
        XCTAssertTrue(FactoryNewProjectForm(spaces: emptySpaces, heads: [], selectedFolder: nil, goal: "").isEmptyFolders)

        XCTAssertFalse(FactoryNewProjectForm(spaces: spaces, heads: existing, selectedFolder: nil, goal: "Goal").canStart)
        XCTAssertFalse(FactoryNewProjectForm(spaces: spaces, heads: existing, selectedFolder: "   ", goal: "Goal").canStart)
        XCTAssertFalse(FactoryNewProjectForm(spaces: spaces, heads: existing, selectedFolder: "~/code/photos", goal: "Goal").canStart)

        let notGit = FactoryNewProjectForm(spaces: spaces, heads: existing, selectedFolder: "~/Documents/drafts", goal: "Goal")
        XCTAssertEqual(notGit.notGitMessage, "This folder is not a git repository. Pick another folder, or make it one.")
        XCTAssertFalse(notGit.canCheck || notGit.canStart)

        let blankGoal = FactoryNewProjectForm(spaces: spaces, heads: existing, selectedFolder: "~/code/notes", goal: "  \n\t ")
        XCTAssertTrue(blankGoal.canCheck && !blankGoal.canStart)

        let unchecked = FactoryNewProjectForm(
            spaces: spaces, heads: existing, selectedFolder: "~/code/notes", goal: "A notes page with search"
        )
        XCTAssertTrue(unchecked.canCheck)
        XCTAssertFalse(unchecked.canStart, "Start stays off until start checks pass")
        XCTAssertEqual(unchecked.checkButtonLabel, "Check this folder")
        XCTAssertEqual(unchecked.startHint, "Start turns on when no check fails.")

        let emptyChecks = FactoryNewProjectForm(
            spaces: spaces, heads: existing, selectedFolder: "~/code/notes", goal: "A notes page with search", checks: []
        )
        XCTAssertFalse(emptyChecks.canStart, "Empty check results must not enable Start")
        XCTAssertNil(emptyChecks.checksSummary)
        XCTAssertEqual(emptyChecks.startHint, "Start turns on when no check fails.")

        let ok = FactoryStartCheck(name: "git version", status: "ok", detail: "git version 2.47.1")
        let warn = FactoryStartCheck(name: "start fetch", status: "warn", detail: "stale", fix: "run: git fetch origin (sign in to git if it asks)")
        let failCLI = FactoryStartCheck(name: "start codex", status: "fail", detail: "codex is not signed in", fix: "run: codex login")
        let failPlain = FactoryStartCheck(name: "start agy", status: "fail", detail: "agy is not signed in", fix: "run agy without arguments and sign in")
        let skip = FactoryStartCheck(name: "github remote", status: "skip", detail: "needs gh")
        XCTAssertEqual([ok, warn, failCLI, skip].map(\.statusWord), ["pass", "warning", "fail", "skipped"])
        XCTAssertEqual(failCLI.commandFix?.command, "codex login")
        XCTAssertNil(failCLI.commandFix?.rest)
        XCTAssertEqual(warn.commandFix?.command, "git fetch origin")
        XCTAssertEqual(warn.commandFix?.rest, "(sign in to git if it asks)")
        XCTAssertEqual(FactoryStartCheck(name: "hc", status: "fail", fix: "run: hc server start, then check again").commandFix?.rest, "then check again")
        XCTAssertNil(failPlain.commandFix)
        XCTAssertNil(FactoryStartCheck(name: "gh", status: "fail", fix: "install GitHub CLI, then run: gh auth login").commandFix)

        let oneFail = FactoryNewProjectForm(spaces: spaces, heads: existing, selectedFolder: "~/code/notes", goal: "Goal", checks: [ok, failCLI])
        XCTAssertTrue(oneFail.hasFailedChecks && !oneFail.canStart)
        XCTAssertEqual(oneFail.checkButtonLabel, "Check again")
        XCTAssertEqual(oneFail.startHint, "Start turns on when no check fails.")
        XCTAssertEqual(oneFail.checksSummary, "1 check failed. Fix it, then check again.")
        XCTAssertEqual(
            FactoryNewProjectForm(spaces: spaces, heads: existing, selectedFolder: "~/code/notes", goal: "Goal", checks: [failCLI, failPlain]).checksSummary,
            "2 checks failed. Fix them, then check again."
        )

        let warnPass = FactoryNewProjectForm(spaces: spaces, heads: existing, selectedFolder: "~/code/notes", goal: "Goal", checks: [ok, warn, skip])
        XCTAssertTrue(!warnPass.hasFailedChecks && warnPass.canStart)
        XCTAssertEqual(warnPass.startHint, "Starting runs agents on your model accounts. They use your quota.")
        XCTAssertEqual(warnPass.checksSummary, "1 pass, 1 warning. You can start.")

        let allPass = FactoryNewProjectForm(spaces: spaces, heads: existing, selectedFolder: "~/code/notes", goal: "Goal", checks: [ok])
        XCTAssertTrue(allPass.canStart)
        XCTAssertEqual(allPass.startHint, "Starting runs agents on your model accounts. They use your quota.")
        XCTAssertEqual(allPass.checksSummary, "All 1 checks pass.")

        let passWithSkip = FactoryNewProjectForm(
            spaces: spaces, heads: existing, selectedFolder: "~/code/notes", goal: "Goal", checks: [ok, skip]
        )
        XCTAssertTrue(passWithSkip.canStart)
        XCTAssertEqual(passWithSkip.checksSummary, "All 2 checks pass.")
    }

    /// `POST /api/factory/projects/start` and `/check` wire shapes, 400 `results`, 403 & 503 refusals.
    func testStartAndCheckRoutesAndErrorMapping() async throws {
        StubProtocol.seen = []
        defer { StubProtocol.seen = []; StubProtocol.handler = nil }

        let checksJSON = #"{"error":"not starting; fix these first","results":[{"name":"git version","status":"ok","detail":"git 2.47"},{"name":"start codex","status":"fail","detail":"codex is not signed in","fix":"run: codex login"}]}"#
        StubProtocol.handler = { _ in (400, Data(checksJSON.utf8)) }
        do {
            try await DaemonClient.$idempotencyKey.withValue("start-1") {
                try await StubProtocol.client().startFactory(folder: "~/code/notes", goal: "A notes page with search")
            }
            XCTFail("expected FactoryStartChecksError")
        } catch let err as FactoryStartChecksError {
            let req = try XCTUnwrap(StubProtocol.seen.last)
            XCTAssertEqual(req.httpMethod, "POST")
            XCTAssertEqual(req.url?.path, "/api/factory/projects/start")
            XCTAssertEqual(req.value(forHTTPHeaderField: "Idempotency-Key"), "start-1")
            XCTAssertEqual(try JSONDecoder().decode([String: String].self, from: try XCTUnwrap(req.httpBody)), ["folder": "~/code/notes", "goal": "A notes page with search"])
            XCTAssertEqual(err.results, [
                FactoryStartCheck(name: "git version", status: "ok", detail: "git 2.47"),
                FactoryStartCheck(name: "start codex", status: "fail", detail: "codex is not signed in", fix: "run: codex login"),
            ])
        }

        StubProtocol.handler = { _ in (200, Data(#"{"results":[{"name":"git version","status":"ok","detail":"git 2.47"}]}"#.utf8)) }
        let checked = try await StubProtocol.client().checkFactory(folder: "~/code/notes")
        XCTAssertEqual(StubProtocol.seen.last?.url?.path, "/api/factory/projects/check")
        XCTAssertEqual(try JSONDecoder().decode([String: String].self, from: try XCTUnwrap(StubProtocol.seen.last?.httpBody)), ["folder": "~/code/notes"])
        XCTAssertEqual(checked, [FactoryStartCheck(name: "git version", status: "ok", detail: "git 2.47")])

        StubProtocol.handler = { _ in (200, Data(#"{"ok":true,"device_id":"dev-1"}"#.utf8)) }
        try await StubProtocol.client().startFactory(folder: "~/code/notes", goal: "A notes page with search")

        XCTAssertEqual(FactoryNewProjectForm.spacesFailureMessage(computerName: "Work Mac"), "No folders to pick: herdr cannot be reached on Work Mac. Open herdr-app on Work Mac, then try again.")
        XCTAssertEqual(FactoryNewProjectForm.startErrorMessage(DaemonError.rejected(status: 403, message: "proof_required"), computerName: "Work Mac"), "Not started: Work Mac no longer knows this phone. Pair this phone again in Settings ▸ Work Mac, then press Start again.")
        XCTAssertTrue(FactoryNewProjectForm.isFactoryUnavailable(DaemonError.rejected(status: 503, message: "factory is not available")))
    }

    /// `POST /api/factory/projects/check` must override the 8 s JSON session timeout with
    /// at least the web page's 45 s start-check budget (`web/factory/project.js` `AbortSignal.timeout(45000)`).
    func testCheckRouteUsesStartCheckTimeoutPastEightSecondSessionLimit() async throws {
        DelayedCheckProtocol.reset()
        defer { DelayedCheckProtocol.reset() }

        let config = DeviceCredentials.ephemeral(timeout: 0.15)
        config.protocolClasses = [DelayedCheckProtocol.self]
        let client = DaemonClient(endpoint: DaemonEndpoint(text: "127.0.0.1:7799")!, session: URLSession(configuration: config))

        let checked = try await client.checkFactory(folder: "~/code/notes")
        XCTAssertEqual(checked, [FactoryStartCheck(name: "git version", status: "ok", detail: "git 2.47")])
        let timeout = try XCTUnwrap(DelayedCheckProtocol.seenTimeout)
        XCTAssertGreaterThanOrEqual(timeout, 45, "check route must allow at least 45 s for start checks")
    }
}

private final class DelayedCheckProtocol: URLProtocol {
    nonisolated(unsafe) static var seenTimeout: TimeInterval?
    static func reset() { seenTimeout = nil }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        Self.seenTimeout = request.timeoutInterval
        DispatchQueue.global().asyncAfter(deadline: .now() + 0.35) {
            let url = self.request.url!
            let res = HTTPURLResponse(url: url, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: nil)!
            let body = Data(#"{"results":[{"name":"git version","status":"ok","detail":"git 2.47"}]}"#.utf8)
            self.client?.urlProtocol(self, didReceive: res, cacheStoragePolicy: .notAllowed)
            self.client?.urlProtocol(self, didLoad: body)
            self.client?.urlProtocolDidFinishLoading(self)
        }
    }

    override func stopLoading() {}
}
