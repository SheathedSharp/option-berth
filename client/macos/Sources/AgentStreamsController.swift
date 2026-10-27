import BerthAgent
import BerthTerminal
import Foundation

/// Starts and tracks `oberth agent stream` children for the console workbench.
/// App-wide singleton so switching worktrees (WorkspaceConsole `.id(root)` remount)
/// does not drop live structured sessions or their projected agent-session cards.
@MainActor
final class AgentStreamsController: ObservableObject {
    static let shared = AgentStreamsController()

    /// First-turn prompt for native Agent PTY cards (persists across worktree remount).
    struct NativeCardMeta: Equatable {
        var prompt: String
        var since: Date
    }

    @Published private(set) var sessions: [AgentStreamSession] = []
    @Published private(set) var nativeCardMeta: [UUID: NativeCardMeta] = [:]
    private let registry = AgentStreams()
    private var lineHooks: [UUID: AgentStreamProcess] = [:]
    private var queuedText: [UUID: [String]] = [:]
    private var unreadIDs: Set<UUID> = []
    private var readDetailID: UUID?
    /// Resume file passed at launch before `session.started` provides `reference`.
    private var launchResumeFileByID: [UUID: String] = [:]
    /// Last structured session that reported `unsupported`; cleared on next launch.
    @Published private(set) var unsupportedNotice: (sessionID: UUID, message: String)?

    /// Frame coalescing for the stream path.
    ///
    /// **Why not publish per line.** `item.updated` arrives per token delta, so
    /// the bridge emits hundreds of lines a second while a model answers. Each
    /// line used to run `publish()`, which re-sorts every session and sends
    /// `objectWillChange` to the whole console — the console then re-projects
    /// every card (`agentStreamBlocks`, `structuredAgentRows`) several times per
    /// body. That is the same per-event amplification as the PTY byte path, and
    /// it is why a long agent answer made the window unresponsive.
    ///
    /// Facts still land in the registry immediately; only the `@Published`
    /// hand-off to SwiftUI waits for the tick.
    private lazy var publishCoalescer = FrameCoalescer { [weak self] in
        self?.publish()
    }

    private init() {}

    private func canonical(_ root: String) -> String {
        guard root.hasPrefix("/"), !root.utf8.contains(0) else { return "" }
        return URL(fileURLWithPath: root).standardizedFileURL.resolvingSymlinksInPath().path
    }

    /// Structured sessions belonging to this worktree (for block-stream projection).
    func sessions(inWorktree root: String) -> [AgentStreamSession] {
        let key = canonical(root)
        guard !key.isEmpty else { return [] }
        return sessions.filter { canonical($0.worktree) == key }
    }

    func setNativeCardMeta(id: UUID, prompt: String, since: Date = Date()) {
        var next = nativeCardMeta
        next[id] = NativeCardMeta(prompt: prompt, since: since)
        nativeCardMeta = next
    }

    func clearNativeCardMeta(id: UUID) {
        var next = nativeCardMeta
        next.removeValue(forKey: id)
        nativeCardMeta = next
    }

    private func sessionFileBindings() -> [AgentSessionFileGuard.Binding] {
        registry.all.map { session in
            let file = session.snapshot.reference?.sessionFile ?? launchResumeFileByID[session.id]
            return AgentSessionFileGuard.Binding(sessionID: session.id, sessionFile: file,
                                                 active: lineHooks[session.id] != nil)
        }
    }

    /// Running structured session already bound to this provider session file, if any.
    func runningSession(usingSessionFile path: String, excluding: UUID? = nil) -> UUID? {
        AgentSessionFileGuard.runningHolder(among: sessionFileBindings(), for: path, excluding: excluding)
    }

    func sessionFileConflictMessage(for path: String, excluding: UUID? = nil) -> String? {
        guard runningSession(usingSessionFile: path, excluding: excluding) != nil else { return nil }
        return "此会话文件正被另一运行中的结构化会话使用 / Session file is in use by another active structured session"
    }

    func slotAvailable(agentPTYCount: Int) -> Bool {
        let global = TerminalSessions.shared.sessions.count + AgentStreamRegistry.all.count
        return global < AgentStreams.maxSessions
    }

    func session(_ id: UUID) -> AgentStreamSession? {
        registry.session(id)
    }

    func queuedCount(for id: UUID) -> Int {
        queuedText[id]?.count ?? 0
    }

    func queuedMessages(for id: UUID) -> [String] {
        queuedText[id] ?? []
    }

    /// Removes the first queued message and returns it for restoring the composer draft.
    @discardableResult
    func withdrawQueued(id: UUID) -> String? {
        guard var queue = queuedText[id], !queue.isEmpty else { return nil }
        let text = queue.removeFirst()
        queuedText[id] = queue.isEmpty ? nil : queue
        publishNow()
        return text
    }

    /// Returns queued turns joined for restoring the composer when a session ends.
    func drainQueuedText(id: UUID) -> String? {
        guard let queue = queuedText[id], !queue.isEmpty else { return nil }
        queuedText.removeValue(forKey: id)
        publishNow()
        return queue.joined(separator: "\n\n")
    }

    func isUnread(_ id: UUID) -> Bool {
        unreadIDs.contains(id)
    }

    func markDetailVisible(_ id: UUID?) {
        readDetailID = id
        if let id {
            unreadIDs.remove(id)
            ConsoleAgentAlerts.shared.detailBecameVisible(id)
        }
    }

    func launch(binary: String, provider: ExternalAgent, root: String, title: String,
                environment: [String: String], firstTurn: String?,
                resumeFile: String? = nil, reuseID: UUID? = nil) throws -> AgentStreamSession {
        guard provider.supportsStream else {
            throw AgentStreamFailure("此 Agent 不支持结构化流 / Provider does not support structured stream")
        }
        if let resumeFile, let message = sessionFileConflictMessage(for: resumeFile, excluding: reuseID) {
            throw AgentStreamFailure(message)
        }
        let process = AgentStreamProcess()
        unsupportedNotice = nil
        let sessionID = reuseID ?? UUID()
        if reuseID == nil {
            var snapshot = AgentStreamSnapshot()
            snapshot.phase = .starting
            let session = AgentStreamSession(id: sessionID, provider: provider.id, title: title,
                                             worktree: canonical(root), snapshot: snapshot, process: process)
            _ = try registry.register(session)
        } else {
            registry.update(sessionID) { session in
                session.process = process
                session.snapshot.phase = .starting
            }
        }
        wireProcess(process, sessionID: sessionID)
        if let resumeFile { launchResumeFileByID[sessionID] = resumeFile }
        let args = ExternalAgent.streamArguments(provider: provider.id, root: root, resumeFile: resumeFile)
        try process.start(binary: binary, arguments: args, directory: root, environment: environment)
        lineHooks[sessionID] = process
        AgentStreamRegistry.track(id: sessionID, worktree: root, title: title, process: process)
        publishNow()
        if let firstTurn, !firstTurn.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            try sendTurn(id: sessionID, text: firstTurn)
        }
        return registry.session(sessionID)!
    }

    func sendTurn(id: UUID, text: String) throws {
        guard let process = lineHooks[id] else {
            if case .ended(let reason) = registry.session(id)?.snapshot.phase, reason == .closed {
                throw AgentStreamFailure("会话已结束，请续接后再发送 / Session ended; resume before sending")
            }
            throw AgentStreamFailure("结构化会话未在运行 / Structured session is not running")
        }
        try process.sendTurnStart(text: text)
    }

    func queue(id: UUID, text: String) {
        var queue = queuedText[id] ?? []
        if queue.count < 8 { queue.append(text) }
        queuedText[id] = queue
        publishNow()
    }

    func respond(id: UUID, requestID: String, decision: AgentDecision) throws {
        guard let process = lineHooks[id] else {
            throw AgentStreamFailure("结构化会话未在运行 / Structured session is not running")
        }
        registry.update(id) { session in
            if let index = session.snapshot.requests.firstIndex(where: { $0.id == requestID }) {
                session.snapshot.requests[index].answering = decision
            }
        }
        try process.sendRequestRespond(requestID: requestID, decision: decision)
        publishNow()
    }

    func interrupt(id: UUID) throws {
        guard let process = lineHooks[id] else {
            throw AgentStreamFailure("没有可中断的轮次 / No turn to interrupt")
        }
        try process.sendInterrupt()
    }

    func end(_ id: UUID) {
        launchResumeFileByID.removeValue(forKey: id)
        queuedText.removeValue(forKey: id)
        unreadIDs.remove(id)
        clearNativeCardMeta(id: id)
        if let process = lineHooks[id] {
            try? process.closeSession()
            _ = process.waitUntilExit()
        }
        registry.remove(id)
        lineHooks.removeValue(forKey: id)
        AgentStreamRegistry.untrack(id: id)
        publishNow()
    }

    func dropSession(_ id: UUID) {
        launchResumeFileByID.removeValue(forKey: id)
        queuedText.removeValue(forKey: id)
        unreadIDs.remove(id)
        clearNativeCardMeta(id: id)
        lineHooks.removeValue(forKey: id)
        registry.drop(id)
        publishNow()
    }

    func resumeEndedSession(binary: String, provider: ExternalAgent, root: String, id: UUID,
                            environment: [String: String], firstTurn: String?) throws -> AgentStreamSession {
        guard let existing = registry.session(id) else {
            throw AgentStreamFailure("找不到结构化会话 / Structured session not found")
        }
        guard case .ended = existing.snapshot.phase else {
            throw AgentStreamFailure("会话仍在运行 / Session is still active")
        }
        guard let resumeFile = existing.snapshot.reference?.sessionFile else {
            throw AgentStreamFailure("此会话没有可续接的文件 / No session file to resume")
        }
        return try launch(binary: binary, provider: provider, root: root, title: existing.title,
                          environment: environment, firstTurn: firstTurn, resumeFile: resumeFile, reuseID: id)
    }

    private func wireProcess(_ process: AgentStreamProcess, sessionID: UUID) {
        process.setHandlers(onLine: { [weak self] line in
            Task { @MainActor in
                guard let self else { return }
                self.registry.applyLine(sessionID, line: line)
                // Cheap pre-filter: only the two notification types are worth
                // decoding a second time. Everything else already went through
                // the reducer above, so re-parsing each token delta here was
                // pure repeated work on the hot path.
                if line.contains("\"turn.completed\"") || line.contains("\"request.opened\"") {
                    self.noteUnreadIfNeeded(sessionID: sessionID, line: line)
                }
                self.noteUnsupportedIfNeeded(sessionID: sessionID)
                self.drainQueueIfIdle(sessionID: sessionID)
                self.publishCoalescer.signal()
            }
        }, onEnd: { [weak self] _ in
            Task { @MainActor in
                self?.launchResumeFileByID.removeValue(forKey: sessionID)
                self?.lineHooks.removeValue(forKey: sessionID)
                AgentStreamRegistry.untrack(id: sessionID)
                self?.publishCoalescer.signal()
            }
        })
    }

    private func noteUnsupportedIfNeeded(sessionID: UUID) {
        guard let snapshot = registry.session(sessionID)?.snapshot,
              snapshot.bridgeErrorCode == "unsupported" else { return }
        let message = snapshot.bridgeErrorMessage ?? "此版本不支持结构化协议"
        unsupportedNotice = (sessionID, message)
        queuedText.removeValue(forKey: sessionID)
        if let process = lineHooks[sessionID] {
            try? process.closeSession()
            _ = process.waitUntilExit()
        }
        lineHooks.removeValue(forKey: sessionID)
    }

    private func noteUnreadIfNeeded(sessionID: UUID, line: String) {
        guard readDetailID != sessionID,
              let data = line.data(using: .utf8),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let type = object["type"] as? String else { return }
        switch type {
        case "turn.completed", "request.opened":
            unreadIDs.insert(sessionID)
            let title = registry.session(sessionID)?.title ?? "Agent"
            ConsoleAgentAlerts.shared.streamEvent(sessionID: sessionID, title: title, line: line,
                                                   detailVisible: readDetailID == sessionID)
        default:
            break
        }
    }

    private func drainQueueIfIdle(sessionID: UUID) {
        guard let snapshot = registry.session(sessionID)?.snapshot else { return }
        let state = AgentDisplayDeriver.targetState(phase: snapshot.phase, turns: snapshot.turns, requests: snapshot.requests)
        guard state == .idle, var queue = queuedText[sessionID], !queue.isEmpty else { return }
        let text = queue.removeFirst()
        queuedText[sessionID] = queue.isEmpty ? nil : queue
        try? sendTurn(id: sessionID, text: text)
    }

    private func publish() {
        sessions = registry.all.sorted { $0.title.localizedStandardCompare($1.title) == .orderedAscending }
    }

    /// Publish immediately, dropping any scheduled tick so the same facts are
    /// not announced twice. Every non-hot path (launch, queue edits, end, drop,
    /// respond) goes through here: those are user actions whose result the
    /// person is waiting to see, and each is far too rare to need coalescing.
    private func publishNow() {
        publishCoalescer.flushNow()
    }
}
