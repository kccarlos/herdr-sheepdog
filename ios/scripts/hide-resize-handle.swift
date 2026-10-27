// Paints over the iPadOS window resize handle (a small arc in the bottom-right corner that
// the simulator draws on every iPad frame) by copying, row by row, the pixels just left of it.
//   swift scripts/hide-resize-handle.swift <png>...
import CoreGraphics
import Foundation
import ImageIO

for path in CommandLine.arguments.dropFirst() {
    let url = URL(fileURLWithPath: path)
    guard let src = CGImageSourceCreateWithURL(url as CFURL, nil), let image = CGImageSourceCreateImageAtIndex(src, 0, nil),
          let ctx = CGContext(data: nil, width: image.width, height: image.height, bitsPerComponent: 8, bytesPerRow: 0,
                              space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue)
    else { fatalError("cannot read \(path)") }
    ctx.draw(image, in: CGRect(x: 0, y: 0, width: image.width, height: image.height))
    let buffer = ctx.data!.assumingMemoryBound(to: UInt8.self)
    let stride = ctx.bytesPerRow
    let boxWidth = 62, boxHeight = 54
    for y in (image.height - boxHeight) ..< image.height {
        let from = y * stride + (image.width - boxWidth - 6) * 4
        for x in (image.width - boxWidth) ..< image.width {
            for c in 0 ..< 4 { buffer[y * stride + x * 4 + c] = buffer[from + c] }
        }
    }
    let out = ctx.makeImage()!
    let dest = CGImageDestinationCreateWithURL(url as CFURL, "public.png" as CFString, 1, nil)!
    CGImageDestinationAddImage(dest, out, nil)
    CGImageDestinationFinalize(dest)
}
