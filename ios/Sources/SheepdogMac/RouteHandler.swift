import Foundation
import SheepdogKit

/// What a `sheepdog://` URL does in the Mac app. The parse is Kit's
/// `AppRoute`, the one the iOS app uses. The voice routes go to the Mac's
/// voice session, which decides what they mean for the session as it is
/// (`AppRoute.actions`): `talk` connects `/ws`, which takes the voice from
/// the phone, or unmutes the session that runs.
@MainActor
public struct RouteHandler {
    private let voice: VoiceControlling
    private let log: AppLogging

    public init(voice: VoiceControlling, log: AppLogging) {
        self.voice = voice
        self.log = log
    }

    public func handle(_ url: URL) {
        guard let route = AppRoute(url: url) else {
            log.log("ignored a URL that is not a sheepdog route")
            return
        }
        switch route {
        case .talk, .mute, .toggleMute, .stop, .end:
            voice.handle(route: route)
        case .agent, .window, .toggleTalk:
            log.log("ignored \(route.url.absoluteString): not handled by this app")
        }
    }
}
