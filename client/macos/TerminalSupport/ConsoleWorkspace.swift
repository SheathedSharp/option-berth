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
        if agent { agentLayout = PaneLayout(session: id); agentSelection = id }
        else { terminalLayout = PaneLayout(session: id); terminalSelection = id }
    }
    public func select(_ id: UUID, agent: Bool) {
        if agent { agentLayout.show(id) } else { terminalLayout.show(id) }
        if agent {
            agentSelection = id
        } else {
            terminalSelection = id
        }
    }
    public func forget(_ id: UUID) {
        terminalLayout.remove(id); agentLayout.remove(id)
        if terminalSelection == id { terminalSelection = terminalLayout.focused }
        if agentSelection == id { agentSelection = agentLayout.focused }
    }
}

/// The sole owner of client PTYs. Project removal never makes a live session
/// unreachable: the session manager lists this registry independently of manifests.
@MainActor
public final class TerminalSessions: ObservableObject {
    public static let shared = TerminalSessions()
    @Published public private(set) var sessions: [TerminalSession] = []
    private var workspaces: [String: ConsoleWorkspace] = [:]
    /// Default for newly created UI workspaces only; never changes an existing selection.
    public var defaultProviderID = "codex"
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
        value.providerID = defaultProviderID
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
    public func snapshot(remembered: [SavedSession] = []) throws -> WorkspaceArchive {
        var records = sessions.map { SavedSession(id: $0.id, worktree: $0.worktree, title: $0.title, kind: $0.kind) }
        let current = Set(records.map(\.id))
        records += remembered.filter { !current.contains($0.id) }
        // Removing a manifest may discard its UI cache, but must not prevent
        // saving the still-owned standalone session's navigation metadata.
        let roots = Set(workspaces.keys.filter { !$0.isEmpty }).union(records.map(\.worktree))
        let values = roots.map { root in
            let state = workspaces[root] ?? ConsoleWorkspace()
            return SavedWorkspace(root: root, agentMode: state.agentMode, providerID: state.providerID, nativeExpanded: state.nativeExpanded,
                           terminalLayout: state.terminalLayout, agentLayout: state.agentLayout)
        }.sorted { $0.root < $1.root }
        let result = WorkspaceArchive(workspaces: values, sessions: records)
        try result.validate(); return result
    }
    public func restoreMetadata(_ archive: WorkspaceArchive) throws {
        try archive.validate()
        guard sessions.isEmpty else { throw TerminalFailure("请先关闭当前会话再恢复布局 / Close current sessions before restoring layout") }
        var restored: [String: ConsoleWorkspace] = [:]
        for saved in archive.workspaces {
            let state = ConsoleWorkspace()
            state.agentMode = saved.agentMode; state.providerID = saved.providerID; state.nativeExpanded = saved.nativeExpanded
            state.terminalLayout = saved.terminalLayout; state.agentLayout = saved.agentLayout
            state.terminalSelection = saved.terminalLayout.focused; state.agentSelection = saved.agentLayout.focused
            restored[saved.root] = state
        }
        workspaces = restored; objectWillChange.send()
    }
    public func replaceRestoredReference(_ old: UUID, with session: TerminalSession) throws {
        let state = workspace(session.worktree)
        var terminal = state.terminalLayout, agent = state.agentLayout
        if terminal.sessions.contains(old) { try terminal.replaceReference(old, with: session.id) }
        if agent.sessions.contains(old) { try agent.replaceReference(old, with: session.id) }
        state.terminalLayout = terminal; state.agentLayout = agent
        if state.terminalSelection == old { state.terminalSelection = session.id }
        if state.agentSelection == old { state.agentSelection = session.id }
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
