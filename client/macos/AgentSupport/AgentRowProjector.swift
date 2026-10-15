import Foundation

public enum AgentRowProjector {
    public static func streamRow(id: UUID, provider: String, title: String, snapshot: AgentStreamSnapshot,
                                 unread: Bool, isTarget: Bool, queuedCount: Int, now: Date) -> AgentRow {
        let status = AgentDisplayDeriver.status(phase: snapshot.phase, turns: snapshot.turns, requests: snapshot.requests)
        let activity = AgentDisplayDeriver.activity(phase: snapshot.phase, turns: snapshot.turns,
                                                    requests: snapshot.requests, now: now)
        let since = snapshot.turns.last?.endedAt ?? snapshot.turns.last?.sentAt
        return AgentRow(id: id, provider: provider, transport: .stream, title: title, status: status,
                        activity: activity, since: since, unread: unread, isTarget: isTarget, queuedCount: queuedCount)
    }

    public static func terminalRow(id: UUID, provider: String, title: String, status: AgentDisplayStatus,
                                   activity: String?, unread: Bool, isTarget: Bool) -> AgentRow {
        AgentRow(id: id, provider: provider, transport: .terminal, title: title, status: status,
                 activity: activity, since: nil, unread: unread, isTarget: isTarget, queuedCount: 0)
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
