import Foundation

/// What the phone remembers of one daemon's conversation, so the app opens
/// on the last conversation at once — before the network, or without it.
///
/// No secrets: the endpoint is stored as `host:port` and the token never
/// enters the file.
public struct ConversationSnapshot: Codable, Equatable, Sendable {
    public static let currentVersion = 1

    public var version: Int
    /// `host:port`, for a person reading the file; the file name is the key.
    public var endpoint: String
    public var items: [ConversationItem]
    public var nextBefore: Int64?
    /// The last `/events` id seen, to resume the stream after a relaunch.
    public var lastEventID: Int64?
    public var savedAt: Date

    enum CodingKeys: String, CodingKey {
        case version, endpoint, items
        case nextBefore = "next_before"
        case lastEventID = "last_event_id"
        case savedAt = "saved_at"
    }

    public init(endpoint: String, items: [ConversationItem], nextBefore: Int64?, lastEventID: Int64?, savedAt: Date = Date()) {
        self.version = ConversationSnapshot.currentVersion
        self.endpoint = endpoint
        self.items = items
        self.nextBefore = nextBefore
        self.lastEventID = lastEventID
        self.savedAt = savedAt
    }

    /// The newest `capacity` items of a history. When items are cut, the
    /// oldest kept one is where "load older" continues.
    public init(endpoint: DaemonEndpoint, history: ConversationHistory, lastEventID: Int64?, capacity: Int = ConversationHistory.cacheCapacity, savedAt: Date = Date()) {
        let items = Array(history.items.suffix(capacity))
        let cut = history.items.count > items.count
        self.init(
            endpoint: endpoint.display,
            items: items,
            nextBefore: cut ? items.first?.id : history.nextBefore,
            lastEventID: lastEventID,
            savedAt: savedAt
        )
    }

    public var history: ConversationHistory {
        ConversationHistory(items: items, nextBefore: nextBefore)
    }
}

/// One small JSON file per daemon endpoint in Application Support.
public struct ConversationCache: Sendable {
    public let directory: URL

    public init(directory: URL) {
        self.directory = directory
    }

    /// `Application Support/Conversations`.
    public static func standard() -> ConversationCache {
        let base = (try? FileManager.default.url(for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true))
            ?? FileManager.default.temporaryDirectory
        return ConversationCache(directory: base.appendingPathComponent("Conversations", isDirectory: true))
    }

    /// The file-name key for an endpoint: host and port, with anything that
    /// is not a plain file-name character replaced. The token is not part
    /// of it: the same daemon with a new token is the same conversation.
    public static func key(for endpoint: DaemonEndpoint) -> String {
        let raw = "\(endpoint.host.lowercased())_\(endpoint.port)"
        let allowed = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyz0123456789.-_")
        let scalars = raw.unicodeScalars.map { allowed.contains($0) ? Character($0) : "_" }
        return String(scalars)
    }

    public func fileURL(for endpoint: DaemonEndpoint) -> URL {
        directory.appendingPathComponent("\(ConversationCache.key(for: endpoint)).json")
    }

    public func load(for endpoint: DaemonEndpoint) -> ConversationSnapshot? {
        guard let data = try? Data(contentsOf: fileURL(for: endpoint)),
              let snapshot = try? ConversationCache.decoder.decode(ConversationSnapshot.self, from: data),
              snapshot.version == ConversationSnapshot.currentVersion
        else { return nil }
        return snapshot
    }

    public func save(_ snapshot: ConversationSnapshot, for endpoint: DaemonEndpoint) throws {
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        var url = fileURL(for: endpoint)
        let data = try ConversationCache.encoder.encode(snapshot)
        try data.write(to: url, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        // A cache: the daemon has the record, so it need not be in backups.
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try? url.setResourceValues(values)
    }

    public func clear(for endpoint: DaemonEndpoint) {
        try? FileManager.default.removeItem(at: fileURL(for: endpoint))
    }

    static let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.dateEncodingStrategy = .custom { date, encoder in
            var c = encoder.singleValueContainer()
            try c.encode(ISO8601DateFormatter.sheepdog.string(from: date))
        }
        e.outputFormatting = [.sortedKeys]
        return e
    }()

    static var decoder: JSONDecoder { DaemonClient.decoder }
}
