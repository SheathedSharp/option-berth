import Foundation
import SwiftUI

/// UI navigation only. No process, service fact or transcript is persisted here.
@MainActor
public final class ConsoleWorkspace: ObservableObject {
    @Published public var agentMode = false
    @Published public var providerID = "codex"
    @Published public var draft = ""
    @Published public var nativeExpanded = false
    @Published public var terminalSelection: UUID?
    @Published public var agentSelection: UUID?
    @Published public var terminalSplit: UUID?
    @Published public var agentSplit: UUID?
    @Published public var terminalLayout = PaneLayout()
    @Published public var agentLayout = PaneLayout()

    public init() {}

    public func split(_ id: UUID, beside target: UUID, agent: Bool, axis: PaneLayout.Axis) throws {
        var layout = agent ? agentLayout : terminalLayout
        if layout.nodes.isEmpty { layout = PaneLayout(session: target) }
        try layout.split(id, beside: target, axis: axis)
        if agent { agentLayout = layout } else { terminalLayout = layout }
    }
    public func single(_ id: UUID, agent: Bool) {
        if agent { agentLayout = PaneLayout(session: id); agentSplit = nil }
        else { terminalLayout = PaneLayout(session: id); terminalSplit = nil }
    }
    public func select(_ id: UUID, agent: Bool) {
        if agent { agentLayout.show(id) } else { terminalLayout.show(id) }
        if agent {
            if agentSplit == id { agentSplit = agentSelection }
            agentSelection = id
        } else {
            if terminalSplit == id { terminalSplit = terminalSelection }
            terminalSelection = id
        }
    }
    public func forget(_ id: UUID) {
        terminalLayout.remove(id); agentLayout.remove(id)
        if terminalSelection == id { terminalSelection = terminalSplit; terminalSplit = nil }
        if agentSelection == id { agentSelection = agentSplit; agentSplit = nil }
        if terminalSplit == id { terminalSplit = nil }
        if agentSplit == id { agentSplit = nil }
    }
}

/// The sole owner of client PTYs. Project removal never makes a live session
/// unreachable: the session manager lists this registry independently of manifests.
@MainActor
public final class TerminalSessions: ObservableObject {
    public static let shared = TerminalSessions()
    @Published public private(set) var sessions: [TerminalSession] = []
    private var workspaces: [String: ConsoleWorkspace] = [:]
    public var activeCount: Int { sessions.filter(\.isActive).count }
    public init() {}

    private func canonical(_ root: String) -> String {
        guard root.hasPrefix("/"), !root.utf8.contains(0) else { return "" }
        return URL(fileURLWithPath: root).standardizedFileURL.resolvingSymlinksInPath().path
    }
    public func workspace(_ root: String) -> ConsoleWorkspace {
        let key = canonical(root)
        if let value = workspaces[key] { return value }
        let value = ConsoleWorkspace()
        workspaces[key] = value
        return value
    }
    /// Called only after an explicit project removal, not after a failed scan.
    public func forgetWorkspace(_ root: String) {
        workspaces.removeValue(forKey: canonical(root))
    }
    public func inWorktree(_ root: String) -> [TerminalSession] {
        let key = canonical(root)
        return key.isEmpty ? [] : sessions.filter { $0.worktree == key }
    }
    @discardableResult
    public func add(worktree: String, title: String, kind: String = "terminal", executable: String,
                    arguments: [String], environment: [String: String]? = nil, shellIntegration: Bool = false) throws -> TerminalSession {
        guard sessions.count < 16 else { throw TerminalFailure("先关闭一个会话 / Close a session before opening another (16 maximum)") }
        let session = TerminalSession(worktree: worktree, title: title, kind: kind)
        session.onChange = { [weak self] in self?.objectWillChange.send() }
        try session.start(executable: executable, arguments: arguments, environment: environment, shellIntegration: shellIntegration)
        sessions.append(session)
        select(session)
        return session
    }
    public func stopAll(allowForce: Bool) {
        // A stubborn session must not escalate another newly started session.
        for session in sessions { session.stop(force: allowForce && session.isStopping) }
    }
    public func select(_ session: TerminalSession) {
        guard sessions.contains(where: { $0.id == session.id }) else { return }
        let state = workspace(session.worktree)
        let agent = session.kind.hasPrefix("agent:")
        state.agentMode = agent
        state.select(session.id, agent: agent)
    }
    @discardableResult
    public func remove(_ session: TerminalSession) -> Bool {
        guard !session.isActive else { return false }
        let state = workspaces[session.worktree]
        state?.forget(session.id)
        sessions.removeAll { $0.id == session.id }
        if let state {
            let promotedID = state.agentMode ? state.agentSelection : state.terminalSelection
            if let promoted = sessions.first(where: { $0.id == promotedID }),
               promoted.kind.hasPrefix("agent:") != state.agentMode {
                // Mixed splits can promote a different kind. Keep it visible.
                if state.agentMode { state.agentSelection = nil }
                else { state.terminalSelection = nil }
                select(promoted)
            }
        }
        return true
    }
}
