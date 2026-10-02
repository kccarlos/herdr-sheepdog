import AVFoundation
import Foundation
import UIKit
import LiveKit
import SheepdogKit

/// The voice over WebRTC, through sheepdog-bridge (its
/// docs/client-contract.md is the spec): fetch a token from the bridge with
/// the daemon's token, join the room, publish the microphone, play the
/// bridge's `sheepdog-voice` track, and carry the daemon's control JSON on
/// the reliable data topic `sheepdog.control`.
///
/// LiveKit owns the audio on this path — the session, voice-processing echo
/// cancellation, Opus, NetEQ's jitter buffer — so the app's `AudioEngine`
/// (capture, VAD, jitter buffer, playback reports) does not run. The bridge
/// sends the daemon `playback` reports and, unless the app says otherwise,
/// `speaking` and the silence gate; `ControlPacket.outgoing` keeps the app
/// from sending the first two.
///
/// What the view model sees is the `VoiceTransport` seam:
/// * `opened` once the room is joined *and* the bridge has the daemon
///   (`{"type":"bridge","daemon":"connected"}`, or any daemon message);
/// * `closed(retrying: true)` when the bridge loses the daemon, when
///   LiveKit starts reconnecting (Wi-Fi ↔ cellular, an ICE restart), and
///   when the room drops for good (then a fresh token and a rejoin, with
///   the socket's backoff);
/// * `link` for the bridge's daemon link (Health, and "in use by the
///   browser").
///
/// After LiveKit's own reconnect the bridge's `/ws` stayed up and the
/// daemon sends no new `status`, so the last one is replayed: the session
/// core starts the microphone again off it, as it would off a fresh one.
@MainActor
final class LiveKitTransport: VoiceTransport {
    var onEvent: ((VoiceTransportEvent) -> Void)?
    /// The bridge's participant is an active speaker: the dog is talking.
    /// Drives the orb's "Speaking" (the app's player is not running).
    var onBridgeSpeaking: ((Bool) -> Void)?
    /// The microphone could not be published or unmuted.
    var onMicrophoneFailed: ((String) -> Void)?

    var muted = false
    var gateClosed = false
    /// Never said on this path (see `ControlPacket.outgoing`).
    var speaking = false

    /// Settings ▸ Bridge address (empty: the daemon's host, port 7778).
    var bridgeAddress = ""

    /// The bridge's daemon link, as it last said.
    private(set) var link: BridgeLink?
    /// The local microphone's level, 0…1, for the voice meter.
    var microphoneLevel: Float { room?.localParticipant.audioLevel ?? 0 }
    /// Where the token came from, for Health.
    var bridgeDisplay: String { LiveKitBridge.endpoint(bridgeAddress: bridgeAddress, daemon: endpoint)?.display ?? "not set" }

    static let pingInterval: TimeInterval = 10
    /// How long to wait for the bridge's greeting after joining.
    static let greetingTimeout: TimeInterval = 8

    private var endpoint: DaemonEndpoint
    private let session: URLSession
    /// Stable per device (the contract's recommendation): a rejoin is the
    /// same participant, with no ghost left in the room.
    private let identity: String?

    private var room: Room?
    private var observer: RoomObserver?
    private var generation = 0
    private var userClosed = true
    /// The daemon is reachable through the bridge (what `opened` said).
    private var isOpen = false
    private var lastStatus: WSMessage?
    /// LiveKit reconnected underneath a live `/ws`: replay `lastStatus`.
    private var replayStatusOnOpen = false
    private var connectTask: Task<Void, Never>?
    private var retryTask: Task<Void, Never>?
    private var pingLoop: Task<Void, Never>?
    private var retryDelay = VoiceSocket.retryMinimum

    // One sender per room, so control messages leave in order.
    private var outbox: AsyncStream<Data>.Continuation?
    private var sender: Task<Void, Never>?

    // The microphone: what the view model wants, what the room has.
    private var microphoneWanted = false
    private var microphoneApplied = false
    private var microphoneTask: Task<Void, Never>?

    init(endpoint: DaemonEndpoint, session: URLSession? = nil) {
        self.endpoint = endpoint
        if let session {
            self.session = session
        } else {
            let config = URLSessionConfiguration.ephemeral
            config.timeoutIntervalForRequest = 15
            self.session = URLSession(configuration: config)
        }
        identity = LiveKitBridge.identity(deviceID: UIDevice.current.identifierForVendor)
    }

    func update(endpoint: DaemonEndpoint) {
        self.endpoint = endpoint
    }

    // MARK: - Lifecycle

    func connect() {
        userClosed = false
        retryDelay = VoiceSocket.retryMinimum
        open()
    }

    func disconnect(reason: String) {
        userClosed = true
        teardown()
    }

    private func open() {
        teardown()
        generation += 1
        let generation = self.generation
        guard let bridge = LiveKitBridge.endpoint(bridgeAddress: bridgeAddress, daemon: endpoint),
              let request = LiveKitBridge.tokenRequest(bridge: bridge, identity: identity) else {
            onEvent?(.failed(.malformed("the bridge address \(bridgeAddress) is not a URL"), retrying: false))
            return
        }
        connectTask = Task { [weak self] in
            await self?.join(bridge: bridge, request: request, generation: generation)
        }
    }

    private func join(bridge: DaemonEndpoint, request: URLRequest, generation: Int) async {
        // A fresh token on every join: it is cheap, and a rejoin after a
        // long sleep must not present an expired one.
        let token: BridgeToken
        do {
            let (data, response) = try await session.data(for: request)
            let status = (response as? HTTPURLResponse)?.statusCode ?? 0
            token = try LiveKitBridge.tokenResponse(status: status, body: data, bridge: bridge)
        } catch let error as DaemonError {
            guard generation == self.generation else { return }
            if case .unauthorized = error {
                // A refused token does not fix itself by retrying.
                userClosed = true
                onEvent?(.failed(error, retrying: false))
                return
            }
            failAndRetry(error)
            return
        } catch {
            guard generation == self.generation else { return }
            let detail = (error as? URLError).map(DaemonClient.describe) ?? error.localizedDescription
            failAndRetry(.unreachable(endpoint: bridge.display, detail: "The bridge did not answer. \(detail)"))
            return
        }
        guard generation == self.generation, !Task.isCancelled else { return }

        let observer = RoomObserver(owner: self, generation: generation)
        let room = Room(
            delegate: observer,
            connectOptions: ConnectOptions(autoSubscribe: false),
            roomOptions: RoomOptions(
                // Voice-processing I/O: echo cancellation and noise
                // suppression in the platform, as the app's own engine has.
                defaultAudioCaptureOptions: AudioCaptureOptions(echoCancellation: true, autoGainControl: true, noiseSuppression: true),
                defaultAudioPublishOptions: AudioPublishOptions(name: LiveKitBridge.microphoneTrackName)
            )
        )
        self.observer = observer
        self.room = room
        startSender(room: room)
        let started = Date()
        do {
            try await room.connect(url: token.url, token: token.token)
            SheepdogLog.socket.info("livekit: Room.connect took \(Int(Date().timeIntervalSince(started) * 1000), privacy: .public) ms")
        } catch {
            SheepdogLog.socket.error("livekit: Room.connect failed after \(Int(Date().timeIntervalSince(started) * 1000), privacy: .public) ms")
            guard generation == self.generation else { return }
            failAndRetry(.unreachable(endpoint: bridge.display, detail: "The bridge answered, but LiveKit at \(token.url) did not: \(error.localizedDescription)"))
            return
        }
        guard generation == self.generation else {
            await room.disconnect()
            return
        }
        SheepdogLog.socket.info("livekit: joined \(token.room, privacy: .public) as \(token.identity, privacy: .public)")
        retryDelay = VoiceSocket.retryMinimum
        joined(room)
    }

    /// The room is up (first join, or after LiveKit's own reconnect).
    private func joined(_ room: Room) {
        // The bridge forgets the phone's state when the last phone leaves:
        // say mute (and a closed gate) on every join, even before it has
        // the daemon — it remembers them and replays them to `/ws`. It is
        // also the contract's "control message right after connecting",
        // which the bridge answers with `bridge` + `status`. That answer is
        // lost if it comes before this connection can receive data (seen
        // on a same-identity rejoin whose `connect` returned in 30 ms), so
        // the message waits until the bridge's voice track is subscribed —
        // proof the downlink is up — or 2 s, whichever is first.
        greetingSent = false
        greetWhenReady(generation: generation)
        startPinging()
        subscribeVoice(in: room)
        // A new room starts with no microphone (`teardown` reset the flag);
        // after LiveKit's own reconnect the track is as it was.
        pumpMicrophone()
        if room.remoteParticipants[Participant.Identity(from: LiveKitBridge.bridgeIdentity)] == nil {
            onEvent?(.failed(.unreachable(endpoint: bridgeDisplay, detail: "Joined the LiveKit room, but sheepdog-bridge is not in it. Is it running?"), retrying: true))
        }
        expectGreeting(generation: generation)
    }

    private var greetingSent = false

    private func greetWhenReady(generation: Int) {
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 2_000_000_000)
            guard let self, generation == self.generation else { return }
            self.sendGreeting()
        }
    }

    private func sendGreeting() {
        guard !greetingSent else { return }
        greetingSent = true
        sayState()
    }

    /// `sayState()` above is the control message the contract asks for
    /// right after connecting: the bridge answers the first one from each
    /// connection with its link state and the daemon's latest `status`.
    /// If that has not come after a while, say so, so the reconnect policy
    /// can raise its banner instead of "Reconnecting…" forever.
    private func expectGreeting(generation: Int) {
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(LiveKitTransport.greetingTimeout * 1_000_000_000))
            guard let self, generation == self.generation, !self.userClosed, !self.isOpen, self.link == nil else { return }
            SheepdogLog.socket.error("livekit: no word from the bridge \(LiveKitTransport.greetingTimeout, privacy: .public) s after joining")
            self.onEvent?(.failed(.unreachable(endpoint: self.bridgeDisplay, detail: "Joined the LiveKit room, but the bridge has not said whether it has the daemon."), retrying: true))
        }
    }

    private func failAndRetry(_ error: DaemonError) {
        onEvent?(.failed(error, retrying: true))
        onEvent?(.closed(reason: "could not connect", retrying: true))
        scheduleRetry()
    }

    private func scheduleRetry() {
        guard !userClosed else { return }
        let delay = retryDelay
        retryDelay = min(retryDelay * 2, VoiceSocket.retryMaximum)
        retryTask?.cancel()
        retryTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            guard let self, !Task.isCancelled, !self.userClosed else { return }
            self.open()
        }
    }

    private func teardown() {
        generation += 1
        connectTask?.cancel(); connectTask = nil
        retryTask?.cancel(); retryTask = nil
        pingLoop?.cancel(); pingLoop = nil
        microphoneTask?.cancel(); microphoneTask = nil
        microphoneApplied = false
        outbox?.finish(); outbox = nil
        sender?.cancel(); sender = nil
        isOpen = false
        replayStatusOnOpen = false
        lastStatus = nil
        link = nil
        observer = nil
        if let room {
            self.room = nil
            Task { await room.disconnect() }
        }
    }

    // MARK: - Room events (from RoomObserver, on the main actor)

    fileprivate func roomReconnecting(generation: Int) {
        guard generation == self.generation else { return }
        SheepdogLog.socket.info("livekit: reconnecting")
        guard isOpen else { return }
        isOpen = false
        replayStatusOnOpen = true
        onEvent?(.closed(reason: "network changed", retrying: true))
    }

    fileprivate func roomReconnected(generation: Int) {
        guard generation == self.generation, let room else { return }
        SheepdogLog.socket.info("livekit: reconnected")
        joined(room)
        if link == .connected { setOpen() }
    }

    fileprivate func roomDisconnected(generation: Int, error: Error?) {
        guard generation == self.generation, !userClosed else { return }
        SheepdogLog.socket.error("livekit: disconnected: \(error.map(String.init(describing:)) ?? "no error", privacy: .public)")
        let wasOpen = isOpen
        teardown()
        onEvent?(.closed(reason: wasOpen ? "connection lost" : "could not connect", retrying: true))
        scheduleRetry()
    }

    fileprivate func participantChanged(generation: Int, identity: String, connected: Bool) {
        guard generation == self.generation, identity == LiveKitBridge.bridgeIdentity else { return }
        if connected {
            SheepdogLog.socket.info("livekit: the bridge joined")
            if let room { subscribeVoice(in: room) }
        } else {
            SheepdogLog.socket.error("livekit: the bridge left the room")
            link = nil
            onEvent?(.link(.disconnected))
            setClosed(reason: "the bridge left")
        }
    }

    fileprivate func voiceSubscribed(generation: Int) {
        guard generation == self.generation else { return }
        SheepdogLog.socket.info("livekit: subscribed to \(LiveKitBridge.voiceTrackName, privacy: .public)")
        sendGreeting()
    }

    fileprivate func trackPublished(generation: Int) {
        guard generation == self.generation, let room else { return }
        subscribeVoice(in: room)
    }

    fileprivate func speakersChanged(generation: Int, identities: [String]) {
        guard generation == self.generation else { return }
        onBridgeSpeaking?(identities.contains(LiveKitBridge.bridgeIdentity))
    }

    fileprivate func received(generation: Int, data: Data, topic: String) {
        guard generation == self.generation, topic == LiveKitBridge.controlTopic else { return }
        let packet: ControlPacket
        do {
            packet = try ControlPacket.decode(data)
        } catch {
            SheepdogLog.socket.error("livekit: undecodable control packet: \(String(describing: error), privacy: .public)")
            return
        }
        switch packet {
        case let .bridge(link):
            SheepdogLog.socket.info("livekit: bridge says the daemon is \(link.rawValue, privacy: .public)")
            self.link = link
            onEvent?(.link(link))
            switch link {
            case .connected:
                replayStatusOnOpen = false
                setOpen()
            case .disconnected:
                if isOpen {
                    setClosed(reason: "the bridge lost the daemon")
                } else {
                    onEvent?(.failed(.unreachable(endpoint: endpoint.display, detail: "The bridge at \(bridgeDisplay) is up but cannot reach the daemon."), retrying: true))
                }
            case .replaced:
                // The browser page has the daemon. The bridge takes it back
                // in 30 s, or when the phone says anything but `ping`; the
                // daemon's own `status closed/replaced` has told the core.
                break
            }
        case let .message(message):
            // A daemon message means the bridge has the daemon, whatever
            // order its `bridge` message and the first `status` came in.
            if !isOpen, message.type != .pong {
                if link == nil || link == .disconnected {
                    link = .connected
                    onEvent?(.link(.connected))
                }
                setOpen()
            }
            if message.type == .status { lastStatus = message }
            if message.type == .transcript {
                SheepdogLog.socket.info("livekit: transcript \(message.dir?.rawValue ?? "?", privacy: .public) final=\(message.final ?? false, privacy: .public) (\(message.text?.count ?? 0, privacy: .public) chars)")
            }
            onEvent?(.control(message))
        }
    }

    private func setOpen() {
        guard !isOpen, room?.connectionState == .connected else { return }
        SheepdogLog.socket.info("livekit: open")
        isOpen = true
        sayState()
        onEvent?(.opened)
        if replayStatusOnOpen, let lastStatus {
            onEvent?(.control(lastStatus))
        }
        replayStatusOnOpen = false
    }

    private func setClosed(reason: String) {
        guard isOpen else { return }
        isOpen = false
        onEvent?(.closed(reason: reason, retrying: true))
    }

    // MARK: - Sending

    @discardableResult
    func send(_ message: WSMessage) -> Bool {
        guard let outbox, let data = ControlPacket.outgoing(message) else { return false }
        outbox.yield(data)
        return true
    }

    private func sayState() {
        send(.mute(muted))
        if gateClosed { send(.silence(true)) }
    }

    private func startSender(room: Room) {
        let (stream, continuation) = AsyncStream<Data>.makeStream()
        outbox = continuation
        let options = DataPublishOptions(topic: LiveKitBridge.controlTopic, reliable: true)
        sender = Task {
            for await data in stream {
                // Before the join completes there is nowhere to send; wait.
                while room.connectionState != .connected, !Task.isCancelled {
                    try? await Task.sleep(nanoseconds: 50_000_000)
                }
                if Task.isCancelled { return }
                do {
                    try await room.localParticipant.publish(data: data, options: options)
                } catch {
                    SheepdogLog.socket.error("livekit: data publish failed: \(String(describing: error), privacy: .public)")
                }
            }
        }
    }

    /// The contract wants a `ping` every 5–30 s. No pong timeout: while the
    /// bridge's `/ws` is down it drops pings, and the daemon link's state
    /// comes from the `bridge` message instead.
    private func startPinging() {
        pingLoop?.cancel()
        pingLoop = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: UInt64(LiveKitTransport.pingInterval * 1_000_000_000))
                guard let self, !Task.isCancelled else { return }
                if self.isOpen { self.send(WSMessage(type: .ping)) }
            }
        }
    }

    // MARK: - Audio

    /// Subscribes the bridge's `sheepdog-voice` (and nothing else: another
    /// phone's microphone is not for us to play). LiveKit plays a
    /// subscribed remote audio track by itself.
    private func subscribeVoice(in room: Room) {
        guard let bridge = room.remoteParticipants[Participant.Identity(from: LiveKitBridge.bridgeIdentity)] else { return }
        for case let publication as RemoteTrackPublication in bridge.trackPublications.values
            where publication.name == LiveKitBridge.voiceTrackName && !publication.isSubscribed {
            Task {
                do {
                    try await publication.set(subscribed: true)
                } catch {
                    SheepdogLog.socket.error("livekit: subscribe \(LiveKitBridge.voiceTrackName, privacy: .public) failed: \(String(describing: error), privacy: .public)")
                }
            }
        }
    }

    /// Whether the microphone track should be live (published and unmuted).
    /// The view model derives it from the session core; muting the track is
    /// what "muted means no audio" asks for, on top of the `mute` message.
    func setMicrophone(live: Bool) {
        guard live != microphoneWanted else { return }
        microphoneWanted = live
        pumpMicrophone()
    }

    private func pumpMicrophone() {
        guard microphoneTask == nil, let room, room.connectionState == .connected,
              microphoneWanted != microphoneApplied else { return }
        let want = microphoneWanted
        let generation = self.generation
        microphoneTask = Task { [weak self] in
            var failure: String?
            do {
                try await room.localParticipant.setMicrophone(enabled: want)
            } catch {
                failure = error.localizedDescription
            }
            guard let self, generation == self.generation else { return }
            self.microphoneTask = nil
            if let failure {
                SheepdogLog.socket.error("livekit: microphone \(want ? "on" : "off", privacy: .public) failed (record permission \(AVAudioApplication.shared.recordPermission.rawValue, privacy: .public)): \(failure, privacy: .public)")
                if want {
                    // Do not loop on a microphone that will not start.
                    self.microphoneWanted = false
                    self.onMicrophoneFailed?(failure)
                }
                return
            }
            self.microphoneApplied = want
            self.pumpMicrophone()
        }
    }
}

/// LiveKit calls its delegate from its own queues; this hops each callback
/// onto the main queue in order (control packets must stay ordered) and
/// drops the ones from a room the transport has moved past.
private final class RoomObserver: NSObject, RoomDelegate, @unchecked Sendable {
    private weak var owner: LiveKitTransport?
    private let generation: Int

    @MainActor
    init(owner: LiveKitTransport, generation: Int) {
        self.owner = owner
        self.generation = generation
    }

    private func onMain(_ body: @escaping @MainActor (LiveKitTransport, Int) -> Void) {
        let generation = generation
        DispatchQueue.main.async { [weak self] in
            MainActor.assumeIsolated {
                guard let owner = self?.owner else { return }
                body(owner, generation)
            }
        }
    }

    func room(_ room: Room, didStartReconnectWithMode reconnectMode: ReconnectMode) {
        onMain { $0.roomReconnecting(generation: $1) }
    }

    func room(_ room: Room, didCompleteReconnectWithMode reconnectMode: ReconnectMode) {
        onMain { $0.roomReconnected(generation: $1) }
    }

    func room(_ room: Room, didDisconnectWithError error: LiveKitError?) {
        onMain { $0.roomDisconnected(generation: $1, error: error) }
    }

    func room(_ room: Room, participantDidConnect participant: RemoteParticipant) {
        let identity = participant.identity?.stringValue ?? ""
        onMain { $0.participantChanged(generation: $1, identity: identity, connected: true) }
    }

    func room(_ room: Room, participantDidDisconnect participant: RemoteParticipant) {
        let identity = participant.identity?.stringValue ?? ""
        onMain { $0.participantChanged(generation: $1, identity: identity, connected: false) }
    }

    func room(_ room: Room, participant: RemoteParticipant, didPublishTrack publication: RemoteTrackPublication) {
        onMain { $0.trackPublished(generation: $1) }
    }

    func room(_ room: Room, participant: RemoteParticipant, didSubscribeTrack publication: RemoteTrackPublication) {
        guard participant.identity?.stringValue == LiveKitBridge.bridgeIdentity, publication.name == LiveKitBridge.voiceTrackName else { return }
        onMain { $0.voiceSubscribed(generation: $1) }
    }

    func room(_ room: Room, didUpdateSpeakingParticipants participants: [Participant]) {
        let identities = participants.compactMap { $0.identity?.stringValue }
        onMain { $0.speakersChanged(generation: $1, identities: identities) }
    }

    func room(_ room: Room, participant: RemoteParticipant?, didReceiveData data: Data, forTopic topic: String, encryptionType: EncryptionType) {
        onMain { $0.received(generation: $1, data: data, topic: topic) }
    }
}
