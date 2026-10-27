import AppKit
import BerthTerminal
import Combine
import Foundation

struct CheckFailure: Error { let reason: String }

@main
@MainActor
enum TerminalChecks {
    static func check(_ condition: @autoclosure () -> Bool, _ reason: String) throws {
        if !condition() { throw CheckFailure(reason: reason) }
    }
    static func until(_ condition: () -> Bool, timeout: TimeInterval = 5) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while !condition(), Date() < deadline { RunLoop.current.run(until: Date().addingTimeInterval(0.01)) }
        return condition()
    }

    static func shellCommandCaptureChecks() throws {
        // Tabs expand to column stops (ls / columnar TTY output).
        try check(ShellCommandCapture.expandTabs("a\tb") == "a       b", "tab expand width 8")
        try check(ShellCommandCapture.expandTabs("1234567\tx") == "1234567 x", "tab near stop")
        // NUL cells become spaces so columnar gaps survive strip.
        let withNul = "AGENTS.md\u{0}\u{0}core"
        let cleaned = ShellCommandCapture.finalizeAccumulated(withNul)
        try check(cleaned.contains("AGENTS.md  core") || cleaned.contains("AGENTS.md core"),
                  "NUL gap should become spaces, got: \(cleaned)")
        // Multiple spaces must not be collapsed by finalize itself.
        let spaced = ShellCommandCapture.finalizeAccumulated("AGENTS.md   core   frontend")
        try check(spaced == "AGENTS.md   core   frontend", "preserve interior spaces: \(spaced)")
        print("PASS: ShellCommandCapture tab/NUL/space layout")
    }
    static func main() {
        _ = NSApplication.shared
        var owned: [TerminalSession] = []
        let root = URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent("berth-pty-\(UUID().uuidString)")
        defer {
            for session in owned { session.stop(force: true) }
            _ = until { owned.allSatisfy { !$0.isActive } }
            try? FileManager.default.removeItem(at: root)
        }
        do {
            try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
            try shellCommandCaptureChecks()
            try mixedPaneChecks(root: root)
            try workspaceChecks(root: root)
            try nativeConsoleChecks(root: root)
            let session = TerminalSession(worktree: root.path)
            owned.append(session)
            try check(session.terminal.process == nil && !session.isActive, "construction launched a process")
            try check(session.terminal.clipboardRead(source: session.terminal) == nil, "OSC clipboard read was allowed")
            let canonical = session.worktree
            session.terminal.hostCurrentDirectoryUpdate(source: session.terminal, directory: "/untrusted")
            try check(session.worktree == canonical, "OSC retargeted worktree")
            var output = ""
            session.terminal.onBytes = { output += String(decoding: $0, as: UTF8.self) }
            let literal = "中文 $(touch nope); 'quoted'"
            try session.start(executable: "/bin/sh", arguments: ["-c", "printf '%s\\n' \"$PWD\" \"$1\"; sleep 0.05; exit 7", "fixture", literal], environment: ["HOME": root.path, "PATH": "/usr/bin:/bin"])
            try check(until { !session.isActive }, "PTY child did not exit")
            try check(session.state.contains("7"), "exit status was not decoded")
            try check(output.contains(canonical) && output.contains(literal), "PTY lost cwd or literal argv")
            try check(!FileManager.default.fileExists(atPath: root.appendingPathComponent("nope").path), "prompt was executed as shell text")
            let first = TerminalSession(worktree: root.path)
            let second = TerminalSession(worktree: root.path)
            owned += [first, second]
            try first.start(executable: "/bin/sleep", arguments: ["20"], environment: ["PATH": "/usr/bin:/bin"])
            try second.start(executable: "/bin/sleep", arguments: ["20"], environment: ["PATH": "/usr/bin:/bin"])
            first.stop(force: true)
            try check(until { !first.isActive }, "first child not reaped")
            try check(second.isActive && Darwin.kill(second.terminal.process!.shellPid, 0) == 0, "stopping one session affected another")
            second.stop(force: true)
            try check(until { !second.isActive }, "second child not reaped")
            let keyboard = TerminalSession(worktree: root.path)
            owned.append(keyboard)
            let window = NSWindow(contentRect: NSRect(x: 120, y: 120, width: 760, height: 320),
                                  styleMask: [.titled, .closable, .resizable], backing: .buffered, defer: false)
            window.title = "option-berth · isolated native terminal check"
            window.contentView = keyboard.terminal
            NSApp.setActivationPolicy(.regular)
            NSApp.activate(ignoringOtherApps: true)
            window.makeKeyAndOrderFront(nil)
            window.makeFirstResponder(keyboard.terminal)
            defer { window.close() }
            var keyboardOutput = ""
            keyboard.terminal.onBytes = { keyboardOutput += String(decoding: $0, as: UTF8.self) }
            try keyboard.start(executable: "/bin/sh", arguments: ["-c", "read -r line; printf 'PTY_INPUT:%s\\n' \"$line\"; stty size"], environment: ["HOME": root.path, "PATH": "/usr/bin:/bin"])
            for character in "native-check\r" {
                let value = String(character)
                let event = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [],
                    timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber,
                    context: nil, characters: value, charactersIgnoringModifiers: value,
                    isARepeat: false, keyCode: character == "\r" ? 36 : 0)!
                window.sendEvent(event)
            }
            try check(until { !keyboard.isActive && keyboardOutput.contains("PTY_INPUT:native-check") }, "native window keyboard input was not delivered")
            if CommandLine.arguments.count == 3, CommandLine.arguments[1] == "--snapshot" {
                // CALayer/Metal contents are not captured by cacheDisplay. Capture
                // only this synthetic test window, never the user's desktop.
                RunLoop.current.run(until: Date().addingTimeInterval(0.3))
                let capture = Process()
                capture.executableURL = URL(fileURLWithPath: "/usr/sbin/screencapture")
                capture.arguments = ["-x", "-l", String(window.windowNumber), CommandLine.arguments[2]]
                try capture.run()
                capture.waitUntilExit()
                try check(capture.terminationStatus == 0, "window capture unavailable; PTY checks remain separate")
            }
            let flood = TerminalSession(worktree: root.path)
            owned.append(flood)
            var floodBytes = 0
            var floodTail = ""
            // Chain instead of replace: the session's own byte handler is what
            // records activity, and the assertions below check it still runs.
            let floodSessionHandler = flood.terminal.onBytes
            flood.terminal.onBytes = {
                floodSessionHandler?($0)
                floodBytes += $0.count
                floodTail = String((floodTail + String(decoding: $0, as: UTF8.self)).suffix(128))
            }
            // Publishing regression: byte facts are coalesced to a frame tick, so
            // a 1 MiB flood must produce far fewer notifications than SwiftTerm
            // chunks. Counting the whole registry (not just the session) is the
            // point — that is what used to re-evaluate the window per chunk.
            var floodPublishes = 0
            let floodCancellable = flood.objectWillChange.sink { _ in floodPublishes += 1 }
            defer { floodCancellable.cancel() }
            try flood.start(executable: "/bin/sh", arguments: ["-c", "/usr/bin/yes terminal-row | /usr/bin/head -c 1048576; printf '\\nPTY-FLOOD-END\\n'"], environment: ["PATH": "/usr/bin:/bin"])
            try check(until { !flood.isActive && floodBytes >= 1_048_576 && floodTail.contains("PTY-FLOOD-END") },
                      "bounded large-output stream lost its final bytes or exit")
            // Let the last scheduled frame tick land before reading the count.
            _ = until({ floodPublishes > 0 }, timeout: 1)
            RunLoop.current.run(until: Date().addingTimeInterval(0.1))
            try check(floodPublishes > 0, "coalescing starved the UI of output updates entirely")
            try check(floodPublishes < 400,
                      "output publishing was not coalesced: \(floodPublishes) notifications for 1 MiB")
            // Coalescing must not swallow the facts: the tail still has to carry
            // the final line. The PTY translates \n to \r\n, so match without
            // assuming the marker is the very last byte.
            try check(flood.activity.outputTail.contains("PTY-FLOOD-END"),
                      "coalesced tail lost the final line: \(flood.activity.outputTail.suffix(40))")
            print("PUBLISH CHECK 1 MiB flood -> \(floodPublishes) notifications")
            // Closing sessions must release their PTYs, not accumulate file descriptors.
            let baselineFDs = try FileManager.default.contentsOfDirectory(atPath: "/dev/fd").count
            for _ in 0..<24 {
                // A synchronous test does not return to AppKit's outer event
                // loop. Drain its per-event Cocoa autoreleases explicitly.
                try autoreleasepool {
                    var transient: TerminalSession? = TerminalSession(worktree: root.path)
                    try transient!.start(executable: "/usr/bin/true", arguments: [], environment: ["PATH": "/usr/bin:/bin"])
                    try check(until { transient?.isActive == false }, "short-lived child was not reaped")
                    transient = nil
                }
                RunLoop.current.run(until: Date().addingTimeInterval(0.01))
            }
            print("FD CHECK baseline=\(baselineFDs), observed=\(try FileManager.default.contentsOfDirectory(atPath: "/dev/fd").count)")
            try check(until {
                ((try? FileManager.default.contentsOfDirectory(atPath: "/dev/fd").count) ?? Int.max) <= baselineFDs + 3
            }, "PTY descriptors accumulated after sessions were released")
            // Native text/IME commits use the terminal's own input path. No global clipboard is touched.
            let inputSession = TerminalSession(worktree: root.path)
            owned.append(inputSession)
            var inputOutput = ""
            inputSession.terminal.onBytes = { inputOutput += String(decoding: $0, as: UTF8.self) }
            try inputSession.start(executable: "/bin/sh", arguments: ["-c", "read -r line; printf 'TEXT:%s\\n' \"$line\""], environment: ["PATH": "/usr/bin:/bin"])
            inputSession.terminal.insertText("中文 native input", replacementRange: NSRange(location: NSNotFound, length: 0))
            inputSession.terminal.send(source: inputSession.terminal, data: [13][...])
            try check(until { !inputSession.isActive && inputOutput.contains("TEXT:中文 native input") }, "native text insertion was lost")
            let invalid = TerminalSession(worktree: root.appendingPathComponent("missing").path)
            do {
                try invalid.start(executable: "/bin/sh", arguments: [])
                throw CheckFailure(reason: "invalid worktree accepted")
            } catch is TerminalFailure {}
            var snap = SessionActivitySnapshot()
            snap.alternateScreen = true
            try check(AgentActivityInference.infer(isActive: true, isTaskMode: false, snapshot: snap) == .unknown,
                      "alternate screen must not infer working")
            snap.alternateScreen = false
            snap.lastBellAt = Date()
            try check(AgentActivityInference.infer(isActive: true, isTaskMode: false, snapshot: snap) == .unknown,
                      "recent bell must not infer waiting input")
            print("PASS: real PTY, literal argv, cwd, exit code, independent stop, native window keyboard/text input, 1 MiB output, 24-cycle PTY reclamation, invalid input, OSC policy, agent activity inference, owned cleanup")
        } catch {
            fputs("terminal checks failed: \(error)\n", stderr)
            for session in owned { session.stop(force: true) }
            _ = until { owned.allSatisfy { !$0.isActive } }
            try? FileManager.default.removeItem(at: root)
            exit(1)
        }
    }
}
