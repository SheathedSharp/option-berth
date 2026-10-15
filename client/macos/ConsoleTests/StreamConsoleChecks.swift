import AppKit
import Darwin
import SwiftUI
import BerthTerminal
@testable import BerthClient

extension ConsoleChecks {
    static func streamStructuredConsoleChecks(parentRoot: URL) throws {
        let registry = TerminalSessions.shared
        let root = parentRoot.appendingPathComponent("stream-worktree")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try Data().write(to: root.appendingPathComponent("fake-session.jsonl"))
        let binary = parentRoot.appendingPathComponent("fixture-stream-oberth")
        let commandLog = root.appendingPathComponent("stream-cmd.log")
        let bridgePIDFile = root.appendingPathComponent("stream-bridge.pid")
        try? FileManager.default.removeItem(at: commandLog)
        try? FileManager.default.removeItem(at: bridgePIDFile)
        try Data(streamOberthFixtureSource.utf8).write(to: binary)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: binary.path)
        let priorBin = ProcessInfo.processInfo.environment["BERTH_BIN"]
        let priorLog = ProcessInfo.processInfo.environment["BERTH_STREAM_FIXTURE_LOG"]
        setenv("BERTH_BIN", binary.path, 1)
        setenv("BERTH_STREAM_FIXTURE_LOG", commandLog.path, 1)
        setenv("BERTH_STREAM_FIXTURE_PID", bridgePIDFile.path, 1)
        defer {
            if let priorBin { setenv("BERTH_BIN", priorBin, 1) } else { unsetenv("BERTH_BIN") }
            if let priorLog { setenv("BERTH_STREAM_FIXTURE_LOG", priorLog, 1) } else { unsetenv("BERTH_STREAM_FIXTURE_LOG") }
            unsetenv("BERTH_STREAM_FIXTURE_PID")
            unsetenv("BERTH_STREAM_UNSUPPORTED")
            for session in registry.inWorktree(root.path) {
                TerminalWindows.shared.bringBack(session)
                if session.isActive { session.stop(force: true) }
            }
            registry.forgetWorkspace(root.path)
            try? FileManager.default.removeItem(at: binary)
            try? FileManager.default.removeItem(at: commandLog)
        }
        let workspace = registry.workspace(root.path)
        workspace.providerID = "codex"
        workspace.providerChosen = true
        let env = ["HOME": parentRoot.path, "PATH": "/usr/bin:/bin",
                   "BERTH_STREAM_FIXTURE_LOG": commandLog.path]
        let shellScript = "printf 'STREAM-SHELL\\n'; while IFS= read -r _; do :; done"
        let shell = try registry.add(worktree: root.path, title: "Shell stream", executable: "/bin/sh",
                                     arguments: ["-c", shellScript], environment: env)
        defer {
            if shell.isActive { shell.stop(force: true) }
            registry.remove(shell)
        }
        let host = NSHostingView(rootView: WorkspaceConsole(root: root.path))
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 960, height: 620),
                              styleMask: [.titled, .resizable], backing: .buffered, defer: false)
        window.contentView = host
        defer { window.contentView = nil; window.close() }
        mountTestWindow(window)
        raiseTestWindow(NSApplication.shared, window)
        pump(0.35)
        func composerField() -> NSTextView? {
            find(NSTextView.self, in: host).first { $0.accessibilityIdentifier() == "console.sessionComposer" }
        }
        guard var editor = composerField() else {
            fatalError("stream console missing composer editor")
        }
        func focusComposer(clear: Bool = false) {
            find(NSButton.self, in: host).first { $0.accessibilityIdentifier() == "console.focusComposer" }?.performClick(nil)
            pump(0.08)
            if let field = composerField() {
                editor = field
                window.makeFirstResponder(field)
            }
            if clear { workspace.draft = ""; pump(0.05) }
        }
        func sendAgent(_ text: String, flags: NSEvent.ModifierFlags) {
            focusComposer(clear: true)
            workspace.draft = text
            pump(0.05)
            chord(flags)
        }
        focusComposer()
        func chord(_ flags: NSEvent.ModifierFlags) {
            let submit = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: flags,
                timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
                characters: "\r", charactersIgnoringModifiers: "\r", isARepeat: false, keyCode: 36)!
            if window.firstResponder === editor {
                if !editor.performKeyEquivalent(with: submit) { editor.keyDown(with: submit) }
            } else {
                window.sendEvent(submit)
            }
        }
        func streamRows() -> [NSButton] {
            find(NSButton.self, in: host).filter { $0.accessibilityIdentifier().hasPrefix("console.streamStatus.") }
        }
        sendAgent("round-one", flags: .command)
        eventually("⌘↩ did not start a structured stream session") { !streamRows().isEmpty }
        require(streamRows().count == 1, "first ⌘↩ should create one structured session")
        eventually("first turn should finish on the bridge") {
            (try? String(contentsOf: commandLog, encoding: .utf8))?.contains("round-one") == true
        }
        pump(0.2)
        sendAgent("round-two", flags: .command)
        eventually("second ⌘↩ on same bridge did not send a second turn") {
            (try? String(contentsOf: commandLog, encoding: .utf8))?.contains("round-two") == true
        }
        if let pidText = try? String(contentsOf: bridgePIDFile, encoding: .utf8),
           let bridgePID = Int32(pidText.trimmingCharacters(in: .whitespacesAndNewlines)) {
            require(kill(bridgePID, 0) == 0, "structured bridge should stay alive across two turns")
        }
        streamRows().first?.performClick(nil)
        pump(0.15)
        let logAfterTwo = try String(contentsOf: commandLog, encoding: .utf8)
        require(logAfterTwo.contains(#"turn.start"#) && logAfterTwo.contains("round-one") && logAfterTwo.contains("round-two"),
                "same bridge should accept two turns on one stdin stream")
        sendAgent("cmd-item-turn", flags: .command)
        eventually("cmd-item-turn should reach the bridge") {
            (try? String(contentsOf: commandLog, encoding: .utf8))?.contains("cmd-item-turn") == true
        }
        streamRows().first?.performClick(nil)
        pump(0.2)
        eventually("structured detail should list a command item") {
            find(NSButton.self, in: host).contains { $0.accessibilityIdentifier() == "console.streamAdoptCommand" }
        }
        guard let adopt = find(NSButton.self, in: host).first(where: {
            $0.accessibilityIdentifier() == "console.streamAdoptCommand"
        }) else { fatalError("stream detail missing adopt-command control") }
        adopt.performClick(nil)
        eventually("adopt command should copy into the composer draft") { workspace.draft == "npm run fix" }
        var gitRequest: BerthGitPathRequest?
        let gitObserver = NotificationCenter.default.addObserver(forName: .berthOpenGitPath, object: nil, queue: .main) { note in
            gitRequest = note.object as? BerthGitPathRequest
        }
        defer { NotificationCenter.default.removeObserver(gitObserver) }
        sendAgent("git-diff-turn", flags: .command)
        eventually("git-diff-turn should reach the bridge") {
            (try? String(contentsOf: commandLog, encoding: .utf8))?.contains("git-diff-turn") == true
        }
        streamRows().first?.performClick(nil)
        pump(0.2)
        eventually("stream detail should offer git review for a changed file") {
            find(NSButton.self, in: host).contains { $0.accessibilityIdentifier() == "console.streamGit.src/demo.go" }
        }
        find(NSButton.self, in: host).first { $0.accessibilityIdentifier() == "console.streamGit.src/demo.go" }?.performClick(nil)
        eventually("git review should post a worktree path request") {
            gitRequest?.worktree == root.path && gitRequest?.path == "src/demo.go"
        }
        sendAgent("hang-me", flags: .command)
        eventually("hang-me should start a long-running turn") {
            (try? String(contentsOf: commandLog, encoding: .utf8))?.contains("hang-me") == true
        }
        focusComposer(clear: true)
        workspace.draft = "/interrupt"
        pump(0.05)
        window.makeFirstResponder(editor)
        pump(0.05)
        chord([])
        eventually("/interrupt should write turn.interrupt to the bridge") {
            (try? String(contentsOf: commandLog, encoding: .utf8))?.contains("turn.interrupt") == true
        }
        sendAgent("approve-me", flags: .command)
        eventually("approve-me turn did not reach the bridge") {
            (try? String(contentsOf: commandLog, encoding: .utf8))?.contains("approve-me") == true
        }
        streamRows().first?.performClick(nil)
        pump(0.2)
        func approvalAllowButton() -> NSButton? {
            find(NSButton.self, in: host).first {
                $0.accessibilityIdentifier().contains("streamApprove.r1.allow")
                    || $0.accessibilityIdentifier().contains("streamRowApprove.r1.allow")
            }
        }
        eventually("approval turn did not open a request card") { approvalAllowButton() != nil }
        let escape = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [],
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
            characters: "\u{1b}", charactersIgnoringModifiers: "\u{1b}", isARepeat: false, keyCode: 53)!
        NSApp.sendEvent(escape)
        pump(0.08)
        require(approvalAllowButton() != nil, "Escape must not answer or dismiss an approval request")
        sendAgent("queue-drain", flags: .command)
        eventually("⌘↩ during approval should queue instead of answering") { workspace.draft.isEmpty }
        let logBeforeAllow = try String(contentsOf: commandLog, encoding: .utf8)
        require(!logBeforeAllow.contains("queue-drain"), "queued text must not be sent while waiting for approval")
        streamRows().first?.performClick(nil)
        pump(0.15)
        guard let withdraw = find(NSButton.self, in: host).first(where: {
            ($0.accessibilityIdentifier() ?? "").hasPrefix("console.streamQueueWithdraw.")
        }) else { fatalError("stream detail missing queue withdraw control") }
        NSApp.sendAction(#selector(NSButton.performClick(_:)), to: withdraw, from: nil)
        pump(0.1)
        require(workspace.draft == "queue-drain", "withdraw should restore queued text to the composer")
        workspace.draft = ""
        sendAgent("queue-drain", flags: .command)
        eventually("re-queued message should stay out of the bridge until approval completes") {
            workspace.draft.isEmpty
        }
        approvalAllowButton()?.performClick(nil)
        eventually("allow did not close the approval request") { approvalAllowButton() == nil }
        eventually("queued turn should drain after approval completed") {
            (try? String(contentsOf: commandLog, encoding: .utf8))?.contains("queue-drain") == true
        }
        let logAfterAllow = try String(contentsOf: commandLog, encoding: .utf8)
        require(logAfterAllow.contains(#"request.respond"#) && logAfterAllow.contains(#"allow"#),
                "approval should write request.respond with allow to the bridge stdin")
        sendAgent("second-session", flags: [.command, .shift])
        eventually("⇧⌘↩ should open a second structured session") { streamRows().count == 2 }
        guard let close = find(NSButton.self, in: host).first(where: {
            $0.accessibilityIdentifier().hasPrefix("console.streamClose.")
        }) else { fatalError("stream close control missing") }
        close.performClick(nil)
        eventually("ending a structured session should remove its row") { streamRows().count == 1 }
        let logAfterClose = try String(contentsOf: commandLog, encoding: .utf8)
        require(logAfterClose.contains("session.close"), "ending should send session.close to the bridge")
        if let pidText = try? String(contentsOf: bridgePIDFile, encoding: .utf8),
           let pid = Int32(pidText.trimmingCharacters(in: .whitespacesAndNewlines)) {
            eventually("stream bridge process should exit after session.close") { kill(pid, 0) != 0 }
        } else {
            fatalError("stream fixture did not record bridge pid")
        }
        try streamProviderQuestionCardCheck(parentRoot: parentRoot)
        try streamResumeAfterEndedBridgeCheck(parentRoot: parentRoot)
        try streamNativeHandoffCheck(parentRoot: parentRoot)
        try streamSessionFileConflictCheck(parentRoot: parentRoot)
        try streamUnsupportedConsoleCheck(parentRoot: parentRoot)
        pump(0.2)
        print("PASS: structured stream second turn, queue, Esc, respond bytes, ⇧⌘↩ second session, end/close, resume after bridge exit")
    }

    static func streamShellAttachmentAgentSendCheck(parentRoot: URL) throws {
        let root = parentRoot.appendingPathComponent("stream-attach-worktree")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try Data().write(to: root.appendingPathComponent("fake-session.jsonl"))
        let binary = parentRoot.appendingPathComponent("fixture-stream-oberth")
        if !FileManager.default.fileExists(atPath: binary.path) {
            try Data(streamOberthFixtureSource.utf8).write(to: binary)
            try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: binary.path)
        }
        let commandLog = root.appendingPathComponent("stream-attach-cmd.log")
        try? FileManager.default.removeItem(at: commandLog)
        setenv("BERTH_BIN", binary.path, 1)
        setenv("BERTH_STREAM_FIXTURE_LOG", commandLog.path, 1)
        defer {
            unsetenv("BERTH_STREAM_FIXTURE_LOG")
            try? FileManager.default.removeItem(at: commandLog)
        }
        let registry = TerminalSessions.shared
        let workspace = registry.workspace(root.path)
        workspace.providerID = "codex"
        workspace.providerChosen = true
        let env = ["HOME": parentRoot.path, "PATH": "/usr/bin:/bin",
                   "BERTH_STREAM_FIXTURE_LOG": commandLog.path]
        let shell = try registry.add(worktree: root.path, title: "Shell attach", executable: "/bin/zsh",
                                     arguments: ["-i"], environment: env, shellIntegration: true)
        defer {
            if shell.isActive { shell.stop(force: true) }
            registry.remove(shell)
            for session in registry.inWorktree(root.path) {
                if session.isActive { session.stop(force: true) }
                registry.remove(session)
            }
            registry.forgetWorkspace(root.path)
        }
        let host = NSHostingView(rootView: WorkspaceConsole(root: root.path))
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 900, height: 560),
                              styleMask: [.titled], backing: .buffered, defer: false)
        window.contentView = host
        defer { window.contentView = nil; window.close() }
        mountTestWindow(window)
        raiseTestWindow(NSApplication.shared, window)
        pump(0.45)
        shell.terminal.send(source: shell.terminal, data: Array("printf 'ATTACH-TEST\\n'\n".utf8)[...])
        eventually("zsh integration should record ATTACH-TEST command block") {
            shell.commandBlocks.contains { $0.command?.contains("ATTACH-TEST") == true && $0.endedAt != nil }
        }
        eventually("attach worktree should offer send-to-agent") {
            find(NSButton.self, in: host).contains { $0.accessibilityIdentifier() == "console.commandBlock.sendToAgent" }
        }
        find(NSButton.self, in: host).first { $0.accessibilityIdentifier() == "console.commandBlock.sendToAgent" }?.performClick(nil)
        eventually("shell attach should surface in composer") {
            find(NSView.self, in: host).contains {
                $0.accessibilityIdentifier() == "console.composerAttachment"
                    && ($0.accessibilityValue() as? String)?.contains("ATTACH-TEST") == true
            }
        }
        find(NSButton.self, in: host).first { $0.accessibilityIdentifier() == "console.focusComposer" }?.performClick(nil)
        pump(0.1)
        guard let editor = find(NSTextView.self, in: host).first(where: {
            $0.accessibilityIdentifier() == "console.sessionComposer"
        }) else { fatalError("attach stream check missing composer") }
        workspace.draft = "see attachment"
        pump(0.05)
        require(find(NSView.self, in: host).contains { $0.accessibilityIdentifier() == "console.composerAttachment" },
                "shell attachment should remain until send")
        window.makeFirstResponder(editor)
        pump(0.75)
        let submit = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: .command,
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
            characters: "\r", charactersIgnoringModifiers: "\r", isARepeat: false, keyCode: 36)!
        if !editor.performKeyEquivalent(with: submit) { editor.keyDown(with: submit) }
        eventually("⌘↩ should start a structured stream from shell attachment") {
            find(NSButton.self, in: host).contains { $0.accessibilityIdentifier().hasPrefix("console.streamStatus.") }
        }
        eventually("⌘↩ should start a stream with merged shell attachment text") {
            let log = try? String(contentsOf: commandLog, encoding: .utf8)
            return log?.contains("ATTACH-TEST") == true && log?.contains("```shell") == true
        }
    }

    static func streamProviderQuestionCardCheck(parentRoot: URL) throws {
        let root = parentRoot.appendingPathComponent("stream-question-worktree")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        let binary = parentRoot.appendingPathComponent("fixture-stream-oberth")
        try Data(streamOberthFixtureSource.utf8).write(to: binary)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: binary.path)
        setenv("BERTH_BIN", binary.path, 1)
        defer { unsetenv("BERTH_BIN") }
        let registry = TerminalSessions.shared
        let workspace = registry.workspace(root.path)
        workspace.providerID = "codex"
        workspace.providerChosen = true
        let shell = try registry.add(worktree: root.path, title: "Shell", executable: "/bin/sh",
                                     arguments: ["-c", "sleep 300"], environment: ["HOME": parentRoot.path, "PATH": "/usr/bin:/bin"])
        defer {
            for session in registry.inWorktree(root.path) {
                if session.isActive { session.stop(force: true) }
                registry.remove(session)
            }
            registry.forgetWorkspace(root.path)
        }
        let host = NSHostingView(rootView: WorkspaceConsole(root: root.path))
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 900, height: 560),
                              styleMask: [.titled], backing: .buffered, defer: false)
        window.contentView = host
        defer { window.contentView = nil; window.close() }
        mountTestWindow(window)
        pump(0.35)
        workspace.draft = "provider-ask"
        find(NSButton.self, in: host).first { $0.accessibilityIdentifier() == "console.focusComposer" }?.performClick(nil)
        pump(0.1)
        guard let editor = find(NSTextView.self, in: host).first(where: {
            $0.accessibilityIdentifier() == "console.sessionComposer"
        }) else { fatalError("question card check missing composer") }
        window.makeFirstResponder(editor)
        let submit = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: .command,
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
            characters: "\r", charactersIgnoringModifiers: "\r", isARepeat: false, keyCode: 36)!
        if !editor.performKeyEquivalent(with: submit) { editor.keyDown(with: submit) }
        eventually("provider question turn should create a stream row") {
            find(NSButton.self, in: host).contains { $0.accessibilityIdentifier().hasPrefix("console.streamStatus.") }
        }
        find(NSButton.self, in: host).first { $0.accessibilityIdentifier().hasPrefix("console.streamStatus.") }?.performClick(nil)
        pump(0.1)
        eventually("stream detail should offer native continuation for provider questions") {
            find(NSButton.self, in: host).contains {
                ($0.accessibilityIdentifier() ?? "").hasPrefix("console.streamContinueNative.")
            }
        }
    }

    static func streamResumeAfterEndedBridgeCheck(parentRoot: URL) throws {
        let root = parentRoot.appendingPathComponent("stream-resume-worktree")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try Data().write(to: root.appendingPathComponent("fake-session.jsonl"))
        let binary = parentRoot.appendingPathComponent("fixture-stream-oberth")
        let commandLog = root.appendingPathComponent("stream-resume-cmd.log")
        let bridgePIDFile = root.appendingPathComponent("stream-resume-bridge.pid")
        setenv("BERTH_BIN", binary.path, 1)
        setenv("BERTH_STREAM_FIXTURE_LOG", commandLog.path, 1)
        setenv("BERTH_STREAM_FIXTURE_PID", bridgePIDFile.path, 1)
        defer {
            unsetenv("BERTH_STREAM_FIXTURE_LOG")
            unsetenv("BERTH_STREAM_FIXTURE_PID")
            try? FileManager.default.removeItem(at: commandLog)
        }
        let registry = TerminalSessions.shared
        let workspace = registry.workspace(root.path)
        workspace.providerID = "codex"
        workspace.providerChosen = true
        _ = try registry.add(worktree: root.path, title: "Shell", executable: "/bin/sh",
                             arguments: ["-c", "sleep 300"], environment: ["HOME": parentRoot.path, "PATH": "/usr/bin:/bin"])
        defer {
            for session in registry.inWorktree(root.path) {
                if session.isActive { session.stop(force: true) }
                registry.remove(session)
            }
            registry.forgetWorkspace(root.path)
        }
        let host = NSHostingView(rootView: WorkspaceConsole(root: root.path))
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 900, height: 560),
                              styleMask: [.titled], backing: .buffered, defer: false)
        window.contentView = host
        defer { window.contentView = nil; window.close() }
        mountTestWindow(window)
        raiseTestWindow(NSApplication.shared, window)
        pump(0.35)
        func sendComposer(_ text: String) {
            find(NSButton.self, in: host).first { $0.accessibilityIdentifier() == "console.focusComposer" }?.performClick(nil)
            pump(0.08)
            workspace.draft = text
            pump(0.05)
            guard let editor = find(NSTextView.self, in: host).first(where: {
                $0.accessibilityIdentifier() == "console.sessionComposer"
            }) else { fatalError("resume check missing composer") }
            window.makeFirstResponder(editor)
            let submit = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: .command,
                timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
                characters: "\r", charactersIgnoringModifiers: "\r", isARepeat: false, keyCode: 36)!
            if !editor.performKeyEquivalent(with: submit) { editor.keyDown(with: submit) }
        }
        sendComposer("close-bridge")
        eventually("resume check needs a structured stream row") {
            find(NSButton.self, in: host).contains { $0.accessibilityIdentifier().hasPrefix("console.streamStatus.") }
        }
        if let pidText = try? String(contentsOf: bridgePIDFile, encoding: .utf8),
           let pid = Int32(pidText.trimmingCharacters(in: .whitespacesAndNewlines)) {
            eventually("close-bridge should end the fixture bridge process") { kill(pid, 0) != 0 }
        }
        eventually("bridge exit should keep the structured session row for resume") {
            find(NSButton.self, in: host).contains { $0.accessibilityIdentifier().hasPrefix("console.streamStatus.") }
        }
        pump(0.2)
        sendComposer("after-resume")
        eventually("⌘↩ should resume ended stream and send a new turn") {
            (try? String(contentsOf: commandLog, encoding: .utf8))?.contains("after-resume") == true
        }
        let log = try String(contentsOf: commandLog, encoding: .utf8)
        require(log.contains(#""fixture":"session.ended"#),
                "fixture should mark session.ended before bridge exit")
        if let pidText2 = try? String(contentsOf: bridgePIDFile, encoding: .utf8),
           let pid2 = Int32(pidText2.trimmingCharacters(in: .whitespacesAndNewlines)) {
            require(kill(pid2, 0) == 0, "resumed stream should run a new bridge process")
        }
    }

    static func streamNativeHandoffCheck(parentRoot: URL) throws {
        let root = parentRoot.appendingPathComponent("stream-native-worktree")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try Data().write(to: root.appendingPathComponent("fake-session.jsonl"))
        let registry = TerminalSessions.shared
        let workspace = registry.workspace(root.path)
        workspace.providerID = "codex"
        workspace.providerChosen = true
        let shell = try registry.add(worktree: root.path, title: "Shell", executable: "/bin/sh",
                                     arguments: ["-c", "sleep 300"], environment: ["HOME": parentRoot.path, "PATH": "/usr/bin:/bin"])
        defer {
            for session in registry.inWorktree(root.path) {
                if session.isActive { session.stop(force: true) }
                registry.remove(session)
            }
            registry.forgetWorkspace(root.path)
        }
        let host = NSHostingView(rootView: WorkspaceConsole(root: root.path))
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 800, height: 520),
                              styleMask: [.titled], backing: .buffered, defer: false)
        window.contentView = host
        defer { window.contentView = nil; window.close() }
        mountTestWindow(window)
        pump(0.35)
        workspace.draft = "warmup"
        pump(0.05)
        find(NSButton.self, in: host).first { $0.accessibilityIdentifier() == "console.focusComposer" }?.performClick(nil)
        pump(0.1)
        let submit = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: .command,
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
            characters: "\r", charactersIgnoringModifiers: "\r", isARepeat: false, keyCode: 36)!
        NSApp.sendEvent(submit)
        eventually("native handoff needs a structured stream row") {
            find(NSButton.self, in: host).contains { $0.accessibilityIdentifier().hasPrefix("console.streamStatus.") }
        }
        find(NSButton.self, in: host).first { $0.accessibilityIdentifier() == "console.focusComposer" }?.performClick(nil)
        pump(0.1)
        workspace.draft = "/native"
        pump(0.05)
        if let field = find(NSTextView.self, in: host).first(where: { $0.accessibilityIdentifier() == "console.sessionComposer" }) {
            window.makeFirstResponder(field)
        }
        pump(0.05)
        let enter = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [],
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
            characters: "\r", charactersIgnoringModifiers: "\r", isARepeat: false, keyCode: 36)!
        NSApp.sendEvent(enter)
        eventually("/native should open a native agent PTY") {
            registry.inWorktree(root.path).contains { $0.kind == "agent:codex:native" && $0.isActive }
        }
        eventually("native handoff should end the structured stream bridge") {
            !AgentStreamRegistry.all.contains { $0.worktree == root.path && $0.process.isRunning }
        }
    }

    static func streamSessionFileConflictCheck(parentRoot: URL) throws {
        let root = parentRoot.appendingPathComponent("stream-conflict-worktree")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        let sessionFile = root.appendingPathComponent("fake-session.jsonl")
        try Data().write(to: sessionFile)
        let binary = parentRoot.appendingPathComponent("fixture-stream-oberth")
        let commandLog = root.appendingPathComponent("stream-conflict-cmd.log")
        setenv("BERTH_BIN", binary.path, 1)
        setenv("BERTH_STREAM_FIXTURE_LOG", commandLog.path, 1)
        setenv("BERTH_TEST_RESUME_FILE", sessionFile.path, 1)
        defer {
            unsetenv("BERTH_TEST_RESUME_FILE")
            unsetenv("BERTH_STREAM_FIXTURE_LOG")
            try? FileManager.default.removeItem(at: commandLog)
        }
        let registry = TerminalSessions.shared
        let workspace = registry.workspace(root.path)
        workspace.providerID = "codex"
        workspace.providerChosen = true
        let shell = try registry.add(worktree: root.path, title: "Shell", executable: "/bin/sh",
                                     arguments: ["-c", "sleep 300"], environment: ["HOME": parentRoot.path, "PATH": "/usr/bin:/bin"])
        defer {
            if shell.isActive { shell.stop(force: true) }
            registry.remove(shell)
            registry.forgetWorkspace(root.path)
        }
        let host = NSHostingView(rootView: WorkspaceConsole(root: root.path))
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 800, height: 520),
                              styleMask: [.titled], backing: .buffered, defer: false)
        window.contentView = host
        defer { window.contentView = nil; window.close() }
        mountTestWindow(window)
        pump(0.35)
        workspace.draft = "hang-me"
        pump(0.05)
        find(NSButton.self, in: host).first { $0.accessibilityIdentifier() == "console.focusComposer" }?.performClick(nil)
        pump(0.1)
        let submit = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: .command,
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
            characters: "\r", charactersIgnoringModifiers: "\r", isARepeat: false, keyCode: 36)!
        NSApp.sendEvent(submit)
        eventually("conflict check needs a running structured stream") {
            find(NSButton.self, in: host).contains { $0.accessibilityIdentifier().hasPrefix("console.streamStatus.") }
        }
        eventually("hang-me should reach the fixture bridge") {
            (try? String(contentsOf: commandLog, encoding: .utf8))?.contains("hang-me") == true
        }
        let nativesBefore = registry.inWorktree(root.path).filter { $0.kind.hasPrefix("agent:") }.count
        find(NSButton.self, in: host).first { $0.accessibilityIdentifier() == "console.focusComposer" }?.performClick(nil)
        pump(0.1)
        workspace.draft = "/resume"
        pump(0.05)
        if let field = find(NSTextView.self, in: host).first(where: { $0.accessibilityIdentifier() == "console.sessionComposer" }) {
            window.makeFirstResponder(field)
        }
        pump(0.05)
        let enter = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [],
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
            characters: "\r", charactersIgnoringModifiers: "\r", isARepeat: false, keyCode: 36)!
        NSApp.sendEvent(enter)
        eventually("/resume should refuse while the session file is in use") {
            find(NSView.self, in: host).contains {
                let value = $0.accessibilityValue() as? String ?? ""
                return value.contains("会话文件") || value.localizedCaseInsensitiveContains("session file")
            }
        }
        let nativesAfter = registry.inWorktree(root.path).filter { $0.kind.hasPrefix("agent:") }.count
        require(nativesBefore == nativesAfter, "/resume must not open a native agent while the file is in use")
    }

    static func streamUnsupportedConsoleCheck(parentRoot: URL) throws {
        let root = parentRoot.appendingPathComponent("stream-unsupported-worktree")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        setenv("BERTH_STREAM_UNSUPPORTED", "1", 1)
        defer { unsetenv("BERTH_STREAM_UNSUPPORTED") }
        let registry = TerminalSessions.shared
        let workspace = registry.workspace(root.path)
        workspace.providerID = "codex"
        workspace.providerChosen = true
        let shell = try registry.add(worktree: root.path, title: "Shell", executable: "/bin/sh",
                                     arguments: ["-c", "sleep 300"], environment: ["HOME": parentRoot.path, "PATH": "/usr/bin:/bin"])
        defer {
            if shell.isActive { shell.stop(force: true) }
            registry.remove(shell)
            registry.forgetWorkspace(root.path)
        }
        let host = NSHostingView(rootView: WorkspaceConsole(root: root.path))
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 800, height: 520),
                              styleMask: [.titled], backing: .buffered, defer: false)
        window.contentView = host
        defer { window.contentView = nil; window.close() }
        mountTestWindow(window)
        pump(0.35)
        workspace.draft = "probe"
        pump(0.05)
        find(NSButton.self, in: host).first { $0.accessibilityIdentifier() == "console.focusComposer" }?.performClick(nil)
        pump(0.1)
        let submit = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: .command,
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
            characters: "\r", charactersIgnoringModifiers: "\r", isARepeat: false, keyCode: 36)!
        NSApp.sendEvent(submit)
        eventually("unsupported stream should surface guidance in the composer hint") {
            find(NSView.self, in: host).contains {
                let value = $0.accessibilityValue() as? String ?? ""
                return value.localizedCaseInsensitiveContains("unsupported") || value.contains("原生终端")
            }
        }
    }

    private static let streamOberthFixtureSource = #"""
#!/usr/bin/python3
import json, sys, os

def emit(obj):
    print(json.dumps(obj, separators=(',', ':')), flush=True)

def log_cmd(cmd):
    path = os.environ.get("BERTH_STREAM_FIXTURE_LOG")
    if path:
        with open(path, "a", encoding="utf-8") as handle:
            handle.write(json.dumps(cmd, separators=(',', ':')) + "\n")

session_started = False
busy = False
holding = None
active_busy_turn = None
turns = 0
worktree = "/fixture"
session_file = None

args = sys.argv[1:]
if args[:3] == ["agent", "list", "--json"]:
    print(json.dumps({"providers": [{
        "id": "codex", "name": "Codex fixture", "command": "codex",
        "installed": True, "native_prompt": True, "stream": True, "resume_file": True
    }]}))
    raise SystemExit(0)
if args[:2] == ["agent", "plan"] and "--json" in args and "--worktree" in args:
    provider = args[2]
    worktree = args[args.index("--worktree") + 1]
    mode = args[args.index("--mode") + 1] if "--mode" in args else "native"
    plan = {"provider": provider, "executable": "/bin/sh",
            "arguments": ["-c", "printf 'NATIVE-FIXTURE\\n'; sleep 120"],
            "worktree": worktree, "mode": mode}
    if "--resume-file" in args:
        resume = args[args.index("--resume-file") + 1]
        plan["resume"] = {"session_id": "fake-1", "source_version": "1", "worktree": worktree,
                          "source": resume, "approval_owner": "provider_native"}
    print(json.dumps(plan))
    raise SystemExit(0)
if args[:2] == ["agent", "stream"] and "--worktree" in args:
    worktree = args[args.index("--worktree") + 1]
    session_file = os.path.join(worktree, "fake-session.jsonl")
    pidfile = os.environ.get("BERTH_STREAM_FIXTURE_PID")
    if pidfile:
        with open(pidfile, "w", encoding="utf-8") as handle:
            handle.write(str(os.getpid()))
    for line in sys.stdin:
        cmd = json.loads(line)
        log_cmd(cmd)
        typ = cmd.get("type")
        cid = cmd.get("id")
        if typ == "turn.start":
            if os.environ.get("BERTH_STREAM_UNSUPPORTED") == "1":
                emit({"v": 1, "type": "ack", "id": cid})
                emit({"v": 1, "type": "error", "id": cid, "code": "unsupported", "message": "fixture unsupported protocol"})
                continue
            if busy:
                emit({"v": 1, "type": "error", "id": cid, "code": "busy", "message": "busy"})
                continue
            emit({"v": 1, "type": "ack", "id": cid})
            if not session_started:
                session_started = True
                emit({"v": 1, "type": "session.started", "provider": "codex", "protocol": "fake",
                      "worktree": worktree, "provider_session_id": "fake-1",
                      "session_file": session_file, "capabilities": ["approvals"]})
            turns += 1
            turn = f"t{turns}"
            busy = True
            text = cmd.get("text", "")
            emit({"v": 1, "type": "turn.started", "turn": turn, "input": text})
            if text == "approve-me":
                emit({"v": 1, "type": "request.opened", "turn": turn, "request": {
                    "id": "r1", "kind": "command", "command": "rm -rf .cache", "cwd": worktree,
                    "reason": "cleanup", "decisions": ["allow", "deny"]
                }})
                holding = turn
            elif text == "hang-me":
                active_busy_turn = turn
            elif text == "cmd-item-turn":
                emit({"v": 1, "type": "item.completed", "turn": turn, "item": {
                    "id": "c1", "kind": "command", "command": "npm run fix", "status": "completed", "exit_code": 0
                }})
                busy = False
                emit({"v": 1, "type": "turn.completed", "turn": turn, "outcome": "completed"})
            elif text == "git-diff-turn":
                emit({"v": 1, "type": "turn.diff", "turn": turn, "files": [{"path": "src/demo.go", "added": 2, "removed": 0}],
                      "patch": "", "truncated": False})
                busy = False
                emit({"v": 1, "type": "turn.completed", "turn": turn, "outcome": "completed"})
            elif text == "provider-ask":
                emit({"v": 1, "type": "notice", "turn": turn, "level": "warn",
                      "code": "provider_question", "message": "Which module should we refactor first?"})
                busy = False
                emit({"v": 1, "type": "turn.completed", "turn": turn, "outcome": "completed"})
            elif text == "close-bridge":
                busy = False
                emit({"v": 1, "type": "turn.completed", "turn": turn, "outcome": "completed"})
                log_cmd({"fixture": "session.ended", "reason": "closed"})
                emit({"v": 1, "type": "session.ended", "reason": "closed", "exit_code": 0})
                raise SystemExit(0)
            else:
                busy = False
                emit({"v": 1, "type": "turn.completed", "turn": turn, "outcome": "completed"})
        elif typ == "turn.interrupt":
            emit({"v": 1, "type": "ack", "id": cid})
            if active_busy_turn:
                emit({"v": 1, "type": "turn.completed", "turn": active_busy_turn, "outcome": "interrupted"})
                active_busy_turn = None
                busy = False
            elif holding:
                turn = holding
                holding = None
                busy = False
                emit({"v": 1, "type": "turn.completed", "turn": turn, "outcome": "interrupted"})
        elif typ == "request.respond":
            emit({"v": 1, "type": "ack", "id": cid})
            emit({"v": 1, "type": "request.closed", "request": cmd.get("request"), "decision": cmd.get("decision"), "by": "client"})
            if holding:
                turn = holding
                holding = None
                busy = False
                emit({"v": 1, "type": "turn.completed", "turn": turn, "outcome": "completed"})
        elif typ == "session.close":
            emit({"v": 1, "type": "session.ended", "reason": "closed", "exit_code": 0})
            raise SystemExit(0)
    raise SystemExit(0)
raise SystemExit(97)
"""#
}
