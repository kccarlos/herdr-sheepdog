import XCTest
@testable import SheepdogKit

final class ConversationCacheTests: XCTestCase {
    private var directory: URL!

    override func setUpWithError() throws {
        directory = FileManager.default.temporaryDirectory.appendingPathComponent("cache-tests-\(UUID().uuidString)")
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: directory)
    }

    private func item(_ id: Int64, _ kind: ConversationItem.Kind = .shepherd) -> ConversationItem {
        // Whole milliseconds: the cache keeps `ts` to the millisecond.
        ConversationItem(id: id, ts: Date(timeIntervalSince1970: 1_790_000_000.125), kind: kind, text: "item \(id)")
    }

    func testRoundTripKeepsItemsPagingAndTheResumeID() throws {
        let cache = ConversationCache(directory: directory)
        let endpoint = DaemonEndpoint(text: "192.168.1.20:7777", token: "s3cret")!
        let page = try DaemonClient.decoder.decode(ConversationPage.self, from: Data(Fixtures.conversationPage.utf8))
        var history = ConversationHistory()
        history.applyNewest(page)
        try cache.save(ConversationSnapshot(endpoint: endpoint, history: history, lastEventID: 1_790_127_642_099_438), for: endpoint)

        let loaded = try XCTUnwrap(cache.load(for: endpoint))
        XCTAssertEqual(loaded.items.map(\.id), history.items.map(\.id))
        XCTAssertEqual(loaded.items.map(\.kind), history.items.map(\.kind))
        XCTAssertEqual(loaded.items.first { $0.kind == .approval && $0.resolution == .confirmed }?.options?.count, 3)
        XCTAssertEqual(loaded.nextBefore, page.nextBefore)
        XCTAssertEqual(loaded.lastEventID, 1_790_127_642_099_438)
        XCTAssertEqual(loaded.endpoint, "192.168.1.20:7777")
        for (a, b) in zip(loaded.items, history.items) {
            XCTAssertEqual(a.ts.timeIntervalSince1970, b.ts.timeIntervalSince1970, accuracy: 0.001)
        }
    }

    func testRoundTripKeepsTheResetSubkindAndID() throws {
        let cache = ConversationCache(directory: directory)
        let endpoint = DaemonEndpoint(text: "mac.local:7777")!
        let resetItem = ConversationItem(id: 100, ts: Date(timeIntervalSince1970: 1_790_000_000), kind: .notice, text: "New conversation", subkind: "reset", resetID: "rs-abc123")
        try cache.save(ConversationSnapshot(endpoint: endpoint, history: ConversationHistory(items: [item(1), resetItem]), lastEventID: 100), for: endpoint)

        let loaded = try XCTUnwrap(cache.load(for: endpoint))
        let loadedReset = try XCTUnwrap(loaded.items.first { $0.id == 100 })
        XCTAssertEqual(loadedReset.subkind, "reset")
        XCTAssertEqual(loadedReset.resetID, "rs-abc123")
        XCTAssertTrue(loadedReset.isResetNotice)
    }

    func testTheTokenNeverReachesTheFile() throws {
        let cache = ConversationCache(directory: directory)
        let endpoint = DaemonEndpoint(text: "mac.local:7777", token: "tok-DO-NOT-STORE")!
        try cache.save(ConversationSnapshot(endpoint: endpoint, history: ConversationHistory(items: [item(1)]), lastEventID: nil), for: endpoint)
        let text = try String(contentsOf: cache.fileURL(for: endpoint), encoding: .utf8)
        XCTAssertFalse(text.contains("tok-DO-NOT-STORE"))
        XCTAssertFalse(cache.fileURL(for: endpoint).lastPathComponent.contains("tok"))
    }

    func testKeyedByHostAndPortNotToken() {
        let a = DaemonEndpoint(text: "192.168.1.20:7777", token: "one")!
        let b = DaemonEndpoint(text: "http://192.168.1.20:7777", token: "two")!
        let c = DaemonEndpoint(text: "192.168.1.20:7799")!
        let v6 = DaemonEndpoint(text: "[fd7a::1]:7777")!
        XCTAssertEqual(ConversationCache.key(for: a), ConversationCache.key(for: b))
        XCTAssertNotEqual(ConversationCache.key(for: a), ConversationCache.key(for: c))
        XCTAssertEqual(ConversationCache.key(for: a), "192.168.1.20_7777")
        XCTAssertEqual(ConversationCache.key(for: v6), "fd7a__1_7777")
        XCTAssertEqual(ConversationCache.key(for: DaemonEndpoint(text: "Mac.Local")!), "mac.local_7777")
    }

    func testEachDaemonHasItsOwnConversation() throws {
        let cache = ConversationCache(directory: directory)
        let home = DaemonEndpoint(text: "192.168.1.20:7777")!
        let work = DaemonEndpoint(text: "100.64.0.2:7777")!
        try cache.save(ConversationSnapshot(endpoint: home, history: ConversationHistory(items: [item(1)]), lastEventID: 1), for: home)
        try cache.save(ConversationSnapshot(endpoint: work, history: ConversationHistory(items: [item(2), item(3)]), lastEventID: 3), for: work)
        XCTAssertEqual(cache.load(for: home)?.items.map(\.id), [1])
        XCTAssertEqual(cache.load(for: work)?.items.map(\.id), [2, 3])
        cache.clear(for: home)
        XCTAssertNil(cache.load(for: home))
        XCTAssertNotNil(cache.load(for: work))
    }

    func testTheSnapshotKeepsTheNewest300AndPagesOnFromThere() {
        let endpoint = DaemonEndpoint(text: "mac.local:7777")!
        let history = ConversationHistory(items: (1 ... 450).map { item(Int64($0)) }, nextBefore: nil)
        let snapshot = ConversationSnapshot(endpoint: endpoint, history: history, lastEventID: nil)
        XCTAssertEqual(snapshot.items.count, ConversationHistory.cacheCapacity)
        XCTAssertEqual(snapshot.items.first?.id, 151)
        XCTAssertEqual(snapshot.items.last?.id, 450)
        XCTAssertEqual(snapshot.nextBefore, 151, "load older continues from the oldest kept item")
    }

    func testAMissingOrForeignFileIsNoCache() throws {
        let cache = ConversationCache(directory: directory)
        let endpoint = DaemonEndpoint(text: "mac.local:7777")!
        XCTAssertNil(cache.load(for: endpoint))
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        try Data("not json".utf8).write(to: cache.fileURL(for: endpoint))
        XCTAssertNil(cache.load(for: endpoint))
    }
}
