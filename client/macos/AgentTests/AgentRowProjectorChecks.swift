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

        let turn = AgentTurn(id: "t1", input: "hi", sentAt: Date(timeIntervalSince1970: 1_700_000_000),
                             outcome: .completed,
                             items: [.message(id: "m1", text: "hello from agent\nsecond line")],
                             endedAt: Date(timeIntervalSince1970: 1_700_000_030))
        let turn2 = AgentTurn(id: "t2", input: "second", sentAt: Date(timeIntervalSince1970: 1_700_000_040),
                              outcome: .running, items: [])
        let snapshot = AgentStreamSnapshot(phase: .ready, turns: [turn], requests: [])
        let row = AgentRowProjector.streamRow(id: id, provider: "pi", title: "Pi 1 · 流",
                                              snapshot: snapshot, unread: false, isTarget: true,
                                              queuedCount: 0, now: Date(timeIntervalSince1970: 1_700_000_060))
        try check(row.userPrompt == "hi", "stream row carries user prompt")
        try check(row.resultText?.contains("hello from agent") == true, "stream row carries result text")
        try check(row.status == .completed, "completed turn status")
        try check(row.sessionID == id, "stream row sessionID matches session")

        let preview = AgentDisplayDeriver.resultPreview(turn: turn, activity: "ignored", requests: [])
        try check(preview?.hasPrefix("hello from agent") == true, "resultPreview prefers message body")

        let multi = AgentStreamSnapshot(phase: .ready, turns: [turn, turn2], requests: [])
        let blocks = AgentRowProjector.streamTurnBlocks(
            id: id, provider: "pi", title: "Pi 1 · 流", snapshot: multi, unread: false, isTarget: true,
            queued: ["queued hello"], now: Date(timeIntervalSince1970: 1_700_000_080))
        try check(blocks.count == 3, "one card per turn + queued")
        try check(blocks[0].userPrompt == "hi" && blocks[0].status == .completed, "first turn completed")
        try check(blocks[1].userPrompt == "second" && blocks[1].status == .working, "second turn running")
        try check(blocks[2].status == .queued && blocks[2].userPrompt == "queued hello", "queued card")
        try check(Set(blocks.map(\.id)).count == 3, "turn cards have unique ids")
        try check(blocks.allSatisfy { $0.sessionID == id }, "turn cards share sessionID")

        let terminal = AgentRowProjector.terminalRow(
            id: id, provider: "opencode", title: "OpenCode 3", status: .working,
            activity: "终端", unread: false, isTarget: true,
            since: Date(timeIntervalSince1970: 1_700_000_100),
            userPrompt: "hi", resultText: "thinking…")
        try check(terminal.userPrompt == "hi", "terminal row carries user prompt")
        try check(terminal.resultText == "thinking…", "terminal row carries result text")
        try check(terminal.since != nil, "terminal row carries since")
        try check(AgentRowProjector.terminalStatus(isActive: true, needsAttention: false,
                                                   isTaskAgent: false, taskRunning: false) == .working,
                  "native interactive status is working")
        try check(AgentRowProjector.terminalStatus(isActive: false, needsAttention: false,
                                                   isTaskAgent: false, taskRunning: false) == .ended(resumable: false),
                  "exited native status is ended")
    }
}
