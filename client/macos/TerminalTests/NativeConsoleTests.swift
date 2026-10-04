import AppKit
import BerthTerminal

@MainActor
extension TerminalChecks {
    static func nativeConsoleChecks(root: URL) throws {
        let first = TerminalSession(worktree: root.path, title: "left")
        let second = TerminalSession(worktree: root.path, title: "right", kind: "agent:fixture:native")
        var one = "", two = ""
        first.terminal.onBytes = { one += String(decoding: $0, as: UTF8.self) }
        second.terminal.onBytes = { two += String(decoding: $0, as: UTF8.self) }
        let window = NSWindow(contentRect: NSRect(x: 100, y: 100, width: 680, height: 280),
                              styleMask: [.titled, .closable, .resizable], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.title = "option-berth · synthetic split input check"
        let split = NSSplitView(frame: window.contentView!.bounds)
        split.isVertical = true
        split.dividerStyle = .thin
        split.addArrangedSubview(first.terminal)
        split.addArrangedSubview(second.terminal)
        window.contentView = split
        window.makeKeyAndOrderFront(nil)
        defer {
            first.stop(force: true); second.stop(force: true)
            _ = until { !first.isActive && !second.isActive }
            window.close()
        }
        let environment = ["HOME": root.path, "PATH": "/usr/bin:/bin", "PWD": "/unrelated"]
        let script = "printf 'READY:%s\\n' \"$PWD\"; read -r line; printf 'INPUT:%s\\n' \"$line\"; stty size"
        try first.start(executable: "/bin/sh", arguments: ["-c", script], environment: environment)
        try second.start(executable: "/bin/sh", arguments: ["-c", script], environment: environment)
        try check(until { one.contains("READY:") && two.contains("READY:") }, "native panes did not start")
        try check(one.contains(first.worktree) && two.contains(second.worktree), "PWD does not match launch cwd")
        split.setPosition(250, ofDividerAt: 0)
        split.adjustSubviews()
        window.setContentSize(NSSize(width: 720, height: 320))
        split.layoutSubtreeIfNeeded()
        try check(first.terminal.frame.width > 100 && second.terminal.frame.width > 100, "divider collapsed a native pane")
        try check(first.terminal.terminal.cols > 0 && second.terminal.terminal.cols > 0, "native pane resize lost terminal geometry")
        for (terminal, message) in [(first.terminal, "left-only"), (second.terminal, "right-only")] {
            try check(window.makeFirstResponder(terminal), "pane cannot accept native focus")
            try check(HostedTerminalView.containing(window.firstResponder) === terminal, "find routing did not identify focused pane")
            for character in message + "\r" {
                let text = String(character)
                let event = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [],
                    timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber,
                    context: nil, characters: text, charactersIgnoringModifiers: text,
                    isARepeat: false, keyCode: character == "\r" ? 36 : 0)!
                window.sendEvent(event)
            }
        }
        try check(until { !first.isActive && !second.isActive && one.contains("INPUT:left-only") && two.contains("INPUT:right-only") },
                  "native split input did not reach its own process")
        try check(!one.contains("right-only") && !two.contains("left-only"), "keyboard input broadcast across panes")
        // Direct search engine calls do not read or modify the user's Find pasteboard.
        try check(first.terminal.findNext("left-only", scrollToResult: false), "native history search did not find output")
        try check(!first.terminal.findNext("right-only", scrollToResult: false), "history search crossed pane boundaries")
        first.terminal.clearSearch()
        let oldColumns = first.terminal.terminal.cols
        first.terminal.font = NSFont.monospacedSystemFont(ofSize: 18, weight: .regular)
        try check(first.terminal.font.pointSize == 18 && first.terminal.terminal.cols <= oldColumns, "font update did not resize terminal")
        print("PASS: native divider geometry, per-pane focus/input, cwd/PWD identity, isolated native history search, live font sizing; no user clipboard accessed")
    }
}
