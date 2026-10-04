import AppKit
import SwiftUI
import BerthTerminal

@MainActor
final class TerminalSessions: ObservableObject {
    static let shared = TerminalSessions()
    @Published private(set) var sessions: [TerminalSession] = []
    var activeCount: Int { sessions.filter(\.isActive).count }
    func add(worktree: String, title: String, kind: String = "terminal", executable: String, arguments: [String]) throws -> TerminalSession {
        guard sessions.count < 16 else { throw TerminalFailure("先关闭一个会话 / Close a session before opening another (16 maximum)") }
        let session = TerminalSession(worktree: worktree, title: title, kind: kind)
        session.onChange = { [weak self] in self?.objectWillChange.send() }
        try session.start(executable: executable, arguments: arguments)
        sessions.append(session)
        return session
    }
    func remove(_ session: TerminalSession) {
        guard !session.isActive else { return }
        sessions.removeAll { $0.id == session.id }
    }
    func inWorktree(_ root: String) -> [TerminalSession] {
        guard root.hasPrefix("/") else { return [] }
        let canonical = URL(fileURLWithPath: root).standardizedFileURL.resolvingSymlinksInPath().path
        return sessions.filter { $0.worktree == canonical }
    }
}

struct TerminalSurface: NSViewRepresentable {
    let session: TerminalSession
    func makeNSView(context: Context) -> HostedTerminalView {
        session.terminal.nativeBackgroundColor = NSColor(Ink.canvas)
        session.terminal.nativeForegroundColor = NSColor(Ink.ink)
        return session.terminal
    }
    func updateNSView(_ view: HostedTerminalView, context: Context) {}
}
