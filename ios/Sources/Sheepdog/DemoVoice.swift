import AVFoundation
import Speech

/// The offline demo's voice, entirely on the device: `AVSpeechSynthesizer`
/// reads the scripted lines, and `SFSpeechRecognizer` with on-device
/// recognition only hears questions. No audio and no text leave the phone;
/// no cloud model is called. Where on-device recognition is not available
/// (or the user says no to it), the demo works with the suggested
/// questions and typing.
@MainActor
final class DemoVoice: NSObject {
    /// Speaking started or stopped (the orb's speaking state).
    var onSpeaking: (Bool) -> Void = { _ in }
    /// A finished question heard by the recognizer.
    var onHeard: (String) -> Void = { _ in }
    /// The listener stopped by itself (the recognizer or the engine failed).
    var onListenerFailed: (String) -> Void = { _ in }

    private let synthesizer = AVSpeechSynthesizer()
    private let recognizer = SFSpeechRecognizer(locale: Locale(identifier: "en-US"))
    private let engine = AVAudioEngine()
    private var request: SFSpeechAudioBufferRecognitionRequest?
    private var task: SFSpeechRecognitionTask?
    private var heardSoFar = ""
    private var settle: Task<Void, Never>?
    private(set) var listening = false
    var muted = false {
        didSet { if muted { resetRecognition() } }
    }

    override init() {
        super.init()
        synthesizer.delegate = self
    }

    // MARK: - Speaking

    func say(_ text: String) {
        let utterance = AVSpeechUtterance(string: text)
        utterance.voice = AVSpeechSynthesisVoice(language: "en-US")
        utterance.rate = AVSpeechUtteranceDefaultSpeechRate
        synthesizer.speak(utterance)
    }

    func stopSpeaking() {
        synthesizer.stopSpeaking(at: .immediate)
    }

    var isSpeaking: Bool { synthesizer.isSpeaking }

    // MARK: - Listening

    /// Asks for the microphone and speech recognition, and whether this
    /// device can recognise on-device. False means: questions by tap or
    /// typing only.
    static func requestListening() async -> Bool {
        guard let recognizer = SFSpeechRecognizer(locale: Locale(identifier: "en-US")),
              recognizer.supportsOnDeviceRecognition
        else { return false }
        let speech = await withCheckedContinuation { continuation in
            SFSpeechRecognizer.requestAuthorization { continuation.resume(returning: $0 == .authorized) }
        }
        guard speech else { return false }
        return await AudioEngine.requestMicrophone()
    }

    func startListening() throws {
        guard !listening else { return }
        guard let recognizer, recognizer.isAvailable, recognizer.supportsOnDeviceRecognition else {
            throw DemoVoiceError.unavailable
        }
        let session = AVAudioSession.sharedInstance()
        try session.setCategory(.playAndRecord, mode: .default, options: [.allowBluetooth, .allowBluetoothA2DP, .defaultToSpeaker])
        try session.setActive(true)
        let input = engine.inputNode
        let format = input.outputFormat(forBus: 0)
        // No usable input (a simulator without one, a route mid-change):
        // installing a tap on it raises an Objective-C exception.
        guard format.sampleRate > 0, format.channelCount > 0 else { throw DemoVoiceError.unavailable }
        input.removeTap(onBus: 0)
        input.installTap(onBus: 0, bufferSize: 1024, format: format) { [weak self] buffer, _ in
            // The audio thread: hand the buffer to whichever request is
            // current. Reading `request` here races only with a reset, which
            // at worst drops one buffer.
            Task { @MainActor in self?.append(buffer) }
        }
        engine.prepare()
        try engine.start()
        listening = true
        resetRecognition()
    }

    func stopListening() {
        guard listening else { return }
        listening = false
        engine.inputNode.removeTap(onBus: 0)
        engine.stop()
        settle?.cancel()
        task?.cancel()
        task = nil
        request = nil
    }

    func stop() {
        stopSpeaking()
        stopListening()
    }

    private func append(_ buffer: AVAudioPCMBuffer) {
        // Not while muted, and not while the voice talks: it would hear itself.
        guard listening, !muted, !synthesizer.isSpeaking else { return }
        request?.append(buffer)
    }

    /// A fresh recognition request: after each question, after the voice
    /// spoke, and on unmute.
    private func resetRecognition() {
        settle?.cancel()
        task?.cancel()
        task = nil
        request?.endAudio()
        request = nil
        heardSoFar = ""
        guard listening, !muted, let recognizer else { return }
        let request = SFSpeechAudioBufferRecognitionRequest()
        request.requiresOnDeviceRecognition = true
        request.shouldReportPartialResults = true
        self.request = request
        taskStarted = Date()
        task = recognizer.recognitionTask(with: request) { [weak self] result, error in
            let text = result?.bestTranscription.formattedString
            let final = result?.isFinal ?? false
            Task { @MainActor in
                guard let self, self.request === request else { return }
                if let text, !text.isEmpty {
                    self.failures = 0
                    self.partial(text, final: final)
                }
                if error != nil, (text ?? "").isEmpty { self.recognitionFailed() }
            }
        }
    }

    /// The recognizer gave up without words (no speech, or no on-device
    /// model). Try again after a pause; after a few failures in a row, stop
    /// listening rather than spin, and let the demo fall back to taps.
    private var failures = 0
    private var taskStarted = Date.distantPast
    private func recognitionFailed() {
        // Only quick failures count: a long quiet stretch that ends in
        // "no speech" is normal use.
        failures = Date().timeIntervalSince(taskStarted) < 5 ? failures + 1 : 1
        task = nil
        request = nil
        guard failures < 3 else {
            stopListening()
            onListenerFailed("Spoken questions are not available on this device.")
            return
        }
        settle?.cancel()
        settle = Task { [weak self] in
            try? await Task.sleep(for: .seconds(2))
            guard !Task.isCancelled else { return }
            self?.resetRecognition()
        }
    }

    /// A question is finished when the words stop changing for a moment.
    private func partial(_ text: String, final: Bool) {
        heardSoFar = text
        settle?.cancel()
        if final {
            finishQuestion()
            return
        }
        settle = Task { [weak self] in
            try? await Task.sleep(for: .milliseconds(1300))
            guard !Task.isCancelled else { return }
            self?.finishQuestion()
        }
    }

    private func finishQuestion() {
        let text = heardSoFar.trimmingCharacters(in: .whitespacesAndNewlines)
        resetRecognition()
        if !text.isEmpty { onHeard(text) }
    }
}

enum DemoVoiceError: LocalizedError {
    case unavailable
    var errorDescription: String? { "Spoken questions are not available on this device." }
}

extension DemoVoice: AVSpeechSynthesizerDelegate {
    nonisolated func speechSynthesizer(_ synthesizer: AVSpeechSynthesizer, didStart utterance: AVSpeechUtterance) {
        Task { @MainActor in self.onSpeaking(true) }
    }

    nonisolated func speechSynthesizer(_ synthesizer: AVSpeechSynthesizer, didFinish utterance: AVSpeechUtterance) {
        Task { @MainActor in
            guard !self.synthesizer.isSpeaking else { return }
            self.onSpeaking(false)
            self.resetRecognition()
        }
    }

    nonisolated func speechSynthesizer(_ synthesizer: AVSpeechSynthesizer, didCancel utterance: AVSpeechUtterance) {
        Task { @MainActor in
            guard !self.synthesizer.isSpeaking else { return }
            self.onSpeaking(false)
        }
    }
}
