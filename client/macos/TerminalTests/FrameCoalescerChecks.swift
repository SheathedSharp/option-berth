import Foundation
import BerthTerminal

enum FrameCoalescerChecks {
    /// Drives the main run loop so `DispatchQueue.main.asyncAfter` fires the way
    /// it does in the app. `@MainActor` because the type is main-actor isolated.
    @MainActor
    private static func pump(_ seconds: TimeInterval) {
        let end = Date().addingTimeInterval(seconds)
        while Date() < end { RunLoop.main.run(until: Date().addingTimeInterval(0.002)) }
    }

    @MainActor
    static func run() throws {
        // 1. A burst collapses into a single action.
        var runs = 0
        let burst = FrameCoalescer(interval: 0.02) { runs += 1 }
        for _ in 0..<200 { burst.signal() }
        try require(burst.isPending, "a burst scheduled no tick")
        pump(0.12)
        try require(runs == 1, "burst of 200 signals ran \(runs) actions, expected 1")
        try require(!burst.isPending, "tick stayed pending after firing")

        // 2. Signalling again after the tick opens a new window.
        burst.signal()
        pump(0.12)
        try require(runs == 2, "second window did not run exactly once (runs=\(runs))")

        // 3. An idle coalescer never fires on its own.
        var idleRuns = 0
        let idle = FrameCoalescer(interval: 0.02) { idleRuns += 1 }
        pump(0.08)
        try require(idleRuns == 0, "coalescer ran without any signal")

        // 4. flushNow runs immediately and cancels the scheduled tick, so the
        //    same facts are not announced twice.
        var immediate = 0
        let quick = FrameCoalescer(interval: 0.05) { immediate += 1 }
        quick.signal()
        quick.flushNow()
        try require(immediate == 1, "flushNow did not run the action synchronously")
        pump(0.15)
        try require(immediate == 1, "flushNow left a tick that ran a second time (n=\(immediate))")

        // 5. cancelPending drops the tick without running it.
        var cancelled = 0
        let drop = FrameCoalescer(interval: 0.02) { cancelled += 1 }
        drop.signal()
        drop.cancelPending()
        pump(0.1)
        try require(cancelled == 0, "cancelPending still ran the action")

        // 6. The point of the whole thing: events far faster than the frame
        //    interval must still repaint at roughly the frame rate, not once per
        //    event. 500 signals arrive back-to-back with no run-loop pumping in
        //    between — exactly what a token-delta burst looks like — then the
        //    loop is allowed to run.
        var frames = 0
        let stream = FrameCoalescer(interval: 0.016) { frames += 1 }
        for _ in 0..<500 { stream.signal() }
        try require(frames == 0, "signals ran the action synchronously instead of scheduling")
        pump(0.1)
        try require(frames == 1, "a 500-signal burst produced \(frames) repaints, expected 1")
        // Sustained load: the repaint count must stay an order of magnitude
        // below the event count. (An exact bound would just be re-measuring the
        // host's run-loop wake-up granularity, not the coalescer.)
        frames = 0
        for _ in 0..<500 {
            stream.signal()
            pump(0.001)
        }
        pump(0.08)
        try require(frames >= 1, "sustained stream never repainted")
        try require(frames < 500 / 4, "\(frames) repaints for 500 events; coalescing not effective")

        print("PASS: frame coalescing collapses bursts, stays idle without signals, and flushNow/cancelPending do not double-fire")
    }

    private static func require(_ condition: @autoclosure () -> Bool, _ message: String) throws {
        if !condition() { throw CheckFailure(reason: message) }
    }
}
