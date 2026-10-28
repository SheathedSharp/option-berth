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
        try shutdownChecks(root: root)
    }

    /// `requestShutdown` exists so ending a session cannot freeze the window.
    /// Two things must hold: the call returns immediately, and the bridge is
    /// still reclaimed. The fixture ignores `session.close` on purpose, so the
    /// only way out is the bounded SIGKILL escalation.
    private static func shutdownChecks(root: URL) throws {
        let stubborn = root.appendingPathComponent("stubborn-bridge")
        let script = """
        #!/bin/sh
        # Never answers session.close; sits in a read loop until killed.
        while IFS= read -r line; do :; done
        """
        try script.write(to: stubborn, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: stubborn.path)

        let process = AgentStreamProcess()
        var ended = false
        process.setHandlers(onLine: { _ in }, onEnd: { _ in ended = true })
        try process.start(binary: stubborn.path, arguments: ["agent", "stream", "codex"],
                          directory: root.path, environment: ProcessInfo.processInfo.environment)
        guard process.isRunning else { throw FailedCheck(detail: "stubborn bridge did not start") }

        var completed = false
        let began = Date()
        process.requestShutdown(graceSeconds: 0.3) { completed = true }
        let callDuration = Date().timeIntervalSince(began)
        guard callDuration < 0.1 else {
            throw FailedCheck(detail: "requestShutdown blocked its caller for \(callDuration)s")
        }

        // Reclamation is observed: the completion only fires after the child is
        // reaped. Give the grace window plus slack, but bound it.
        let deadline = Date().addingTimeInterval(6)
        while !completed, Date() < deadline {
            RunLoop.current.run(until: Date().addingTimeInterval(0.02))
        }
        guard completed else {
            throw FailedCheck(detail: "stubborn bridge was never reclaimed (call took \(callDuration)s)")
        }
        guard !process.isRunning else { throw FailedCheck(detail: "process still reports running after reclaim") }
        guard ended else { throw FailedCheck(detail: "onEnd never fired for the reclaimed bridge") }
        print("PASS: requestShutdown returns immediately and still reclaims a non-cooperative bridge")
    }
}
