import Foundation

@main enum AgentResumeTests {
    static func main() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("resume-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: false)
        defer { try? FileManager.default.removeItem(at: root) }
        let file = root.appendingPathComponent("session.jsonl")
        try Data("fixture".utf8).write(to: file)
        let doc: [String: Any] = ["provider":"codex","executable":"/bin/sh","arguments":["resume","12345678-1234-4123-8123-123456789abc"],
            "worktree":root.path,"mode":"native", "resume":["session_id":"12345678-1234-4123-8123-123456789abc","source_version":"0.42.0",
                "source":file.path,"worktree":root.path,"approval_owner":"provider_native"]]
        let plan = try JSONDecoder().decode(AgentLaunchPlan.self, from: JSONSerialization.data(withJSONObject: doc))
        try plan.validate(provider: "codex", root: root.path, mode: "native", resumeFile: file.path)
        var refused = 0
        let alternatives: [String?] = [nil, root.appendingPathComponent("missing").path]
        for other in alternatives {
            do { try plan.validate(provider: "codex", root: root.path, mode: "native", resumeFile: other) }
            catch { refused += 1 }
        }
        guard refused == 2 else { fatalError("unexpected or mismatched resume identity accepted") }
        let olderCatalogue = try JSONDecoder().decode(ExternalAgent.self, from: Data(#"{"id":"codex","name":"Codex","command":"codex","installed":true,"native_prompt":true}"#.utf8))
        guard !olderCatalogue.supportsResume else { fatalError("older planner inferred unsupported resume capability") }
        print("PASS: exact resume source, no unsolicited continuation, old catalogue fails closed")
    }
}
