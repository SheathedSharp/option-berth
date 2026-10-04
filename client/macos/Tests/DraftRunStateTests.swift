import Foundation

@main
enum DraftRunStateTests {
    static var checks = 0
    static func check(_ condition: @autoclosure () -> Bool, _ message: String) {
        checks += 1
        if !condition() { fatalError(message) }
    }
    static func main() {
        var state = DraftRunState()
        let a = state.begin()
        state.offer("services: {}\n", for: a)
        check(state.isRunning, "done is not an exit acknowledgement")
        check(state.finish(a, failure: "exit 7") == nil, "failed command committed candidate")
        check(state.phase == .failed && state.problem == "exit 7", "failure lost")
        let b = state.begin()
        state.offer("candidate", for: b); state.cancel()
        check(state.finish(b) == nil, "cancelled result returned bytes")
        check(!state.accepts(b), "cancelled request accepted progress")
        let c = state.begin()
        state.offer("stale", for: b); state.reject("stale error", for: b)
        check(state.problem == nil && state.isRunning, "old request mutated new state")
        check(state.finish(b, failure: "late exit") == nil && state.isRunning, "old exit ended new request")
        state.offer("  services: {}\n", for: c)
        check(state.finish(c) == "  services: {}\n", "successful bytes were normalized")
        check(state.phase == .ready && !state.isRunning, "success never completed")
        check(state.finish(c) == nil, "result applied twice")
        let d = state.begin()
        check(state.finish(d) == nil && state.phase == .failed, "zero exit without candidate accepted")
        let e = state.begin(); state.offer(" \n", for: e)
        check(state.isRunning && state.problem != nil, "invalid candidate prematurely finished")
        state.offer("later candidate", for: e)
        check(state.finish(e) == nil, "later done erased an earlier protocol error")
        let f = state.begin(); state.offer("candidate", for: f); state.reject("provider error", for: f)
        check(state.finish(f) == nil && state.problem == "provider error", "error followed by zero exit accepted")
        print("PASS: \(checks) draft candidate/exit, cancellation, stale generation and byte-preservation checks")
    }
}
