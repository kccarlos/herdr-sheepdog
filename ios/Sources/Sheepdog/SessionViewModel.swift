import AVFoundation
import Combine
import Foundation
import SheepdogKit
import SwiftUI

/// Everything the main screen shows, in one place.
///
/// It owns the socket, the audio engine, the event stream and the
/// conversation, and it is the only thing that decides what the orb says —
/// the views just render it.
@MainActor
final class SessionViewModel: ObservableObject {
    struct Banner: Identifiable, Equatable {
        enum Tone { case error, warning, info }
        let id = UUID()
        var tone: Tone = .error
        var message: String
        var hint: String?
        /// Shown as a button when the user can do something about it.
        var retryTitle: String?
    }

    // MARK: - Published state

    @Published private(set) var state: SessionState = .disconnected
    @Published private(set) var bubbles: [ConversationReducer.Bubble] = []
    @Published private(set) var agents: [Agent] = []
    @Published private(set) var pendingAction: PendingAction?
    @Published private(set) var hello: Hello?
    @Published private(set) var micLevel: Float = 0
    @Published private(set) var metrics = AudioMetrics()
    @Published var banner: Banner?
    @Published var isMuted = false
    /// True from the moment Connect is tapped until Disconnect.
    @Published private(set) var wantsConnection = false
    @Published var showSettings = false

    var voiceName: String { hello?.displayName ?? "Sheepdog" }
    var daemonVersion: String? { hello?.version }
    var endpointDisplay: String { settings.endpoint?.display ?? "not set" }
    var isConfigured: Bool { settings.endpoint != nil }

    /// The agent the flock strip highlights: the one that is blocked.
    var blockedAgent: Agent? { agents.first { $0.state == .blocked } }

    func pendingQuestion(for agent: Agent) -> String? {
        guard let pending = pendingAction, pending.paneID == agent.paneID else { return nil }
        return pending.question ?? pending.spokenSummary
    }

    // MARK: - Collaborators

    let settings: SettingsStore
    private var client: DaemonClient
    private var socket: VoiceSocket
    private let audio = AudioEngine()
    private var reducer = ConversationReducer()

    private var eventTask: Task<Void, Never>?
    private var refreshTask: Task<Void, Never>?
    private var ticker: Task<Void, Never>?
    private var thinkingExpiry: Date?
    private var lastSeq: UInt64 = 0
    private var voiceStatus: WSMessage.Voice?
    private var closedReason: String?
    private var stalled = false
    private var gateClosed = false
    private var speaking = false
    private var daemonVoiceOff = false
    /// The "voice is off" notice is said once per session, not per status.
    private var saidVoiceIsOff = false
    private var socketLife: SessionState.Inputs.Socket = .closed

    init(settings: SettingsStore = SettingsStore()) {
        self.settings = settings
        let endpoint = settings.endpoint ?? DaemonEndpoint(host: "127.0.0.1")
        self.client = DaemonClient(endpoint: endpoint)
        self.socket = VoiceSocket(endpoint: endpoint)
        wire()
        bubbles = reducer.bubbles
    }

    /// Called once from the first `onAppear`: an unconfigured app opens
    /// Settings instead of showing a Connect button that cannot work.
    func start() {
        if !isConfigured || !settings.hasBeenConfigured {
            showSettings = true
        } else {
            Task { await refreshState(quiet: true) }
        }
    }

    /// Settings were saved: repoint everything without losing the conversation.
    func endpointChanged() {
        guard let endpoint = settings.endpoint else { return }
        client.update(endpoint: endpoint)
        socket.update(endpoint: endpoint)
        banner = nil
        hello = nil
        if wantsConnection {
            disconnect()
            connect()
        } else {
            Task { await refreshState(quiet: true) }
        }
    }

    // MARK: - Connect / disconnect

    func connect() {
        guard let endpoint = settings.endpoint else {
            showSettings = true
            return
        }
        client.update(endpoint: endpoint)
        socket.update(endpoint: endpoint)
        banner = nil
        wantsConnection = true
        socketLife = .connecting
        refresh()
        Task { await openSession() }
    }

    private func openSession() async {
        // The auth probe first: a 401 on the WebSocket upgrade is almost
        // unreadable, so find out here where the status code is plain.
        do {
            let state = try await client.state()
            apply(state)
            hello = try? await client.hello()
        } catch let error as DaemonError {
            fail(error)
            return
        } catch {
            fail(.malformed(error.localizedDescription))
            return
        }
        guard wantsConnection else { return }

        guard await AudioEngine.requestMicrophone() else {
            show(Banner(tone: .warning, message: AudioEngine.Failure.microphoneDenied.errorDescription ?? "", hint: nil, retryTitle: nil))
            // The session is still worth having: status and transcripts work
            // without a microphone.
            openSocket()
            return
        }
        openSocket()
    }

    private func openSocket() {
        socket.muted = isMuted
        socket.gateClosed = false
        socket.connect()
        startEvents()
        startRefreshing()
        startTicking()
        refresh()
    }

    func disconnect() {
        wantsConnection = false
        socket.disconnect()
        audio.stop()
        eventTask?.cancel(); eventTask = nil
        refreshTask?.cancel(); refreshTask = nil
        ticker?.cancel(); ticker = nil
        socketLife = .closed
        voiceStatus = nil
        saidVoiceIsOff = false
        closedReason = nil
        stalled = false
        gateClosed = false
        speaking = false
        thinkingExpiry = nil
        micLevel = 0
        refresh()
    }

    func toggleConnection() {
        wantsConnection ? disconnect() : connect()
    }

    // MARK: - Controls

    func toggleMute() {
        setMuted(!isMuted)
    }

    private func setMuted(_ muted: Bool) {
        guard muted != isMuted else { return }
        isMuted = muted
        socket.muted = muted
        if muted {
            // Stop the source first, so no frame can follow the message.
            audio.setMuted(true)
            socket.send(.mute(true))
        } else {
            // Tell the daemon first, so it does not drop the first frames.
            socket.send(.mute(false))
            audio.setMuted(false)
        }
        gateClosed = false
        socket.gateClosed = false
        Haptics.tap()
        refresh()
    }

    /// The Stop button: silence now, and ask the daemon to cancel. It
    /// answers with a `flush`, which we also honour.
    func stop() {
        audio.flushPlayback()
        reducer.apply(.flush)
        bubbles = reducer.bubbles
        socket.send(.cancel)
        speaking = false
        Haptics.stop()
        refresh()
    }

    // MARK: - Wiring

    private func wire() {
        socket.onEvent = { [weak self] event in
            self?.handle(socket: event)
        }
        audio.onFrame = { [weak self] frame in
            self?.socket.send(frame: frame)
        }
        audio.onGate = { [weak self] closed in
            guard let self else { return }
            self.gateClosed = closed
            self.socket.gateClosed = closed
            self.socket.send(.silence(closed))
            self.refresh()
        }
        audio.onLevel = { [weak self] level in
            self?.micLevel = level
        }
        audio.onSpeakingChanged = { [weak self] speaking in
            guard let self else { return }
            self.speaking = speaking
            self.refresh()
        }
        audio.onNotice = { [weak self] notice in
            guard let self else { return }
            self.reducer.apply(.notice(notice))
            self.bubbles = self.reducer.bubbles
        }
    }

    private func handle(socket event: VoiceSocket.Event) {
        switch event {
        case .opened:
            socketLife = .open
            banner = nil
            Haptics.connected()
        case let .control(message):
            handle(control: message)
        case let .audio(data):
            audio.enqueue(data)
        case let .closed(reason, retrying):
            socketLife = retrying ? .reconnecting : .closed
            audio.stop()
            if !retrying {
                wantsConnection = false
                if reason == "replaced" {
                    show(Banner(
                        tone: .warning,
                        message: "Another client took the voice session.",
                        hint: "The daemon serves one client at a time — the browser page on your Mac, maybe.",
                        retryTitle: "Take it back"
                    ))
                }
            }
        case let .failed(error):
            fail(error)
        }
        refresh()
    }

    private func handle(control message: WSMessage) {
        switch message.type {
        case .status:
            voiceStatus = message.voice
            closedReason = message.reason
            stalled = message.stalled ?? false
            if let muted = message.muted, muted != isMuted {
                // The daemon's view wins: it is the one dropping frames.
                isMuted = muted
                socket.muted = muted
                audio.setMuted(muted)
                gateClosed = false
                socket.gateClosed = false
            }
            applyVoiceStatus()
        case .transcript:
            guard let dir = message.dir, let text = message.text else { break }
            reducer.apply(.transcript(dir, text: text, final: message.final ?? false))
            bubbles = reducer.bubbles
        case .flush:
            audio.flushPlayback()
            reducer.apply(.flush)
            bubbles = reducer.bubbles
            speaking = false
        case .error:
            if let text = message.message {
                show(Banner(tone: .warning, message: "The daemon said: \(text)", hint: nil, retryTitle: nil))
            }
        case .pong, .mute, .silence, .cancel, .text, .ping:
            break
        }
        refresh()
    }

    /// Capture runs only while something can hear it.
    private func applyVoiceStatus() {
        let closed = voiceStatus == .closed
        if closed {
            if audio.isRunning { audio.stop() }
            daemonVoiceOff = true
            if !saidVoiceIsOff {
                saidVoiceIsOff = true
                let why = closedReason == "voice_off"
                    ? "Voice is off on the daemon (`--voice=off`), so it cannot hear the microphone. Status, transcripts and pushes still work."
                    : "The daemon's voice session closed (\(closedReason ?? "no reason given"))."
                reducer.apply(.notice(why))
                bubbles = reducer.bubbles
            }
            return
        }
        daemonVoiceOff = false
        saidVoiceIsOff = false
        guard !audio.isRunning, !isMuted else { return }
        do {
            try audio.start(background: settings.keepListeningWhenLocked)
            metrics = audio.metrics
        } catch {
            show(Banner(
                tone: .warning,
                message: (error as? LocalizedError)?.errorDescription ?? "The microphone would not start.",
                hint: "Status and what the dog says still work without it.",
                retryTitle: nil
            ))
        }
    }

    // MARK: - Events and state

    private func startEvents() {
        eventTask?.cancel()
        eventTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self, self.wantsConnection else { return }
                do {
                    for try await output in self.client.events(since: self.lastSeq) {
                        if Task.isCancelled { return }
                        self.handle(event: output)
                    }
                } catch {
                    SheepdogLog.socket.error("event stream: \(String(describing: error), privacy: .public)")
                    #if DEBUG
                    if ProcessInfo.processInfo.arguments.contains("-sheepdogShowStreamErrors") {
                        self.reducer.apply(.notice("event stream: \(error)"))
                        self.bubbles = self.reducer.bubbles
                    }
                    #endif
                }
                try? await Task.sleep(nanoseconds: 2_000_000_000)
            }
        }
    }

    private func handle(event output: SSEParser.Output) {
        switch output {
        case let .event(event):
            lastSeq = max(lastSeq, event.seq)
            apply(event: event)
        case .closed, .undecodable:
            break
        }
    }

    private func apply(event: DaemonEvent) {
        switch event.type {
        case "voice.push":
            guard let text = event.string("text"), !text.isEmpty else { break }
            let needsUser = event.bool("needs_user") ?? (event.string("urgency") == "interrupt")
            reducer.apply(.push(text: text, needsUser: needsUser))
            bubbles = reducer.bubbles
            Haptics.push(needsUser: needsUser)
            refreshSoon()
        case "voice.tool_call":
            thinkingExpiry = Date().addingTimeInterval(30)
        case "voice.tool_result":
            thinkingExpiry = nil
            // With `--voice=off` there are no transcript frames: the event log
            // is the only place the conversation exists.
            if daemonVoiceOff, let spoken = event.string("spoken"), !spoken.isEmpty {
                reducer.apply(.mirroredReply(spoken))
                bubbles = reducer.bubbles
            }
            refreshSoon()
        case "web.utterance":
            if daemonVoiceOff, let text = event.string("text"), !text.isEmpty {
                reducer.apply(.mirroredUtterance(text))
                bubbles = reducer.bubbles
            }
        default:
            if event.type.hasPrefix("herdr.") || event.type.hasPrefix("pending.")
                || event.type.hasPrefix("supervisor.") || event.type.hasPrefix("web.inject")
                || event.type.hasPrefix("investigation.") || event.type.hasPrefix("promise.") {
                refreshSoon()
            }
        }
        refresh()
    }

    /// True while the microphone or the player is doing something.
    private var audioIsBusy: Bool { audio.isRunning || speaking }

    private var pendingRefresh: Task<Void, Never>?

    /// Debounced `/api/state` fetch: many events describe one change.
    private func refreshSoon() {
        guard pendingRefresh == nil else { return }
        pendingRefresh = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 250_000_000)
            guard let self else { return }
            self.pendingRefresh = nil
            await self.refreshState(quiet: true)
        }
    }

    private func startRefreshing() {
        refreshTask?.cancel()
        refreshTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 5_000_000_000)
                guard let self, self.wantsConnection else { return }
                await self.refreshState(quiet: true)
            }
        }
    }

    /// One timer drives the playback release, the metrics and the thinking
    /// timeout, so a missed event cannot leave the orb stuck. It runs fast
    /// only while audio is actually moving; idle (or with the daemon's voice
    /// off) it ticks once a second and leaves the CPU alone.
    private func startTicking() {
        ticker?.cancel()
        ticker = Task { [weak self] in
            while !Task.isCancelled {
                let busy = self?.audioIsBusy ?? false
                try? await Task.sleep(nanoseconds: busy ? 100_000_000 : 1_000_000_000)
                guard let self else { return }
                self.audio.tick()
                self.metrics = self.audio.metrics
                self.socket.gatedFrames = self.audio.gatedFrames
                self.socket.inputPeak = self.audio.inputPeak
                if let expiry = self.thinkingExpiry, expiry < Date() { self.thinkingExpiry = nil }
                self.refresh()
            }
        }
    }

    @discardableResult
    func refreshState(quiet: Bool) async -> Bool {
        guard settings.endpoint != nil else { return false }
        do {
            apply(try await client.state())
            if hello == nil { hello = try? await client.hello() }
            if !quiet { banner = nil }
            return true
        } catch let error as DaemonError {
            if !quiet { fail(error) }
            return false
        } catch {
            return false
        }
    }

    private func apply(_ state: DaemonState) {
        agents = state.agents.sorted { lhs, rhs in
            // The blocked agents first: they are the ones waiting on a person.
            if (lhs.state == .blocked) != (rhs.state == .blocked) { return lhs.state == .blocked }
            return lhs.alias.localizedCaseInsensitiveCompare(rhs.alias) == .orderedAscending
        }
        pendingAction = state.pendingAction
        daemonVoiceOff = state.voice.isOff || state.voice.status == "closed"
    }

    // MARK: - Banners

    private func fail(_ error: DaemonError) {
        show(Banner(tone: .error, message: error.message, hint: error.hint, retryTitle: "Try again"))
        if case .unauthorized = error { showSettingsForToken() }
        wantsConnection = false
        socketLife = .closed
        socket.disconnect()
        audio.stop()
        refresh()
    }

    private func showSettingsForToken() {
        showSettings = true
    }

    private func show(_ banner: Banner) {
        self.banner = banner
    }

    func retry() {
        banner = nil
        connect()
    }

    // MARK: - Derivation

    private func refresh() {
        let inputs = SessionState.Inputs(
            socket: socketLife,
            voice: voiceStatus,
            closedReason: closedReason,
            stalled: stalled,
            muted: isMuted,
            gateClosed: gateClosed,
            speaking: speaking,
            thinking: thinkingExpiry != nil
        )
        let next = SessionState.resolve(inputs)
        if next != state { state = next }
    }

    // MARK: - Test connection (Settings)

    struct Probe {
        var ok: Bool
        var title: String
        var lines: [String]
    }

    /// What the "Test connection" button reports. `/api/hello` is public, so
    /// it works before the token is right; `/api/state` is the token check.
    func probe(address: String, token: String) async -> Probe {
        guard let endpoint = DaemonEndpoint(text: address, token: token) else {
            return Probe(ok: false, title: "That address doesn't look like host:port.", lines: ["Try 192.168.8.141:7777."])
        }
        let probeClient = DaemonClient(endpoint: endpoint)
        do {
            let hello = try await probeClient.hello()
            var lines = [
                "\(hello.name) \(hello.version)",
                "Voice name: \(hello.displayName)",
                "Auth: \(hello.needsToken ? "token required" : "open (loopback)")",
                "Audio: \(hello.audio.inHz) Hz in / \(hello.audio.outHz) Hz out, \(hello.audio.format)",
            ]
            if hello.needsToken {
                // A hello alone would "pass" with the wrong token.
                do {
                    _ = try await probeClient.state()
                    lines.append("Token accepted.")
                } catch let error as DaemonError {
                    return Probe(ok: false, title: error.message, lines: lines + [error.hint].compactMap { $0 })
                }
            }
            return Probe(ok: true, title: "Connected to \(endpoint.display)", lines: lines)
        } catch let error as DaemonError {
            return Probe(ok: false, title: error.message, lines: [error.hint].compactMap { $0 })
        } catch {
            return Probe(ok: false, title: "Could not reach \(endpoint.display).", lines: [])
        }
    }
}
