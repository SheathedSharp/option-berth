import Foundation

@main
enum GitReadQueueTests {
    static func require(_ condition: @autoclosure () -> Bool, _ message: String) {
        if !condition() { fputs("FAIL: \(message)\n", stderr); exit(1) }
    }
    static func main() {
        let queue = GitReadQueue()
        let entered = DispatchSemaphore(value: 0), release = DispatchSemaphore(value: 0)
        let finished = DispatchSemaphore(value: 0)
        let log = Log()
        queue.submit(.patch) { entered.signal(); release.wait(); log.append("running") }
        require(entered.wait(timeout: .now() + 5) == .success, "worker failed to start")
        for n in 0..<10_000 {
            queue.submit(.patch) { log.append("patch-\(n)") }
            queue.submit(.overview) { log.append("overview-\(n)") }
        }
        queue.submit(.tree) { log.append("tree"); finished.signal() }
        release.signal()
        require(finished.wait(timeout: .now() + 5) == .success, "worker did not finish")
        require(log.values == ["running", "patch-9999", "overview-9999", "tree"], "obsolete reads were executed or new kinds starved")
        let entered2 = DispatchSemaphore(value: 0), release2 = DispatchSemaphore(value: 0)
        queue.submit(.overview) { entered2.signal(); release2.wait() }
        require(entered2.wait(timeout: .now() + 5) == .success, "second worker did not start")
        queue.submit(.patch) { log.append("old-worktree") }
        queue.discardPending()
        queue.submit(.patch) { log.append("new-worktree"); finished.signal() }
        release2.signal()
        require(finished.wait(timeout: .now() + 5) == .success, "queue stalled after invalidation")
        require(!log.values.contains("old-worktree") && log.values.last == "new-worktree", "project invalidation retained pending reads")
        // A callback is allowed to submit another read: no lock is held across it.
        queue.submit(.tree) { queue.submit(.tree) { finished.signal() } }
        require(finished.wait(timeout: .now() + 5) == .success, "reentrant submission deadlocked")
        print("PASS: 20,000 queued selections coalesced to latest per kind, project invalidation, fairness and reentrant submission")
    }
    final class Log: @unchecked Sendable {
        let lock = NSLock()
        private var entries: [String] = []
        func append(_ value: String) { lock.lock(); entries.append(value); lock.unlock() }
        var values: [String] { lock.lock(); defer { lock.unlock() }; return entries }
    }
}
