import ActivityKit
import MediaPlayer
import SheepdogKit
import SwiftUI

/// Everything that lets the user run the voice session away from the
/// screen: the Live Activity (lock screen and Dynamic Island) and the
/// earbud remote with its Now Playing entry. Both follow one
/// `VoiceActivityState` (SheepdogKit), live exactly as long as the voice
/// session does, and are fed from `SessionViewModel.publish()` and the
/// daemon state. Their buttons come back as `AppRoute`s through `onRoute`.
@MainActor
final class AwayControls {
    var onRoute: (AppRoute) -> Void = { _ in }

    private let activity = VoiceLiveActivity()
    private let remote = EarbudRemote()

    init() {
        remote.onCommand = { [weak self] command in self?.onRoute(command.route) }
    }

    /// nil: no session, so both go away at once.
    func sync(_ state: VoiceActivityState?, voiceName: String) {
        activity.sync(state, voiceName: voiceName)
        remote.sync(state, voiceName: voiceName)
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
/// with immediate dismissal when the session ends. No push updates: the
/// daemon is not involved.
@MainActor
final class VoiceLiveActivity {
    private typealias Content = VoiceSessionAttributes.ContentState

    private var activity: Activity<VoiceSessionAttributes>?
    private var content: Content?
    private var refreshTask: Task<Void, Never>?
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

    func sync(_ state: VoiceActivityState?, voiceName: String) {
        guard let state else {
            end()
            return
        }
        let next = Content(state)
        if let activity {
            guard next != content else { return }
            content = next
            let update = Self.activityContent(next)
            Task { await activity.update(update) }
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
                pushType: nil
            )
            activity = started
            content = next
            watch(started)
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
        guard let ending = activity else { return }
        let last = content.map { ActivityContent(state: $0, staleDate: nil) }
        activity = nil
        content = nil
        Task { await ending.end(last, dismissalPolicy: .immediate) }
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
                return
            }
        }
    }

    /// Moves the stale date along while the session lasts, so only a
    /// banner orphaned by a killed app goes stale.
    private func startRefreshing() {
        refreshTask?.cancel()
        refreshTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: UInt64(VoiceActivityState.refreshInterval * 1_000_000_000))
                guard !Task.isCancelled, let self, let activity = self.activity, let content = self.content else { return }
                await activity.update(Self.activityContent(content))
            }
        }
    }

    private static func activityContent(_ state: Content) -> ActivityContent<Content> {
        ActivityContent(state: state, staleDate: Date().addingTimeInterval(VoiceActivityState.staleAfter))
    }
}

extension VoiceSessionAttributes.ContentState {
    init(_ state: VoiceActivityState) {
        self.init(
            phase: state.phase.rawValue,
            muted: state.muted,
            waiting: state.waiting,
            title: state.title,
            waitingText: state.waitingText,
            accessibilityLabel: state.accessibilityLabel,
            compactAccessibilityLabel: state.compactAccessibilityLabel
        )
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

    func sync(_ state: VoiceActivityState?, voiceName: String) {
        guard let state else {
            deactivate()
            return
        }
        if targets.isEmpty { activate() }
        var next: [String: Any] = [
            MPMediaItemPropertyTitle: voiceName,
            MPMediaItemPropertyArtist: state.waitingText.map { "\(state.title) · \($0)" } ?? state.title,
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
