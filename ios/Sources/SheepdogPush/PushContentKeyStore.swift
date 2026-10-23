import Foundation
import Security

/// Where the content key lives: one keychain item in the app group's
/// keychain access group, so the Notification Service Extension can read
/// what the app stored. Readable after the first unlock since boot (a push
/// arrives while the phone is locked), never synced or migrated to another
/// device. The app writes it after every push registration with its daemon
/// and deletes it when the phone leaves that daemon; the extension only
/// reads.
public final class PushContentKeyStore {
    /// The app group both targets share (project.yml `SHEEPDOG_APP_GROUP`,
    /// read from the `SheepdogAppGroup` Info.plist key of the running
    /// bundle, which the app and the extension both carry).
    public static var appGroup: String {
        let value = Bundle.main.object(forInfoDictionaryKey: "SheepdogAppGroup") as? String
        guard let value, value.hasPrefix("group.") else { return "group.com.github.kccarlos.herdr-sheepdog-ios" }
        return value
    }

    private let service: String
    private let account: String
    private let accessGroup: String?

    /// `accessGroup` nil uses the target's default access group (tests).
    public init(service: String = "com.github.kccarlos.herdr-sheepdog-ios.push",
                account: String = "content-key",
                accessGroup: String? = PushContentKeyStore.appGroup) {
        self.service = service
        self.account = account
        self.accessGroup = accessGroup
    }

    /// The 32-byte key, or nil when none is stored (or it is damaged).
    public func key() -> Data? {
        var query = baseQuery()
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var item: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess,
              let data = item as? Data, data.count == 32 else { return nil }
        return data
    }

    /// Stores the daemon's `content_key` (standard base64 of 32 bytes).
    /// Returns false for a value that is not that, leaving the old key.
    @discardableResult
    public func store(base64 value: String) -> Bool {
        guard let data = Data(base64Encoded: value), data.count == 32 else { return false }
        return store(data)
    }

    @discardableResult
    public func store(_ data: Data) -> Bool {
        guard data.count == 32 else { return false }
        if key() == data { return true }
        let query = baseQuery()
        let update: [String: Any] = [
            kSecValueData as String: data,
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
        ]
        var status = SecItemUpdate(query as CFDictionary, update as CFDictionary)
        if status == errSecItemNotFound {
            var insert = query
            insert[kSecValueData as String] = data
            insert[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            status = SecItemAdd(insert as CFDictionary, nil)
        }
        return status == errSecSuccess
    }

    public func remove() {
        SecItemDelete(baseQuery() as CFDictionary)
    }

    // MARK: - Several computers

    /// The account of one computer's key: each paired computer issues its
    /// own content key, so each has its own item.
    public static func account(computer: String) -> String { "content-key.\(computer)" }

    /// The store for one computer's key.
    public static func forComputer(_ id: String, accessGroup: String? = PushContentKeyStore.appGroup) -> PushContentKeyStore {
        PushContentKeyStore(account: account(computer: id), accessGroup: accessGroup)
    }

    /// Every content key stored under this service (one per computer): the
    /// extension tries each, since a key that is not the sender's fails
    /// AES-GCM's check.
    public func allKeys() -> [Data] {
        var query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitAll,
        ]
        if let accessGroup { query[kSecAttrAccessGroup as String] = accessGroup }
        var items: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &items) == errSecSuccess else { return [] }
        let list = (items as? [Data]) ?? ((items as? Data).map { [$0] } ?? [])
        return list.filter { $0.count == 32 }
    }

    private func baseQuery() -> [String: Any] {
        var query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        if let accessGroup { query[kSecAttrAccessGroup as String] = accessGroup }
        return query
    }
}

/// The paired computers' names for the Notification Service Extension,
/// which adds the computer's name to an alert only when several are
/// paired: the app writes them to the App Group's defaults (an empty map
/// with one computer), the extension only reads.
public enum PushComputerNames {
    static let key = "push.computerNames"

    public static func write(_ names: [String: String], suite: String = PushContentKeyStore.appGroup) {
        let defaults = UserDefaults(suiteName: suite)
        if names.count > 1 {
            defaults?.set(names, forKey: key)
        } else {
            defaults?.removeObject(forKey: key)
        }
    }

    public static func read(suite: String = PushContentKeyStore.appGroup) -> [String: String] {
        UserDefaults(suiteName: suite)?.dictionary(forKey: key) as? [String: String] ?? [:]
    }
}
