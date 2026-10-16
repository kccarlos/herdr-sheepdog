import CryptoKit
import Foundation
import UserNotifications

// End-to-end encrypted notifications through the shared push relay
// (herdr-sheepdog `docs/dev/push.md`, "End-to-end encryption"). The daemon
// seals the same `{aps, sheepdog}` JSON the direct APNs path sends with
// AES-256-GCM under this phone's content key; APNs delivers a placeholder
// alert ("Sheepdog" / "New update", `mutable-content: 1`) carrying
// `e2e: {c, n}`, and the Notification Service Extension swaps in the real
// alert. This file is compiled into both SheepdogKit (for the tests) and
// the extension, which links nothing else: no networking, no SheepdogKit.

public enum PushE2E {
    /// What the extension shows when it cannot decrypt: never the raw data.
    public static let fallbackTitle = "Sheepdog"
    public static let fallbackBody = "Open the app to see the update."

    /// The notification categories (PushActions in SheepdogKit uses the
    /// same strings): a fallback gets the plain one, so no Approve /
    /// Decline buttons appear on a notification the app cannot answer.
    static let agentCategory = "AGENT"

    public enum OpenError: Error, Equatable {
        case badKey
        case malformed
        case authentication
        case notJSON
    }

    /// Decrypts `e2e.c` (standard base64: ciphertext with the 16-byte GCM
    /// tag appended, Go's `gcm.Seal` output) with `e2e.n` (standard base64,
    /// the 12-byte nonce) under the 32-byte content key, and parses the
    /// plaintext JSON object.
    public static func open(ciphertext c: String, nonce n: String, key: Data) throws -> [String: Any] {
        guard key.count == 32 else { throw OpenError.badKey }
        guard let sealed = Data(base64Encoded: c), let nonceData = Data(base64Encoded: n),
              sealed.count > 16, nonceData.count == 12,
              let nonce = try? AES.GCM.Nonce(data: nonceData),
              let box = try? AES.GCM.SealedBox(nonce: nonce, ciphertext: sealed.dropLast(16), tag: sealed.suffix(16))
        else { throw OpenError.malformed }
        let plaintext: Data
        do {
            plaintext = try AES.GCM.open(box, using: SymmetricKey(data: key))
        } catch {
            throw OpenError.authentication
        }
        guard let object = (try? JSONSerialization.jsonObject(with: plaintext)) as? [String: Any] else { throw OpenError.notJSON }
        return object
    }

    /// The notification to show for `content` as delivered:
    /// - without `e2e` (the direct APNs path, a self-built app): unchanged;
    /// - with `e2e` that opens under `key`: the plaintext's alert, thread,
    ///   category and `aps` / `sheepdog` dictionaries, so the app's tap and
    ///   Approve / Decline handling read it exactly like a direct push;
    /// - with `e2e` that does not open (no key, wrong key, damaged data):
    ///   the neutral fallback.
    public static func rewrite(_ content: UNNotificationContent, key: Data?) -> UNNotificationContent {
        guard let e2e = content.userInfo["e2e"] else { return content }
        guard let key,
              let dict = e2e as? [String: Any],
              let c = dict["c"] as? String, let n = dict["n"] as? String,
              let plaintext = try? open(ciphertext: c, nonce: n, key: key)
        else { return fallback(content) }
        return decrypted(content, plaintext: plaintext)
    }

    /// The neutral notification for a relay push the extension could not
    /// open (also used when its time runs out).
    public static func fallback(_ content: UNNotificationContent) -> UNNotificationContent {
        guard let out = content.mutableCopy() as? UNMutableNotificationContent else { return content }
        out.title = fallbackTitle
        out.subtitle = ""
        out.body = fallbackBody
        out.categoryIdentifier = agentCategory
        var info = content.userInfo
        info.removeValue(forKey: "e2e")
        info.removeValue(forKey: "sheepdog")
        out.userInfo = info
        return out
    }

    static func decrypted(_ content: UNNotificationContent, plaintext: [String: Any]) -> UNNotificationContent {
        guard let out = content.mutableCopy() as? UNMutableNotificationContent else { return content }
        let aps = plaintext["aps"] as? [String: Any] ?? [:]
        if let alert = aps["alert"] as? [String: Any] {
            out.title = alert["title"] as? String ?? fallbackTitle
            out.subtitle = alert["subtitle"] as? String ?? ""
            out.body = alert["body"] as? String ?? ""
        } else if let alert = aps["alert"] as? String {
            out.title = fallbackTitle
            out.body = alert
        }
        if let thread = aps["thread-id"] as? String { out.threadIdentifier = thread }
        if let category = aps["category"] as? String { out.categoryIdentifier = category }
        var info = content.userInfo
        info.removeValue(forKey: "e2e")
        var mergedAPS = info["aps"] as? [String: Any] ?? [:]
        for (k, v) in aps { mergedAPS[k] = v }
        info["aps"] = mergedAPS
        if let sheepdog = plaintext["sheepdog"] as? [String: Any] {
            info["sheepdog"] = sheepdog
        } else {
            info.removeValue(forKey: "sheepdog")
        }
        out.userInfo = info
        return out
    }
}
