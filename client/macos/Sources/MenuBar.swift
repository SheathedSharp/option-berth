import AppKit
import BerthTerminal

/// Keep native Edit/Window menus and their responder-chain actions intact.
/// Terminal and agent text inputs use standard copy, paste, selection and undo.
enum MenuBar { static let viewTitle = "视图" }

final class MenuBarDelegate: NSObject, NSApplicationDelegate {
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        MainActor.assumeIsolated {
            guard TerminalSessions.shared.activeCount > 0 else {
                return ConfigurationEditorWindows.shared.confirmTermination() ? .terminateNow : .terminateCancel
            }
            let alert = ClientAlert.make()
            alert.messageText = "仍有终端会话 / Terminal sessions are still running"
            alert.informativeText = "请先在终端页结束会话并确认退出，再退出应用。不会在后台强制停止未知进程。\nEnd the sessions and wait for their exit before quitting."
            alert.addButton(withTitle: "返回 / Cancel")
            let force = TerminalSessions.shared.sessions.contains { $0.isStopping }
            alert.addButton(withTitle: force ? "强制结束会话 / Force end sessions" : "结束会话 / End sessions")
            if alert.runModal() == .alertSecondButtonReturn {
                TerminalSessions.shared.stopAll(allowForce: force)
            }
            // A stop request is not an exit acknowledgment; quitting can be retried
            // after the observed exits even when the last window was closed.
            return .terminateCancel
        }
    }

}
