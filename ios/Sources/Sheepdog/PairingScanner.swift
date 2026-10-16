import AVFoundation
import SwiftUI
import UIKit

/// Settings ▸ Daemon ▸ Scan pairing code: the camera, looking for the QR
/// code `sheepdog pair` prints. It hands back the first QR text it reads;
/// the caller decides whether it is a pairing link (SheepdogKit
/// PairingLink).
struct PairingScannerSheet: View {
    let onCode: (String) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var access = AVCaptureDevice.authorizationStatus(for: .video)

    var body: some View {
        NavigationStack {
            Group {
                switch access {
                case .authorized:
                    if AVCaptureDevice.default(for: .video) == nil {
                        message("No camera", "This device has no camera. Type the address and token instead.")
                    } else {
                        QRScannerView(onCode: onCode)
                            .ignoresSafeArea(edges: .bottom)
                            .overlay(alignment: .bottom) {
                                Text("Point at the code from `sheepdog setup` or `sheepdog pair`")
                                    .font(.footnote)
                                    .padding(10)
                                    .background(.ultraThinMaterial, in: Capsule())
                                    .padding(.bottom, 32)
                            }
                    }
                case .notDetermined:
                    ProgressView()
                        .task {
                            _ = await AVCaptureDevice.requestAccess(for: .video)
                            access = AVCaptureDevice.authorizationStatus(for: .video)
                        }
                default:
                    message("Camera access is off", "Allow the camera for Sheepdog in the Settings app, or type the address and token instead.")
                }
            }
            .navigationTitle("Scan pairing code")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .accessibilityIdentifier("scan.cancel")
                }
            }
        }
    }

    private func message(_ title: String, _ detail: String) -> some View {
        ContentUnavailableView(title, systemImage: "camera", description: Text(detail))
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
