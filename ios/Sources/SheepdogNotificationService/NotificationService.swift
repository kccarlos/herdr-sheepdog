import UserNotifications

/// The Notification Service Extension: decrypts a relay push's `e2e`
/// payload with the content key the app stored in the shared keychain and
/// shows the real alert (`PushE2E.rewrite`). A push without `e2e` (the
/// direct APNs path) is delivered untouched. When it cannot decrypt, or
/// iOS ends its time, it shows the neutral "Open the app to see the
/// update" and never the raw data. No networking.
final class NotificationService: UNNotificationServiceExtension {
    private var contentHandler: ((UNNotificationContent) -> Void)?
    private var original: UNNotificationContent?

    override func didReceive(_ request: UNNotificationRequest, withContentHandler contentHandler: @escaping (UNNotificationContent) -> Void) {
        let content = request.content
        guard content.userInfo["e2e"] != nil else {
            contentHandler(content)
            return
        }
        self.contentHandler = contentHandler
        original = content
        let result = PushE2E.rewrite(content, key: PushContentKeyStore().key())
        deliver(result)
    }

    override func serviceExtensionTimeWillExpire() {
        guard let original else { return }
        deliver(PushE2E.fallback(original))
    }

    private func deliver(_ content: UNNotificationContent) {
        guard let handler = contentHandler else { return }
        contentHandler = nil
        original = nil
        handler(content)
    }
}
