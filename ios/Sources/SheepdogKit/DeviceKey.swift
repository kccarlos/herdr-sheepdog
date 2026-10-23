import Foundation
import Security

/// This phone's key for one computer (docs/dev/design/security.md "Key
/// storage"): a P-256 key in the Secure Enclave, tagged with the daemon id,
/// usable after the first unlock (so a notification's Approve works on a
/// locked phone), never in backups or iCloud. The Simulator has no Secure
/// Enclave and gets a software key with the same attributes.
public enum DeviceKey {
    /// The keychain tag of the key for a daemon.
    public static func tag(for daemonID: String) -> String { "sheepdog.device.\(daemonID)" }

    /// True when keys live in the Secure Enclave (the enroll call's
    /// `hardware`).
    public static var isHardware: Bool {
        #if targetEnvironment(simulator)
        return false
        #else
        return true
        #endif
    }

    /// The key for daemonID, nil when there is none (a new or restored
    /// phone).
    public static func load(daemonID: String) -> SecKey? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassKey,
            kSecAttrApplicationTag as String: Data(tag(for: daemonID).utf8),
            kSecAttrKeyType as String: kSecAttrKeyTypeECSECPrimeRandom,
            kSecReturnRef as String: true,
        ]
        var item: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess, let item else { return nil }
        return (item as! SecKey) // swiftlint:disable:this force_cast
    }

    /// The key for daemonID, made when there is none.
    public static func loadOrCreate(daemonID: String) throws -> SecKey {
        if let key = load(daemonID: daemonID) { return key }
        var error: Unmanaged<CFError>?
        guard let access = SecAccessControlCreateWithFlags(nil, kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly, isHardware ? .privateKeyUsage : [], &error) else {
            throw error!.takeRetainedValue() as Error
        }
        var attributes: [String: Any] = [
            kSecAttrKeyType as String: kSecAttrKeyTypeECSECPrimeRandom,
            kSecAttrKeySizeInBits as String: 256,
            kSecPrivateKeyAttrs as String: [
                kSecAttrIsPermanent as String: true,
                kSecAttrApplicationTag as String: Data(tag(for: daemonID).utf8),
                kSecAttrAccessControl as String: access,
            ] as [String: Any],
        ]
        if isHardware {
            attributes[kSecAttrTokenID as String] = kSecAttrTokenIDSecureEnclave
        }
        guard let key = SecKeyCreateRandomKey(attributes as CFDictionary, &error) else {
            throw error!.takeRetainedValue() as Error
        }
        return key
    }

    /// The public key as the daemon wants it: the 65-byte uncompressed
    /// point, base64url.
    public static func publicKey(_ key: SecKey) -> String? {
        guard let pub = SecKeyCopyPublicKey(key),
              let raw = SecKeyCopyExternalRepresentation(pub, nil) as Data?
        else { return nil }
        return SPKIPin.base64url(raw)
    }

    /// An ECDSA P-256 SHA-256 signature (ASN.1 DER) of text.
    public static func sign(_ key: SecKey, _ text: String) throws -> Data {
        var error: Unmanaged<CFError>?
        guard let sig = SecKeyCreateSignature(key, .ecdsaSignatureMessageX962SHA256, Data(text.utf8) as CFData, &error) as Data? else {
            throw error!.takeRetainedValue() as Error
        }
        return sig
    }

    /// Deletes the key for daemonID (Remove, or a new computer at that id).
    public static func delete(daemonID: String) {
        let query: [String: Any] = [
            kSecClass as String: kSecClassKey,
            kSecAttrApplicationTag as String: Data(tag(for: daemonID).utf8),
        ]
        SecItemDelete(query as CFDictionary)
    }
}

/// What a device signs for a session (docs/dev/ws-contract.md "Auth"): the
/// daemon id and pin bind it to this computer, the nonce makes it one-time.
public enum SignedText {
    public static func session(daemonID: String, pin: String, deviceID: String, nonce: String, unix: Int64) -> String {
        ["sheepdog-auth-v1", daemonID, pin, deviceID, nonce, String(unix), "session"].joined(separator: "\n")
    }
}
