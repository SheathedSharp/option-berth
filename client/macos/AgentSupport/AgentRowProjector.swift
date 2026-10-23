import Foundation

public enum AgentRowProjector {
    /// Session-level row for status column / nav chips (one per session).
    public static func streamRow(id: UUID, provider: String, title: String, snapshot: AgentStreamSnapshot,
                                 unread: Bool, isTarget: Bool, queuedCount: Int, now: Date) -> AgentRow {
        let status = AgentDisplayDeriver.status(phase: snapshot.phase, turns: snapshot.turns, requests: snapshot.requests)
        let activity = AgentDisplayDeriver.activity(phase: snapshot.phase, turns: snapshot.turns,
                                                    requests: snapshot.requests, now: now)
        let latest = snapshot.turns.last
        let since = latest?.endedAt ?? latest?.sentAt
        let userPrompt = latest.flatMap { prompt in
            let trimmed = prompt.input.trimmingCharacters(in: .whitespacesAndNewlines)
            return trimmed.isEmpty ? nil : trimmed
        }
        let resultText = AgentDisplayDeriver.resultPreview(turn: latest, activity: activity, requests: snapshot.requests)
        return AgentRow(id: id, sessionID: id, provider: provider, transport: .stream, title: title, status: status,
                        activity: activity, since: since, unread: unread, isTarget: isTarget, queuedCount: queuedCount,
                        userPrompt: userPrompt, resultText: resultText,
                        exitCode: latest.flatMap(turnExitCode))
    }

    /// One block-stream card per user turn (+ queued prompts). Shell parity: each ⌘↩ is its own card.
    public static func streamTurnBlocks(id: UUID, provider: String, title: String, snapshot: AgentStreamSnapshot,
                                        unread: Bool, isTarget: Bool, queued: [String], now: Date) -> [AgentRow] {
        var rows: [AgentRow] = []
        let requests = snapshot.requests
        for turn in snapshot.turns {
            let status = turnStatus(phase: snapshot.phase, turn: turn, requests: requests)
            let activity = turnActivity(status: status, turn: turn, requests: requests, now: now)
            let prompt = turn.input.trimmingCharacters(in: .whitespacesAndNewlines)
            let resultText = AgentDisplayDeriver.resultPreview(
                turn: turn,
                activity: activity,
                requests: turn.outcome == .running ? requests.filter { $0.turn == turn.id } : [])
            rows.append(AgentRow(
                id: stableCardID(session: id, key: "turn:\(turn.id)"),
                sessionID: id,
                provider: provider,
                transport: .stream,
                title: title,
                status: status,
                activity: activity,
                since: turn.endedAt ?? turn.sentAt,
                unread: unread && turn.id == snapshot.turns.last?.id,
                isTarget: isTarget && turn.id == snapshot.turns.last?.id,
                queuedCount: 0,
                userPrompt: prompt.isEmpty ? nil : prompt,
                resultText: resultText,
                exitCode: turnExitCode(turn)))
        }
        for (index, text) in queued.enumerated() {
            let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !trimmed.isEmpty else { continue }
            rows.append(AgentRow(
                id: stableCardID(session: id, key: "queue:\(index):\(trimmed)"),
                sessionID: id,
                provider: provider,
                transport: .stream,
                title: title,
                status: .queued,
                activity: "排队中",
                since: now,
                unread: false,
                isTarget: false,
                queuedCount: 0,
                userPrompt: trimmed,
                resultText: nil,
                exitCode: nil))
        }
        // Starting with no turns yet — one placeholder so the session is visible in the stream.
        if rows.isEmpty, case .starting = snapshot.phase {
            rows.append(AgentRow(
                id: stableCardID(session: id, key: "starting"),
                sessionID: id,
                provider: provider,
                transport: .stream,
                title: title,
                status: .starting,
                activity: nil,
                since: now,
                unread: false,
                isTarget: isTarget,
                queuedCount: 0,
                userPrompt: nil,
                resultText: nil,
                exitCode: nil))
        }
        return rows
    }

    public static func terminalRow(id: UUID, provider: String, title: String, status: AgentDisplayStatus,
                                   activity: String?, unread: Bool, isTarget: Bool,
                                   since: Date? = nil, userPrompt: String? = nil,
                                   resultText: String? = nil, exitCode: Int? = nil) -> AgentRow {
        AgentRow(id: id, sessionID: id, provider: provider, transport: .terminal, title: title, status: status,
                 activity: activity, since: since, unread: unread, isTarget: isTarget, queuedCount: 0,
                 userPrompt: userPrompt,
                 resultText: resultText ?? activity,
                 exitCode: exitCode)
    }

    /// Status for a native Agent PTY peer card in the unified block stream.
    public static func terminalStatus(isActive: Bool, needsAttention: Bool, isTaskAgent: Bool,
                                      taskRunning: Bool) -> AgentDisplayStatus {
        if !isActive { return .ended(resumable: false) }
        if needsAttention { return .needsYou }
        if isTaskAgent { return taskRunning ? .working : .idle }
        return .working
    }

    public static func aggregateCounts(_ rows: [AgentRow]) -> (needsYou: Int, working: Int) {
        var needsYou = 0
        var working = 0
        var seenSessions = Set<UUID>()
        for row in rows {
            // Session-level chips may pass one row per session; turn blocks may pass many —
            // count each session once for header badges.
            let key = row.sessionID
            if seenSessions.contains(key) { continue }
            seenSessions.insert(key)
            switch row.status {
            case .needsYou: needsYou += 1
            case .working, .starting: working += 1
            default: break
            }
        }
        return (needsYou, working)
    }

    /// Deterministic card UUID from session + key (stable across redraws).
    public static func stableCardID(session: UUID, key: String) -> UUID {
        let seed = Array(session.uuidString.utf8) + [0] + Array(key.utf8)
        var bytes = [UInt8](repeating: 0, count: 16)
        for (index, byte) in seed.enumerated() {
            bytes[index % 16] ^= byte &+ UInt8(index % 251)
        }
        bytes[6] = (bytes[6] & 0x0F) | 0x40
        bytes[8] = (bytes[8] & 0x3F) | 0x80
        return UUID(uuid: (
            bytes[0], bytes[1], bytes[2], bytes[3],
            bytes[4], bytes[5], bytes[6], bytes[7],
            bytes[8], bytes[9], bytes[10], bytes[11],
            bytes[12], bytes[13], bytes[14], bytes[15]
        ))
    }

    private static func turnStatus(phase: StreamPhase, turn: AgentTurn, requests: [AgentRequest]) -> AgentDisplayStatus {
        switch phase {
        case .starting:
            return turn.outcome == .running ? .working : .starting
        case .ending:
            return turn.outcome == .running ? .working : statusFromOutcome(turn.outcome)
        case .ended(let reason):
            if turn.outcome == .running {
                switch reason {
                case .closed: return .ended(resumable: true)
                case .providerExit, .lost: return .lost
                }
            }
            return statusFromOutcome(turn.outcome)
        case .ready:
            if turn.outcome == .running, requests.contains(where: { $0.turn == turn.id }) {
                return .needsYou
            }
            return statusFromOutcome(turn.outcome)
        }
    }

    private static func statusFromOutcome(_ outcome: TurnOutcome) -> AgentDisplayStatus {
        switch outcome {
        case .running: return .working
        case .completed: return .completed
        case .failed: return .failed
        case .interrupted: return .interrupted
        case .lost: return .lost
        }
    }

    private static func turnActivity(status: AgentDisplayStatus, turn: AgentTurn,
                                     requests: [AgentRequest], now: Date) -> String? {
        switch status {
        case .needsYou:
            if let request = requests.first(where: { $0.turn == turn.id }) {
                if let command = request.command { return "等你确认 · \(command)" }
                if let path = request.paths.first { return "等你确认 · \(path)" }
            }
            return "等你确认"
        case .working, .starting:
            return AgentDisplayDeriver.resultPreview(turn: turn, activity: nil, requests: []) ?? "运行中"
        case .queued:
            return "排队中"
        case .completed, .failed, .interrupted, .lost, .ended, .idle:
            return AgentDisplayDeriver.resultPreview(turn: turn, activity: nil, requests: [])
        }
    }

    private static func turnExitCode(_ turn: AgentTurn) -> Int? {
        if case .failed(let code, _) = turn.outcome, let value = Int(code) {
            return value
        }
        // Prefer the last finished command item's exit code when present.
        for item in turn.items.reversed() {
            if case .command(_, _, _, let status, let exitCode, _, _) = item {
                if status == .completed || status == .failed, let exitCode {
                    return exitCode
                }
            }
        }
        return nil
    }
}
