import Foundation

/// The decision half of "can the app quit yet", separated from AppKit so it can
/// be tested without a modal alert or a running `NSApplication`.
///
/// The quit path has to answer one question repeatedly while sessions die:
/// *have they all actually exited?* A signal receipt is not an exit, and the
/// engine's own rule is the same — see `docs/architecture.md`「信号回执不是
/// 退出」. So this type only ever reports observed state; it never infers.
enum QuitReadiness {
    /// What is still keeping the app alive.
    struct Blockers: Equatable {
        var shells: [String] = []
        var agentStreams = 0

        var isClear: Bool { shells.isEmpty && agentStreams == 0 }

        /// A single line naming what is still running, for the "did not exit"
        /// alert. Never empty when `isClear` is false.
        var summary: String {
            let streams = Array(repeating: "agent stream", count: agentStreams)
            return (shells + streams).joined(separator: "、")
        }
    }

    /// Observed state → what is still blocking. Pure; the caller supplies the
    /// facts it just read.
    static func blockers(activeShellTitles: [String], activeAgentStreams: Int) -> Blockers {
        Blockers(shells: activeShellTitles, agentStreams: max(0, activeAgentStreams))
    }

    /// How the poll loop should proceed this tick.
    enum Step: Equatable {
        /// Nothing is running: finish the quit.
        case finish
        /// Still running and time remains: poll again.
        case keepWaiting
        /// Still running and out of time: report, do not claim success.
        case giveUp(Blockers)
    }

    /// Decide one tick. `now` and `deadline` are parameters so the timeout is
    /// testable without sleeping.
    static func step(blockers: Blockers, now: Date, deadline: Date) -> Step {
        if blockers.isClear { return .finish }
        return now >= deadline ? .giveUp(blockers) : .keepWaiting
    }
}
