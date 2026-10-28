import Foundation

/// Collapses a burst of "something changed" signals into at most one action per
/// frame.
///
/// **Why this exists.** Both hot paths in the console receive facts far faster
/// than a screen can show them: SwiftTerm hands over PTY bytes per read, and the
/// structured bridge emits one NDJSON line per token delta. Publishing each of
/// those to SwiftUI sends `objectWillChange` through the whole window, so a
/// `yes`, a build log or a long model answer re-evaluates the sidebar, the
/// service board and every block card hundreds of times a second. The main
/// thread cannot keep up and the window stops responding.
///
/// Facts themselves are never deferred — callers keep writing them into their
/// own stores immediately. Only the repaint hand-off waits for the tick, so a
/// reader always sees current data.
///
/// `@MainActor` because every caller hands results to SwiftUI, and because the
/// pending work item is plain mutable state that should not cross threads.
@MainActor
public final class FrameCoalescer {
    /// Upper bound on latency. 16 ms sits inside one frame at 60 Hz, so a burst
    /// still paints once per frame instead of once per event.
    public static let defaultInterval: TimeInterval = 0.016

    private let interval: TimeInterval
    private let action: () -> Void
    private var pending: DispatchWorkItem?
    private var signalled = false

    /// - Parameter action: runs on the main actor, at most once per `interval`.
    public init(interval: TimeInterval = FrameCoalescer.defaultInterval, action: @escaping () -> Void) {
        self.interval = interval
        self.action = action
    }

    /// Whether a tick is currently scheduled. Exposed for tests and for callers
    /// that need to know whether a flush is already on its way.
    public var isPending: Bool { pending != nil }

    /// Record a change and make sure one tick is scheduled.
    ///
    /// Repeated calls inside the same window collapse into the tick already
    /// queued; none of them is lost, because the tick runs the action once for
    /// the whole window.
    public func signal() {
        signalled = true
        guard pending == nil else { return }
        let work = DispatchWorkItem { [weak self] in
            MainActor.assumeIsolated { self?.flush() }
        }
        pending = work
        DispatchQueue.main.asyncAfter(deadline: .now() + interval, execute: work)
    }

    /// Run now and cancel any scheduled tick, so the same facts are not
    /// announced twice. Use for actions that must not wait (a process exit, a
    /// modal result) or that are about to be followed by a synchronous read.
    public func flushNow() {
        cancelPending()
        action()
    }

    /// Drop a scheduled tick without running it. The caller is about to publish
    /// equivalent facts itself, or the facts were superseded by an exit.
    public func cancelPending() {
        pending?.cancel()
        pending = nil
        signalled = false
    }

    private func flush() {
        pending = nil
        guard signalled else { return }
        signalled = false
        action()
    }
}
