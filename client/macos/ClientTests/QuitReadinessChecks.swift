import Foundation
@testable import BerthClient

extension ClientChecks {
    /// The quit path is easy to get wrong in a way nobody notices until they are
    /// already trying to close the app: reporting "done" while something is
    /// still alive, or waiting forever on a shell that ignores the signal.
    static func quitReadinessChecks() {
        let now = Date()

        // Nothing running → finish immediately.
        let clear = QuitReadiness.blockers(activeShellTitles: [], activeAgentStreams: 0)
        require(clear.isClear, "empty blocker set reported as busy")
        require(QuitReadiness.step(blockers: clear, now: now, deadline: now) == .finish,
                "a clear blocker set did not finish the quit")

        // Something alive and time remains → keep observing, never finish.
        let busy = QuitReadiness.blockers(activeShellTitles: ["Shell 1"], activeAgentStreams: 1)
        require(!busy.isClear, "live shell reported as clear")
        let s1 = QuitReadiness.step(blockers: busy, now: now, deadline: now.addingTimeInterval(6))
        require(s1 == .keepWaiting,
                "a live session finished the quit early")

        // Out of time with something alive → give up and say what it is. This is
        // the case that must never return success.
        let expired = QuitReadiness.step(blockers: busy, now: now.addingTimeInterval(7),
                                         deadline: now.addingTimeInterval(6))
        guard case .giveUp(let remaining) = expired else {
            return require(false, "timeout with a live session did not report failure")
        }
        require(remaining.summary.contains("Shell 1"), "timeout report dropped the shell name")
        require(remaining.summary.contains("agent stream"), "timeout report dropped the agent stream")

        // Identity of the summary: every blocker is named, and nothing is
        // invented when only one kind is alive.
        let streamsOnly = QuitReadiness.blockers(activeShellTitles: [], activeAgentStreams: 2)
        require(streamsOnly.summary == "agent stream、agent stream",
                "agent-only summary was wrong: \(streamsOnly.summary)")
        let shellsOnly = QuitReadiness.blockers(activeShellTitles: ["A", "B"], activeAgentStreams: 0)
        require(shellsOnly.summary == "A、B", "shell-only summary was wrong: \(shellsOnly.summary)")

        // Deadline exactly at now counts as expired: a stuck app should stop
        // waiting rather than spin one more tick.
        require(QuitReadiness.step(blockers: busy, now: now, deadline: now) == .giveUp(busy),
                "deadline boundary kept waiting instead of giving up")

        // Negative counts cannot fabricate blockers.
        let clamped = QuitReadiness.blockers(activeShellTitles: [], activeAgentStreams: -3)
        require(clamped.isClear, "negative stream count created a phantom blocker")

        print("PASS: quit readiness only finishes on observed exits and names what is still running")
    }
}
