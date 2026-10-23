import Foundation

public struct AgentStreamSession: Identifiable, Equatable, Sendable {
    public let id: UUID
    public let provider: String
    public let title: String
    /// Canonical worktree root this structured session belongs to (survives UI remount).
    public let worktree: String
    public var snapshot: AgentStreamSnapshot
    public var process: AgentStreamProcess

    public init(id: UUID = UUID(), provider: String, title: String, worktree: String = "",
                snapshot: AgentStreamSnapshot = AgentStreamSnapshot(), process: AgentStreamProcess) {
        self.id = id
        self.provider = provider
        self.title = title
        self.worktree = worktree
        self.snapshot = snapshot
        self.process = process
    }

    public static func == (lhs: AgentStreamSession, rhs: AgentStreamSession) -> Bool {
        lhs.id == rhs.id && lhs.provider == rhs.provider && lhs.title == rhs.title
            && lhs.worktree == rhs.worktree && lhs.snapshot == rhs.snapshot
    }
}

/// Registry for structured agent bridges. Slot budget is enforced by the console alongside PTY sessions.
public final class AgentStreams: @unchecked Sendable {
    public static let maxSessions = 16
    private let lock = NSLock()
    private var sessions: [UUID: AgentStreamSession] = [:]

    public init() {}

    public var all: [AgentStreamSession] {
        lock.lock()
        defer { lock.unlock() }
        return Array(sessions.values)
    }

    public func count() -> Int {
        lock.lock()
        defer { lock.unlock() }
        return sessions.count
    }

    public func session(_ id: UUID) -> AgentStreamSession? {
        lock.lock()
        defer { lock.unlock() }
        return sessions[id]
    }

    @discardableResult
    public func register(_ session: AgentStreamSession) throws -> AgentStreamSession {
        lock.lock()
        defer { lock.unlock() }
        guard sessions.count < Self.maxSessions else {
            throw AgentStreamFailure("结构化 Agent 已达上限 \(Self.maxSessions) / Structured agent limit reached")
        }
        sessions[session.id] = session
        return session
    }

    public func remove(_ id: UUID) {
        lock.lock()
        if let session = sessions.removeValue(forKey: id) {
            session.process.cancel()
        }
        lock.unlock()
    }

    public func drop(_ id: UUID) {
        lock.lock()
        sessions.removeValue(forKey: id)
        lock.unlock()
    }

    public func applyLine(_ id: UUID, line: String) {
        lock.lock()
        guard var session = sessions[id] else { lock.unlock(); return }
        session.snapshot = AgentStreamReducer.apply(line, to: session.snapshot)
        sessions[id] = session
        lock.unlock()
    }

    public func update(_ id: UUID, _ mutate: (inout AgentStreamSession) -> Void) {
        lock.lock()
        guard var session = sessions[id] else { lock.unlock(); return }
        mutate(&session)
        sessions[id] = session
        lock.unlock()
    }
}
