import Foundation

public enum AgentRowProjector {
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
        return AgentRow(id: id, provider: provider, transport: .stream, title: title, status: status,
                        activity: activity, since: since, unread: unread, isTarget: isTarget, queuedCount: queuedCount,
                        userPrompt: userPrompt, resultText: resultText)
    }

    public static func terminalRow(id: UUID, provider: String, title: String, status: AgentDisplayStatus,
                                   activity: String?, unread: Bool, isTarget: Bool,
                                   since: Date? = nil, userPrompt: String? = nil,
                                   resultText: String? = nil) -> AgentRow {
        AgentRow(id: id, provider: provider, transport: .terminal, title: title, status: status,
                 activity: activity, since: since, unread: unread, isTarget: isTarget, queuedCount: 0,
                 userPrompt: userPrompt,
                 resultText: resultText ?? activity)
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
        for row in rows {
            switch row.status {
            case .needsYou: needsYou += 1
            case .working, .starting: working += 1
            default: break
            }
        }
        return (needsYou, working)
    }
}
