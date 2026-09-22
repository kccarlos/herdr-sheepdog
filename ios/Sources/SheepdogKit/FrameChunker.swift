import Foundation

/// Re-chunks a stream of samples into fixed-size frames.
///
/// The input tap hands us whatever the hardware likes (typically ~4 096
/// frames at 48 kHz, so ~4.3 capture frames per callback), and the contract
/// wants 20–100 ms frames; this makes them exactly 40 ms.
public struct FrameChunker: Sendable {
    public let size: Int
    private var pending: [Int16] = []

    public init(frameSamples: Int = AudioContract.frameSamples) {
        self.size = max(1, frameSamples)
        pending.reserveCapacity(self.size)
    }

    /// Appends `samples` and returns every complete frame; the remainder is
    /// kept for the next call.
    public mutating func push(_ samples: [Int16]) -> [[Int16]] {
        var frames: [[Int16]] = []
        var offset = 0
        while offset < samples.count {
            let take = min(size - pending.count, samples.count - offset)
            pending.append(contentsOf: samples[offset ..< offset + take])
            offset += take
            if pending.count == size {
                frames.append(pending)
                pending.removeAll(keepingCapacity: true)
            }
        }
        return frames
    }

    /// Samples waiting for the next frame.
    public var buffered: Int { pending.count }

    public mutating func reset() { pending.removeAll(keepingCapacity: true) }
}

public extension Array where Element == Int16 {
    /// PCM16 little-endian bytes, the wire format of a binary frame.
    var pcm16LEData: Data {
        var out = Data(capacity: count * 2)
        for sample in self {
            let u = UInt16(bitPattern: sample)
            out.append(UInt8(u & 0xff))
            out.append(UInt8(u >> 8))
        }
        return out
    }

    /// Decodes PCM16 little-endian bytes. A trailing odd byte is dropped
    /// (it would be a contract violation).
    init(pcm16LE data: Data) {
        var out = [Int16]()
        out.reserveCapacity(data.count / 2)
        var index = data.startIndex
        while index + 1 < data.endIndex {
            let lo = UInt16(data[index])
            let hi = UInt16(data[index + 1])
            out.append(Int16(bitPattern: lo | (hi << 8)))
            index += 2
        }
        self = out
    }
}
