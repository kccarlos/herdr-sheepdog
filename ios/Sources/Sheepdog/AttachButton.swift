import PhotosUI
import SheepdogKit
import SwiftUI
import UIKit
import UniformTypeIdentifiers

/// The composer's 📎 (docs/dev/design/phone-terminal.md §5 "Attachments"):
/// one menu with Photo, Camera, File and Paste image. The file uploads to
/// the daemon, which keeps it on the computer, and `onAttached` gets its
/// path there for the prompt.
struct AttachButton: View {
    let upload: (Data, String, String) async throws -> AttachmentReply
    let onAttached: (AttachmentReply) -> Void

    @State private var showPhotos = false
    @State private var photoItem: PhotosPickerItem?
    @State private var showFiles = false
    @State private var showCamera = false
    @State private var uploading = false
    @State private var failure: String?
    @State private var retry: (Data, String, String)?

    /// What the menu offers besides Photo and File.
    private struct Sources: Equatable {
        var camera: Bool
        var pasteImage: Bool
    }

    var body: some View {
        Group {
            if uploading {
                ProgressView()
                    .frame(width: 32, height: 32)
                    .accessibilityLabel("Uploading")
            } else {
                // From values only (`StableMenu`): an open menu stays still
                // while the window re-renders.
                stableMenu(
                    Sources(camera: UIImagePickerController.isSourceTypeAvailable(.camera), pasteImage: UIPasteboard.general.hasImages),
                    probe: "attach"
                ) { sources in
                    Button { showPhotos = true } label: { Label("Photo", systemImage: "photo") }
                    if sources.camera {
                        Button { showCamera = true } label: { Label("Camera", systemImage: "camera") }
                    }
                    Button { showFiles = true } label: { Label("File", systemImage: "doc") }
                    if sources.pasteImage {
                        Button { pasteImage() } label: { Label("Paste image", systemImage: "doc.on.clipboard") }
                    }
                } label: { _ in
                    Image(systemName: "paperclip")
                        .font(.system(size: 20, weight: .regular))
                        .foregroundStyle(.secondary)
                        .frame(width: 32, height: 32)
                        .contentShape(Rectangle())
                }
                .accessibilityLabel("Attach")
                .accessibilityHint("Attach a photo or a file. It stays on your computer and its path goes into the prompt.")
            }
        }
        .accessibilityIdentifier("agentScreen.attach")
        .photosPicker(isPresented: $showPhotos, selection: $photoItem, matching: .images)
        .onChange(of: photoItem) { _, item in
            guard let item else { return }
            photoItem = nil
            Task { await sendPhoto(item) }
        }
        .fileImporter(isPresented: $showFiles, allowedContentTypes: [.image, .pdf, .text, .sourceCode, .json, .data]) { result in
            if case let .success(url) = result { sendFile(url) }
        }
        .fullScreenCover(isPresented: $showCamera) {
            CameraPicker { image in
                showCamera = false
                if let image, let data = image.jpegData(compressionQuality: 0.85) {
                    start(data, "photo.jpg", "image/jpeg")
                }
            }
            .ignoresSafeArea()
        }
        .alert(failure ?? "", isPresented: Binding(get: { failure != nil }, set: { if !$0 { failure = nil } })) {
            if let retry, failure != Attachments.tooBigMessage {
                Button("Retry") { start(retry.0, retry.1, retry.2) }
            }
            Button("OK", role: .cancel) {}
        }
    }

    private func sendPhoto(_ item: PhotosPickerItem) async {
        guard let data = try? await item.loadTransferable(type: Data.self) else {
            failure = "Could not read that photo."
            return
        }
        // HEIC and other formats go as JPEG, which every agent reads.
        if let image = UIImage(data: data), let jpeg = image.jpegData(compressionQuality: 0.85) {
            start(jpeg, "photo.jpg", "image/jpeg")
        } else {
            failure = "Could not read that photo."
        }
    }

    private func sendFile(_ url: URL) {
        let scoped = url.startAccessingSecurityScopedResource()
        defer { if scoped { url.stopAccessingSecurityScopedResource() } }
        guard let data = try? Data(contentsOf: url) else {
            failure = "Could not read that file."
            return
        }
        let type = UTType(filenameExtension: url.pathExtension)?.preferredMIMEType ?? "application/octet-stream"
        start(data, url.lastPathComponent, type)
    }

    private func pasteImage() {
        guard let image = UIPasteboard.general.image, let data = image.pngData() else {
            failure = "There is no image to paste."
            return
        }
        start(data, "pasted.png", "image/png")
    }

    private func start(_ data: Data, _ name: String, _ type: String) {
        guard data.count <= Attachments.maxBytes else {
            failure = Attachments.tooBigMessage
            return
        }
        retry = (data, name, type)
        uploading = true
        Task {
            defer { uploading = false }
            do {
                let reply = try await upload(data, name, type)
                retry = nil
                Haptics.tap()
                onAttached(reply)
            } catch {
                let text = changesErrorText(error)
                failure = text == Attachments.tooBigMessage ? text : "Upload failed: \(text)"
            }
        }
    }
}

/// The system camera, for one photo.
private struct CameraPicker: UIViewControllerRepresentable {
    let done: (UIImage?) -> Void

    func makeUIViewController(context: Context) -> UIImagePickerController {
        let picker = UIImagePickerController()
        picker.sourceType = .camera
        picker.delegate = context.coordinator
        return picker
    }

    func updateUIViewController(_: UIImagePickerController, context _: Context) {}

    func makeCoordinator() -> Coordinator { Coordinator(done: done) }

    final class Coordinator: NSObject, UIImagePickerControllerDelegate, UINavigationControllerDelegate {
        let done: (UIImage?) -> Void
        init(done: @escaping (UIImage?) -> Void) { self.done = done }

        func imagePickerController(_: UIImagePickerController, didFinishPickingMediaWithInfo info: [UIImagePickerController.InfoKey: Any]) {
            done(info[.originalImage] as? UIImage)
        }

        func imagePickerControllerDidCancel(_: UIImagePickerController) { done(nil) }
    }
}
