import AVFoundation
import Combine
import Foundation
import SheepdogKit
import SwiftUI

/// The screens Settings can push.
enum SettingsRoute: Hashable {
    case models
    case languages
    /// Settings ▸ Connection, with `daemonNotice` shown.
    case daemon
}

/// Everything the main screen shows, in one place.
///
/// It orchestrates the computers, the event stream and the conversation.
/// The voice — the session core, the audio engine, `/ws`, the orb, mute,
/// hold-to-talk and its hints — is `VoiceEngine`'s (`voice`); this is its
/// host, running the effects that touch the conversation, banners and
/// refreshes. History, approvals and the provider picker are still here.
@MainActor
final class SessionViewModel: ObservableObject {
    struct Banner: Identifiable, Equatable {
        enum Tone { case error, warning, info }
        /// From what it says: the same banner raised again is the same
        /// banner, not a new one sliding in.
        var id: String { "\(tone).\(message)" }
        var tone: Tone = .error
        var message: String
        var hint: String?
        /// Shown as a button when the user can do something about it.
        var retryTitle: String?
        /// What that button does: reconnect (the default) or open the
        /// system's microphone-mode panel.
        var action: Action = .retry
        /// A page in the daemon's docs that explains the fix (same Wi-Fi,
        /// Tailscale, pairing again), shown as a link.
        var help: URL?
        /// The notice about this phone's audio or the voice that raised
        /// it, so the banner can clear itself once it is no longer true.
        var audioNotice: LocalNotice?

        enum Action: Equatable { case retry, microphoneModes }
    }

    // MARK: - Published state

    /// The voice: the session core, the audio engine, `/ws` and the meter,
    /// with the orb, mute, talk mode, hold, the microphone modes and the
    /// one-time hints the views read (`VoiceEngine`).
    let voice: VoiceEngine
    /// The conversation's rows, paging and typed line.
    let conversationModel = ConversationModel()
    /// The approval card pinned above the controls, if a worker is asking:
    /// the current computer's pinned question with its phase.
    var approval: ApprovalFlow.Card? { fleet.current?.approval }
    /// The agents, the open questions and the view state with them.
    let agentsModel = AgentsModel()
    /// The agents as the screen groups them: running (blocked, working,
    /// idle and done) and the rest.
    var flock: Flock { agentsModel.flock }
    /// The pane, navigation stack, Settings route and window request.
    let navigation = NavigationModel()
    /// A question's answer phase, kept on the current computer's model.
    func questionPhase(_ id: String) -> ApprovalFlow.Phase { fleet.current?.phase(id) ?? .open(error: nil) }
    /// Every sheet and cover over the main screen: Settings, New agent,
    /// Spaces, a card, the pairing flow, the primers, the demo
    /// (`PresentationCoordinator`).
    let presentation = PresentationCoordinator()
    /// The permission primer on screen, while it waits for its answer.
    private var primer: PermissionPrimer?
    private var primerAnswer: CheckedContinuation<Bool, Never>?
    /// The link's presentation, address rows, stream life, handshake and
    /// `/api/hello`. Four sources feed `resolvedVoiceProvider`; each tells the
    /// audio engine's jitter-buffer profile explicitly when it changes.
    let linkModel = LinkModel()
    /// Can the app talk to the current computer right now, and if not, why
    /// and what it is doing about it: the computer's `ComputerLink`, run by
    /// `linkRuntime` (docs/dev/design/ios-state-architecture.md 3.2). The
    /// one truth for the connection; not published, because `lastSeen`
    /// moves on every request.
    private(set) var link = ComputerLink(addresses: [])
    /// What the daemon pushes about an open agent window over `/ws`,
    /// handed straight to that window (`ScreenStreamRouter`), never
    /// through a published property: a frame must not re-render the app.
    let screenRouter = ScreenStreamRouter()
    /// The agent window over `/ws`: one subscription for the app.
    private var screenStream = AgentScreenStream()
    /// Cards (feature `cards`): the card open as a sheet, the main
    /// screen's navigation (a card's "Open window" pushes onto it), the
    /// `visibility` reports and the cards this phone got.
    var presentedCard: ScreenCard? { presentation.card }
    private var cardVisibility = CardVisibility()
    private var cardInbox = CardInbox()
    /// The small "card waiting" label (a card that arrived as a row while
    /// the app is on screen).
    let cardLabel = CardLabelModel()
    /// The orchestrator's model, from `/api/state`'s `brain`, when the
    /// daemon reports one.
    @Published private(set) var brain: BrainInfo?
    /// Whether the ⋯ menu offers "New conversation" — learned from the
    /// daemon's own answer to `POST /api/conversation/reset` (a 404 hides
    /// it), optimistic (shown) until then.
    @Published private(set) var conversationResetSupported = true
    /// The banner, the feedback line, the update and pairing notices.
    let notices = NoticeModel()
    /// The typed line in flight or last failed, with its idempotency key.
    private var typedAttempt: (text: String, key: String)?

    // The Health pane.
    /// Uptime, GPT-Live's billed seconds and the recent problems.
    let healthModel = HealthModel()
    /// Settings, models and providers (the Settings and Health panes).
    let settingsModel = SettingsModel()

    /// True from the moment Connect is tapped until Disconnect.
    var wantsConnection: Bool { voice.wantsConnection }

    /// From `/api/state` (the live `/ws` status is `voice.liveVoiceProvider`).
    @Published private(set) var stateVoiceProvider: String? {
        didSet { voice.providerSourcesChanged() }
    }
    @Published private(set) var stateVoiceModel: String?
    /// Settings is the presentation on screen.
    var showSettings: Bool { presentation.isShowing(.settings) }

    /// Health's "Change in Settings ›": Settings, pushed to Models.
    func openModels() {
        navigation.settingsRoute = .models
        presentation.present(.settings)
    }

    /// "Gemini · gemini-3.5-flash": the orchestrator as the catalog names it, else
    /// as `/api/state` reports it.
    var brainDisplay: String? {
        if let models = settingsModel.brainModels, !models.model.isEmpty {
            let provider = models.providers.first { $0.id == models.provider }?.displayName ?? models.provider
            return [provider, models.model].filter { !$0.isEmpty }.joined(separator: " · ")
        }
        return brain?.display
    }

    /// The Models row's one-line value in Settings.
    var modelsSummary: String {
        let parts = [voiceProviderDisplay, brainDisplay].compactMap { $0 }
        return parts.isEmpty ? "—" : parts.joined(separator: ", ")
    }

    var voiceName: String { linkModel.hello?.displayName ?? "Sheepdog" }

    /// How many computers are paired (`ComputerStore`).
    @Published private(set) var computerCount = 0

    /// The one switch for every computer label in the app (names, Talk to,
    /// sections, ⌘K, VoiceOver): only once a second computer is paired.
    /// With one computer the UI looks exactly as it always did.
    var showsComputerLabels: Bool { computerCount > 1 }

    /// The other paired computers (`SessionViewModel+Computers.swift`).
    var others: [ComputerModel] { fleet.others }
    /// The current computer's daemon id ("" for a bare address).
    var currentComputerID: String { settings.computers.currentID ?? "" }

    /// The current computer's name as the app shows it.
    var currentComputerName: String {
        settings.computer?.displayName ?? linkModel.hello?.computer?.name ?? "Your computer"
    }

    /// The line above the voice bar: what to do, not only what is happening.
    var statusLine: String {
        if demo != nil, voice.state == .noMicrophone || voice.state == .muted {
            return "Demo: tap a question above, or type"
        }
        return VoiceBarModel.statusLine(state: voice.state, talkMode: voice.talkMode, holding: voice.holding, noSignal: voice.noMicSignal)
    }

    /// The voice provider id, from whichever source knows it right now: the
    /// live `/ws` status, the provider picker's own state, `/api/state`, or
    /// `/api/hello` — in that order of freshness. One resolver instead of
    /// this fallback chain copied at each of its two call sites.
    private var resolvedVoiceProvider: String? {
        voice.liveVoiceProvider ?? settingsModel.voiceProviders?.provider ?? stateVoiceProvider ?? linkModel.hello?.voiceProvider
    }

    /// "Gemini Live · gemini-3.8-live", from whichever source knows.
    var voiceProviderDisplay: String? {
        let id = resolvedVoiceProvider
        let option = settingsModel.voiceProviders?.providers.first { $0.id == id }
        let model = (id == settingsModel.voiceProviders?.provider ? settingsModel.voiceProviders?.model : nil)
            ?? (id == stateVoiceProvider ? stateVoiceModel : nil)
            ?? (id == linkModel.hello?.voiceProvider ? linkModel.hello?.voiceModel : nil)
            ?? option?.model
        return VoiceProviders.display(provider: option?.displayName ?? id, model: model)
    }
    var daemonVersion: String? { linkModel.hello?.version }

    /// The computer answers: notifications stay quiet in the foreground,
    /// the screen already shows what they would say.
    var sessionIsLive: Bool { link.isUsable }
    var endpointDisplay: String { settings.endpoint?.display ?? "not set" }
    var isConfigured: Bool { settings.endpoint != nil }

    func pendingQuestion(for agent: Agent) -> String? {
        agentsModel.pendingQuestion(for: agent)
    }

    // MARK: - Collaborators

    let settings: SettingsStore
    /// Every computer's runtime, the current one included
    /// (docs/dev/design/ios-state-architecture.md 3.1): switching changes
    /// one id, and nothing is rebuilt or repointed.
    let fleet: ComputerFleet
    /// The phone's network path (`NWPathMonitor`), handed to every
    /// computer's link through `fleet.setPath`. The demo is always on
    /// Wi-Fi, whatever the Mac's network; `-sheepdogNoNetwork` has none.
    let pathSource: NetworkPathSource
    /// What each computer's runtime was made for (its address and pairing);
    /// a change starts a new epoch. Each connection reads its computer's
    /// endpoint from `endpoints` when it makes a client.
    var fingerprints: [String: String] = [:]
    let endpoints = EndpointBook()
    /// The current computer's endpoint the session last attached to.
    private var attachedEndpoint: DaemonEndpoint?
    /// The Live Activity and the earbud remote (`AwayControls.swift`).
    let away = AwayControls()
    /// The conversation lives on the current computer's model
    /// (`ComputerModel.conversation`), so a late reply from another one
    /// never lands in it; these read and write it in place.
    private var conversation: ComputerConversation {
        get { fleet.current?.conversation ?? spareConversation }
        _modify {
            if let model = fleet.current {
                yield &model.conversation
            } else {
                yield &spareConversation
            }
        }
    }
    /// The conversation while no computer is set up.
    private var spareConversation = ComputerConversation()
    private var reducer: ConversationReducer {
        get { conversation.reducer }
        _modify { yield &conversation.reducer }
    }
    private var history: ConversationHistory {
        get { conversation.history }
        _modify { yield &conversation.history }
    }
    /// The orchestrator's replies and held pushes, which the history does not carry.
    private var activity: BrainActivity {
        get { conversation.activity }
        _modify { yield &conversation.activity }
    }
    private var lastSeq: UInt64 {
        get { conversation.lastSeq }
        set { conversation.lastSeq = newValue }
    }
    private let cache: ConversationCache
    private var saveTask: Task<Void, Never>?
    private var historySyncTask: Task<Void, Never>?
    /// The computers whose newest page is being fetched: one sync per
    /// computer, so a sync still in flight for the computer the app left
    /// does not hold back the new one's.
    private var syncingHistory: Set<String> = []

    private(set) var appPhase: AppPhase = .active

    /// Whether the person agreed to send their voice and words to AI
    /// services (`SessionViewModel+Consent.swift`).
    let consent: AIConsentModel

    /// The demo sends nothing anywhere; debug launches that start
    /// configured (the UI tests) skip the question unless asked to show it.
    private static func skipsConsent(demo: Bool) -> Bool {
        if demo { return true }
        #if DEBUG
        if DebugLaunch.address != nil || DebugLaunch.resetState || DebugLaunch.computers != nil { return !DebugLaunch.askConsent }
        #endif
        return false
    }

    init(settings: SettingsStore = SettingsStore(), cache: ConversationCache = .standard(), demo: DemoDaemon? = nil) {
        self.settings = settings
        self.cache = cache
        self.demo = demo
        consent = AIConsentModel(store: AIConsent(defaults: settings.defaults), bypass: Self.skipsConsent(demo: demo != nil))
        let pathSource = Self.makePathSource(demo: demo != nil)
        self.pathSource = pathSource
        fleet = ComputerFleet(path: pathSource.path)
        let endpoint = settings.endpoint ?? DaemonEndpoint(host: "127.0.0.1")
        voice = VoiceEngine(endpoint: endpoint, talkMode: settings.talkMode, keepListeningWhenLocked: settings.keepListeningWhenLocked)
        computerCount = settings.computers.count
        #if DEBUG
        if DebugLaunch.resetHints || DebugLaunch.resetState {
            for key in VoiceEngine.hintKeys {
                UserDefaults.standard.removeObject(forKey: key)
            }
        }
        if DebugLaunch.renderChurn {
            Task { @MainActor [weak self] in
                try? await Task.sleep(for: .seconds(DebugLaunch.renderChurnDelay))
                while self != nil {
                    self?.objectWillChange.send()
                    try? await Task.sleep(nanoseconds: 100_000_000)
                }
            }
        }
        if DebugLaunch.countRenders {
            renderProbe = objectWillChange.sink { _ in RenderProbe.hit("model") }
        }
        #endif
        settingsModel.session = self
        agentsModel.fleet = fleet
        voice.resolveProvider = { [weak self] in self?.resolvedVoiceProvider }
        linkModel.onHelloChange = { [weak self] in self?.voice.providerSourcesChanged() }
        settingsModel.onVoiceProvidersChange = { [weak self] in self?.voice.providerSourcesChanged() }
        voice.host = self
        // Every computer's runtime, made now (started in `start`), so the
        // cached conversation loads into the current one.
        syncFleet()
        fleet.makeCurrent(settings.endpoint == nil ? nil : currentComputerID)
        attachedEndpoint = settings.endpoint
        presentation.onFollowup = { [weak self] followup in
            switch followup {
            case let .openWindow(request): self?.navigation.windowRequest = request
            }
        }
        presentation.onClosed = { [weak self] closed in self?.presentationClosed(closed) }
        away.onRoute = { [weak self] route in self?.handle(route: route) }
        away.onActivityToken = { [weak self] token in self?.sendActivityToken(token) }
        // The last conversation, at once: before the network, or without it.
        loadCache()
        conversationChanged(save: false)
    }

    /// The network path's source: `NWPathMonitor`, or a fixed path for the
    /// demo and the debug launch argument.
    private static func makePathSource(demo: Bool) -> NetworkPathSource {
        if demo { return ManualNetworkPath() }
        #if DEBUG
        if DebugLaunch.noNetwork { return ManualNetworkPath(NetworkPath(satisfied: false)) }
        #endif
        return SystemNetworkMonitor()
    }

    /// Runs a request on the current computer. Its reply comes back only
    /// if that computer is still current at the same epoch; otherwise it
    /// throws and nothing is applied (a late reply from the computer the
    /// app left lands nowhere).
    func current<T>(_ body: @escaping @Sendable (DaemonClient) async throws -> T) async throws -> T {
        try await fleet.onCurrent(as: DaemonClient.self, body)
    }

    /// Runs a tap that changes something through the current computer's
    /// `ActionRunner`, captured now: it finishes on that computer even if
    /// the app switches meanwhile (decision 6), with its dedup key
    /// (`key(computerID)`), timeout, idempotency key and safe retry.
    func action<T>(_ key: (String) -> String, _ kind: ActionRunner.Kind, idempotencyKey: String? = nil,
                   _ body: @escaping @Sendable (DaemonClient) async throws -> T) async throws -> T {
        guard settings.endpoint != nil, let model = fleet.current else { throw DaemonError.malformed("no daemon address set") }
        return try await model.actions.run(key(model.id), kind, as: DaemonClient.self, idempotencyKey: idempotencyKey, body)
    }

    /// Whether the current computer has an action under this key in flight.
    func actionInFlight(_ key: (String) -> String) -> Bool {
        guard let model = fleet.current else { return false }
        return model.actions.isInFlight(key(model.id))
    }

    private(set) var started = false
    private var certificateObserver: NSObjectProtocol?
    #if DEBUG
    private var renderProbe: AnyCancellable?
    #endif

    /// Called once from the first `onAppear`: an unconfigured app opens
    /// Settings instead of showing a Connect button that cannot work.
    func start() {
        // Every window's first appearance calls this (iPad); once is enough.
        guard !started else { return }
        started = true
        attachCurrent()
        for model in fleet.models { model.start() }
        pathSource.start { [weak self] path in self?.fleet.setPath(path) }
        if demo != nil {
            startDemo()
            return
        }
        // Siri, the Action Button and the Live Activity's buttons reach the
        // session from here on; a route that came first (a cold launch from
        // "Talk to Sheepdog") runs now.
        SheepdogIntentBridge.install { [weak self] url in self?.open(url: url) }
        syncAway()
        // A promoted pin or a newly learned certificate expiry reaches the
        // computer's link (`credentialsChanged(.pinPromoted)`), so the
        // rotation window is current without a relaunch.
        certificateObserver = NotificationCenter.default.addObserver(forName: DeviceCredentials.certificateChanged, object: nil, queue: .main) { [weak self] note in
            guard let credentials = note.object as? DeviceCredentials else { return }
            let expires = note.userInfo?[DeviceCredentials.certExpiresKey] as? Date
            MainActor.assumeIsolated { self?.certificateChanged(daemonID: credentials.daemonID, certExpires: expires) }
        }
        if !isConfigured || !settings.hasBeenConfigured {
            // A cold launch from the Camera app's pairing link asks about
            // the link instead (and reopens Settings if declined).
            if presentation.current == nil { presentation.present(.settings) }
        } else {
            Task {
                await refreshState(quiet: true)
                await syncHistory()
                await backfillActivity()
            }
        }
    }

    /// The app came back to the foreground: what happened while it was
    /// away is on the daemon.
    func becameActive() {
        // The daemon may have pushed the Live Activity while the app was
        // away: the next sync rewrites it even if our own state is unchanged.
        away.forgetShownActivity()
        send(.scenePhase(.active))
        // Coming back kicks the link: a probe now (which refreshes the
        // state), and the channels reopen once it answers.
        setAppPhase(.active)
        Task { await PushCoordinator.shared.refreshAuthorization() }
        guard isConfigured, settings.hasBeenConfigured else { return }
        Task {
            await syncHistory()
            await backfillActivity()
        }
    }

    /// The app is going to the background: write the cache now, not later.
    func willResignActive() {
        saveCache(now: true)
    }

    /// The app is in the background (screen locked, or another app). With
    /// Keep listening with the screen locked off, the core pauses: the audio
    /// stops and the daemon is told we are muted, so it closes a per-minute
    /// GPT-Live session. `becameActive` undoes it.
    func enteredBackground() {
        willResignActive()
        send(.keepListeningLockedChanged(settings.keepListeningWhenLocked))
        send(.scenePhase(.background))
        setAppPhase(.background)
    }

    /// Settings were saved: repoint everything without losing the conversation.
    func endpointChanged() {
        // A re-pair or a new address renews that computer's epoch; a
        // removed one stops. Every other runtime keeps running.
        let previous = fleet.current
        syncFleet()
        guard let endpoint = settings.endpoint else {
            fleet.makeCurrent(nil)
            attachedEndpoint = nil
            attachCurrent()
            return
        }
        if let old = attachedEndpoint { PushCoordinator.shared.endpointChanged(from: old) }
        let sameDaemon = attachedEndpoint.map { ConversationCache.key(for: endpoint) == ConversationCache.key(for: $0) } ?? false
        fleet.makeCurrent(currentComputerID)
        attachedEndpoint = endpoint
        // The voice and the terminal follow the current computer, at the
        // address its link uses.
        voice.update(endpoint: channelEndpoint(endpoint))
        if demo == nil {
            termHub.update(endpoint: channelEndpoint(endpoint))
            agentsModel.setShellOffered(nil)
        }
        notices.banner = nil
        linkModel.setHello(fleet.current?.hello)
        attachCurrent()
        if !sameDaemon {
            // Another computer, its own conversation. The one the app left
            // keeps running (its answers finish there, decision 6); only its
            // live bubbles end with the voice.
            if let previous, previous !== fleet.current {
                previous.conversation.reducer.clear()
                saveCache(now: true, model: previous)
            }
            cardInbox.clear()
            healthModel.clear()
            settingsModel.clear()
            notices.clearForSwitch()
            if !conversation.loaded { loadCache() }
            conversationChanged(save: false)
            // Learned per daemon: a different one gets the benefit of the
            // doubt again rather than staying hidden from the last one's 404.
            conversationResetSupported = true
            // What the new computer last said, at once.
            if let state = fleet.current?.lastState { apply(state) }
        }
        if voice.core.connection.wantsConnection {
            disconnect()
            connect()
        }
        // The new computer's stream may have been open all along (as
        // another computer it only refreshed on its events), so nothing
        // reopens to catch the conversation up: sync it here.
        Task {
            await syncHistory()
            await backfillActivity()
        }
    }

    // MARK: - Connect / disconnect

    func connect() {
        guard let endpoint = settings.endpoint else {
            presentation.present(.settings)
            return
        }
        guard requireAIConsent(onAgree: { [weak self] in self?.connect() }) else { return }
        voice.update(endpoint: channelEndpoint(endpoint))
        send(.connectTapped)
    }

    /// The current computer at the address its link uses now (same pin):
    /// `/ws` and `/term` open there, like requests and `/events`. Before the
    /// first race is won, the preferred address.
    func channelEndpoint(_ endpoint: DaemonEndpoint) -> DaemonEndpoint {
        endpoint.at(fleet.current?.link.addressInUse)
    }

    /// The `probe` effect: the auth probe first — a 401 on the WebSocket
    /// upgrade is almost unreadable, so find out here where the status code
    /// is plain — then the microphone permission.
    private func openSession() async {
        // Withdrawn while the probe was on its way: no voice session.
        guard consent.isAllowed else {
            send(.endTapped)
            return
        }
        if fleet.current?.link.state == .noNetwork {
            send(.noNetwork)
            return
        }
        do {
            accept(try await current { try await $0.state() })
            await loadHello()
            await settingsModel.loadVoiceProviders()
        } catch let error as DaemonError {
            send(.probeFailed(error))
            return
        } catch is ComputerFleet.Moved, is ComputerModel.Stale {
            // Asked of the computer the app left: the connect that followed
            // the switch has its own probe.
            return
        } catch {
            send(.probeFailed(.malformed(error.localizedDescription)))
            return
        }
        guard voice.core.connection.wantsConnection else { return }
        // Why first, then the system prompt (only if the user goes on).
        let granted: Bool
        if AVAudioApplication.shared.recordPermission == .undetermined, !(await ask(.microphone)) {
            granted = false
        } else {
            granted = await AudioEngine.requestMicrophone()
        }
        // The permission prompt can sit there for a while; the core ignores
        // the answer if End was tapped meanwhile.
        send(.probeSucceeded(micGranted: granted))
    }

    /// Shows a permission primer and waits for the answer: true to go on
    /// to the system prompt. The UI tests start configured and skip it
    /// (`-sheepdogAskPrimers` shows it).
    func ask(_ primer: PermissionPrimer) async -> Bool {
        #if DEBUG
        if DebugLaunch.address != nil || DebugLaunch.resetState, !DebugLaunch.askPrimers { return true }
        #endif
        // A primer still waiting is answered "not now" and closed.
        if let waiting = self.primer { answerPrimer(false, closing: waiting) }
        return await withCheckedContinuation { continuation in
            primerAnswer = continuation
            self.primer = primer
            presentation.present(.primer(primer))
        }
    }

    /// The primer's answer: once, and its cover closes.
    func answerPrimer(_ yes: Bool) {
        guard let primer else { return }
        answerPrimer(yes, closing: primer)
    }

    private func answerPrimer(_ yes: Bool, closing primer: PermissionPrimer) {
        self.primer = nil
        presentation.dismiss(.primer(primer))
        primerAnswer?.resume(returning: yes)
        primerAnswer = nil
    }

    func disconnect() {
        send(.endTapped)
        voice.meter.setLevel(0)
    }

    /// A banner's Try again: the link tries now, and the voice starts over
    /// with a clean disconnect (if one is needed) and a fresh connect.
    func reconnect() {
        fleet.current?.retry()
        if voice.core.connection.wantsConnection { disconnect() }
        connect()
    }

    /// Try now, Try again, the chip sheet's Reconnect: the link tries at
    /// once (a probe, the backoff starts over). A running voice session
    /// starts over too; one that is off stays off.
    func retryLink() {
        fleet.current?.retry()
        if voice.core.connection.wantsConnection {
            disconnect()
            connect()
        }
    }

    // MARK: - Controls

    /// The one-time caption in the bar's status line: the hint, short
    /// enough for two lines beside the orb.
    var muteCaption: String { "Mic off — \(voiceName) can still talk" }

    func setTalkMode(_ mode: TalkMode) {
        guard mode != voice.core.mic.talkMode else { return }
        settings.talkMode = mode
        send(.talkModeChanged(mode))
    }

    /// The provider picker changed it (`SettingsModel`): the core learns it.
    func voiceProviderSelected(_ id: String?) { send(.providerSelected(id)) }

    /// Every session event, to the voice (`VoiceEngine.send`).
    private func send(_ event: SessionEvent) {
        voice.send(event)
    }

    // MARK: - Away from the screen

    /// The Live Activity and the earbud remote follow the session: there
    /// while a voice session is wanted, gone the moment it ends.
    private func syncAway() {
        // The Live Activity and the earbud remote belong to the real session.
        guard demo == nil else { return }
        #if DEBUG
        if let demo = DebugLaunch.demoLiveActivity {
            away.sync(LiveActivitySamples.demo(demo), voiceName: voiceName)
            return
        }
        #endif
        let activityState = wantsConnection
            ? LiveActivityContent.make(
                state: voice.state,
                muted: voice.isMuted,
                holdToTalk: voice.talkMode == .holdToTalk,
                talking: voice.holding,
                agents: agentsModel.agents,
                questions: agentsModel.questions,
                computer: showsComputerLabels ? currentComputerName : nil,
                others: otherSnapshots,
                suspendedInBackground: voice.core.suspension.backgroundSuspended
            )
            : nil
        away.sync(activityState, voiceName: voiceName)
    }

    /// The Live Activity's push token to the daemon (nil: the activity
    /// ended), so it can update the banner while the app is away. Best
    /// effort: without it the banner just stops updating while away.
    private func sendActivityToken(_ token: String?) {
        guard demo == nil else { return }
        Task {
            if let token {
                _ = try? await current { try await $0.registerLiveActivity(token: token, environment: PushCoordinator.environment) }
            } else {
                _ = try? await current { try await $0.endLiveActivity() }
            }
        }
    }

    /// A `sheepdog://` link, or an App Intent speaking one.
    func open(url: URL) {
        guard demo == nil else { return }
        if let code = PairingCode(url.absoluteString) {
            // Whatever is up (Settings) closes first, so the question is
            // on top.
            presentation.presentInstead(.pairing(.confirm(code)))
            return
        }
        guard let route = AppRoute(url: url) else {
            SheepdogLog.session.info("ignored link \(url.absoluteString, privacy: .public)")
            return
        }
        handle(route: route)
    }

    func handle(route: AppRoute) {
        for action in route.actions(wantsConnection: wantsConnection, muted: voice.isMuted, talkMode: voice.talkMode) {
            switch action {
            case .showConversation:
                navigation.pane = .conversation
            case .connect:
                connect()
            case .toggleMute:
                voice.toggleMute()
            case .toggleTalk:
                voice.toggleTalk()
            case .stop:
                voice.stop()
            case .disconnect:
                disconnect()
            case let .openAgent(id):
                // What a tap on an agent's notification does.
                PushCoordinator.shared.focus(paneID: id)
            case let .openWindow(id, computer):
                // A Live Activity row. Another computer's agent shows on
                // the Agents page, with Talk to; the app never switches on
                // its own.
                if let computer, computer != currentComputerID, session(computer) != nil {
                    navigation.pane = .flock
                } else {
                    openAgentWindow(paneID: id)
                }
            }
        }
    }

    // MARK: - Events and state

    // MARK: - The link

    /// Who needs the current computer's link: the app in front, or the
    /// voice in the background with Keep listening on.
    var currentDemand: LinkDemand {
        LinkDemand(isCurrent: true, phase: appPhase, voiceOn: wantsConnection, keepListening: settings.keepListeningWhenLocked)
    }

    /// Each computer's demand: the current one's with the voice, the
    /// others' (`otherDemand`).
    func updateDemands() {
        let others = otherDemand
        for model in fleet.models {
            model.setDemand(model === fleet.current ? currentDemand : others)
        }
    }

    private func setAppPhase(_ phase: AppPhase) {
        guard phase != appPhase else { return }
        // Keep listening may have changed since: the demand first, then
        // the phase (coming to the foreground kicks every link). Other
        // computers stream in the foreground and stop in the background
        // (decision 2).
        updateDemands()
        appPhase = phase
        for model in fleet.models { model.setAppPhase(phase) }
    }

    /// The current computer's certificate changed under its credentials.
    func currentCertificateChanged(certExpires: Date?) {
        fleet.current?.certificateChanged(certExpires: certExpires)
    }

    /// Attaches the session to the current computer's model: its link, its
    /// state, its `/events` output. Every other model is detached and runs
    /// on its own (refreshing on its events). Nothing is rebuilt.
    func attachCurrent() {
        let current = fleet.current
        for model in fleet.models where model !== current { detach(model) }
        updateDemands()
        // The voice's `/ws` and the terminal's `/term` are the current
        // computer's channels: its link alone opens and reopens them.
        voice.attach(to: current)
        current?.attach(.term, termHub)
        guard let model = current else {
            linkChanged(ComputerLink(addresses: []))
            setLiveUpdates(.idle)
            return
        }
        // Each hook checks that its model is still current: a late call
        // from the computer the app left lands nowhere.
        model.onLinkChange = { [weak self, weak model] link in
            guard let self, let model, self.fleet.current === model else { return }
            self.linkChanged(link)
        }
        model.onState = { [weak self, weak model] state in
            guard let self, let model, self.fleet.current === model else { return }
            self.apply(state)
            if self.linkModel.hello == nil { Task { await self.loadHello() } }
        }
        model.onStreamOutput = { [weak self, weak model] output in
            guard let self, let model, self.fleet.current === model else { return }
            self.handle(event: output)
        }
        model.onStreamOpening = { [weak self, weak model] in
            guard let self, let model, self.fleet.current === model else { return }
            // Every (re)open: what the stream missed is in the history
            // too, and a page is cheaper than a long replay to read.
            Task {
                await self.syncHistory()
                await self.backfillActivity()
            }
        }
        model.onLiveUpdates = { [weak self, weak model] life in
            guard let self, let model, self.fleet.current === model else { return }
            self.setLiveUpdates(life)
        }
        model.eventsSince = { [weak model] in model.map { Self.resumeID($0.conversation) } ?? 0 }
        // The voice's card names the computer in an error only while
        // several are paired, like the rest of the current computer's errors.
        model.approvalErrorName = { [weak self] in
            guard let self, self.showsComputerLabels else { return nil }
            return self.currentComputerName
        }
        linkChanged(model.link)
        setLiveUpdates(model.liveUpdates)
    }

    private func detach(_ model: ComputerModel) {
        model.onLinkChange = nil
        model.onState = nil
        model.onStreamOutput = nil
        model.onStreamOpening = nil
        model.onLiveUpdates = nil
        model.eventsSince = nil
        model.approvalErrorName = nil
        voice.detach(from: model)
        model.attach(.term, nil)
    }

    /// `/api/hello` for the current computer, kept on its model.
    func loadHello() async {
        guard let model = fleet.current, let fetched = try? await current({ try await $0.hello() }) else { return }
        model.hello = fetched
        linkModel.setHello(fetched)
    }

    /// The link moved: what the screen shows follows, and the transitions
    /// that need an action get one.
    private func linkChanged(_ next: ComputerLink) {
        let previous = link
        link = next
        presentLink()
        presentAddresses()
        if case let .refused(problem) = next.state, previous.state != next.state {
            // Only a new scan fixes it: the voice ends too, and the scan
            // sheet opens once.
            send(.tokenRefused(endpoint: endpointDisplay))
            needsNewScan(problem)
        }
        // Coming online needs nothing more here: the channel set opens
        // `/events`, and every open syncs the history.
    }

    /// What the Agents pane shows when it has no agents, from the link.
    /// `link` itself is not observable, so the view re-renders on the
    /// presentation, which `presentLink` updates whenever the link's state
    /// moves; reading it here is what registers that dependency.
    var flockPaneState: FlockPaneState {
        _ = linkModel.presentation
        return FlockPaneState.resolve(link: link, hasAgents: !flock.isEmpty)
    }

    private func presentLink() {
        let presented = LinkPresentation(link, name: showsComputerLabels ? currentComputerName : nil, voiceOn: wantsConnection)
        linkModel.setPresentation(presented)
    }

    private func setLiveUpdates(_ life: SocketLife) {
        linkModel.setLiveUpdates(life)
    }

    /// Where `/events` resumes: the last id seen (kept in the cache across
    /// launches), else the newest history item's.
    private static func resumeID(_ conversation: ComputerConversation) -> UInt64 {
        if conversation.lastSeq > 0 { return conversation.lastSeq }
        if let newest = conversation.history.newestID, newest > 0 { return UInt64(newest) }
        return 0
    }

    private func handle(event output: SSEParser.Output) {
        switch output {
        case let .event(event):
            // A replay after a reconnect starts after our id, but an event
            // seen twice must still change nothing.
            if event.seq > 0, event.seq <= lastSeq { return }
            lastSeq = max(lastSeq, event.seq)
            apply(event: event)
        case .closed, .undecodable:
            break
        }
    }

    /// An event is news, worth a haptic, only when it is recent — not when
    /// a reconnect replays the last hour.
    private func isFresh(_ event: DaemonEvent) -> Bool {
        guard let ts = event.ts else { return true }
        return Date().timeIntervalSince(ts) < 60
    }

    private func apply(event: DaemonEvent) {
        let folded = history.fold(event) != nil
        if activity.fold(event) || folded {
            conversationChanged()
        }
        healthModel.fold(event)
        switch event.type {
        case "voice.push":
            guard let text = event.string("text"), !text.isEmpty, event.string("urgency") != "silent" else { break }
            if isFresh(event) { Haptics.push() }
            refreshSoon()
        case "voice.tool_call":
            send(.toolCall)
        case "voice.tool_result":
            send(.toolResult)
        case "pending.created":
            if let pending = PendingAction(event: event) {
                fleet.current?.questionCreated(pending)
                // The needs-you cue: a new open question with options, once
                // per question, and only when it is news (not a replay).
                if pending.isAnswerPrompt, isFresh(event), cuedPendingIDs.insert(pending.id).inserted {
                    Haptics.needsYou()
                }
            }
            refreshSoon()
        case "conversation.reset":
            // Fires for every reset, including our own (the app resets its
            // own reducer immediately on the POST reply too, so a replay
            // here is a no-op) and ones started elsewhere (the browser,
            // voice "start over"). The divider itself came from `fold`
            // above; this only closes whatever bubble was still open.
            reducer.apply(.reset)
            conversationChanged()
        case "pending.confirmed", "pending.expired", "pending.cancelled", "pending.replaced":
            let resolution = ConversationItem.Resolution(rawValue: String(event.type.dropFirst("pending.".count))) ?? .unknown
            let id = event.type == "pending.replaced" ? event.string("old_id") : event.string("id")
            if let id {
                fleet.current?.questionResolved(id, as: resolution, answer: event.string("answer"), via: event.string("via"))
            }
            refreshSoon()
        default:
            if event.type.hasPrefix("herdr.") || event.type.hasPrefix("supervisor.") || event.type.hasPrefix("web.inject")
                || event.type.hasPrefix("investigation.") || event.type.hasPrefix("promise.") || event.type == "daemon.start" {
                refreshSoon()
            }
        }
    }

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

    @discardableResult
    func refreshState(quiet: Bool) async -> Bool {
        guard settings.endpoint != nil else { return false }
        do {
            accept(try await current { try await $0.state() })
            if linkModel.hello == nil { await loadHello() }
            if !quiet { notices.banner = nil }
            return true
        } catch let error as DaemonError {
            // A loud refresh that fails ends the session, as a failed
            // Connect would.
            if !quiet { send(.probeFailed(error)) }
            SheepdogLog.session.error("state: \(String(describing: error), privacy: .public)")
            return false
        } catch {
            // Quiet even on a decode failure: the periodic and
            // event-debounced refreshes retry on their own, and a banner
            // for a transient bad payload would be more alarming than
            // useful. Still logged, so a persistent failure is visible.
            SheepdogLog.session.error("state: \(String(describing: error), privacy: .public)")
            return false
        }
    }

    /// A state fetched straight from the current computer: it lands on
    /// that computer's model, which hands it back through `onState`.
    private func accept(_ state: DaemonState) {
        if let model = fleet.current { model.accept(state) } else { apply(state) }
    }

    /// The agents and questions the last applied state showed, to tell
    /// what changed (the cue, the Live Activity).
    private var appliedAgents: [Agent] = []
    private var appliedQuestions: [PendingAction] = []

    /// The current computer applied a state: its agents, questions and
    /// answer phases are already on its model; here the cues, the Live
    /// Activity and the rest of the state follow.
    private func apply(_ state: DaemonState) {
        let agents = agentsModel.agents
        let agentsChanged = agents != appliedAgents
        appliedAgents = agents
        let open = agentsModel.questions
        let questionsChanged = open != appliedQuestions
        appliedQuestions = open
        if questionsChanged {
            // A question that arrived without its own event (another
            // agent's, read from the state) rises with a light tap; the
            // event-driven one already had the needs-you cue.
            if questionsSeen {
                for q in open where q.isAnswerPrompt && q.id != state.pendingAction?.id && cuedPendingIDs.insert(q.id).inserted {
                    Haptics.push()
                }
            } else {
                for q in open { cuedPendingIDs.insert(q.id) }
            }
        }
        questionsSeen = true
        // The Live Activity's rows and counts.
        if agentsChanged || questionsChanged { syncAway() }
        assign(\.brain, state.brain)
        assign(\.stateVoiceProvider, state.voiceProvider)
        assign(\.stateVoiceModel, state.voiceModel)
        notices.setUpdate(state.update)
    }

    /// Assigns a published property only when the value differs: setting
    /// an equal value still announces a change to every observer.
    func assign<Value: Equatable>(_ key: ReferenceWritableKeyPath<SessionViewModel, Value>, _ value: Value) {
        if self[keyPath: key] != value { self[keyPath: key] = value }
    }

    // MARK: - Conversation history

    private func conversationChanged(save: Bool = true) {
        cardInbox.absorb(history.items)
        // A card item whose card the daemon's store dropped still opens
        // what this phone got live.
        let items = history.items.map { item -> ConversationItem in
            guard item.kind == .card, item.card == nil, let card = cardInbox.card(for: item) else { return item }
            var filled = item
            filled.card = card
            return filled
        }
        conversationModel.setRows(ConversationTimeline.rows(history: items, live: reducer.bubbles, activity: activity, cards: cardInbox.live))
        conversationModel.setCanLoadOlder(history.canLoadOlder)
        if save { saveCache(now: false) }
    }

    /// Loads the newest page and merges it: the server's copy wins, and the
    /// finished transcript bubbles it now has stop showing twice.
    func syncHistory() async {
        guard settings.endpoint != nil, let syncing = fleet.current?.id, !syncingHistory.contains(syncing) else { return }
        // A bubble still growing on /ws is already an item on the server
        // (from its first fragment); swapping now would show it twice. The
        // dog's `out` bubble often closes only when the next turn starts.
        guard reducer.openBubble == nil else {
            scheduleHistorySync()
            return
        }
        syncingHistory.insert(syncing)
        defer { syncingHistory.remove(syncing) }
        let sent = Date()
        do {
            let page = try await current { try await $0.conversation(limit: 200) }
            history.applyNewest(page, requestedAt: sent)
            reducer.dropSettledSpeech(before: sent)
            conversationChanged()
        } catch {
            // Quiet: the cache and the live stream still show something,
            // and the state probe reports what is wrong in plain words.
            SheepdogLog.socket.error("history: \(String(describing: error), privacy: .public)")
        }
    }

    /// The orchestrator's recent replies and the held pushes from the daemon's
    /// current run (`GET /api/events`, prefix-filtered), for what the
    /// stream did not bring this time: a relaunch, or events before the
    /// stream's resume point. Quiet on failure — the stream still folds
    /// them live.
    func backfillActivity() async {
        guard settings.endpoint != nil else { return }
        var changed = false
        for type in ["voice.tool_result", "voice.push_"] {
            guard let events = try? await current({ try await $0.recentEvents(type: type, limit: 100) }) else { continue }
            for event in events where activity.fold(event) { changed = true }
        }
        if changed { conversationChanged(save: false) }
    }

    /// A voice turn just ended: once the daemon has logged it, the newest
    /// page has it as server items, which is what the cache keeps.
    private func scheduleHistorySync() {
        historySyncTask?.cancel()
        historySyncTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 2_500_000_000)
            guard !Task.isCancelled, let self else { return }
            await self.syncHistory()
        }
    }

    /// Pull-to-load-older: the page before the oldest item shown.
    func loadOlder() async {
        guard let before = history.nextBefore, !conversationModel.loadingOlder else { return }
        conversationModel.setLoadingOlder(true)
        defer { conversationModel.setLoadingOlder(false) }
        do {
            let page = try await current { try await $0.conversation(limit: 100, before: before) }
            history.applyOlder(page)
            conversationChanged()
        } catch let error as DaemonError {
            show(Banner(tone: .warning, message: "Couldn't load the earlier conversation.", hint: error.message, retryTitle: nil))
        } catch {}
    }

    /// Settings ▸ Clear conversation on this phone.
    func clearConversation() {
        if let endpoint = settings.endpoint { cache.clear(for: endpoint) }
        history.clear()
        reducer.clear()
        activity.clear()
        cardInbox.clear()
        conversationChanged(save: false)
    }

    // MARK: - Health

    /// When the daemon started: its `daemon.start` event, else the newest
    /// "Sheepdog started." divider in the history (the event ring may
    /// have rolled past it on a long run).
    var daemonStartedAt: Date? {
        healthModel.health.startedAt ?? history.items.last { $0.kind == .notice && $0.text.hasPrefix("Sheepdog started") }?.ts
    }

    /// Everything the Health pane shows, fetched once when it opens (and
    /// on pull to refresh); `/events` keeps the problems and the billed
    /// seconds current after that.
    func loadHealth() async {
        guard settings.endpoint != nil, healthModel.begin() else { return }
        defer { healthModel.end() }
        async let state: Bool = refreshState(quiet: true)
        async let settings: Void = settingsModel.loadAll()
        if linkModel.hello == nil { await loadHello() }
        // `/api/events` filters by one type prefix: one small request each.
        var next = healthModel.health
        for type in ["daemon.start", "voice.billed_seconds", "web.client.stats", "brain.fallback", "brain.rate_limited", "voice.connect_error",
                     "voice.push_", "supervisor.echo_dropped", "web.audio_unheard", "voice.playback", "worker.session_missing"] {
            let limit = type == "web.client.stats" || type == "daemon.start" ? 1 : 30
            guard let events = try? await current({ try await $0.recentEvents(type: type, limit: limit) }) else { continue }
            for event in events { next.fold(event) }
        }
        healthModel.replace(with: next)
        _ = await (state, settings)
    }

    // MARK: - Typed input

    /// The text field under the conversation: says `text` as if spoken,
    /// through `POST /api/utterance`, which records it as typed
    /// user input (the rail takes it like a transcript). The
    /// conversation shows it from `/events` (or the next history sync when
    /// not connected). Returns true when the daemon took it.
    func sendTyped(_ text: String) async -> Bool {
        let text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, settings.endpoint != nil, !conversationModel.sendingTyped else { return false }
        guard requireAIConsent() else {
            if consent.decision == .withdrawn { conversationModel.setTypedError(Self.aiOffMessage) }
            return false
        }
        conversationModel.setSendingTyped(true)
        conversationModel.setTypedError(nil)
        defer { conversationModel.setSendingTyped(false) }
        // The same line sent again after a failure keeps its key: if the
        // first did reach the computer, it is not said twice.
        if typedAttempt?.text != text { typedAttempt = (text, UUID().uuidString) }
        let computer = fleet.currentID
        do {
            _ = try await action(ActionRunner.Key.typedLine, .typedLine, idempotencyKey: typedAttempt?.key) { try await $0.sendUtterance(text) }
            typedAttempt = nil
            conversationModel.typedLineSent()
            Haptics.tap()
            if !voice.core.connection.wantsConnection {
                // No event stream: the daemon has logged it by now.
                await syncHistory()
                await backfillActivity()
            }
            return true
        } catch is ComputerModel.Stale {
            typedAttempt = nil
        } catch {
            // A reply (even a refusal) is final; only no reply keeps the key.
            if !ActionRunner.isTransport(error) { typedAttempt = nil }
            // The error shows in the composer it was typed in.
            guard fleet.currentID == computer else { return false }
            conversationModel.setTypedError(requestFailure(error).message)
            Haptics.answerFailed()
        }
        return false
    }

    // MARK: - Agent window

    /// `AgentScreenView` opened `paneID`'s window: stream it over `/ws`
    /// when the daemon can (`AgentScreenStream`).
    func openAgentScreen(paneID: String) {
        if let message = screenStream.open(paneID: paneID) { voice.send(message: message) }
    }

    /// The window closed: stop its stream.
    func closeAgentScreen(paneID: String) {
        if let message = screenStream.close(paneID: paneID) { voice.send(message: message) }
    }

    /// Whether `paneID`'s window is pushed over `/ws` (else the view polls).
    func agentScreenStreaming(paneID: String) -> Bool {
        screenStream.isStreaming(paneID)
    }

    /// `GET /api/agents/{pane}/screen` for `AgentScreenView`. Throws when no
    /// daemon is configured.
    func agentScreen(paneID: String, hash: String?) async throws -> AgentScreen? {
        guard settings.endpoint != nil else { throw DaemonError.malformed("no daemon address set") }
        return try await current { try await $0.agentScreen(paneID: paneID, hash: hash) }
    }

    private var inputSeq = 0

    /// `POST /api/agents/{pane}/input` with a prompt.
    func agentInput(paneID: String, text: String) async throws -> AgentInputReply {
        guard settings.endpoint != nil else { throw DaemonError.malformed("no daemon address set") }
        return try await action({ ActionRunner.Key.input($0, paneID) + ":" + text }, .input) { try await $0.agentInput(paneID: paneID, text: text) }
    }

    /// `POST /api/agents/{pane}/input` with keys.
    func agentInput(paneID: String, keys: [AgentKey]) async throws -> AgentInputReply {
        guard settings.endpoint != nil else { throw DaemonError.malformed("no daemon address set") }
        // Keys are not idempotent: every press is its own action.
        inputSeq += 1
        let seq = inputSeq
        return try await action({ ActionRunner.Key.input($0, paneID) + ":\(seq)" }, .input) { try await $0.agentInput(paneID: paneID, keys: keys) }
    }

    /// `GET /api/agents/{pane}/changes` (the changes view).
    func agentChanges(paneID: String) async throws -> AgentChanges? {
        guard settings.endpoint != nil else { throw DaemonError.malformed("no daemon address set") }
        return try await current { try await $0.agentChanges(paneID: paneID) }
    }

    /// `GET /api/agents/{pane}/changes/{path}`.
    func agentDiff(paneID: String, path: String) async throws -> FileDiff? {
        guard settings.endpoint != nil else { throw DaemonError.malformed("no daemon address set") }
        return try await current { try await $0.agentDiff(paneID: paneID, path: path) }
    }

    /// `POST /api/agents/{pane}/attachments`.
    func uploadAttachment(paneID: String, data: Data, fileName: String, mimeType: String) async throws -> AttachmentReply {
        guard settings.endpoint != nil else { throw DaemonError.malformed("no daemon address set") }
        return try await current { try await $0.uploadAttachment(paneID: paneID, data: data, fileName: fileName, mimeType: mimeType) }
    }

    // MARK: - Live agent window (/term)

    /// The app's one `/term` connection, shared by every open agent window
    /// (`TermHub`). The demo's answers from its scripted sessions.
    private(set) lazy var termHub: TermHub = {
        let hub = makeTermHub()
        hub.onReady = { [weak self] ready in
            self?.agentsModel.setShellOffered(ready.shell)
        }
        return hub
    }()


    /// Whether to show the shell's entry points: the daemon has `/term` and
    /// has not said the shell is off.
    var shellEntryShown: Bool { termAvailability == .yes && agentsModel.shellOffered != false }

    /// Learns `term-ready.shell` before any window opens (the Agents tab).
    func warmUpTerm() {
        guard termAvailability == .yes, agentsModel.shellOffered == nil else { return }
        termHub.warmUp()
    }

    private func makeTermHub() -> TermHub {
        if let demo {
            #if DEBUG
            let behavior = DebugLaunch.demoTerm
            let tickInterval: TimeInterval = DebugLaunch.frameChurn ? 0.05 : 1
            #else
            let behavior = DemoTermTransport.Behavior.normal
            let tickInterval: TimeInterval = 1
            #endif
            return TermHub(endpoint: DemoDaemon.endpoint, makeTransport: { _ in
                DemoTermTransport(behavior: behavior, tickInterval: tickInterval) { [weak demo] pane in demo?.agentScreen(paneID: pane) }
            })
        }
        return TermHub(endpoint: settings.endpoint ?? DaemonEndpoint(host: "127.0.0.1"))
    }

    /// Whether the daemon has the user's own agent actions (New agent,
    /// Rename, Stop, Close).
    var agentActionsAvailable: Bool {
        if demo != nil { return true }
        if case let .ready(_, _, features) = linkModel.handshake { return features.contains(AgentAction.feature) }
        return fleet.current?.features?.contains(AgentAction.feature) ?? false
    }

    /// Runs `body` against the daemon (the agent actions). Throws when no
    /// daemon is configured.
    func daemon<T>(_ body: @escaping @Sendable (DaemonClient) async throws -> T) async throws -> T {
        guard settings.endpoint != nil else { throw DaemonError.malformed("no daemon address set") }
        return try await current(body)
    }

    /// What a failed request says where it was made: a timeout while the
    /// link is online is one slow answer, not an unreachable computer.
    func requestFailure(_ error: Error) -> RequestFailure {
        RequestFailure(error, linkUsable: link.isUsable, name: showsComputerLabels ? currentComputerName : nil)
    }

    /// Whether the agent window can go live (`/term`). A pure read: the
    /// features are remembered where the handshake lands (`publish`).
    var termAvailability: TermAvailability {
        if demo != nil { return .yes }
        return TermAvailability.from(handshake: linkModel.handshake, remembered: fleet.current?.features)
    }

    // MARK: - New conversation

    /// The ⋯ menu's "New conversation", after the confirmation dialog.
    /// Unlike `clearConversation()` this keeps the scrollback — it only
    /// resets the orchestrator's and the voice model's short-term context on
    /// the daemon, which then logs a `subkind: reset` notice we pick up
    /// the normal way (live, from `/events`, or from the next history
    /// sync), the same as any other notice.
    func startNewConversation() {
        guard settings.endpoint != nil else { return }
        Task { [weak self] in
            guard let self else { return }
            do {
                let response = try await self.current { try await $0.resetConversation(via: "app") }
                self.conversationResetSupported = ConversationResetAvailability(response: response).showsAction
                guard response != nil else {
                    self.show(Banner(tone: .info, message: "This daemon doesn't have New conversation yet.", hint: nil, retryTitle: nil))
                    return
                }
                // No optimistic local divider: the daemon already logged
                // the reset before answering, so the next history sync (or
                // the `conversation.reset` event, on the same event loop
                // that closes the reducer's open bubble) has it with a
                // real id — the same id-based merge every other notice
                // uses, so it cannot show twice.
                reducer.apply(.reset)
                conversationChanged()
                Haptics.newConversation()
                await self.syncHistory()
            } catch let error as DaemonError {
                self.show(Banner(tone: .warning, message: "Couldn't start a new conversation.", hint: error.hint ?? error.message, retryTitle: nil))
            } catch {
                self.show(Banner(tone: .warning, message: "Couldn't start a new conversation.", hint: error.localizedDescription, retryTitle: nil))
            }
        }
    }

    /// Reads the current computer's cached conversation into its model.
    private func loadCache() {
        conversation.loaded = true
        guard let endpoint = settings.endpoint, let snapshot = cache.load(for: endpoint) else {
            history = ConversationHistory()
            lastSeq = 0
            return
        }
        history = snapshot.history
        lastSeq = UInt64(max(0, snapshot.lastEventID ?? 0))
    }

    /// Writes a computer's conversation (the current one's by default) to
    /// its own cache, keyed by its own endpoint.
    private func saveCache(now: Bool, model: ComputerModel? = nil) {
        let target = model ?? fleet.current
        guard let endpoint = target.flatMap({ endpoints[$0.id] }) ?? settings.endpoint else { return }
        let saved = target?.conversation ?? spareConversation
        if model == nil { saveTask?.cancel() }
        let snapshot = ConversationSnapshot(endpoint: endpoint, history: saved.history, lastEventID: saved.lastSeq > 0 ? Int64(clamping: saved.lastSeq) : nil)
        let cache = self.cache
        let write = {
            do { try cache.save(snapshot, for: endpoint) } catch {
                SheepdogLog.socket.error("cache: \(String(describing: error), privacy: .public)")
            }
        }
        if now {
            write()
            return
        }
        saveTask = Task.detached(priority: .utility) {
            try? await Task.sleep(nanoseconds: 1_000_000_000)
            guard !Task.isCancelled else { return }
            write()
        }
    }

    // MARK: - Approvals

    /// Questions the needs-you cue already played for.
    private var cuedPendingIDs: Set<String> = []
    /// The first state has been read: questions open before it are not news.
    private var questionsSeen = false

    // MARK: - Cards

    /// The app is in the foreground.
    private var screenVisible = false
    /// The voice screen is the one in front (or a card sheet over it).
    private var voiceScreenInFront = false

    /// The main screen says what is in front: sent to the daemon as
    /// `visibility` when it changed and the daemon takes it.
    func setScreen(visible: Bool, voiceScreen: Bool) {
        screenVisible = visible
        voiceScreenInFront = voiceScreen
        // The conversation is in front: its rows are the card.
        if voiceScreen || !visible { cardLabel.hide() }
        if demo == nil, let message = cardVisibility.update(visible: visible, voiceScreen: voiceScreen) {
            voice.send(message: message)
        }
    }

    /// A sheet the model cannot see opened or closed over the voice screen.
    func setScreenCover(_ name: String, _ covering: Bool) {
        agentsModel.setScreenCover(name, covering)
    }

    /// A card from the daemon (or the demo): once, as a sheet when the
    /// voice screen is in front (replacing a card already open), else
    /// only as a row in the conversation.
    func receiveCard(_ card: ScreenCard) {
        let arrival = cardInbox.receive(card, canPresent: screenVisible && voiceScreenInFront)
        guard arrival != .duplicate else { return }
        conversationChanged()
        if arrival == .open {
            let shown = Presentation.card(cardInbox.card(id: card.cardID) ?? card)
            // A newer card replaces the card on screen.
            if presentedCard != nil { presentation.replace(shown) } else { presentation.present(shown) }
            Haptics.tap()
            AccessibilityNotification.Announcement("On your screen: \(card.displayTitle)").post()
        } else {
            // Another screen is in front: a small label points at the row.
            cardLabel.show(cardInbox.card(id: card.cardID) ?? card, appActive: appPhase == .active)
        }
        // The card's own history item replaces the live row.
        scheduleHistorySync()
    }

    /// A sheet or cover finished closing.
    private func presentationClosed(_ closed: Presentation) {
        switch closed {
        case .pairing:
            // Declined a pairing link with nothing paired: back to the
            // first-run screen.
            if !isConfigured { presentation.present(.settings) }
        case let .primer(kind) where primer == kind:
            // Closed without an answer: "not now".
            answerPrimer(false)
        default:
            break
        }
    }

    /// A card row was tapped.
    func openCard(_ card: ScreenCard) {
        cardLabel.hide()
        presentation.present(.card(card))
    }

    /// A card's "Open window": close the sheet, then open the agent's
    /// window (a push made under a sheet that is closing is lost).
    func openAgentWindow(paneID: String) {
        navigation.pane = .conversation
        presentation.dismiss(then: .openWindow(.agent(paneID)))
    }

    // MARK: - Feedback marks

    /// The daemon takes feedback marks: `bot-ready` listed `feedback`.
    var feedbackSupported: Bool { linkModel.handshake.features.contains(FeedbackTag.feature) }

    /// The user marked the moment `at` ("that paused", or another tag):
    /// `POST /api/feedback`, then a haptic and "Noted". Works while the
    /// voice runs; nothing about the voice session changes.
    func markFeedback(_ tag: FeedbackTag, note: String? = nil, at: Date) {
        let mark = FeedbackMark(tag: tag, note: note, at: at, conversationTurn: conversationModel.rows.last?.id)
        Task { [weak self] in
            guard let self else { return }
            do {
                if try await self.current { try await $0.sendFeedback(mark) } != nil {
                    Haptics.answered()
                    self.showFeedbackNotice("Noted")
                } else {
                    Haptics.answerFailed()
                    self.showFeedbackNotice("This daemon can't take marks yet")
                }
            } catch {
                Haptics.answerFailed()
                self.showFeedbackNotice("Couldn't save the mark")
            }
        }
    }

    private func showFeedbackNotice(_ text: String) { notices.showFeedback(text) }

    /// The question an agent's card shows: the voice's own (through
    /// `approval`) or another open one, with its tap state.
    func questionCard(for paneID: String) -> ApprovalFlow.Card? {
        if let approval, approval.pending.paneID == paneID { return approval }
        guard let question = agentsModel.questions.first(where: { $0.paneID == paneID }) else { return nil }
        return ApprovalFlow.Card(pending: question, phase: questionPhase(question.id))
    }

    /// The questions floating above the capsule on the Conversation: every
    /// open one, the current computer's first and then by age (one
    /// computer today, so by age), each with its tap state; the voice's
    /// own one through `approval`, kept in front while it shows how it
    /// ended.
    var floatingQuestions: [ApprovalFlow.Card] {
        let open = QuestionOrder.ordered(agentsModel.questions) { _ in true }
        // A settled question with a row in the conversation collapses into
        // that row at once (the motion pass); one without stays to say how
        // it ended.
        let settledVoice = approval.flatMap { card -> PendingAction? in
            guard card.isSettled, !conversationModel.rows.contains(where: { row in
                if case let .item(item) = row { return item.kind == .approval && item.pendingID == card.pending.id }
                return false
            }) else { return nil }
            return card.pending
        }
        let list = QuestionOrder.floating(open: open, voice: settledVoice, voiceSettled: settledVoice != nil)
        let local = list.map { question in
            if let approval, approval.pending.id == question.id { return approval }
            return ApprovalFlow.Card(pending: question, phase: questionPhase(question.id))
        }
        // Then the other computers' questions, oldest first.
        return local + remoteQuestions.map { remote in
            ApprovalFlow.Card(pending: remote.namespaced, phase: session(remote.computerID)?.phase(remote.pending.id) ?? .open(error: nil))
        }
    }

    /// The ids of the questions still open, for the rows that point at them.
    var openQuestionIDs: Set<String> {
        var ids = Set(agentsModel.questions.map(\.id))
        if let approval, approval.isSettled { ids.remove(approval.pending.id) }
        return ids
    }

    /// Answers a question by tap, whichever it is and wherever it was
    /// tapped (the voice's card, a question card, the floating stack):
    /// through its computer's model, under the question's key, so every
    /// surface reads one phase. The answer finishes on that computer even
    /// if the app switches meanwhile (decision 6), and its refresh follows.
    func answer(question id: String, option index: Int) {
        if let remote = RemoteQuestion.parse(id) {
            // Another computer's: answered through its own daemon.
            session(remote.computer)?.answer(question: remote.question, option: index)
            return
        }
        guard let model = fleet.current else { return }
        let voiceQuestion = approval.map(\.pending).flatMap { $0.id == id ? $0 : nil }
        guard let question = voiceQuestion ?? agentsModel.questions.first(where: { $0.id == id }),
              let landed = model.answer(question, option: index) else { return }
        guard voiceQuestion != nil else { return }
        Task { [weak self, weak model] in
            await landed.value
            // Without the event stream (not connected), the voice's answered
            // approval item comes from the server.
            guard let self, let model, self.fleet.current === model, !self.voice.core.connection.wantsConnection else { return }
            await self.syncHistory()
        }
    }

    // MARK: - Banners

    private func show(_ banner: Banner) {
        notices.banner = banner
    }

    func retry() {
        notices.banner = nil
        reconnect()
    }

    /// The orb's "Audio paused by iOS — tap to resume".
    func resumeAudio() {
        send(.resumeTapped)
    }

    /// The banner's button.
    func bannerAction() {
        switch notices.banner?.action ?? .retry {
        case .retry:
            retry()
        case .microphoneModes:
            notices.banner = nil
            voice.showMicrophoneModes()
        }
    }

    // MARK: - Diagnostics

    // MARK: - Connection chip popover

    /// Times a `GET /api/hello` round trip for the connection chip's
    /// popover. `nil` when there is nowhere to ask or it did not answer.
    func pingRoundTrip() async -> TimeInterval? {
        guard settings.endpoint != nil else { return nil }
        let start = Date()
        do {
            _ = try await current { try await $0.hello() }
            return Date().timeIntervalSince(start)
        } catch {
            return nil
        }
    }

    // MARK: - Offline demo

    /// The scripted demo this view model runs instead of a daemon (nil for
    /// the real app). Settings, cache and client are the demo's own, so
    /// nothing here touches the real daemon address, token or conversation.
    let demo: DemoDaemon?
    var isDemo: Bool { demo != nil }
    private var demoVoice: DemoVoice?
    /// The real view model shows the demo (`Presentation.demo`).
    var demoActive: Bool { presentation.isShowing(.demo) }
    /// Set on the demo's view model: leave it.
    var onLeaveDemo: () -> Void = {}

    /// A view model for the offline demo: its own settings (a throwaway
    /// defaults suite, secrets in memory), an empty cache in a temporary
    /// directory and a fresh `DemoDaemon`.
    static func makeDemo() -> SessionViewModel {
        let suite = "sheepdog.demo"
        UserDefaults().removePersistentDomain(forName: suite)
        let defaults = UserDefaults(suiteName: suite) ?? .standard
        let store = SettingsStore(defaults: defaults, computers: ComputerStore(defaults: defaults, secrets: MemorySecretStore()))
        store.address = DemoDaemon.endpoint.display
        store.hasBeenConfigured = true
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("SheepdogDemo-\(UUID().uuidString)", isDirectory: true)
        #if DEBUG
        DemoClock.offset = DebugLaunch.demoClockOffset
        let start = DemoClock.now()
        let demo = DebugLaunch.demoPaused ? DemoDaemon(clock: { start }) : DemoDaemon(clock: DemoClock.now)
        demo.update = DebugLaunch.demoUpdate
        demo.recording = DebugLaunch.demoRecording
        demo.longQuestion = DebugLaunch.demoLongQuestion
        #else
        let demo = DemoDaemon()
        #endif
        return SessionViewModel(settings: store, cache: ConversationCache(directory: directory), demo: demo)
    }

    /// The first-run screen's and Settings' "Try the demo".
    func openDemo() {
        guard demo == nil else { return }
        // Settings (the first-run screen) closes first.
        presentation.presentInstead(.demo)
    }

    /// The demo was closed: back to the real app, and to the first-run
    /// Settings (once the demo's cover is gone) if no daemon is saved yet.
    func demoClosed() {
        presentation.dismiss(.demo)
        if !isConfigured || !settings.hasBeenConfigured { presentation.present(.settings) }
    }

    /// Leave the demo (its header and its Settings).
    func leaveDemo() {
        guard let demo else { return }
        demoVoice?.stop()
        disconnect()
        demo.stop()
        onLeaveDemo()
    }

    /// One of the demo's suggested questions.
    func askDemo(_ question: String) {
        Task { _ = await sendTyped(question) }
    }

    /// The demo without its on-device voice (`-sheepdogDemoSilent`, UI
    /// tests only).
    private var demoSilent: Bool {
        #if DEBUG
        return DebugLaunch.demoSilent
        #else
        return false
        #endif
    }

    private func startDemo() {
        guard let demo else { return }
        demo.onCards = { [weak self] cards in
            for card in cards { self?.receiveCard(card) }
        }
        #if DEBUG
        if DebugLaunch.demoCard {
            Task { [weak self] in
                try? await Task.sleep(for: .seconds(DebugLaunch.demoCardDelay))
                self?.askDemo("Show me the agents")
            }
        }
        #endif
        guard !demoSilent else {
            demo.start()
            Task {
                await refreshState(quiet: true)
                await syncHistory()
            }
            connect()
            return
        }
        let voice = DemoVoice()
        voice.onSpeaking = { [weak self] speaking in self?.send(.playbackChanged(speaking: speaking)) }
        voice.onHeard = { [weak self] text in self?.askDemo(text) }
        voice.onListenerFailed = { [weak self] message in self?.send(.audioStartFailed(message)) }
        demoVoice = voice
        demo.onLines = { [weak self] lines in
            guard let self, self.voice.core.connection.wantsConnection else { return }
            for line in lines { voice.say(line.text) }
        }
        demo.start()
        Task {
            await refreshState(quiet: true)
            await syncHistory()
        }
        // The demo talks from the start: no address to set, no permission
        // needed to hear it.
        connect()
    }

    /// The microphone was turned on in the demo: ask for speech recognition
    /// and the microphone now (once), then listen; if the person says no,
    /// the mic goes back off and questions stay by tap or typing.
    private func demoListenOnFirstUse() {
        guard let speaker = demoVoice, !speaker.listening, !speaker.asking else { return }
        Task {
            guard let granted = await speaker.listenOnFirstUse() else { return }
            if !granted, !voice.core.mic.muted { send(.muteTapped) }
        }
    }

    /// The session core's effects that would touch `/ws` or the audio
    /// engine, done on the device instead. False: run it the usual way.
    private func runDemo(_ effect: SessionEffect) -> Bool {
        switch effect {
        case .probe:
            Task {
                do {
                    accept(try await current { try await $0.state() })
                    await loadHello()
                } catch {
                    send(.probeFailed(.malformed(error.localizedDescription)))
                    return
                }
                guard voice.core.connection.wantsConnection else { return }
                // No permission prompt here: the demo asks when the person
                // first taps the microphone (`demoListenOnFirstUse`).
                send(.probeSucceeded(micGranted: !demoSilent))
            }
        case .openSocket:
            send(.socketOpened)
            voice.receive(control: WSMessage(type: .status, voice: .connected, muted: voice.core.mic.muted))
        case .closeSocket:
            demoVoice?.stop()
            send(.socketClosed(reason: "demo ended", willRetry: false))
        case .send:
            break
        case .startAudio, .resumeAudio:
            do {
                // Listening starts at once only where the person already
                // allowed it; otherwise the demo starts with the mic off
                // and asks at the first tap on it.
                let allowed = DemoVoice.listeningAllowed
                if allowed { try demoVoice?.startListening() }
                send(.audioStarted(gateClosed: true))
                if !allowed, demoVoice != nil, !voice.core.mic.muted { send(.muteTapped) }
            } catch {
                send(.audioStartFailed(error.localizedDescription))
            }
        case .stopAudio, .suspendAudio:
            demoVoice?.stopListening()
        case let .setCaptureMuted(muted):
            demoVoice?.muted = muted
            if !muted { demoListenOnFirstUse() }
        case .setManualGate:
            break
        case .flushPlayback:
            demoVoice?.stopSpeaking()
        case .showBanner(.microphoneDenied), .showBanner(.microphoneFailed):
            // Expected in the demo where on-device recognition is missing
            // or declined: the status line says to tap a question or type.
            break
        default:
            return false
        }
        return true
    }

    // MARK: - Pairing

    // MARK: - Pairing

    /// How a pairing ended.
    enum PairOutcome {
        case paired(PairedComputer)
        case problem(PairingProblem)
        case failed(title: String, detail: String)
    }

    /// Pairs with the computer a scanned code names (docs/dev/ws-contract.md
    /// "Pairing"): finds the first address whose key matches the code's pin,
    /// makes this phone's key, enrolls once (codes are one-time), signs in,
    /// and only then saves the computer and repoints the app.
    func pair(_ code: PairingCode) async -> PairOutcome {
        let trust = PinnedTrustDelegate { [code.pin] }
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 8
        let session = URLSession(configuration: config, delegate: trust, delegateQueue: nil)
        defer { session.finishTasksAndInvalidate() }
        let mismatches = MismatchCounter()
        let reachable = await code.firstReachable { address in
            guard let url = URL(string: "https://\(address)/api/hello") else { return false }
            // Each address's own verdict: the probes run in parallel.
            let attempt = trust.attempt()
            do {
                let (_, response) = try await session.data(for: URLRequest(url: url), delegate: attempt)
                return (response as? HTTPURLResponse)?.statusCode == 200
            } catch {
                SheepdogLog.session.error("pair probe \(address, privacy: .public) failed: \(String(describing: error), privacy: .public)")
                if attempt.sawMismatch { mismatches.add() }
                return false
            }
        }
        guard let address = reachable, let base = URL(string: "https://\(address)/") else {
            if mismatches.count > 0 { return .problem(.pinMismatch) }
            return .failed(title: "None of the code's addresses answered", detail: "Tried \(code.addresses.joined(separator: ", ")). \(SetupHelp.unreachableFix)")
        }
        do {
            DeviceKey.delete(daemonID: code.daemonID)
            let key = try DeviceKey.loadOrCreate(daemonID: code.daemonID)
            guard let publicKey = DeviceKey.publicKey(key) else {
                return .failed(title: "This phone could not make its key", detail: "Try again; restart the phone if it keeps failing.")
            }
            let auth = DeviceAuth(base: base, session: session, trust: trust)
            let kind = UIDevice.current.userInterfaceIdiom == .pad ? "ipados" : "ios"
            let enrolled = try await auth.enroll(code: code.code, publicKey: publicKey, name: UIDevice.current.name, kind: kind, hardware: DeviceKey.isHardware)
            let computer = PairedComputer(
                daemonID: enrolled.daemonID.isEmpty ? code.daemonID : enrolled.daemonID,
                name: code.name.isEmpty ? enrolled.name : code.name,
                addresses: [address] + code.addresses.filter { $0 != address },
                pin: code.pin, deviceID: enrolled.deviceID, pairedAt: Date()
            )
            let store = settings.computers
            // The app never switches on its own: another computer is added
            // beside the current one. The first one, or a re-pair of the
            // current one, repoints the session.
            let repoint = store.currentID == nil || store.currentID == computer.daemonID
            store.save(computer, address: address)
            settings.hasBeenConfigured = true
            _ = try await store.credentials(for: computer.daemonID).ticket(base: base)
            notices.setPairingProblem(nil)
            computersChanged()
            if repoint {
                store.currentID = computer.daemonID
                endpointChanged()
            }
            return .paired(computer)
        } catch let problem as PairingProblem {
            return .problem(problem)
        } catch let error as DaemonError {
            return .failed(title: error.message, detail: error.hint ?? "")
        } catch {
            return .failed(title: "Pairing did not finish", detail: error.localizedDescription)
        }
    }

    /// Settings ▸ Remove: tells the computer to forget this phone (best
    /// effort, `DELETE /api/devices/self`), then forgets the computer and
    /// this phone's key for it.
    func removeComputer() async {
        guard let id = settings.computers.currentID else {
            // A bare loopback address (the tests): forget it.
            if let endpoint = settings.endpoint { cache.clear(for: endpoint) }
            settings.address = ""
            settings.hasBeenConfigured = false
            endpointChanged()
            return
        }
        await removeComputer(id)
    }

    /// Removes one computer: the computer forgets this phone (best effort),
    /// then the app forgets it, its key and its conversation. Removing the
    /// current one moves the voice to the first one left; removing the last
    /// one goes back to the first run.
    func removeComputer(_ id: String) async {
        let store = settings.computers
        let wasCurrent = store.currentID == id
        await PushCoordinator.shared.unregister(computer: id)
        // Before the store forgets it, which stops its model: on the
        // address its link uses now, not the stored one.
        if let computer = fleet.model(id) {
            _ = try? await computer.actions.run("unpair:\(id)", ActionRunner.Kind(timeout: 4), as: DaemonClient.self) { try await $0.unpairDevice() }
        } else if let endpoint = store.endpoint(for: id), endpoint.secure, let base = endpoint.url(path: "/"),
                  let credentials = endpoint.credentials,
                  let ticket = try? await credentials.ticket(base: base) {
            await DeviceAuth(base: base, session: credentials.session(timeout: 4)).unpair(ticket: ticket)
        }
        if let endpoint = store.endpoint(for: id) { cache.clear(for: endpoint) }
        store.remove(id)
        if store.count == 0 { settings.hasBeenConfigured = false }
        computersChanged()
        if wasCurrent {
            if voice.core.connection.wantsConnection { disconnect() }
            endpointChanged()
            if store.count == 0 { showFirstRunIfNeeded() }
        }
    }

    /// Makes another computer the current one: the voice session ends
    /// cleanly and, if it was running, connects to the new computer; its
    /// conversation replaces the old one's (app-redesign.md section 4).
    func switchComputer(to id: String) {
        let store = settings.computers
        guard store.currentID != id, store.computer(id) != nil else { return }
        store.currentID = id
        computersChanged()
        endpointChanged()
        // A divider marks whom the conversation is with now.
        record(.talkingTo(currentComputerName), save: false)
        AccessibilityNotification.Announcement("Talking to \(currentComputerName)").post()
    }

    /// The list of computers changed (paired, removed, renamed, moved).
    func computersChanged() {
        computerCount = settings.computers.count
        syncFleet()
        // A second computer puts names in the status copy.
        presentLink()
        // Every computer gets this phone's push token, and the extension
        // the names it labels alerts with.
        if demo == nil { PushCoordinator.shared.endpointChanged(from: nil) }
        objectWillChange.send()
    }

    /// Back to the first-run screen once no computer is left.
    private func showFirstRunIfNeeded() {
        guard !isConfigured else { return }
        linkModel.setHello(nil)
        presentation.present(.settings)
    }

    /// The scan-again screen a refusal opens, from Settings or the main
    /// screen.
    func needsNewScan(_ problem: PairingProblem) {
        notices.setPairingProblem(problem)
        if showSettings {
            notices.daemonNoticeProblem = problem
            return
        }
        presentation.present(.pairing(.problem(problem)))
    }

    // MARK: - Test connection (Settings)

    struct Probe: Equatable {
        var ok: Bool
        var title: String
        var lines: [String]
        /// A page that explains the fix, for a failure.
        var help: URL? = nil
    }

    /// What "Test connection" reports: `/api/hello`, then `/api/state` with
    /// this phone's ticket.
    func testConnection() async -> Probe {
        guard let preferred = settings.endpoint else {
            return Probe(ok: false, title: "Not paired yet.", lines: ["Scan the pairing code on your computer."])
        }
        let endpoint = channelEndpoint(preferred)
        let probeClient = DaemonClient(endpoint: endpoint)
        do {
            let hello = try await probeClient.hello()
            _ = try await probeClient.state()
            var lines = ["\(hello.name) \(hello.version)", "Voice name: \(hello.displayName)"]
            lines.append(endpoint.secure ? "Encrypted, key checked." : "This computer (loopback).")
            return Probe(ok: true, title: "Connected to \(settings.computer?.name ?? endpoint.display)", lines: lines)
        } catch let DaemonError.pairing(problem) {
            needsNewScan(problem)
            return Probe(ok: false, title: problem.title, lines: [problem.message])
        } catch let error as DaemonError {
            return Probe(ok: false, title: error.message, lines: [error.hint].compactMap { $0 }, help: SetupHelp.helpURL(for: error))
        } catch {
            return Probe(ok: false, title: "Could not reach \(endpoint.display).", lines: [SetupHelp.unreachableFix], help: SetupHelp.remoteAccessURL)
        }
    }
}

// MARK: - The voice's host

/// What the voice hands back (`VoiceEngineHost`): the probe, the
/// conversation, banners and refreshes, and what else rides `/ws`.
extension SessionViewModel: VoiceEngineHost {
    /// The core's effects that are not the voice's own; the demo's
    /// on-device stand-ins come first.
    func voiceEngine(_ engine: VoiceEngine, run effect: SessionEffect) -> Bool {
        if demo != nil, runDemo(effect) { return true }
        switch effect {
        case .probe:
            Task { await openSession() }
        case .flushTranscript:
            reducer.apply(.flush)
            conversationChanged()
        case let .showBanner(banner):
            show(Banner(banner))
        case .clearBanner:
            notices.banner = nil
        case let .notice(text):
            record(LocalNotice(text: text))
        case .refreshState:
            refreshSoon()
        case .syncHistory:
            scheduleHistorySync()
        default:
            return false
        }
        return true
    }

    func voiceEngineWantsConnectionChanged(_ engine: VoiceEngine) {
        // The voice keeps the current computer's link up in the
        // background (Keep listening), the others back off further while
        // it is on, and Settings says "Talking here".
        updateDemands()
        presentLink()
    }

    func voiceEngine(_ engine: VoiceEngine, handshakeChanged handshake: ProtocolHandshake.State) {
        guard linkModel.handshake != handshake else { return }
        linkModel.setHandshake(handshake)
        // The features are learned per computer, so a window opened
        // while the voice is off still knows about `term`.
        if case let .ready(_, _, features) = linkModel.handshake { fleet.current?.features = features }
    }

    func voiceEngineDidPublish(_ engine: VoiceEngine) {
        syncAway()
        // iOS gave the audio back, or the voice returned: the banner that
        // said otherwise is no longer true.
        if let notice = notices.banner?.audioNotice, !notice.bannerStillApplies(in: engine.state) { clearAudioBanner() }
    }

    func voiceEngine(_ engine: VoiceEngine, socket event: VoiceSocket.Event) {
        switch event {
        case .opened:
            #if DEBUG
            // The UI tests start configured; a permission alert would sit
            // on top of everything they tap.
            if DebugLaunch.address != nil || DebugLaunch.resetState, !DebugLaunch.askPush { return }
            #endif
            PushCoordinator.shared.sessionConnected()
        case .closed, .failed, .paused:
            screenStream.socketClosed()
            cardVisibility.socketClosed()
        case let .handshake(state):
            if let message = state.mismatchMessage {
                fleet.current?.send(.requestFailed(.incompatible(message)))
            }
            if let message = screenStream.handshake(state) { engine.send(message: message) }
            if let message = cardVisibility.handshake(state) { engine.send(message: message) }
        case .control, .audio:
            break
        }
    }

    func voiceEngine(_ engine: VoiceEngine, control message: WSMessage) {
        switch message.type {
        case .transcript:
            guard let dir = message.dir, let text = message.text else { break }
            reducer.apply(.transcript(dir, text: text, final: message.final ?? false))
            conversationChanged()
            if message.final == true { scheduleHistorySync() }
        case .screen, .screenError:
            if let event = screenStream.receive(message), let pane = screenStream.paneID {
                screenRouter.deliver(event, to: pane)
            }
        case .card:
            if let card = message.card { receiveCard(card) }
        default:
            break
        }
    }

    func voiceEngine(_ engine: VoiceEngine, notice: LocalNotice) {
        record(notice)
    }

    /// Where a notice about this phone goes (design doc, section 5.5).
    /// Every one is kept in the reducer, for the technical view, and in the
    /// unified log, so a sysdiagnose has what the daemon never saw. The
    /// timeline lists only the context ones; a banner carries the ones that
    /// need a tap; the route glyph in the voice bar shows the rest.
    private func record(_ notice: LocalNotice, save: Bool = true) {
        switch notice {
        case .talkingTo, .voiceOff, .voiceClosed, .unrecognised:
            SheepdogLog.session.notice("notice: \(notice.logText, privacy: .public)")
        default:
            SheepdogLog.audio.notice("notice: \(notice.logText, privacy: .public)")
        }
        reducer.apply(.notice(notice.text))
        conversationChanged(save: save)
        switch notice {
        case let .routeChanged(output):
            // The voice bar's glyph changes; VoiceOver hears it too.
            AccessibilityNotification.Announcement(output.map { "Audio moved to \($0)" } ?? "The audio route changed").post()
        case .listeningAgain:
            clearAudioBanner()
        default:
            if let words = notice.banner { showAudioBanner(notice, words) }
        }
    }

    /// Raised only over nothing or an earlier audio banner: a connection
    /// error keeps the screen.
    private func showAudioBanner(_ notice: LocalNotice, _ words: (message: String, hint: String)) {
        guard notices.banner == nil || notices.banner?.audioNotice != nil else { return }
        var banner = Banner(tone: notice == .voiceOff ? .info : .warning, message: words.message, hint: words.hint, retryTitle: nil)
        banner.audioNotice = notice
        show(banner)
    }

    private func clearAudioBanner() {
        if notices.banner?.audioNotice != nil { notices.banner = nil }
    }

    var voiceEngineSendsDrains: Bool { feedbackSupported }

    var voiceEngineCanShowTip: Bool { notices.banner == nil }

    func voiceEngineShowMicrophoneTip(_ engine: VoiceEngine) {
        show(Banner(
            tone: .info,
            message: "Noisy room? Turn on Voice Isolation.",
            hint: "iOS filters out the room before \(voiceName) hears it. It is in Control Center ▸ Mic Mode, or Settings ▸ Microphone mode.",
            retryTitle: "Microphone mode",
            action: .microphoneModes
        ))
    }
}

// MARK: - Addresses (Settings ▸ Connection ▸ Addresses)

extension SessionViewModel {
    /// The rows of Settings ▸ Connection ▸ Addresses, from the current
    /// computer's link; published only when they change.
    func presentAddresses() {
        let rows = settings.computer.map {
            AddressRow.rows(addresses: Self.raceAddresses(preferred: settings.address, $0.addresses), preferred: settings.address, link: link)
        } ?? []
        linkModel.setAddressRows(rows)
    }

    /// The address the current computer's link uses now, else the
    /// preferred one (Settings' header).
    /// Reads the presentation so a view re-renders when the link moves.
    var addressInUse: String {
        _ = linkModel.presentation
        return link.onlineAddress ?? settings.address
    }

    /// Checks a typed address by the computer's pin and, when it answers
    /// with the computer's key, keeps it (a router's port forward). The link
    /// races it with the others from then on. Nil on success, else the
    /// reason in plain words.
    func addAddress(_ text: String) async -> String? {
        guard let computer = settings.computer else { return "Scan the pairing code on your computer first." }
        switch ComputerAddress.normalize(text, known: computer.addresses) {
        case let .failure(problem):
            return problem.message
        case let .success(address):
            if let problem = await checkAddress(address) { return problem }
            settings.credentials?.addAddress(address)
            addressesChanged()
            return nil
        }
    }

    /// Prefer this address: the link tries it first from now on, on every
    /// network, and moves there if it answers with the computer's key; if it
    /// does not, the link stays on (or races to) another address. No check
    /// first: the race is the check.
    func preferAddress(_ address: String) {
        guard address != settings.address else { return }
        settings.address = address
        fleet.current?.prefer(address)
        addressesChanged()
    }

    /// Forgets an address that is neither preferred nor in use.
    func removeAddress(_ address: String) {
        guard address != settings.address, address != link.onlineAddress else { return }
        settings.credentials?.removeAddress(address)
        addressesChanged()
    }

    /// Nil when address answers over TLS with the computer's key (the
    /// pinned session: the pin is the check, never the address).
    private func checkAddress(_ address: String) async -> String? {
        guard let endpoint = DaemonEndpoint(text: address, secure: true, credentials: settings.credentials) else { return ComputerAddress.Problem.unreadable.message }
        do {
            _ = try await DaemonClient(endpoint: endpoint).hello()
            return nil
        } catch DaemonError.untrusted {
            return "Something answered at \(address), but not with this computer's key, so the app did not use it."
        } catch let error as DaemonError {
            return error.message
        } catch {
            return "Could not reach \(address). \(SetupHelp.unreachableFix)"
        }
    }

    /// The current computer's address set changed: its link hears it
    /// (same epoch, same pin) and the screen shows it.
    private func addressesChanged() {
        syncFleet()
        presentAddresses()
        objectWillChange.send()
    }
}

/// Counts pin mismatches across the parallel address probes.
private final class MismatchCounter: @unchecked Sendable {
    private let lock = NSLock()
    private var n = 0
    func add() { lock.lock(); n += 1; lock.unlock() }
    var count: Int { lock.lock(); defer { lock.unlock() }; return n }
}

/// A pairing sheet inside Settings (Add a computer, Scan a new code). Its
/// id is where it started (`PairingStage.id`), so building it again is
/// the same sheet: a re-render cannot dismiss and re-present it.
struct PairingSheet: Identifiable {
    var stage: PairingStage
    var id: String { stage.presentationID }
}

extension SessionViewModel.Banner {
    /// The words for a banner the session core raised.
    init(_ banner: SessionBanner) {
        switch banner {
        case let .connectionFailed(error):
            self.init(tone: .error, message: error.message, hint: error.hint, retryTitle: "Try again", help: SetupHelp.helpURL(for: error))
        case .noNetwork:
            self.init(tone: .warning, message: "This phone is offline.", hint: "Connect again when it is back on a network.", retryTitle: "Try again")
        case .replaced:
            self.init(
                tone: .warning,
                message: "Another client took the voice session.",
                hint: "The daemon serves one client at a time — the browser page on your computer, maybe.",
                retryTitle: "Take it back"
            )
        case .microphoneDenied:
            self.init(tone: .warning, message: AudioEngine.Failure.microphoneDenied.errorDescription ?? "", hint: nil, retryTitle: nil)
        case let .microphoneFailed(message):
            self.init(tone: .warning, message: message, hint: "Status and what the voice says still work without it.", retryTitle: nil)
        case let .daemonSaid(text):
            self.init(tone: .warning, message: "The daemon said: \(text)", hint: nil, retryTitle: nil)
        case let .protocolMismatch(text):
            self.init(
                tone: .error,
                message: text,
                hint: "The app and the daemon ship separately; the newer one still works with the older one only down to its minimum.",
                retryTitle: nil
            )
        case let .audioResumeFailed(message):
            self.init(
                tone: .warning,
                message: "iOS would not give the audio back (\(message)).",
                hint: "Finish the call or whatever is using audio, then tap the orb again.",
                retryTitle: nil
            )
        }
    }
}
