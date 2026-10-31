import Foundation
import SheepdogKit

/// What a `sheepdog://` URL does in the Mac app. The parse is Kit's
/// `AppRoute`, the one the iOS app uses. Only `talk` is known so far, and it
/// only logs: the voice session arrives in PR 9.
public struct RouteHandler {
    private let log: AppLogging

    public init(log: AppLogging) {
        self.log = log
    }

    public func handle(_ url: URL) {
        guard let route = AppRoute(url: url) else {
            log.log("ignored a URL that is not a sheepdog route")
            return
        }
        switch route {
        case .talk:
            log.log("sheepdog://talk: voice arrives in PR 9")
        default:
            log.log("ignored \(route.url.absoluteString): not handled by this shell yet")
        }
    }
}
