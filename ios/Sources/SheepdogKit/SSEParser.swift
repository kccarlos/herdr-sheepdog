import Foundation

/// Line-by-line parser for the daemon's `GET /events` stream.
///
/// The daemon writes `id: <seq>`, `event: log`, `data: <the event JSON>`,
/// a blank line between records, `: keepalive` comments, and
/// `event: closed` when it is shutting down or dropped us as a slow
/// subscriber. Pure, so the whole thing is unit-tested without a socket.
public struct SSEParser: Sendable {
    public enum Output: Equatable, Sendable {
        case event(DaemonEvent)
        /// The daemon closed the stream on purpose: `shutdown` or `dropped`.
        case closed(reason: String)
        /// A `data:` line that did not decode; kept so the caller can log it.
        case undecodable(String)
    }

    private var eventName = ""
    private var data = ""
    private var lastID: UInt64 = 0

    /// The highest `id:` seen, for `?since=` on a reconnect.
    public var lastSeq: UInt64 { lastID }

    public init(lastSeq: UInt64 = 0) {
        self.lastID = lastSeq
    }

    /// Feeds one line (without its newline). Returns a record when the line
    /// ended one.
    public mutating func push(line: String) -> Output? {
        if line.isEmpty { return flush() }
        if line.hasPrefix(":") { return nil } // a comment, e.g. ": keepalive"
        let (field, value) = SSEParser.split(line)
        switch field {
        case "id":
            if let n = UInt64(value) { lastID = max(lastID, n) }
        case "event":
            eventName = value
        case "data":
            if !data.isEmpty { data += "\n" }
            data += value
        default:
            break
        }
        return nil
    }

    private mutating func flush() -> Output? {
        defer {
            eventName = ""
            data = ""
        }
        guard !data.isEmpty else { return nil }
        if eventName == "closed" {
            let reason = (try? JSONSerialization.jsonObject(with: Data(data.utf8))) as? [String: Any]
            return .closed(reason: (reason?["reason"] as? String) ?? "closed")
        }
        guard let event = SSEParser.decode(data, seq: lastID) else { return .undecodable(data) }
        return .event(event)
    }

    private static func split(_ line: String) -> (String, String) {
        guard let colon = line.firstIndex(of: ":") else { return (line, "") }
        let field = String(line[line.startIndex ..< colon])
        var rest = line[line.index(after: colon)...]
        if rest.first == " " { rest = rest.dropFirst() }
        return (field, String(rest))
    }

    /// Decodes one `data:` payload — the daemon's `eventlog.Event` JSON.
    public static func decode(_ json: String, seq: UInt64) -> DaemonEvent? {
        guard
            let object = (try? JSONSerialization.jsonObject(with: Data(json.utf8))) as? [String: Any],
            let type = object["type"] as? String
        else { return nil }
        var payload: [String: JSONValue] = [:]
        if let raw = object["payload"] as? [String: Any],
           let data = try? JSONSerialization.data(withJSONObject: raw),
           let decoded = try? JSONDecoder().decode([String: JSONValue].self, from: data) {
            payload = decoded
        }
        var timestamp: Date?
        if let ts = object["ts"] as? String { timestamp = ISO8601DateFormatter.sheepdog.date(from: ts) }
        return DaemonEvent(
            seq: seq,
            ts: timestamp,
            source: (object["source"] as? String) ?? "",
            type: type,
            paneID: object["pane_id"] as? String,
            payload: payload
        )
    }
}

extension ISO8601DateFormatter {
    /// The daemon writes RFC 3339 with fractional seconds.
    static let sheepdog: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()
}
