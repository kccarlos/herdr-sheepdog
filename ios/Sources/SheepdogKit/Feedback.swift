import Foundation

/// The user's mark on a moment of the conversation ("that paused"), for
/// the daemon's `POST /api/feedback` (feature `feedback`, daemon
/// docs/ws-contract.md "Feedback and drains"). The daemon keeps a
/// snapshot of the last 30 s with it, and `sheepdog feedback` says
/// whether the pause was the voice model, the network or this app.
public enum FeedbackTag: String, CaseIterable, Identifiable, Sendable {
    case paused
    case wrongAnswer = "wrong_answer"
    case tooShort = "too_short"
    case tooLong = "too_long"
    case misheard
    case other

    public var id: String { rawValue }

    /// The button's words.
    public var label: String {
        switch self {
        case .paused: return "Paused"
        case .wrongAnswer: return "Wrong answer"
        case .tooShort: return "Too short"
        case .tooLong: return "Too long"
        case .misheard: return "Misheard me"
        case .other: return "Other"
        }
    }

    public var systemImage: String {
        switch self {
        case .paused: return "pause.circle"
        case .wrongAnswer: return "xmark.circle"
        case .tooShort: return "text.badge.minus"
        case .tooLong: return "text.badge.plus"
        case .misheard: return "ear"
        case .other: return "ellipsis.circle"
        }
    }

    /// The feature `bot-ready` lists when the daemon takes marks.
    public static let feature = "feedback"
}

/// The body of `POST /api/feedback`.
public struct FeedbackMark: Encodable, Equatable, Sendable {
    public var tag: FeedbackTag
    /// Optional; trimmed, and left out when empty.
    public var note: String?
    /// When the user tapped (the moment marked, not when it was sent).
    public var at: Date
    /// The conversation row on screen, if any.
    public var conversationTurn: String?

    public init(tag: FeedbackTag, note: String? = nil, at: Date, conversationTurn: String? = nil) {
        self.tag = tag
        let trimmed = note?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        self.note = trimmed.isEmpty ? nil : String(trimmed.prefix(500))
        self.at = at
        self.conversationTurn = conversationTurn
    }

    enum CodingKeys: String, CodingKey {
        case tag, note
        case clientTS = "client_ts"
        case conversationTurn = "conversation_turn"
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(tag.rawValue, forKey: .tag)
        try c.encodeIfPresent(note, forKey: .note)
        try c.encode(WSMessage.rfc3339Milliseconds(at), forKey: .clientTS)
        try c.encodeIfPresent(conversationTurn, forKey: .conversationTurn)
    }
}

extension DaemonClient {
    /// `POST /api/feedback` — logs the mark with the daemon's snapshot.
    /// Returns its id, or `nil` from a daemon without the route (404).
    public func sendFeedback(_ mark: FeedbackMark) async throws -> String? {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        let request = try request(path: "/api/feedback", method: "POST", body: try encoder.encode(mark))
        let (data, response) = try await perform(request)
        guard let http = response as? HTTPURLResponse else { throw DaemonError.malformed("no HTTP response") }
        let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
        switch http.statusCode {
        case 200 ..< 300:
            return (object?["id"] as? String) ?? ""
        case 404:
            return nil
        case 401:
            throw DaemonError.unauthorized(endpoint: endpoint.display)
        default:
            throw DaemonError.rejected(status: http.statusCode, message: (object?["error"] as? String) ?? "")
        }
    }
}
