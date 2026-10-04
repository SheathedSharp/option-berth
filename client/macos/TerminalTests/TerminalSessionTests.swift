import AppKit
import BerthTerminal
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
            let invalid = TerminalSession(worktree: root.appendingPathComponent("missing").path)
            do {
                try invalid.start(executable: "/bin/sh", arguments: [])
                throw CheckFailure(reason: "invalid worktree accepted")
            } catch is TerminalFailure {}
            print("PASS: real PTY, literal argv, cwd, exit code, independent stop, native window keyboard input, invalid input, OSC policy, owned cleanup")
        } catch {
            fputs("terminal checks failed: \(error)\n", stderr)
            for session in owned { session.stop(force: true) }
            _ = until { owned.allSatisfy { !$0.isActive } }
            try? FileManager.default.removeItem(at: root)
            exit(1)
        }
    }
}
