import AppKit
import BerthTerminal

/// Keep native Edit/Window menus and their responder-chain actions intact.
/// Terminal and agent text inputs use standard copy, paste, selection and undo.
enum MenuBar { static let viewTitle = "视图" }

final class MenuBarDelegate: NSObject, NSApplicationDelegate {
    /// Set once a quit has been granted, so the automatic retry below does not
    /// re-run the confirmation it already answered.
    private var quitApproved = false
    /// Set while a `.terminateLater` wait is outstanding. A second ⌘Q during the
    /// wait must not start a parallel poll — `reply(toApplicationShouldTerminate:)`
    /// may only be answered once per request.
    private var waitingForExit = false
    /// Upper bound on waiting for sessions to actually exit. A shell that ignores
    /// SIGHUP must not leave the app in "quitting" forever.
    private static let exitDeadline: TimeInterval = 6

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        MainActor.assumeIsolated {
            if waitingForExit { return .terminateCancel }
            if quitApproved {
                return ConfigurationEditorWindows.shared.confirmTermination() ? .terminateNow : .terminateCancel
            }
            guard TerminalSessions.shared.activeCount > 0 || AgentStreamRegistry.activeBridgeCount > 0 else {
                return ConfigurationEditorWindows.shared.confirmTermination() ? .terminateNow : .terminateCancel
            }
            let alert = ClientAlert.make()
            alert.messageText = "仍有会话在运行 / Sessions are still running"
            alert.informativeText = "结束这些会话后退出应用。\nShell, agent and structured-stream sessions will be ended, then the app quits."
            alert.addButton(withTitle: "返回 / Cancel")
            let force = TerminalSessions.shared.sessions.contains { $0.isStopping }
            alert.addButton(withTitle: force ? "强制结束并退出 / Force end and quit" : "结束并退出 / End and quit")
            guard alert.runModal() == .alertSecondButtonReturn else { return .terminateCancel }

            for stream in AgentStreamRegistry.all { AgentStreamRegistry.end(stream.id) }
            TerminalSessions.shared.stopAll(allowForce: force)

            // A signal receipt is not an exit. Observe the real one, then finish
            // the quit; `.terminateLater` keeps the app alive meanwhile so the
            // completion cannot race the app's own teardown.
            let deadline = Date().addingTimeInterval(Self.exitDeadline)
            waitingForExit = true
            waitForSessionsToExit(until: deadline, sender: sender)
            return .terminateLater
        }
    }

    /// Poll the real exit state, then finish the quit — or report why it cannot.
    @MainActor
    private func waitForSessionsToExit(until deadline: Date, sender: NSApplication) {
        let blockers = QuitReadiness.blockers(
            activeShellTitles: TerminalSessions.shared.sessions.filter(\.isActive).map(\.title),
            activeAgentStreams: AgentStreamRegistry.activeBridgeCount)
        switch QuitReadiness.step(blockers: blockers, now: Date(), deadline: deadline) {
        case .finish:
            waitingForExit = false
            quitApproved = true
            sender.reply(toApplicationShouldTerminate: true)
        case .giveUp(let remaining):
            // Never claim success. Name what is still alive and leave the app
            // open; the person can force-quit or go end it by hand.
            let names = remaining.summary
            let alert = ClientAlert.make()
            alert.messageText = "会话没有退出，应用没有关闭 / Sessions did not exit"
            alert.informativeText = "仍然活着：\(names)\nStill running: \(names)"
            alert.addButton(withTitle: "返回 / OK")
            alert.runModal()
            waitingForExit = false
            quitApproved = false
            sender.reply(toApplicationShouldTerminate: false)
        case .keepWaiting:
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.05) { [weak self] in
                MainActor.assumeIsolated { self?.waitForSessionsToExit(until: deadline, sender: sender) }
            }
        }
    }
}
