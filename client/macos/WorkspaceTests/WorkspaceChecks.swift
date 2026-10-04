import Foundation
import AppKit
import BerthTerminal

@main enum WorkspaceChecks {
    static func require(_ value: @autoclosure () -> Bool, _ message: String) { if !value() { fatalError(message) } }
    @MainActor static func pump(_ seconds: TimeInterval = 0.02) { RunLoop.main.run(until: Date().addingTimeInterval(seconds)) }
    @MainActor static func eventually(_ message: String, _ test: () -> Bool) {
        let until = Date().addingTimeInterval(5)
        while !test(), Date() < until { pump() }
        require(test(), message)
    }
    @MainActor static func main() throws {
        parserChecks()
        try shellChecks()
    }
    static func parserChecks() {
        let n = "nonce"
        let wire = "\u{1b}]633;E;printf \\x3b中文;\(n)\u{7}\u{1b}]133;C;\(n)\u{1b}\\output\u{1b}]133;D;7;\(n)\u{7}"
        for chunkSize in 1...32 {
            var parser = CommandBlockParser(nonce: n)
            let bytes = Array(wire.utf8)
            for start in stride(from: 0, to: bytes.count, by: chunkSize) { parser.consume(bytes[start..<min(start + chunkSize, bytes.count)]) }
            require(parser.blocks.count == 1 && parser.blocks[0].exitCode == 7, "OSC framing failed")
            require(parser.blocks[0].command == "printf ;中文", "escaped Unicode command lost")
        }
        var parser = CommandBlockParser(nonce: n)
        func send(_ text: String) { parser.consume(Array(text.utf8)[...]) }
        send("\u{1b}]133;C;foreign\u{7}\u{1b}]133;D;0;foreign\u{7}")
        require(parser.blocks.isEmpty, "foreign shell markers accepted")
        for _ in 0..<300 { send("\u{1b}]133;C;nonce\u{7}\u{1b}]133;D;0;nonce\u{7}") }
        require(parser.blocks.count == 256, "history unbounded")
        send("\u{1b}]133;C;nonce\u{7}\u{1b}]133;D;bad;nonce\u{7}")
        require(parser.blocks.last?.exitCode == nil && parser.blocks.last?.interrupted == true, "unknown exit became success")
        send("\u{1b}]633;E;" + String(repeating: "x", count: 20000) + ";nonce\u{7}\u{1b}]133;C;nonce\u{7}")
        require(parser.blocks.last?.command == nil, "oversized OSC leaked stale command")
        parser.clear(); require(parser.blocks.isEmpty, "clear failed")
        print("PASS: 32 chunk boundaries, OSC BEL/ST, foreign nonce, bounded history/frame, unknown exit")
    }
    @MainActor static func shellChecks() throws {
        let fm = FileManager.default
        let root = fm.temporaryDirectory.appendingPathComponent("shell-check-" + UUID().uuidString)
        try fm.createDirectory(at: root, withIntermediateDirectories: false)
        defer { try? fm.removeItem(at: root) }
        let profile = root.appendingPathComponent(".zshrc")
        let original = """
        export OBERTH_TEST_PROFILE=retained
        PS1='fixture> '
        __fixture_prompt_observer() {
          local result=$?
          print -r -- "$result" > "$HOME/prompt-status"
          return 0
        }
        precmd_functions+=(__fixture_prompt_observer)
        """ + "\n"
        try original.write(to: profile, atomically: true, encoding: .utf8)
        let env = ["HOME": root.path, "PATH":"/usr/bin:/bin", "SHELL":"/bin/zsh", "TMOUT":"15"]
        let session = TerminalSession(worktree: root.path)
        try session.start(executable: "/bin/zsh", arguments: ["-i"], environment: env, shellIntegration: true)
        defer { if session.isActive { session.stop(force: true); eventually("fixture cleanup") { !session.isActive } } }
        let promptStatus = root.appendingPathComponent("prompt-status")
        eventually("original prompt hook never ran at startup") { fm.fileExists(atPath: promptStatus.path) }
        let command = Array("false\r".utf8)
        session.terminal.send(source: session.terminal, data: command[...])
        eventually("zsh command block never completed") { session.commandBlocks.last?.exitCode == 1 }
        require(session.commandBlocks.last?.command == "false", "preexec text missing")
        eventually("failed command suppressed a later precmd hook or changed its input status") {
            (try? String(contentsOf: promptStatus, encoding: .utf8)) == "1\n"
        }
        session.terminal.send(source: session.terminal, data: Array("[[ $OBERTH_TEST_PROFILE == retained ]]\r".utf8)[...])
        eventually("original profile not sourced") { session.commandBlocks.count >= 2 && session.commandBlocks.last?.exitCode == 0 }
        eventually("successful command did not preserve the later prompt hook") {
            (try? String(contentsOf: promptStatus, encoding: .utf8)) == "0\n"
        }
        session.terminal.send(source: session.terminal, data: Array("exit\r".utf8)[...])
        eventually("integrated shell did not exit") { !session.isActive }
        let after = try String(contentsOf: profile, encoding: .utf8)
        require(after == original, "user profile modified")
        print("PASS: real isolated zsh PTY command/exit markers, existing prompt hooks after success/failure, original rc retained, native process exit")
    }
}
