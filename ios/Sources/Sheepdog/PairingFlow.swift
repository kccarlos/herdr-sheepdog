import AVFoundation
import SheepdogKit
import SwiftUI
import UIKit

/// Where the pairing flow is (docs/dev/design/security.md "Pairing"): the
/// camera primer and scanner, the work, and how it ended.
enum PairingStage: Equatable {
    /// Before the system camera prompt.
    case primer
    /// The camera is off for Sheepdog in the Settings app.
    case denied
    /// This device has no camera (an iPad without one, the Simulator).
    case noCamera
    case scanning
    /// A code from outside the app (the voice page's hand-off): asks first.
    case confirm(PairingCode)
    /// Talking to the computer.
    case working(String)
    case paired(String)
    /// The code is used or expired, or the app must scan again.
    case problem(PairingProblem)
    /// Something else: nothing answered, not a pairing code.
    case failed(title: String, detail: String)

    /// Where scanning starts, from the camera's permission.
    static var start: PairingStage {
        switch AVCaptureDevice.authorizationStatus(for: .video) {
        case .notDetermined: return .primer
        case .authorized: return AVCaptureDevice.default(for: .video) == nil ? .noCamera : .scanning
        default: return .denied
        }
    }

    var id: String {
        switch self {
        case .primer: return "primer"
        case .denied: return "denied"
        case .noCamera: return "noCamera"
        case .scanning: return "scanning"
        case .confirm: return "confirm"
        case .working: return "working"
        case .paired: return "paired"
        case let .problem(p): return p == .codeUsed ? "expired" : "scanAgain"
        case .failed: return "failed"
        }
    }
}

/// The pairing flow as one sheet: primer, scanner, "Pairing…", and the end
/// ("Paired with X", "This code has expired", "Please scan again").
struct PairingFlowView: View {
    @EnvironmentObject private var model: SessionViewModel
    @Environment(\.dismiss) private var dismiss
    @Environment(\.openURL) private var openURL
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State var stage: PairingStage
    /// Runs after "Done" on the paired screen.
    var onPaired: () -> Void = {}

    var body: some View {
        NavigationStack {
            Group {
                switch stage {
                case .scanning: scanner
                case .working: working
                default: message
                }
            }
            .animation(Theme.ease(reduceMotion: reduceMotion), value: stage.id)
            .toolbar {
                if !isPaired {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Cancel") { dismiss() }
                            .tint(stage == .scanning ? .white : Theme.primaryText)
                            .accessibilityIdentifier("pairing.cancel")
                    }
                }
            }
            .toolbarBackground(.hidden, for: .navigationBar)
        }
        .interactiveDismissDisabled(isWorking)
        .accessibilityIdentifier("pairing.\(stage.id)")
    }

    private var isPaired: Bool { if case .paired = stage { return true } else { return false } }
    private var isWorking: Bool { if case .working = stage { return true } else { return false } }

    // MARK: - Scanner

    private var scanner: some View {
        ZStack {
            QRScannerView { use($0) }
                .ignoresSafeArea()
                .accessibilityLabel("Camera viewfinder")
            RoundedRectangle(cornerRadius: 28, style: .continuous)
                .strokeBorder(.white.opacity(0.9), lineWidth: 3)
                .frame(width: 240, height: 240)
                .accessibilityHidden(true)
            VStack {
                Spacer()
                VStack(spacing: 4) {
                    Text("Scan the code on your computer")
                        .font(.headline)
                    Text("Open the setup page, or run `sheepdog pair`.")
                        .font(.subheadline)
                        .foregroundStyle(.white.opacity(0.8))
                }
                .multilineTextAlignment(.center)
                .foregroundStyle(.white)
                .padding(.horizontal, 20)
                .padding(.vertical, 14)
                .glassEffect(.regular.tint(.black.opacity(0.35)), in: .rect(cornerRadius: Theme.Radius.large))
                .padding(.horizontal, 24)
                .padding(.bottom, 28)
            }
        }
        .background(Color.black.ignoresSafeArea())
    }

    private var working: some View {
        VStack(spacing: 18) {
            ProgressView()
                .controlSize(.large)
            if case let .working(name) = stage {
                Text(name.isEmpty ? "Pairing…" : "Pairing with \(name)…")
                    .font(.headline)
                    .foregroundStyle(Theme.ink)
            }
            Text("Checking the computer's key and enrolling this phone.")
                .font(.subheadline)
                .foregroundStyle(Theme.muted)
                .multilineTextAlignment(.center)
        }
        .padding(28)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.surface.ignoresSafeArea())
        .accessibilityElement(children: .combine)
    }

    // MARK: - Message screens

    private struct Copy {
        var art: Art
        var title: String
        var message: String
        var primary: String
        var secondary: String?
        var warning = false
    }

    private enum Art {
        case mascot(String)
        case symbol(String)
    }

    private var copy: Copy {
        switch stage {
        case .primer:
            return Copy(art: .symbol("qrcode.viewfinder"), title: "Pair with your computer",
                        message: "Sheepdog uses the camera only to read the pairing code on your computer's screen. Nothing is recorded.",
                        primary: "Continue", secondary: "Not now")
        case .denied:
            return Copy(art: .symbol("camera"), title: "Camera access is off",
                        message: "Turn on the camera for Sheepdog in the Settings app to scan the pairing code.",
                        primary: "Open Settings", secondary: "Not now")
        case .noCamera:
            return Copy(art: .symbol("camera"), title: "No camera here",
                        message: "This device has no camera to read the pairing code. Pair from a phone, or try the demo.",
                        primary: "Close", secondary: nil)
        case let .confirm(code):
            let name = code.name.isEmpty ? code.addresses[0] : code.name
            return Copy(art: .mascot("MascotListening"), title: "Pair with \(name)?",
                        message: "This phone gets its own key for \(name), and every other paired device is told. Only pair with a computer you own.",
                        primary: "Pair", secondary: "Cancel")
        case let .paired(name):
            return Copy(art: .mascot("MascotCelebrating"), title: "Paired with \(name)",
                        message: "The connection is encrypted and checked against your computer's key. Tap the capsule to talk.",
                        primary: "Done", secondary: nil)
        case let .problem(problem):
            return Copy(art: problem == .pinMismatch ? .symbol("exclamationmark.shield") : .mascot("MascotAlert"),
                        title: problem.title, message: problem.message,
                        primary: "Scan again", secondary: "Not now", warning: problem == .pinMismatch)
        case let .failed(title, detail):
            return Copy(art: .mascot("MascotAlert"), title: title, message: detail, primary: "Scan again", secondary: "Not now")
        case .scanning, .working:
            return Copy(art: .symbol("qrcode"), title: "", message: "", primary: "", secondary: nil)
        }
    }

    private var message: some View {
        let copy = copy
        return ScrollView {
            VStack(spacing: 14) {
                Group {
                    switch copy.art {
                    case let .mascot(name):
                        MascotCard(name: name, width: 150)
                            .frame(maxHeight: 170)
                    case let .symbol(name):
                        Image(systemName: name)
                            .font(.system(size: 52, weight: .regular))
                            .foregroundStyle(copy.warning ? Theme.danger : Theme.primaryText)
                            .frame(width: 112, height: 112)
                            .background(Circle().fill(copy.warning ? Theme.danger.opacity(0.12) : Theme.accentSoft))
                    }
                }
                .accessibilityHidden(true)
                .padding(.top, 24)
                Text(copy.title)
                    .font(.title.weight(.bold))
                    .fontDesign(.rounded)
                    .foregroundStyle(Theme.ink)
                    .multilineTextAlignment(.center)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityAddTraits(.isHeader)
                    .accessibilityIdentifier("pairing.title")
                Text(LocalizedStringKey(copy.message))
                    .font(.body)
                    .foregroundStyle(Theme.muted)
                    .multilineTextAlignment(.center)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(.horizontal, 28)
            .frame(maxWidth: 520)
            .frame(maxWidth: .infinity)
        }
        .scrollBounceBehavior(.basedOnSize)
        .safeAreaInset(edge: .bottom) {
            VStack(spacing: 4) {
                Button { primary() } label: {
                    Text(copy.primary)
                        .font(.body.weight(.semibold))
                        .foregroundStyle(Theme.onAccent)
                        .multilineTextAlignment(.center)
                        .frame(maxWidth: .infinity, minHeight: 50)
                        .padding(.vertical, 4)
                        .background(Capsule().fill(Color.accentColor))
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("pairing.primary")
                if let secondary = copy.secondary {
                    Button(secondary) { dismiss() }
                        .font(.body.weight(.medium))
                        .foregroundStyle(Theme.muted)
                        .frame(maxWidth: .infinity, minHeight: 44)
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("pairing.secondary")
                }
            }
            .padding(.horizontal, 28)
            .padding(.top, 8)
            .padding(.bottom, 12)
            .frame(maxWidth: 520)
            .background(Theme.surface)
        }
        .background(Theme.surface.ignoresSafeArea())
    }

    // MARK: - Actions

    private func primary() {
        switch stage {
        case .primer:
            Task {
                _ = await AVCaptureDevice.requestAccess(for: .video)
                stage = PairingStage.start == .primer ? .denied : PairingStage.start
            }
        case .denied:
            if let url = URL(string: UIApplication.openSettingsURLString) { openURL(url) }
        case .noCamera:
            dismiss()
        case let .confirm(code):
            run(code)
        case .paired:
            dismiss()
            onPaired()
        case .problem, .failed:
            stage = PairingStage.start
        case .scanning, .working:
            break
        }
    }

    private func use(_ text: String) {
        guard let code = PairingCode(text) else {
            stage = .failed(title: "That is not a pairing code", detail: "Open the setup page on your computer, or run `sheepdog pair`, and scan the code it shows.")
            return
        }
        run(code)
    }

    private func run(_ code: PairingCode) {
        stage = .working(code.name)
        Task {
            switch await model.pair(code) {
            case let .paired(computer):
                Haptics.connected()
                stage = .paired(computer.name.isEmpty ? computer.addresses[0] : computer.name)
            case let .problem(problem):
                stage = .problem(problem)
            case let .failed(title, detail):
                stage = .failed(title: title, detail: detail)
            }
        }
    }
}

/// The capture session behind the scanner: the back camera with a QR
/// metadata output, stopped once a code is read.
struct QRScannerView: UIViewControllerRepresentable {
    let onCode: (String) -> Void

    func makeUIViewController(context: Context) -> ScannerController {
        let controller = ScannerController()
        controller.onCode = onCode
        return controller
    }

    func updateUIViewController(_ controller: ScannerController, context: Context) {}

    final class ScannerController: UIViewController, AVCaptureMetadataOutputObjectsDelegate {
        var onCode: ((String) -> Void)?
        private let session = AVCaptureSession()
        private var preview: AVCaptureVideoPreviewLayer?
        private var delivered = false

        override func viewDidLoad() {
            super.viewDidLoad()
            view.backgroundColor = .black
            guard let device = AVCaptureDevice.default(for: .video),
                  let input = try? AVCaptureDeviceInput(device: device),
                  session.canAddInput(input)
            else { return }
            session.addInput(input)
            let output = AVCaptureMetadataOutput()
            guard session.canAddOutput(output) else { return }
            session.addOutput(output)
            output.setMetadataObjectsDelegate(self, queue: .main)
            output.metadataObjectTypes = [.qr]
            let layer = AVCaptureVideoPreviewLayer(session: session)
            layer.videoGravity = .resizeAspectFill
            view.layer.addSublayer(layer)
            preview = layer
        }

        override func viewDidLayoutSubviews() {
            super.viewDidLayoutSubviews()
            preview?.frame = view.bounds
        }

        override func viewWillAppear(_ animated: Bool) {
            super.viewWillAppear(animated)
            let session = session
            DispatchQueue.global(qos: .userInitiated).async { session.startRunning() }
        }

        override func viewWillDisappear(_ animated: Bool) {
            super.viewWillDisappear(animated)
            let session = session
            DispatchQueue.global(qos: .userInitiated).async { session.stopRunning() }
        }

        func metadataOutput(_ output: AVCaptureMetadataOutput, didOutput objects: [AVMetadataObject], from connection: AVCaptureConnection) {
            guard !delivered,
                  let code = objects.compactMap({ ($0 as? AVMetadataMachineReadableCodeObject)?.stringValue }).first
            else { return }
            delivered = true
            UINotificationFeedbackGenerator().notificationOccurred(.success)
            let session = session
            DispatchQueue.global(qos: .userInitiated).async { session.stopRunning() }
            onCode?(code)
        }
    }
}
