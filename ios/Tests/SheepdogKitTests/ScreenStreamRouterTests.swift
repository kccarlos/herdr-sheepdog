import SheepdogKit
import XCTest

@MainActor
final class ScreenStreamRouterTests: XCTestCase {
    private let frame = AgentScreenEvent.error("x", ended: false)

    func testDeliversOnlyToThatPane() {
        let router = ScreenStreamRouter()
        var a = 0, b = 0
        let sa = router.subscribe(paneID: "a") { _ in a += 1 }
        let sb = router.subscribe(paneID: "b") { _ in b += 1 }
        for _ in 0 ..< 3 { router.deliver(frame, to: "a") }
        router.deliver(frame, to: "c")
        XCTAssertEqual(a, 3)
        XCTAssertEqual(b, 0)
        _ = (sa, sb)
    }

    func testEndingOrReleasingStopsDelivery() {
        let router = ScreenStreamRouter()
        var count = 0
        let sub = router.subscribe(paneID: "a") { _ in count += 1 }
        router.deliver(frame, to: "a")
        sub.end()
        router.deliver(frame, to: "a")
        XCTAssertEqual(count, 1)
        XCTAssertEqual(router.listeners("a"), 0)

        var released: ScreenStreamRouter.Subscription? = router.subscribe(paneID: "a") { _ in count += 1 }
        XCTAssertEqual(router.listeners("a"), 1)
        released = nil
        XCTAssertNil(released)
        router.deliver(frame, to: "a")
        XCTAssertEqual(count, 1, "a released subscription hears nothing")
        XCTAssertEqual(router.listeners("a"), 0)
    }

    func testTwoWindowsOnOnePaneBothHear() {
        let router = ScreenStreamRouter()
        var count = 0
        let one = router.subscribe(paneID: "a") { _ in count += 1 }
        let two = router.subscribe(paneID: "a") { _ in count += 1 }
        router.deliver(frame, to: "a")
        XCTAssertEqual(count, 2)
        one.end()
        router.deliver(frame, to: "a")
        XCTAssertEqual(count, 3)
        _ = two
    }
}
