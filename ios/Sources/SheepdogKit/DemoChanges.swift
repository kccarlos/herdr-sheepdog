import Foundation

/// The offline demo's changes view and attachments: one scripted set of
/// changes for every agent, and uploads that are answered with a made-up
/// path and never stored.
extension DemoDaemon {
    /// The report the demo's files hold, as a new file in the changes.
    private static let reportLines = DemoFiles.loginLoop.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)

    static let demoChanges = AgentChanges(
        repo: true, root: "/Users/demo/code/shop", branch: "fix-login",
        files: [
            ChangedFile(path: "internal/auth/token.go", status: "modified", added: 9, deleted: 3),
            ChangedFile(path: "internal/web/api.go", status: "modified", added: 4, deleted: 1),
            ChangedFile(path: "internal/auth/token_test.go", status: "untracked", added: 18),
            ChangedFile(path: "docs/reports/login-loop.md", status: "untracked", added: reportLines.count),
            ChangedFile(path: "docs/images/login-flow.png", status: "untracked", binary: true),
        ],
        added: 31 + reportLines.count, deleted: 4
    )

    static let demoDiffs: [String: String] = [
        "docs/images/login-flow.png": "",
        "docs/reports/login-loop.md": """
        diff --git a/docs/reports/login-loop.md b/docs/reports/login-loop.md
        new file mode 100644
        --- /dev/null
        +++ b/docs/reports/login-loop.md
        @@ -0,0 +1,\(reportLines.count) @@
        \(reportLines.map { "+" + $0 }.joined(separator: "\n"))

        """,
        "internal/auth/token.go": """
        diff --git a/internal/auth/token.go b/internal/auth/token.go
        --- a/internal/auth/token.go
        +++ b/internal/auth/token.go
        @@ -12,9 +12,15 @@ import (
         // Check reports whether tok is a valid session token.
         func Check(tok string) error {
        -\tif tok == "" {
        -\t\treturn nil
        +\tif strings.TrimSpace(tok) == "" {
        +\t\treturn errNoToken
         \t}
        -\treturn verify(tok)
        +\tif len(tok) > maxTokenLen {
        +\t\treturn errTooLong
        +\t}
        +\treturn verify(tok, time.Now())
         }

        """,
        "internal/web/api.go": """
        diff --git a/internal/web/api.go b/internal/web/api.go
        --- a/internal/web/api.go
        +++ b/internal/web/api.go
        @@ -88,6 +88,9 @@ func handle(w http.ResponseWriter, r *http.Request) {
         \ttok := r.Header.Get("Authorization")
        -\tlog.Printf("token %s", tok)
        +\tif err := auth.Check(tok); err != nil {
        +\t\thttp.Error(w, "unauthorized", http.StatusUnauthorized)
        +\t\treturn
        +\t}
         \tserve(w, r)
         }

        """,
        "internal/auth/token_test.go": """
        diff --git a/internal/auth/token_test.go b/internal/auth/token_test.go
        new file mode 100644
        --- /dev/null
        +++ b/internal/auth/token_test.go
        @@ -0,0 +1,6 @@
        +package auth
        +
        +func TestEmptyToken(t *testing.T) {
        +\tif Check("") == nil {
        +\t\tt.Fatal("an empty token passed")
        +\t}

        """,
    ]

    func demoPhase4(parts: [String], method: String, body: Data) -> Response {
        if parts[1] == "attachments" {
            guard method == "POST" else { return json(["error": "method not allowed"], status: 405) }
            let name = "20261020-101500-3fa2c1-photo.jpg"
            return json(["path": "/Users/demo/.local/share/sheepdog/attachments/demo/\(name)", "name": name, "bytes": body.count, "type": "image/jpeg"], status: 201)
        }
        if parts.count == 2 {
            return encoded(Self.demoChanges)
        }
        let path = parts[2...].joined(separator: "/").removingPercentEncoding ?? ""
        guard let file = Self.demoChanges.files.first(where: { $0.path == path }), let diff = Self.demoDiffs[path] else {
            return json(["error": "no changes to that file"], status: 404)
        }
        return encoded(FileDiff(path: file.path, status: file.status, binary: file.binary, diff: diff))
    }
}
