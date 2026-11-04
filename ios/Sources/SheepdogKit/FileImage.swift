import CoreGraphics
import Foundation
import ImageIO

/// A picture of the file browser, decoded and cut down to a size the phone
/// can hold. A 16 MiB PNG of 10,000 × 8,000 pixels is 320 MB once decoded;
/// the viewer needs a few thousand pixels at most, so the decode makes only
/// those (`CGImageSourceCreateThumbnailAtIndex`) and the file's own bytes can
/// be let go straight after.
public struct FileImage: Equatable, @unchecked Sendable {
    /// The most pixels on a side for the full-screen viewer (4,096² is 64 MB).
    public static let viewerMaxPixel = 4_096
    /// The most pixels on a side for a picture inside a document, which is
    /// as wide as the screen: enough for a 3× phone at full width.
    public static let inlineMaxPixel = 1_600

    /// The pixels to draw. Decoded already, so drawing does no work. A
    /// `CGImage` never changes once made, so it may cross threads.
    public let cgImage: CGImage
    /// The picture's own size in pixels, whatever `cgImage` was cut down to.
    public let originalWidth: Int
    public let originalHeight: Int

    public init(cgImage: CGImage, originalWidth: Int, originalHeight: Int) {
        self.cgImage = cgImage
        self.originalWidth = originalWidth
        self.originalHeight = originalHeight
    }

    /// 1 for a picture kept whole; below 1 for one cut down. Giving the
    /// drawn image this scale makes its size in points the original's size
    /// in pixels, so "100%" still means one original pixel per point.
    public var scale: Double {
        originalWidth > 0 ? Double(cgImage.width) / Double(originalWidth) : 1
    }

    /// "4032 × 3024".
    public var sizeText: String { "\(originalWidth) × \(originalHeight)" }

    public static func == (a: FileImage, b: FileImage) -> Bool { a.cgImage === b.cgImage }

    // MARK: Decoding

    /// Decodes `data` off the caller's actor, with at most `maxPixel` pixels
    /// on the long side. Nil when the bytes are not a picture ImageIO opens.
    nonisolated public static func decode(_ data: Data, maxPixel: Int) async -> FileImage? {
        guard !Task.isCancelled else { return nil }
        let image = decodeNow(data, maxPixel: maxPixel)
        return Task.isCancelled ? nil : image
    }

    /// The work of `decode`, on the calling thread.
    static func decodeNow(_ data: Data, maxPixel: Int) -> FileImage? {
        // Not cached: the bytes are read once, here.
        let sourceOptions = [kCGImageSourceShouldCache: false] as CFDictionary
        guard let source = CGImageSourceCreateWithData(data as CFData, sourceOptions),
              CGImageSourceGetCount(source) > 0,
              let size = pixelSize(of: source) else { return nil }
        // One route for both: a picture under the cap is "cut" to its own
        // size, which only decodes it here (not on first draw) and turns it
        // upright; a larger one is cut down.
        let longSide = max(size.width, size.height)
        guard let image = CGImageSourceCreateThumbnailAtIndex(source, 0, [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceShouldCacheImmediately: true,
            kCGImageSourceThumbnailMaxPixelSize: min(maxPixel, longSide),
        ] as CFDictionary) else { return nil }
        return FileImage(cgImage: image, originalWidth: size.width, originalHeight: size.height)
    }

    /// The first image's size as it shows (a picture stored sideways is
    /// turned by its orientation), without decoding it.
    static func pixelSize(of source: CGImageSource) -> (width: Int, height: Int)? {
        guard let properties = CGImageSourceCopyPropertiesAtIndex(source, 0, nil) as? [CFString: Any],
              let width = properties[kCGImagePropertyPixelWidth] as? Int,
              let height = properties[kCGImagePropertyPixelHeight] as? Int,
              width > 0, height > 0 else { return nil }
        let orientation = properties[kCGImagePropertyOrientation] as? Int ?? 1
        return (5 ... 8).contains(orientation) ? (height, width) : (width, height)
    }
}
