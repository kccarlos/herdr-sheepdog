import ActivityKit
import MediaPlayer
import SheepdogKit
import SwiftUI

/// Everything that lets the user run the voice session away from the
/// screen: the Live Activity (lock screen and Dynamic Island) and the
/// earbud remote with its Now Playing entry. Both follow one
/// `LiveActivityContent` (SheepdogKit), live exactly as long as the voice
/// session does, and are fed from `SessionViewModel.publish()` and the
/// daemon state. Their buttons come back as `AppRoute`s through `onRoute`.
@MainActor
final class AwayControls {
    var onRoute: (AppRoute) -> Void = { _ in }
    /// The Live Activity's push token (hex) when iOS issues one, nil when
    /// the activity ends: the daemon keeps the banner current with it
    /// while the app is away.
    var onActivityToken: (String?) -> Void {
        get { activity.onPushToken }
        set { activity.onPushToken = newValue }
    }

    private let activity = VoiceLiveActivity()
    private let remote = EarbudRemote()

    init() {
        remote.onCommand = { [weak self] command in self?.onRoute(command.route) }
    }

    /// nil: no session, so both go away at once.
    func sync(_ state: LiveActivityContent?, voiceName: String) {
        activity.sync(state, voiceName: voiceName)
        remote.sync(state, voiceName: voiceName)
    }

    /// The banner may show what the daemon pushed: the next sync writes
    /// it again.
    func forgetShownActivity() {
        activity.forgetShown()
    }

    /// The app is being terminated: do not leave a banner behind.
    func tearDown() {
        activity.sync(nil, voiceName: "")
        remote.sync(nil, voiceName: "")
    }
}

/// The voice session's Live Activity: started when a session starts (the
/// app is in the foreground then), updated locally while the app runs —
/// the audio session keeps it running with the screen locked — and ended
/// with immediate dismissal when the session ends. It is requested with a
/// push token (`onPushToken`), which the app gives the daemon: once the
/// app has been away for a while, the daemon pushes the updates
/// (docs/dev/push.md "Live Activity updates").
@MainActor
final class VoiceLiveActivity {
    private typealias Content = LiveActivityContent

    var onPushToken: (String?) -> Void = { _ in }

    private var activity: Activity<VoiceSessionAttributes>?
    private var tokenTask: Task<Void, Never>?
    private var content: Content?
    private var refreshTask: Task<Void, Never>?
    /// The last update sent; the next waits for it.
    private var updating: Task<Void, Never>?
    private var watchTask: Task<Void, Never>?
    /// The user swiped the banner away during this session: leave it
    /// gone until the next one.
    private var dismissedByUser = false

    init() {
        // A killed app cannot end its activity; end what the last run left.
        // The list is taken now, so an activity this run starts is safe.
        let orphans = Activity<VoiceSessionAttributes>.activities
        if !orphans.isEmpty {
            Task {
                for orphan in orphans { await orphan.end(nil, dismissalPolicy: .immediate) }
            }
        }
    }

    func sync(_ state: LiveActivityContent?, voiceName: String) {
        guard let next = state else {
            end()
            return
        }
        if let activity {
            guard next != content else { return }
            content = next
            let update = Self.activityContent(next)
            // One update at a time, in order: two unordered tasks could
            // land "Reconnecting…" after "Listening", and the banner kept
            // it until the next change.
            let previous = updating
            updating = Task {
                await previous?.value
                await activity.update(update)
            }
        } else if !dismissedByUser {
            start(next, voiceName: voiceName)
        }
    }

    private func start(_ next: Content, voiceName: String) {
        guard ActivityAuthorizationInfo().areActivitiesEnabled else { return }
        do {
            let started = try Activity.request(
                attributes: VoiceSessionAttributes(voiceName: voiceName),
                content: Self.activityContent(next),
                pushType: .token
            )
            activity = started
            content = next
            watch(started)
            watchToken(started)
            startRefreshing()
        } catch {
            // Not allowed right now (in the background, or too many
            // activities): try again on the next change.
            SheepdogLog.session.info("live activity not started: \(error.localizedDescription, privacy: .public)")
        }
    }

    private func end() {
        dismissedByUser = false
        refreshTask?.cancel(); refreshTask = nil
        watchTask?.cancel(); watchTask = nil
        tokenTask?.cancel(); tokenTask = nil
        guard let ending = activity else { return }
        onPushToken(nil)
        let last = content.map { ActivityContent(state: $0, staleDate: nil) }
        activity = nil
        content = nil
        let previous = updating
        updating = nil
        Task {
            await previous?.value
            await ending.end(last, dismissalPolicy: .immediate)
        }
    }

    /// The system ends an activity on its own (the user swiped it
    /// away, or the eight-hour limit): forget it.
    private func watch(_ watched: Activity<VoiceSessionAttributes>) {
        watchTask = Task { [weak self] in
            for await state in watched.activityStateUpdates {
                guard state == .dismissed || state == .ended else { continue }
                guard let self, self.activity?.id == watched.id else { return }
                self.activity = nil
                self.content = nil
                self.dismissedByUser = true
                self.refreshTask?.cancel(); self.refreshTask = nil
                self.tokenTask?.cancel(); self.tokenTask = nil
                self.onPushToken(nil)
                return
            }
        }
    }

    /// Drops the cached content, so the next sync updates the banner.
    func forgetShown() {
        if activity != nil { content = nil }
    }

    /// Hands every push token iOS issues for the activity to the daemon.
    private func watchToken(_ watched: Activity<VoiceSessionAttributes>) {
        tokenTask = Task { [weak self] in
            for await token in watched.pushTokenUpdates {
                guard let self, self.activity?.id == watched.id else { return }
                self.onPushToken(PushToken.hex(token))
            }
        }
    }

    /// Moves the stale date along while the session lasts, so only a
    /// banner orphaned by a killed app goes stale.
    private func startRefreshing() {
        refreshTask?.cancel()
        refreshTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: UInt64(LiveActivityContent.refreshInterval * 1_000_000_000))
                guard !Task.isCancelled, let self, let activity = self.activity else { return }
                // No cached content (forgetShown): the next sync writes it.
                guard let content = self.content else { continue }
                await activity.update(Self.activityContent(content))
            }
        }
    }

    private static func activityContent(_ state: Content) -> ActivityContent<Content> {
        ActivityContent(state: state, staleDate: Date().addingTimeInterval(LiveActivityContent.staleAfter))
    }
}

/// The earbud / AirPods remote and the Now Playing entry for the session.
/// Registered only while a session runs and cleared when it ends. It never
/// touches the audio session: the voice session's own `.playAndRecord`
/// category is what makes Sheepdog the Now Playing app, and this only
/// answers the remote while that holds.
@MainActor
final class EarbudRemote {
    var onCommand: (RemoteCommand) -> Void = { _ in }

    private var targets: [(MPRemoteCommand, Any)] = []
    private var info: [String: Any] = [:]

    func sync(_ state: LiveActivityContent?, voiceName: String) {
        guard let state else {
            deactivate()
            return
        }
        if targets.isEmpty { activate() }
        var next: [String: Any] = [
            MPMediaItemPropertyTitle: voiceName,
            MPMediaItemPropertyArtist: LiveActivityContent.waitingText(state.waiting).map { "\(state.title) · \($0)" } ?? state.title,
            MPNowPlayingInfoPropertyIsLiveStream: true,
            MPNowPlayingInfoPropertyMediaType: MPNowPlayingInfoMediaType.audio.rawValue,
            MPNowPlayingInfoPropertyPlaybackRate: RemoteCommand.playbackRate(muted: state.muted),
        ]
        next[MPMediaItemPropertyAlbumTitle] = "Voice session"
        guard !NSDictionary(dictionary: next).isEqual(to: info) else { return }
        info = next
        let center = MPNowPlayingInfoCenter.default()
        center.nowPlayingInfo = next
        center.playbackState = state.muted ? .paused : .playing
    }

    private func activate() {
        let center = MPRemoteCommandCenter.shared()
        func add(_ command: MPRemoteCommand, _ mapped: RemoteCommand) {
            command.isEnabled = true
            let target = command.addTarget { [weak self] _ in
                self?.onCommand(mapped)
                return .success
            }
            targets.append((command, target))
        }
        add(center.togglePlayPauseCommand, .togglePlayPause)
        add(center.playCommand, .play)
        add(center.pauseCommand, .pause)
        add(center.nextTrackCommand, .nextTrack)
    }

    private func deactivate() {
        for (command, target) in targets {
            command.removeTarget(target)
            command.isEnabled = false
        }
        targets.removeAll()
        guard !info.isEmpty else { return }
        info = [:]
        let center = MPNowPlayingInfoCenter.default()
        center.nowPlayingInfo = nil
        center.playbackState = .stopped
    }
}
