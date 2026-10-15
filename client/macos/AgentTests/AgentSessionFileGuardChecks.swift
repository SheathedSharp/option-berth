import BerthAgent
import Foundation

@MainActor
extension AgentChecks {
    static func agentSessionFileGuardChecks() throws {
        let fileA = "/tmp/wt/fake-session.jsonl"
        let fileB = "/tmp/wt/other.jsonl"
        let id1 = UUID()
        let id2 = UUID()
        try check(
            AgentSessionFileGuard.canonicalSessionFilePath("/tmp/wt/../wt/fake-session.jsonl")
                == AgentSessionFileGuard.canonicalSessionFilePath(fileA),
            "session file paths should canonicalize")
        let bindings = [
            AgentSessionFileGuard.Binding(sessionID: id1, sessionFile: fileA, active: true),
            AgentSessionFileGuard.Binding(sessionID: id2, sessionFile: fileB, active: false),
        ]
        try check(AgentSessionFileGuard.runningHolder(among: bindings, for: fileA) == id1,
                  "active binding should block the same file")
        try check(AgentSessionFileGuard.runningHolder(among: bindings, for: fileA, excluding: id1) == nil,
                  "excluding holder should allow resume on the same id")
        try check(AgentSessionFileGuard.runningHolder(among: bindings, for: fileB) == nil,
                  "inactive binding should not block")
        try check(AgentSessionFileGuard.runningHolder(among: bindings, for: "/missing.jsonl") == nil,
                  "unknown file should not conflict")
    }
}
