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
        try layoutChecks()
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
        let original = "export OBERTH_TEST_PROFILE=retained\nPS1='fixture> '\n"
        try original.write(to: profile, atomically: true, encoding: .utf8)
        let env = ["HOME": root.path, "PATH":"/usr/bin:/bin", "SHELL":"/bin/zsh"]
        let session = TerminalSession(worktree: root.path)
        try session.start(executable: "/bin/zsh", arguments: ["-i"], environment: env, shellIntegration: true)
        defer { if session.isActive { session.stop(force: true); eventually("fixture cleanup") { !session.isActive } } }
        // The shell sends its own prompt before input. This is native PTY input,
        // not a command runner guessing prompt strings from rendered output.
        pump(0.3)
        let command = Array("false\r".utf8)
        session.terminal.send(source: session.terminal, data: command[...])
        eventually("zsh command block never completed") { session.commandBlocks.last?.exitCode == 1 }
        require(session.commandBlocks.last?.command == "false", "preexec text missing")
        session.terminal.send(source: session.terminal, data: Array("[[ $OBERTH_TEST_PROFILE == retained ]]\r".utf8)[...])
        eventually("original profile not sourced") { session.commandBlocks.count >= 2 && session.commandBlocks.last?.exitCode == 0 }
        session.terminal.send(source: session.terminal, data: Array("exit\r".utf8)[...])
        eventually("integrated shell did not exit") { !session.isActive }
        let after = try String(contentsOf: profile, encoding: .utf8)
        require(after == original, "user profile modified")
        print("PASS: real isolated zsh PTY command/exit markers, original rc retained, native process exit")
    }
}
