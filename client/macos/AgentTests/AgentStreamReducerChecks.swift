import BerthAgent
import Foundation

@MainActor
extension AgentChecks {
    static func agentStreamReducerChecks() throws {
        let fixtureURL = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .appendingPathComponent("engine/internal/agentstream/testdata/codex-turn.ndjson")
        let text = try String(contentsOf: fixtureURL, encoding: .utf8)
        let lines = text.split(separator: "\n", omittingEmptySubsequences: true).map(String.init)
        let state = AgentStreamReducer.replay(lines)
        try check(state.phase == .ready, "fixture should reach ready")
        try check(state.turns.count == 1, "fixture should have one turn")
        try check(state.turns[0].outcome == .completed, "turn should complete")
        try check(state.requests.isEmpty, "requests should close")
        try check(state.reference?.sessionID == "thr_123", "session id captured")
        let status = AgentDisplayDeriver.status(phase: state.phase, turns: state.turns, requests: state.requests)
        try check(status == .completed, "display status completed")

        var heavy = AgentStreamSnapshot()
        for index in 0..<60 {
            heavy = AgentStreamReducer.apply(
                #"{"v":1,"type":"turn.started","turn":"t\#(index)","input":"x"}"#, to: heavy)
        }
        try check(heavy.turns.count == AgentStreamReducer.maxTurns && heavy.turnCapDropped, "turn cap enforced")

        let unsupported = AgentStreamReducer.apply(
            #"{"v":1,"type":"error","id":"c9","code":"unsupported","message":"no machine protocol"}"#,
            to: AgentStreamSnapshot(phase: .starting))
        try check(unsupported.bridgeErrorCode == "unsupported", "unsupported code stored")
        try check(unsupported.phase == .ended(.providerExit(code: 1)), "unsupported ends session")
    }
}
