import Foundation
import SheepdogKit

/// What the Settings and Health panes read from the daemon's settings
/// routes: the orchestrator's model catalog, the push chattiness and
/// languages, and the voice providers, with the flags and errors of
/// switching them. Split out of `SessionViewModel` (phase 8), so a catalog
/// or an error changing re-renders the panes that show it and nothing else.
/// Requests go through the session's current computer (`current`), so a
/// late reply from a computer the app left lands nowhere.
@MainActor
@Observable
final class SettingsModel {
    /// The orchestrator's catalog and choice; nil until loaded, and on a
    /// daemon without the route.
    private(set) var brainModels: BrainModels?
    private(set) var switchingBrain = false
    var brainError: String?
    /// The push chattiness; nil until loaded, and without an orchestrator.
    private(set) var daemonSettings: DaemonSettings?
    var settingsError: String?
    /// nil until loaded, and when the daemon has no provider API (404).
    private(set) var voiceProviders: VoiceProviders? {
        didSet { onVoiceProvidersChange?() }
    }
    private(set) var switchingProvider = false
    var providerError: String?

    /// Set by the session: runs requests on the current computer and takes
    /// the two things a switch tells it.
    @ObservationIgnored weak var session: SessionViewModel?
    /// The audio engine's jitter-buffer profile follows the provider.
    @ObservationIgnored var onVoiceProvidersChange: (() -> Void)?

    /// The computer changed: what was loaded was the old one's.
    func clear() {
        brainModels = nil
        daemonSettings = nil
    }

    // MARK: Voice provider (GET/PUT /api/voice/provider[s])

    func loadVoiceProviders() async {
        guard let session, session.settings.endpoint != nil else { return }
        do {
            let list = try await session.current { try await $0.voiceProviders() }
            #if DEBUG
            // The fixture daemon (`--voice=off`) lists no providers.
            if list?.providers.isEmpty ?? true, DebugLaunch.fakeProviders {
                voiceProviders = Self.fakeProviders(current: voiceProviders?.provider ?? "gemini")
                return
            }
            #endif
            voiceProviders = list
        } catch {
            // Hidden rather than an error: the picker is a convenience, and
            // the connection itself reports what is wrong.
            SheepdogLog.session.error("voice providers: \(String(describing: error), privacy: .public)")
            voiceProviders = nil
        }
    }

    func selectVoiceProvider(_ id: String) {
        guard id != voiceProviders?.provider, !switchingProvider else { return }
        switchingProvider = true
        providerError = nil
        Task { [weak self] in
            guard let self, let session = self.session else { return }
            defer { self.switchingProvider = false }
            do {
                let list = try await session.current { try await $0.setVoiceProvider(id) }
                self.voiceProviders = list
                session.voiceProviderSelected(list.provider)
                Haptics.tap()
            } catch let error as DaemonError {
                #if DEBUG
                if DebugLaunch.fakeProviders {
                    self.voiceProviders = Self.fakeProviders(current: id)
                    session.voiceProviderSelected(id)
                    return
                }
                #endif
                self.providerError = error.message
            } catch {
                self.providerError = error.localizedDescription
            }
            await session.refreshState(quiet: true)
        }
    }

    #if DEBUG
    /// The contract's example, for screenshots against a daemon that lists
    /// no voice providers (the `--voice=off` fixture daemon).
    static func fakeProviders(current: String) -> VoiceProviders {
        VoiceProviders(
            provider: current,
            model: current == "openai" ? "gpt-live-1" : "gemini-3.8-live",
            providers: [
                VoiceProviderOption(id: "gemini", label: "Gemini Live", model: "gemini-3.8-live", available: true),
                VoiceProviderOption(id: "openai", label: "GPT-Live", model: "gpt-live-1", available: true, billing: "per_minute"),
            ]
        )
    }
    #endif

    // MARK: Orchestrator models

    func loadBrainModels(provider: String? = nil) async {
        guard let session else { return }
        do {
            brainModels = try await session.current { try await $0.brainModels(provider: provider) }
            brainError = nil
        } catch let error as DaemonError {
            brainError = error.message
        } catch {
            brainError = error.localizedDescription
        }
    }

    /// The orchestrator picker: provider and model for the orchestrator's next turn.
    func selectBrain(provider: String?, model: String) {
        guard !switchingBrain else { return }
        switchingBrain = true
        brainError = nil
        Task { [weak self] in
            guard let self, let session = self.session else { return }
            defer { self.switchingBrain = false }
            do {
                self.brainModels = try await session.current { try await $0.setBrainModel(provider: provider, model: model) }
                Haptics.tap()
            } catch let error as DaemonError {
                self.brainError = error.message
            } catch {
                self.brainError = error.localizedDescription
            }
            await session.refreshState(quiet: true)
        }
    }

    // MARK: Chattiness and languages

    func loadDaemonSettings() async {
        guard let session else { return }
        do {
            daemonSettings = try await session.current { try await $0.settings() }
            settingsError = nil
        } catch let error as DaemonError {
            // No orchestrator is a 503: hide the control, say why.
            daemonSettings = nil
            settingsError = error.message
        } catch {
            settingsError = error.localizedDescription
        }
    }

    func setChattiness(_ level: String) {
        guard level != daemonSettings?.chattiness else { return }
        Task { [weak self] in
            guard let self, let session = self.session else { return }
            do {
                self.daemonSettings = try await session.current { try await $0.setChattiness(level) }
                self.settingsError = nil
                Haptics.tap()
            } catch let error as DaemonError {
                self.settingsError = error.message
            } catch {
                self.settingsError = error.localizedDescription
            }
        }
    }

    /// Sends the language preference (most preferred first); the
    /// daemon's reply replaces the local copy, so the screen shows what
    /// it kept.
    func setLanguages(_ codes: [String]) {
        guard codes != (daemonSettings?.languages ?? []) else { return }
        if var optimistic = daemonSettings {
            optimistic.languages = codes
            daemonSettings = optimistic
        }
        Task { [weak self] in
            guard let self, let session = self.session else { return }
            do {
                self.daemonSettings = try await session.current { try await $0.setLanguages(codes) }
                self.settingsError = nil
                Haptics.tap()
            } catch {
                // Back to what the daemon has, then say why it refused.
                await self.loadDaemonSettings()
                self.settingsError = (error as? DaemonError)?.message ?? error.localizedDescription
            }
        }
    }

    /// The Health pane's fan-out: catalog, chattiness and providers at once.
    func loadAll() async {
        async let models: Void = loadBrainModels()
        async let chattiness: Void = loadDaemonSettings()
        async let providers: Void = loadVoiceProviders()
        _ = await (models, chattiness, providers)
    }
}
