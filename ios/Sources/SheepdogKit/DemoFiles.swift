import CoreGraphics
import CoreText
import Foundation
import ImageIO
import UniformTypeIdentifiers

/// The offline demo's files (`/api/agents/{pane}/files`, `/api/spaces/{id}/files`):
/// one scripted "shop" folder behind the same routes, shapes and refusals as
/// the daemon's (docs/dev/ws-contract.md "Files"), so the browser's own
/// code runs unchanged. Every other folder is a small one. Nothing is read
/// from this phone and nothing is written.
struct DemoFiles {
    enum Content {
        case dir
        case text(String, truncated: Bool = false)
        case image(String)
        /// A file of this many bytes that is not text.
        case binary(Int)
        /// Another path of the folder, or one that leaves it.
        case link(String?)
    }

    struct Node {
        var path: String
        var content: Content
        /// Seconds before now it was changed.
        var age: TimeInterval
        /// Set aside by the sensitive rule: never listed, never read.
        var hidden = false
        /// The size a listing claims, for a file the demo does not hold.
        var size: Int?

        var name: String { (path as NSString).lastPathComponent }
        var parent: String { path.contains("/") ? (path as NSString).deletingLastPathComponent : "" }
    }

    private static let minute: TimeInterval = 60
    private static let hour: TimeInterval = 3600
    private static let day: TimeInterval = 86400

    static let shop: [Node] = [
        Node(path: "README.md", content: .text(readme), age: 40 * minute),
        Node(path: "Makefile", content: .text("dev:\n\tgo run ./cmd/shop & npm --prefix web run dev\n\ntest:\n\tgo test ./... && npm --prefix web test\n"), age: 12 * day),
        Node(path: "package.json", content: .text("{\n  \"name\": \"shop-web\",\n  \"private\": true,\n  \"scripts\": { \"dev\": \"vite\", \"test\": \"vitest run\" }\n}\n"), age: 20 * day),
        Node(path: "build.log", content: .text(buildLog, truncated: true), age: 2 * hour),
        Node(path: "data.sqlite", content: .binary(4_200_000), age: 3 * day),
        Node(path: "big-photo.png", content: .image("A photo"), age: 9 * day, size: 38_000_000),
        Node(path: "notes.md", content: .link(nil), age: 5 * day),
        Node(path: "latest", content: .link("docs/reports/login-loop.md"), age: 6 * minute),
        Node(path: ".env", content: .text("SHOP_SECRET=demo\n"), age: 8 * day, hidden: true),
        Node(path: "deploy.pem", content: .text("-----demo-----\n"), age: 8 * day, hidden: true),

        Node(path: "docs", content: .dir, age: 3 * minute),
        Node(path: "docs/install.md", content: .text("# Install\n\nRun `make dev`, then open http://localhost:5173.\n"), age: 2 * day),
        Node(path: "docs/api.md", content: .text("# API\n\n| Route | What it does |\n|---|---|\n| `POST /login` | Starts a session. |\n| `GET /orders` | Lists your orders. |\n"), age: 26 * hour),
        Node(path: "docs/schema.sql", content: .text("create table orders (\n  id integer primary key,\n  customer text not null,\n  placed_at text not null\n);\n"), age: 2 * day),
        Node(path: "docs/reports", content: .dir, age: 6 * minute),
        Node(path: "docs/reports/login-loop.md", content: .text(loginLoop), age: 6 * minute),
        Node(path: "docs/images", content: .dir, age: 6 * minute),
        Node(path: "docs/images/login-flow.png", content: .image("Path=/ so both pages see it"), age: 6 * minute),
        Node(path: "docs/images/architecture.png", content: .image("The shop: web, API, database"), age: 8 * day),

        Node(path: "internal", content: .dir, age: 9 * minute),
        Node(path: "internal/auth", content: .dir, age: 9 * minute),
        Node(path: "internal/auth/token.go", content: .text(tokenGo), age: 9 * minute),
        Node(path: "internal/auth/token_test.go", content: .text(tokenTestGo), age: 9 * minute),
        Node(path: "internal/web", content: .dir, age: 30 * minute),
        Node(path: "internal/web/api.go", content: .text(apiGo), age: 30 * minute),

        Node(path: "web", content: .dir, age: day),
        Node(path: "web/login.tsx", content: .text("export function Login() {\n  return <form action=\"/login\" method=\"post\" />\n}\n"), age: day),
    ]

    static let small: [Node] = [
        Node(path: "README.md", content: .text("# Notes\n\nNothing here yet.\n"), age: 4 * day),
        Node(path: "ideas.txt", content: .text("1. A calmer inbox\n2. Fewer settings\n"), age: 4 * day),
    ]

    // MARK: Routes

    /// What a files route answers: its status and body, or an image's bytes.
    static func respond(nodes: [Node], rootName: String, path: String, isRead: Bool, query: String?, raw: Bool, now: Date) -> DemoDaemon.Response {
        if !isRead { return list(nodes: nodes, rootName: rootName, path: path, query: query, now: now) }
        return read(nodes: nodes, path: path, raw: raw, now: now)
    }

    private static func list(nodes: [Node], rootName: String, path: String, query: String?, now: Date) -> DemoDaemon.Response {
        guard path.isEmpty || nodes.contains(where: { $0.path == path && $0.isDirectory }) else {
            return refusal(404, "not_found", "not found")
        }
        if path.split(separator: "/").contains(where: { name in nodes.contains { $0.hidden && $0.name == name } }) {
            return refusal(403, "sensitive", "hidden for safety")
        }
        let prefix = path.isEmpty ? "" : path + "/"
        let under = nodes.filter { $0.path.hasPrefix(prefix) && $0.path != path }
        let shown: [Node]
        var hidden = 0
        if let query, !query.isEmpty {
            let matches = under.filter { $0.name.range(of: query, options: .caseInsensitive) != nil }
            hidden = matches.filter(\.hidden).count
            shown = matches.filter { !$0.hidden }.sorted { a, b in
                let (da, db) = (a.path.split(separator: "/").count, b.path.split(separator: "/").count)
                if da != db { return da < db }
                if a.isDirectory != b.isDirectory { return a.isDirectory }
                return a.name.localizedCaseInsensitiveCompare(b.name) == .orderedAscending
            }
        } else {
            let children = under.filter { $0.parent == path }
            hidden = children.filter(\.hidden).count
            shown = children.filter { !$0.hidden }.sorted { a, b in
                if a.isDirectory != b.isDirectory { return a.isDirectory }
                return a.name.localizedCaseInsensitiveCompare(b.name) == .orderedAscending
            }
        }
        let formatter = ISO8601DateFormatter()
        let entries: [[String: Any]] = shown.map { node in
            var entry: [String: Any] = [
                "name": node.name, "path": node.path, "kind": node.kindName, "size": node.byteCount,
                "modified": formatter.string(from: now.addingTimeInterval(-node.age)),
            ]
            if case let .link(target) = node.content, target == nil { entry["blocked"] = true }
            return entry
        }
        return json(200, ["root_name": rootName, "path": path, "truncated": false, "hidden": hidden, "entries": entries])
    }

    private static func read(nodes: [Node], path: String, raw: Bool, now: Date) -> DemoDaemon.Response {
        guard var node = nodes.first(where: { $0.path == path }) else { return refusal(404, "not_found", "not found") }
        if path.split(separator: "/").contains(where: { name in nodes.contains { $0.hidden && $0.name == name } }) {
            return refusal(403, "sensitive", "hidden for safety")
        }
        if case let .link(target) = node.content {
            guard let target, let found = nodes.first(where: { $0.path == target }) else { return refusal(403, "outside", "that leaves the folder") }
            node = found
        }
        let modified = ISO8601DateFormatter().string(from: now.addingTimeInterval(-node.age))
        switch node.content {
        case .dir, .link:
            return refusal(403, "not_file", "not a regular file")
        case let .text(text, truncated):
            if raw { return refusal(415, "not_image", "not an image") }
            return json(200, ["path": path, "size": text.utf8.count, "modified": modified, "kind": "text", "text": text, "truncated": truncated])
        case .binary(let bytes):
            if raw { return refusal(415, "not_image", "not an image") }
            return json(200, ["path": path, "size": bytes, "modified": modified, "kind": "binary", "truncated": false])
        case let .image(caption):
            if !raw {
                return json(200, ["path": path, "size": node.size ?? DemoImage.png(caption).count, "modified": modified, "kind": "image", "truncated": false])
            }
            if let size = node.size, size > 16 << 20 { return refusal(413, "too_large", "too large to show") }
            return DemoDaemon.Response(status: 200, body: DemoImage.png(caption), contentType: "image/png")
        }
    }

    private static func json(_ status: Int, _ object: [String: Any]) -> DemoDaemon.Response {
        DemoDaemon.Response(status: status, body: (try? JSONSerialization.data(withJSONObject: object)) ?? Data())
    }

    private static func refusal(_ status: Int, _ reason: String, _ message: String) -> DemoDaemon.Response {
        json(status, ["error": message, "reason": reason])
    }

    // MARK: Words

    private static let readme = """
    # shop

    A small web shop: a Go API and a React storefront.

    ## Run it

    - `make dev` starts both.
    - `make test` runs the tests.

    See [the install notes](docs/install.md) for the rest.
    """

    private static let buildLog = """
    $ go build ./...
    ok  \tshop/internal/auth\t0.412s
    ok  \tshop/internal/web\t0.288s
    $ npm --prefix web run build
    vite v5.2.0 building for production...
    ✓ 38 modules transformed.
    """

    static let loginLoop = """
    # Login redirect loop

    Signing in sent people back to /login, again and again. The session cookie was set on /app, so /login never saw it.

    ## What changed

    - The cookie path is now `/`.
    - See [internal/auth/token.go](../../internal/auth/token.go).
    - A test covers the redirect.

    ## Results

    | Suite | Passed |
    |---|---|
    | auth | 18 |
    | web | 42 |

    ![Login flow](../images/login-flow.png)
    """

    private static let tokenGo = """
    package auth

    import (
    \t"strings"
    \t"time"
    )

    const maxTokenLen = 512

    // Check reports whether tok is a valid session token.
    func Check(tok string) error {
    \tif strings.TrimSpace(tok) == "" {
    \t\treturn errNoToken
    \t}
    \tif len(tok) > maxTokenLen {
    \t\treturn errTooLong
    \t}
    \treturn verify(tok, time.Now())
    }
    """

    private static let tokenTestGo = """
    package auth

    func TestEmptyToken(t *testing.T) {
    \tif Check("") == nil {
    \t\tt.Fatal("an empty token passed")
    \t}
    }
    """

    private static let apiGo = """
    package web

    func handle(w http.ResponseWriter, r *http.Request) {
    \ttok := r.Header.Get("Authorization")
    \tif err := auth.Check(tok); err != nil {
    \t\thttp.Error(w, "unauthorized", http.StatusUnauthorized)
    \t\treturn
    \t}
    \tserve(w, r)
    }
    """
}

private extension DemoFiles.Node {
    var isDirectory: Bool {
        if case .dir = content { return true }
        return false
    }

    var kindName: String {
        switch content {
        case .dir: "dir"
        case .link: "link"
        default: "file"
        }
    }

    /// A file's size; 0 for a folder or a link.
    var byteCount: Int {
        if let size { return size }
        switch content {
        case let .text(text, _): return text.utf8.count
        case let .binary(bytes): return bytes
        case let .image(caption): return DemoImage.png(caption).count
        case .dir, .link: return 0
        }
    }
}

/// A picture drawn on the spot, so the demo has an image to show without
/// bundling one: three boxes and two arrows, with a caption.
enum DemoImage {
    nonisolated(unsafe) private static var cache: [String: Data] = [:]
    private static let lock = NSLock()

    static func png(_ caption: String) -> Data {
        lock.lock()
        defer { lock.unlock() }
        if let cached = cache[caption] { return cached }
        let data = draw(caption)
        cache[caption] = data
        return data
    }

    private static func draw(_ caption: String) -> Data {
        let width = 960, height = 540
        guard let context = CGContext(data: nil, width: width, height: height, bitsPerComponent: 8, bytesPerRow: 0,
                                      space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return Data() }
        context.setFillColor(CGColor(red: 1, green: 1, blue: 1, alpha: 1))
        context.fill(CGRect(x: 0, y: 0, width: width, height: height))
        let boxes: [(String, CGColor)] = [
            ("/login", CGColor(red: 0.99, green: 0.95, blue: 0.86, alpha: 1)),
            ("cookie", CGColor(red: 0.95, green: 0.93, blue: 0.91, alpha: 1)),
            ("/app", CGColor(red: 0.87, green: 0.93, blue: 0.89, alpha: 1)),
        ]
        let ink = CGColor(red: 0.12, green: 0.12, blue: 0.13, alpha: 1)
        for (index, box) in boxes.enumerated() {
            let rect = CGRect(x: 70 + index * 300, y: 250, width: 220, height: 140)
            context.setFillColor(box.1)
            context.addPath(CGPath(roundedRect: rect, cornerWidth: 20, cornerHeight: 20, transform: nil))
            context.fillPath()
            context.setStrokeColor(ink)
            context.setLineWidth(3)
            context.addPath(CGPath(roundedRect: rect, cornerWidth: 20, cornerHeight: 20, transform: nil))
            context.strokePath()
            text(box.0, size: 34, bold: true, color: ink, centeredAt: CGPoint(x: rect.midX, y: rect.midY - 12), in: context)
            if index < 2 {
                let y = rect.midY
                context.setStrokeColor(ink)
                context.move(to: CGPoint(x: rect.maxX + 8, y: y))
                context.addLine(to: CGPoint(x: rect.maxX + 62, y: y))
                context.move(to: CGPoint(x: rect.maxX + 48, y: y + 12))
                context.addLine(to: CGPoint(x: rect.maxX + 62, y: y))
                context.addLine(to: CGPoint(x: rect.maxX + 48, y: y - 12))
                context.strokePath()
            }
        }
        text(caption, size: 28, bold: false, color: CGColor(red: 0.42, green: 0.43, blue: 0.46, alpha: 1), centeredAt: CGPoint(x: width / 2, y: 150), in: context)
        guard let image = context.makeImage() else { return Data() }
        let output = NSMutableData()
        guard let destination = CGImageDestinationCreateWithData(output, UTType.png.identifier as CFString, 1, nil) else { return Data() }
        CGImageDestinationAddImage(destination, image, nil)
        CGImageDestinationFinalize(destination)
        return output as Data
    }

    private static func text(_ string: String, size: CGFloat, bold: Bool, color: CGColor, centeredAt point: CGPoint, in context: CGContext) {
        let font = CTFontCreateWithName((bold ? "Helvetica-Bold" : "Helvetica") as CFString, size, nil)
        let attributes: [CFString: Any] = [kCTFontAttributeName: font, kCTForegroundColorAttributeName: color]
        let line = CTLineCreateWithAttributedString(CFAttributedStringCreate(nil, string as CFString, attributes as CFDictionary))
        let bounds = CTLineGetBoundsWithOptions(line, [])
        context.textPosition = CGPoint(x: point.x - bounds.width / 2, y: point.y)
        CTLineDraw(line, context)
    }
}
