import BerthAgent
import Foundation

@MainActor
extension AgentChecks {
    static func agentRowProjectorChecks() throws {
        let id = UUID()
        let rows = [
            AgentRow(id: id, provider: "codex", transport: .stream, title: "A", status: .needsYou,
                     activity: nil, since: nil, unread: false, isTarget: true, queuedCount: 0),
            AgentRow(id: UUID(), provider: "codex", transport: .stream, title: "B", status: .working,
                     activity: nil, since: nil, unread: false, isTarget: false, queuedCount: 1),
            AgentRow(id: UUID(), provider: "codex", transport: .stream, title: "C", status: .completed,
                     activity: nil, since: nil, unread: true, isTarget: false, queuedCount: 0),
        ]
        let counts = AgentRowProjector.aggregateCounts(rows)
        try check(counts.needsYou == 1 && counts.working == 1, "aggregate header counts")
    }
}
