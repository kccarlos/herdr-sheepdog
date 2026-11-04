import SwiftUI
import UIKit

/// What the Fit | 100% switch asks of the zoomable picture.
enum ZoomChoice: String, CaseIterable, Identifiable {
    case fit, actual
    var id: String { rawValue }
    var title: String { self == .fit ? "Fit" : "100%" }
    var spoken: String { self == .fit ? "Fit to screen" : "Actual size" }
}

/// A picture in a scroll view that pinches and double-taps: Fit (the whole
/// picture, never enlarged past its own size) up to 4×. It tells SwiftUI the
/// zoom it has and whether it is at Fit, and takes `command` from the Fit |
/// 100% switch.
struct ZoomableImage: UIViewRepresentable {
    let image: UIImage
    let name: String
    @Binding var zoom: CGFloat
    @Binding var fitting: Bool
    @Binding var command: ZoomChoice?

    func makeUIView(context: Context) -> ZoomScrollView {
        let view = ZoomScrollView(image: image)
        view.delegate = context.coordinator
        view.onZoom = { zoom, fitting in
            // From UIKit's layout and gestures, never inside a SwiftUI update.
            DispatchQueue.main.async {
                if abs(self.zoom - zoom) > 0.001 { self.zoom = zoom }
                if self.fitting != fitting { self.fitting = fitting }
            }
        }
        let doubleTap = UITapGestureRecognizer(target: view, action: #selector(ZoomScrollView.doubleTapped(_:)))
        doubleTap.numberOfTapsRequired = 2
        view.addGestureRecognizer(doubleTap)
        view.accessibilityLabel = "\(name), image, \(Int(image.size.width)) by \(Int(image.size.height))"
        return view
    }

    func updateUIView(_ view: ZoomScrollView, context: Context) {
        guard let command else { return }
        view.apply(command)
        DispatchQueue.main.async { self.command = nil }
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    final class Coordinator: NSObject, UIScrollViewDelegate {
        func viewForZooming(in scrollView: UIScrollView) -> UIView? { (scrollView as? ZoomScrollView)?.imageView }

        func scrollViewDidZoom(_ scrollView: UIScrollView) { (scrollView as? ZoomScrollView)?.zoomed() }
    }
}

final class ZoomScrollView: UIScrollView {
    let imageView: UIImageView
    var onZoom: ((CGFloat, Bool) -> Void)?
    private var fitScale: CGFloat = 0
    /// Until the user zooms, the picture follows the window (a rotation).
    private var followsFit = true

    init(image: UIImage) {
        imageView = UIImageView(image: image)
        imageView.frame = CGRect(origin: .zero, size: image.size)
        super.init(frame: .zero)
        addSubview(imageView)
        showsHorizontalScrollIndicator = false
        showsVerticalScrollIndicator = false
        bouncesZoom = true
        contentInsetAdjustmentBehavior = .never
        contentSize = image.size
        isAccessibilityElement = true
        accessibilityTraits = [.image, .adjustable]
        accessibilityValue = "Fit to screen"
    }

    @available(*, unavailable) required init?(coder: NSCoder) { fatalError() }

    override func layoutSubviews() {
        super.layoutSubviews()
        guard bounds.width > 0, bounds.height > 0, let size = imageView.image?.size, size.width > 0, size.height > 0 else { return }
        let fit = min(1, min(bounds.width / size.width, bounds.height / size.height))
        if abs(fit - fitScale) > 0.0001 || minimumZoomScale != fit {
            fitScale = fit
            minimumZoomScale = fit
            maximumZoomScale = max(4, fit * 4)
            if followsFit { zoomScale = fit }
        }
        centre()
        onZoom?(zoomScale, followsFit)
    }

    /// Keeps a picture smaller than the window in the middle.
    private func centre() {
        let x = max(0, (bounds.width - imageView.frame.width) / 2)
        let y = max(0, (bounds.height - imageView.frame.height) / 2)
        contentInset = UIEdgeInsets(top: y, left: x, bottom: y, right: x)
    }

    func zoomed() {
        followsFit = zoomScale <= fitScale * 1.01
        centre()
        onZoom?(zoomScale, followsFit)
        accessibilityValue = followsFit ? "Fit to screen" : "\(Int((zoomScale * 100).rounded())) percent"
    }

    func apply(_ choice: ZoomChoice) {
        switch choice {
        case .fit:
            followsFit = true
            setZoomScale(fitScale, animated: true)
            if zoomScale == fitScale { onZoom?(zoomScale, true); accessibilityValue = "Fit to screen" }
        case .actual:
            followsFit = false
            setZoomScale(min(max(1, minimumZoomScale), maximumZoomScale), animated: true)
            if zoomScale == min(max(1, minimumZoomScale), maximumZoomScale) { onZoom?(zoomScale, false); accessibilityValue = "\(Int((zoomScale * 100).rounded())) percent" }
        }
    }

    @objc func doubleTapped(_ gesture: UITapGestureRecognizer) {
        if zoomScale > fitScale * 1.01 {
            apply(.fit)
            return
        }
        let target = min(max(1, fitScale * 2.5), maximumZoomScale)
        let point = gesture.location(in: imageView)
        let size = CGSize(width: bounds.width / target, height: bounds.height / target)
        zoom(to: CGRect(x: point.x - size.width / 2, y: point.y - size.height / 2, width: size.width, height: size.height), animated: true)
    }

    // VoiceOver's swipe up and down zoom in and out.
    override func accessibilityIncrement() { setZoomScale(min(maximumZoomScale, zoomScale * 1.5), animated: true) }
    override func accessibilityDecrement() { setZoomScale(max(minimumZoomScale, zoomScale / 1.5), animated: true) }
}
