import QuartzCore
import SheepdogKit
import UIKit
import XCTest

/// R2 of docs/dev/design/phone-terminal.md, measured on the simulator only:
/// how long the renderer takes per herdr frame. The design's pass mark
/// (under 4 ms of main-thread time per frame, on an iPhone 12-class device)
/// needs a device; these tests print the simulator's numbers ("R2: …") and
/// fail only far above any budget, so a slow CI machine does not flake.
final class TerminalThroughputTests: XCTestCase {
    /// The recorded bursts plus a worst-case synthetic frame: every cell of
    /// a 58×30 screen a different true colour, half of them CJK.
    private func workload() throws -> [(String, [RecordedFrame])] {
        [
            ("burst (seq 1 200000)", try TerminalFixture.frames("burst")),
            ("colour (3000 lines)", try TerminalFixture.frames("colour")),
            ("worst case (every cell recoloured)", [Self.worstCaseFrame(cols: 58, rows: 30)]),
        ]
    }

    func testHeadlessFeedTimePerFrame() throws {
        for (name, frames) in try workload() {
            let grid = TerminalRenderer.makeGrid(cols: frames[0].cols, rows: frames[0].rows)
            let rounds = 50
            var times: [Double] = []
            for _ in 0..<rounds {
                for frame in frames {
                    let t0 = CACurrentMediaTime()
                    grid.feed(frame.bytes)
                    times.append((CACurrentMediaTime() - t0) * 1000)
                }
            }
            let s = Stats(times)
            print(String(format: "R2: headless feed, %@: %d frames, %.0f bytes/frame, p50 %.3f ms, p99 %.3f ms, max %.3f ms",
                         name, times.count, Double(frames.map(\.bytes.count).reduce(0, +)) / Double(frames.count),
                         s.p50, s.p99, s.max))
            XCTAssertLessThan(s.p50, 50, "\(name): parsing one frame should take far less than a display refresh")
        }
    }

    /// Feed plus a full draw of the view (`layer.render(in:)` draws every
    /// row), on the main thread, the way the window will run.
    @MainActor
    func testSurfaceFeedAndDrawTimePerFrame() throws {
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 402, height: 874))
        let surface = TerminalRenderer.makeSurface()
        surface.view.frame = window.bounds
        window.addSubview(surface.view)
        window.isHidden = false
        surface.view.layoutIfNeeded()
        let format = UIGraphicsImageRendererFormat()
        format.scale = 3
        let renderer = UIGraphicsImageRenderer(bounds: surface.view.bounds, format: format)
        for (name, frames) in try workload() {
            var feed: [Double] = []
            var draw: [Double] = []
            for _ in 0..<10 {
                for frame in frames {
                    let t0 = CACurrentMediaTime()
                    surface.feed(frame.bytes)
                    RunLoop.main.run(until: Date())
                    let t1 = CACurrentMediaTime()
                    _ = renderer.image { ctx in surface.view.layer.render(in: ctx.cgContext) }
                    let t2 = CACurrentMediaTime()
                    feed.append((t1 - t0) * 1000)
                    draw.append((t2 - t1) * 1000)
                }
            }
            let f = Stats(feed), d = Stats(draw)
            print(String(format: "R2: surface %dx%d, %@: feed p50 %.3f ms p99 %.3f ms; full draw p50 %.3f ms p99 %.3f ms",
                         surface.cols, surface.rows, name, f.p50, f.p99, d.p50, d.p99))
            XCTAssertLessThan(f.p50 + d.p50, 100, name)
        }
        XCTAssertGreaterThan(surface.cols, 20, "the surface sized its grid from the view")
    }

    static func worstCaseFrame(cols: Int, rows: Int) -> RecordedFrame {
        var s = "\u{1b}[?2026h\u{1b}[2J"
        for row in 0..<rows {
            s += "\u{1b}[\(row + 1);1H"
            var col = 0
            while col < cols {
                let r = (row * 37 + col * 11) % 256, g = (col * 53) % 256, b = (row * 97) % 256
                s += "\u{1b}[0;38;2;\(r);\(g);\(b);48;2;\(b);\(r);\(g)m"
                if col % 2 == 0, col + 2 <= cols {
                    s += "中"
                    col += 2
                } else {
                    s += "x"
                    col += 1
                }
            }
        }
        s += "\u{1b}[0m\u{1b}[?2026l"
        return RecordedFrame(seq: 1, cols: cols, rows: rows, full: true, bytes: Array(s.utf8))
    }
}

private struct Stats {
    let p50: Double
    let p99: Double
    let max: Double

    init(_ values: [Double]) {
        let sorted = values.sorted()
        p50 = sorted[sorted.count / 2]
        p99 = sorted[min(sorted.count - 1, Int(Double(sorted.count) * 0.99))]
        max = sorted.last ?? 0
    }
}
