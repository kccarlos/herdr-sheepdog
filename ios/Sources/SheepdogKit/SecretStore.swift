import Foundation
import Security

/// Somewhere to keep the daemon token. The protocol exists so the settings
/// logic can be tested without touching the real keychain.
public protocol SecretStore: AnyObject {
    func secret(for account: String) -> String?
    func setSecret(_ value: String?, for account: String)
}

/// The real thing: a `kSecClassGenericPassword` item per account, readable
/// only while the device is unlocked and never synced to iCloud.
public final class KeychainSecretStore: SecretStore {
    private let service: String

    public init(service: String = "com.kccarlos.sheepdog.daemon") {
        self.service = service
    }

    public func secret(for account: String) -> String? {
        var query = baseQuery(account)
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var item: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess,
              let data = item as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    public func setSecret(_ value: String?, for account: String) {
        let query = baseQuery(account)
        guard let value, !value.isEmpty else {
            SecItemDelete(query as CFDictionary)
            return
        }
        let data = Data(value.utf8)
        let update = [kSecValueData as String: data]
        let status = SecItemUpdate(query as CFDictionary, update as CFDictionary)
        if status == errSecItemNotFound {
            var insert = query
            insert[kSecValueData as String] = data
            insert[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
            SecItemAdd(insert as CFDictionary, nil)
        }
    }

    private func baseQuery(_ account: String) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
    }
}

/// A fake for the tests and for previews.
public final class InMemorySecretStore: SecretStore {
    public private(set) var values: [String: String] = [:]

    public init(_ values: [String: String] = [:]) {
        self.values = values
    }

    public func secret(for account: String) -> String? { values[account] }

    public func setSecret(_ value: String?, for account: String) {
        if let value, !value.isEmpty {
            values[account] = value
        } else {
            values.removeValue(forKey: account)
        }
    }
}
