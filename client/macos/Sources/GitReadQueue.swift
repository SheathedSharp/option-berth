import Foundation

/// One running read, at most one pending read of each kind. Rapid selection or
/// worktree changes replace obsolete queued work instead of building an unbounded
/// DispatchQueue backlog. In-flight OS/CLI work is not falsely declared cancelled.
final class GitReadQueue: @unchecked Sendable {
    enum Kind: Hashable { case overview, tree, patch }
    private let lock = NSLock()
    private let queue = DispatchQueue(label: "option-berth.git", qos: .utility)
    private var pending: [Kind: () -> Void] = [:]
    private var order: [Kind] = []
    private var running = false

    func submit(_ kind: Kind, work: @escaping () -> Void) {
        lock.lock()
        if pending[kind] == nil { order.append(kind) }
        pending[kind] = work
        let start = !running
        running = true
        lock.unlock()
        if start { queue.async { self.drain() } }
    }
    func discardPending() {
        lock.lock()
        pending.removeAll()
        order.removeAll()
        lock.unlock()
    }
    private func drain() {
        while true {
            lock.lock()
            guard !order.isEmpty else {
                running = false
                lock.unlock()
                return
            }
            let kind = order.removeFirst() // At most three keys; no history scan.
            let work = pending.removeValue(forKey: kind)
            lock.unlock()
            work?()
        }
    }
}
