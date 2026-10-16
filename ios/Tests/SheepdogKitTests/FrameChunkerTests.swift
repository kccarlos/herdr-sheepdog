import XCTest
@testable import SheepdogKit

final class FrameChunkerTests: XCTestCase {
    func testEmitsFixedFramesAndKeepsTheRemainder() {
        var chunker = FrameChunker(frameSamples: 640)
        // A 48 kHz tap buffer of 4 096 frames becomes ~1 365 samples at
        // 16 kHz: two whole frames and a remainder.
        let frames = chunker.push([Int16](repeating: 1, count: 1_365))
        XCTAssertEqual(frames.count, 2)
        XCTAssertTrue(frames.allSatisfy { $0.count == 640 })
        XCTAssertEqual(chunker.buffered, 85)

        let more = chunker.push([Int16](repeating: 2, count: 1_365))
        XCTAssertEqual(more.count, 2)
        XCTAssertEqual(chunker.buffered, 170)
        // The remainder leads the next frame: sample order is preserved.
        XCTAssertEqual(more[0][0], 1)
        XCTAssertEqual(more[0][85], 2)
    }

    func testSampleOrderSurvivesArbitraryBlockSizes() {
        var chunker = FrameChunker(frameSamples: 4)
        var out: [Int16] = []
        for block in [[Int16(1)], [2, 3], [4, 5, 6, 7], [8]] {
            for frame in chunker.push(block) { out += frame }
        }
        XCTAssertEqual(out, [1, 2, 3, 4, 5, 6, 7, 8])
    }

    func testPCM16LittleEndianRoundTrip() {
        let samples: [Int16] = [0, 1, -1, 32_767, -32_768, 256]
        let data = samples.pcm16LEData
        XCTAssertEqual(data.count, samples.count * 2)
        XCTAssertEqual([UInt8](data.prefix(4)), [0x00, 0x00, 0x01, 0x00])
        XCTAssertEqual([Int16](pcm16LE: data), samples)
    }

    func testATrailingOddByteIsDropped() {
        var data = [Int16(7), 8].pcm16LEData
        data.append(0x33)
        XCTAssertEqual([Int16](pcm16LE: data), [7, 8])
    }
}
