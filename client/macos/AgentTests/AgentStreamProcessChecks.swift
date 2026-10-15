import Foundation
import BerthAgent

enum AgentStreamProcessChecks {
    static func run() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("agent-stream-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let fake = root.appendingPathComponent("oberth")
        let script = """
        #!/bin/sh
        [ "$1" = agent ] && [ "$2" = stream ] || exit 2
        while IFS= read -r line; do
          case "$line" in
            *session.close*) echo '{"v":1,"type":"session.ended","reason":"closed","exit_code":0}'; exit 0 ;;
            *turn.start*)
              echo '{"v":1,"type":"session.started","provider":"codex","protocol":"fake","worktree":"/tmp","provider_session_id":"s1","capabilities":[]}'
              echo '{"v":1,"type":"ack","id":"c1"}'
              echo '{"v":1,"type":"turn.started","turn":"t1","input":"hi"}'
              echo '{"v":1,"type":"turn.completed","turn":"t1","outcome":"completed"}'
              ;;
          esac
        done
        """
        try script.write(to: fake, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: fake.path)
        let process = AgentStreamProcess()
        var lines: [String] = []
        process.setHandlers(onLine: { lines.append($0) }, onEnd: { _ in })
        try process.start(binary: fake.path, arguments: ExternalAgent.streamArguments(provider: "codex", root: root.path),
                          directory: root.path, environment: ProcessInfo.processInfo.environment)
        try process.sendTurnStart(text: "hi")
        Thread.sleep(forTimeInterval: 0.3)
        try process.closeSession()
        _ = process.waitUntilExit()
        let body = lines.joined(separator: "\n")
        guard body.contains(#""type":"session.started"#), body.contains(#""type":"turn.completed"#) else {
            throw FailedCheck(detail: "missing stream events: \(body)")
        }
        let registry = AgentStreams()
        let session = AgentStreamSession(provider: "codex", title: "Codex 1", process: AgentStreamProcess())
        try registry.register(session)
        registry.applyLine(session.id, line: lines.first { $0.contains("session.started") } ?? "")
        guard registry.session(session.id)?.snapshot.phase == .ready else {
            throw FailedCheck(detail: "reducer did not reach ready")
        }
        print("PASS: AgentStreamProcess framing and AgentStreams registry")
    }
}
