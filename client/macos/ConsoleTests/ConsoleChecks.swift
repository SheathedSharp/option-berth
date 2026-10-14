import AppKit
import SwiftUI
import ScreenCaptureKit
import BerthTerminal
@testable import BerthClient

@main @MainActor enum ConsoleChecks {
    /// Set with `BERTH_CONSOLE_TEST_QUIET=1` for local/CI runs that must not steal focus or capture the display.
    static var consoleTestQuiet: Bool { ProcessInfo.processInfo.environment["BERTH_CONSOLE_TEST_QUIET"] == "1" }
    static func mountTestWindow(_ window: NSWindow) {
        if consoleTestQuiet {
            // Keep fixture geometry for NSPopover anchoring; park in a corner without going off-screen.
            let frame = window.frame
            window.setFrame(NSRect(x: 24, y: 24, width: max(frame.width, 960), height: max(frame.height, 570)), display: true)
        }
        window.orderFrontRegardless()
        window.makeKey()
    }
    /// Quiet runs keep the window off-screen and use non-stealing activation so daily work stays foreground.
    static func testWindowAcceptsInput(_ window: NSWindow) -> Bool {
        consoleTestQuiet ? window.isVisible : window.isKeyWindow
    }
    static func launchSurfaces(host: NSView, window: NSWindow) -> [NSView] {
        var roots = NSApp.windows.compactMap(\.contentView)
        roots.append(host)
        if let content = window.contentView { roots.append(content) }
        return roots
    }
    static func raiseTestWindow(_ app: NSApplication, _ window: NSWindow) {
        if consoleTestQuiet {
            if !app.isActive { app.activate(ignoringOtherApps: false) }
            window.orderFrontRegardless()
            window.makeKey()
        } else {
            app.activate(ignoringOtherApps: true)
            window.makeKeyAndOrderFront(nil)
        }
    }
    static func require(_ condition: @autoclosure () -> Bool, _ message: String) {
        guard condition() else { fatalError(message) }
    }
    static func pump(_ seconds: TimeInterval = 0.12) {
        let end = Date().addingTimeInterval(seconds)
        // Foundation timers alone do not dispatch AppKit activation/window-server
        // events. Exercise the same dequeue/send path as NSApplication.run().
        while Date() < end {
            let slice = min(end, Date().addingTimeInterval(0.005))
            if let event = NSApp.nextEvent(matching: .any, until: slice, inMode: .default, dequeue: true) {
                NSApp.sendEvent(event)
            }
            NSApp.updateWindows()
        }
    }
    static func eventually(_ message: String, _ condition: () -> Bool) {
        let end = Date().addingTimeInterval(5)
        while !condition(), Date() < end { pump(0.01) }
        require(condition(), message)
    }
    static func find<T: NSView>(_ type: T.Type, in view: NSView) -> [T] {
        var values = (view as? T).map { [$0] } ?? []
        for child in view.subviews { values += find(type, in: child) }
        return values
    }
    static func main() throws {
        let envInput = ProcessInfo.processInfo.environment
        guard envInput["BERTH_CONSOLE_TEST"] == "1", let userHome = envInput["HOME"],
              envInput["CFFIXED_USER_HOME"] == userHome, envInput["ZDOTDIR"] == userHome,
              envInput["SHELL"] == "/bin/zsh", let home = envInput["BERTH_HOME"], home.hasPrefix(userHome + "/") else {
            fputs("ConsoleChecks requires explicit isolated HOME, BERTH_HOME and zsh startup directory\n", stderr); exit(2)
        }
        let deadline = DispatchWorkItem { fputs("ConsoleChecks native deadline exceeded\n", stderr); exit(1) }
        DispatchQueue.global().asyncAfter(deadline: .now() + 90, execute: deadline)
        defer { deadline.cancel() }
        let app = NSApplication.shared
        app.setActivationPolicy(consoleTestQuiet ? .regular : .accessory)
        app.finishLaunching()
        let root = URL(fileURLWithPath: home).appendingPathComponent("console-fixture")
        let other = root.appendingPathComponent("other")
        try FileManager.default.createDirectory(at: other, withIntermediateDirectories: true)
        // Initial discovery is a synthetic local catalogue too, not whichever
        // daily oberth happens to be installed on the developer's machine.
        let catalogue = root.appendingPathComponent("fixture-catalogue")
        try Data(#"""
#!/bin/sh
[ "$#" = 3 ] && [ "$1" = agent ] && [ "$2" = list ] && [ "$3" = --json ] || exit 97
/bin/sleep 0.05
printf '%s\n' '{"providers":[{"id":"codex","name":"Codex fixture","command":"codex","installed":true,"native_prompt":true}]}'
"""#.utf8).write(to: catalogue)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: catalogue.path)
        setenv("BERTH_BIN", catalogue.path, 1)
        // A synthetic prompt prevents machine/user identity in visual evidence.
        setenv("PROMPT", "fixture> ", 1); setenv("PS1", "fixture> ", 1)
        composerModelChecks()
        let registry = TerminalSessions.shared
        let env = ["HOME":root.path, "PATH":"/usr/bin:/bin"]
        try agentComposerPTYChecks(root: root, env: env)
        let script = "printf 'READY:%s\\n' \"$PWD\"; while IFS= read -r value; do printf 'INPUT:%s\\n' \"$value\"; done"
        var owned: [TerminalSession] = []
        defer {
            for session in owned { TerminalWindows.shared.bringBack(session); if session.isActive { session.stop(force: true) } }
            eventually("owned console PTYs did not exit") { owned.allSatisfy { !$0.isActive } }
            for session in owned { registry.remove(session) }
            registry.forgetWorkspace(root.path); registry.forgetWorkspace(other.path)
            try? FileManager.default.removeItem(at: root)
        }
        let shell = try registry.add(worktree: root.path, title: "Shell fixture", executable: "/bin/sh", arguments: ["-c", script], environment: env)
        owned.append(shell)
        // A controlled shell stands in for an external agent's native PTY. This
        // does not claim to run or validate a real provider's model or approvals.
        let agent = try registry.add(worktree: root.path, title: "Agent fixture", kind: "agent:codex:native", executable: "/bin/sh", arguments: ["-c", script], environment: env, select: false)
        owned.append(agent)
        let workspace = registry.workspace(root.path)
        require(workspace.activeSelection == shell.id, "background launch stole selection")
        require(SessionNavigationModel.group(for: "terminal") == .shell, "shell session was not grouped as terminal")
        require(SessionNavigationModel.group(for: "agent:codex:native") == .agent, "agent session was not grouped as agent")
        require(SessionNavigationModel.matches(query: "codex", title: "Codex 1", kind: "agent:codex:native", state: "running"), "session search missed provider")
        require(!SessionNavigationModel.matches(query: "missing", title: "Codex 1", kind: "agent:codex:native", state: "running"), "session search accepted an unrelated term")
        let foreign = try registry.add(worktree: other.path, title: "Other worktree", executable: "/bin/sh", arguments: ["-c", script], environment: env)
        owned.append(foreign)
        var shellOutput = "", agentOutput = ""
        shell.terminal.onBytes = { shellOutput += String(decoding: $0, as: UTF8.self) }
        agent.terminal.onBytes = { agentOutput += String(decoding: $0, as: UTF8.self) }
        eventually("console fixture PTYs were not ready") { shellOutput.contains("READY:") && agentOutput.contains("READY:") }
        require(shellOutput.contains(shell.worktree) && agentOutput.contains(agent.worktree), "starting cwd changed")
        workspace.agentMode = false; workspace.single(shell.id, agent: false)
        try workspace.split(agent.id, beside: shell.id, agent: false, axis: .horizontal)
        let treeRoot = workspace.activeLayout.root
        let shellProcess = shell.terminal.process, agentProcess = agent.terminal.process
        let host = NSHostingView(rootView: WorkspaceConsole(root: root.path))
        host.sizingOptions = []
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 960, height: 570), styleMask: [.titled, .resizable], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false; window.contentView = host
        defer { window.contentView = nil; window.close() }
        mountTestWindow(window)
        raiseTestWindow(app, window)
        eventually("the native test window never became ready") { testWindowAcceptsInput(window) }
        pump(0.25)
        func choose(_ session: TerminalSession) {
            guard let button = find(NSButton.self, in: host).first(where: { $0.accessibilityIdentifier() == "console.session." + session.id.uuidString }) else { fatalError("native shared session button missing") }
            button.performClick(nil)
            raiseTestWindow(app, window)
            eventually("session rail did not select " + session.title) { workspace.activeSelection == session.id }
            if session.kind == "terminal" {
                eventually("shell composer did not focus for " + session.title) {
                    testWindowAcceptsInput(window) && find(NSTextView.self, in: host).contains {
                        $0.accessibilityIdentifier() == "console.sessionComposer" && $0.window?.firstResponder === $0
                    }
                }
            } else {
                eventually("agent PTY did not focus for " + session.title) {
                    testWindowAcceptsInput(window) && window.firstResponder === session.terminal
                }
            }
            pump(0.05)
        }
        require(!find(NSButton.self, in: host).contains { $0.accessibilityIdentifier() == "console.session." + foreign.id.uuidString }, "session strip crossed worktrees")
        func type(_ text: String) {
            for char in text + "\r" {
                let key = String(char)
                let event = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [], timestamp: ProcessInfo.processInfo.systemUptime,
                    windowNumber: window.windowNumber, context: nil, characters: key, charactersIgnoringModifiers: key,
                    isARepeat: false, keyCode: char == "\r" ? 36 : 0)!
                NSApp.sendEvent(event)
            }
        }
        func press(_ keyCode: UInt16, flags: NSEvent.ModifierFlags = [], characters: String = "") {
            let event = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: flags, timestamp: ProcessInfo.processInfo.systemUptime,
                windowNumber: window.windowNumber, context: nil, characters: characters, charactersIgnoringModifiers: characters,
                isARepeat: false, keyCode: keyCode)!
            NSApp.sendEvent(event)
        }
        // Exercise delivery before a rail click can open the palette as a side
        // effect. The mounted view has to be inside this key window already.
        press(48, flags: [.command, .option], characters: "\t")
        eventually("initial Command-Option-Tab did not open the session palette") {
            find(NSView.self, in: host).contains { $0.accessibilityIdentifier() == "console.sessionPalette" }
        }
        press(53, characters: "\u{1b}")
        eventually("initial Escape did not dismiss the session palette") {
            !find(NSView.self, in: host).contains { $0.accessibilityIdentifier() == "console.sessionPalette" }
        }
        choose(agent); press(53, characters: "\u{1b}")
        eventually("Escape did not return from the agent to the remembered Shell") {
            workspace.activeSelection == shell.id && find(NSTextView.self, in: host).contains { $0.accessibilityIdentifier() == "console.sessionComposer" && $0.window?.firstResponder === $0 }
        }
        choose(shell); press(48, flags: [.command], characters: "\t")
        eventually("Command-Tab did not cycle to the parallel agent") { workspace.activeSelection == agent.id && window.firstResponder === agent.terminal }
        choose(shell); press(48, flags: [.command, .option], characters: "\t")
        eventually("Command-Option-Tab did not open the session palette") { find(NSView.self, in: host).contains { $0.accessibilityIdentifier() == "console.sessionPalette" } }
        press(53, characters: "\u{1b}")
        eventually("Escape did not dismiss the session palette") { !find(NSView.self, in: host).contains { $0.accessibilityIdentifier() == "console.sessionPalette" } }
        choose(shell); workspace.draft = "shell-only"; press(36, characters: "\r")
        choose(agent); type("agent-only")
        eventually("native typing did not reach selected PTYs") { shellOutput.contains("INPUT:shell-only") && agentOutput.contains("INPUT:agent-only") }
        require(!shellOutput.contains("agent-only") && !agentOutput.contains("shell-only"), "shared tabs broadcast input")
        require(workspace.activeLayout.root == treeRoot && Set(workspace.activeLayout.sessions) == Set([shell.id, agent.id]), "cross-kind switching discarded the mixed split")
        require(shell.terminal.process === shellProcess && agent.terminal.process === agentProcess, "switching recreated a PTY")
        workspace.draft = "fixture draft"
        choose(shell); choose(agent)
        require(workspace.draft == "fixture draft", "switching lost the unsent draft")
        // Opening a creation surface must not resize live alternate-screen TUIs,
        // replace hosts, mutate the active split, or implicitly launch a process.
        pump(0.2) // Let onAppear provider discovery finish before opening the launcher popover.
        let shellBounds = shell.terminal.bounds, agentBounds = agent.terminal.bounds
        let shellHost = shell.terminal.superview, agentHost = agent.terminal.superview
        guard let create = find(NSButton.self, in: host).first(where: { $0.accessibilityIdentifier() == "console.newSession" }) else { fatalError("unified creation entry missing") }
        for attempt in 0..<4 {
            raiseTestWindow(app, window)
            create.performClick(nil)
            pump(0.05)
            var cancel: NSButton?
            eventually("native creation popover did not open/reopen") {
                cancel = launchSurfaces(host: host, window: window).flatMap { find(NSButton.self, in: $0) }
                    .first { $0.accessibilityIdentifier() == "console.cancelLaunch" }
                if cancel == nil { create.performClick(nil) }
                return cancel != nil
            }
            let targetPicker = launchSurfaces(host: host, window: window).flatMap { find(NSPopUpButton.self, in: $0) }
                .first { $0.accessibilityIdentifier() == "console.launchTarget" }
            require(targetPicker?.isEnabled == true, "provider discovery blocked the Shell selector")
            pump(0.03)
            require(shell.terminal.bounds == shellBounds && agent.terminal.bounds == agentBounds, "creation resized live PTYs")
            require(shell.terminal.superview === shellHost && agent.terminal.superview === agentHost, "creation remounted native hosts")
            require(registry.sessions.count == 3 && workspace.activeLayout.root == treeRoot, "creation changed runtime ownership")
            if attempt == 0 {
                eventually("synthetic provider was not loaded in the popover") {
                    launchSurfaces(host: host, window: window).flatMap { find(NSPopUpButton.self, in: $0) }
                        .contains { picker in
                            picker.itemArray.contains { $0.title == "Codex fixture" }
                                || picker.titleOfSelectedItem == "Codex fixture"
                        }
                }
                pump(0.3) // Capture settled native presentation, not the show animation.
                try captureOwnedWindow(window, to: URL(fileURLWithPath: home).appendingPathComponent("session-launcher-composited-native.png"))
                raiseTestWindow(app, window)
                pump(0.12)
            }
            cancel?.performClick(nil)
            eventually("cancel did not return focus to the selected session") {
                workspace.activeSelection == agent.id && testWindowAcceptsInput(window)
                    && (window.firstResponder === agent.terminal
                        || find(NSTextView.self, in: host).contains { $0.accessibilityIdentifier() == "console.sessionComposer" && $0.window?.firstResponder === $0 })
            }
            require(workspace.draft == "fixture draft", "cancel discarded an unsent draft")
            // Reopen before a prior hide animation's callback can own the new one.
        }
        let editor = NSTextView(frame: NSRect(x: 20, y: 20, width: 140, height: 30))
        host.addSubview(editor); window.makeFirstResponder(editor)
        workspace.draft = "changed draft"; registry.objectWillChange.send(); pump()
        require(window.firstResponder === editor, "an already consumed focus request stole an editor on refresh")
        editor.removeFromSuperview()
        choose(agent); TerminalWindows.shared.detach(agent); pump()
        guard let detached = TerminalWindows.shared.windows[agent.id] else { fatalError("detached native window unavailable") }
        registry.objectWillChange.send(); pump()
        require(agent.terminal.window === detached, "embedded update stole detached terminal")
        func detachedKey(_ keyCode: UInt16, flags: NSEvent.ModifierFlags = [], characters: String = "") {
            let event = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: flags, timestamp: ProcessInfo.processInfo.systemUptime,
                windowNumber: detached.windowNumber, context: nil, characters: characters, charactersIgnoringModifiers: characters,
                isARepeat: false, keyCode: keyCode)!
            NSApp.sendEvent(event)
        }
        detachedKey(48, flags: [.command, .option], characters: "\t")
        detachedKey(53, characters: "\u{1b}")
        pump(0.2)
        require(!find(NSView.self, in: host).contains { $0.accessibilityIdentifier() == "console.sessionPalette" }, "detached window opened the session palette")
        require(workspace.activeSelection == agent.id && agent.terminal.window === detached, "detached shortcut changed session ownership")
        TerminalWindows.shared.bringBack(agent); raiseTestWindow(app, window)
        eventually("returning the detached terminal did not restore focus") {
            if agent.terminal.window !== window { return false }
            if consoleTestQuiet { return testWindowAcceptsInput(window) }
            return window.firstResponder === agent.terminal
        }
        func capture(_ name: String) throws {
            pump()
            try captureOwnedWindow(window, to: URL(fileURLWithPath: home).appendingPathComponent(name + "-composited-native.png"))
        }
        try capture("console-mixed")
        window.setContentSize(NSSize(width: 600, height: 440)); pump(0.25)
        require(host.bounds.width <= 600 && host.bounds.height <= 440, "console forced the minimum window larger")
        try capture("console-narrow")
        require(registry.sessions.count == 3 && owned.allSatisfy(\.isActive), "presentation started or stopped an unexpected process")
        try configurationChecks(parentRoot: root)
        print("PASS: real native shared tabs, mixed layout identity, cwd/draft retention, independent key input, one-shot focus, detached shortcut isolation, detached return and background launch isolation")
    }
    static func composerModelChecks() {
        func route(_ text: String, _ chord: SessionComposerModel.Chord, _ providerChosen: Bool, _ resumeReady: Bool) -> SessionComposerModel.Route {
            SessionComposerModel.route(text: text, chord: chord, providerChosen: providerChosen, resumeReady: resumeReady)
        }
        require(route("ls -la", .plain, false, false) == .shell("ls -la"), "Enter did not route to the shell")
        require(route("  ", .plain, true, true) == .none, "blank Enter was treated as input")
        require(route("/usr/bin/true", .plain, true, true) == .shell("/usr/bin/true"), "a path was captured as a slash command")
        require(route("/", .plain, false, false) == .none, "a bare slash was sent to the shell")
        require(route("/agent", .plain, true, true) == .none, "an ambiguous slash prefix was executed")
        require(route("/agent-select", .plain, false, false) == .openProviders, "/agent-select did not open providers")
        require(route("/agent-select claude", .plain, false, false) == .selectProvider("claude"), "/agent-select did not carry the provider")
        require(route("/agent-new fix tests", .plain, false, false) == .needProvider, "/agent-new skipped provider selection")
        require(route("/agent-new fix tests", .plain, true, false) == .newAgent("fix tests"), "/agent-new dropped its prompt")
        require(route("继续看测试", .command, false, false) == .needProvider, "⌘↩ sent text before an agent was selected")
        require(route("继续看测试", .command, true, false) == .newAgent("继续看测试"), "⌘↩ without a session should start the first agent")
        require(route("继续看测试", .command, false, true) == .resume("继续看测试"), "⌘↩ should resume without a default provider when a target exists")
        require(route("继续看测试", .command, true, true) == .resume("继续看测试"), "⌘↩ did not resume the selected agent")
        require(route("  ", .command, true, true) == .none, "blank ⌘↩ was sent to the agent")
        require(route("新的任务", .commandShift, false, true) == .needProvider, "⇧⌘↩ skipped provider selection")
        require(route("新的任务", .commandShift, true, true) == .newAgent("新的任务"), "⇧⌘↩ did not start a new agent session")
        require(route("", .commandShift, true, false) == .newAgent(""), "⇧⌘↩ required text to create a session")
        require(SessionComposerModel.matches("/").map(\.token).contains("/agent-select"), "slash menu missing /agent-select")
        require(SessionComposerModel.matches("/agent").map(\.token).contains("/agent"), "slash filter missed /agent")
        let shellLine = SessionPTYInput.encodeShellLine("ls -la")
        require(shellLine.bytes?.last == 10, "shell line did not end with LF")
        let agentLine = SessionPTYInput.encodeAgentTurn("hi", bracketedPaste: false)
        require(agentLine.bytes?.last == 13, "agent line did not end with CR")
        let blocked = SessionPTYInput.encodeAgentTurn("a\nb", bracketedPaste: false)
        require(blocked.bytes == nil, "multiline agent text without bracketed paste was accepted")
        let pasted = SessionPTYInput.encodeAgentTurn("line1\nline2", bracketedPaste: true)
        require(pasted.bytes?.starts(with: Array("\u{1B}[200~".utf8)) == true && pasted.bytes?.last == 13,
                "bracketed agent paste was not wrapped with CR")
    }

    static func agentComposerPTYChecks(root: URL, env: [String: String]) throws {
        let registry = TerminalSessions.shared
        let agentScript = root.appendingPathComponent("agent-pty-fixture.py")
        try Data("""
        import sys
        sys.stdout.write("\\033[?2004h")
        print("READY-AGENT", flush=True)
        line = b""
        while True:
            ch = sys.stdin.buffer.read(1)
            if not ch:
                break
            if ch in (b"\\r", b"\\n"):
                sys.stdout.write("\\aECHO:" + line.decode() + "\\n")
                sys.stdout.flush()
                break
            line += ch
        """.utf8).write(to: agentScript)
        var owned: TerminalSession?
        defer {
            if let owned, owned.isActive { owned.stop(force: true) }
            eventually("agent composer fixture did not exit") { owned?.isActive == false }
            if let owned { registry.remove(owned) }
            try? FileManager.default.removeItem(at: agentScript)
        }
        do {
            let agent = try registry.add(worktree: root.path, title: "Agent PTY fixture", kind: "agent:codex:native",
                                         executable: "/usr/bin/python3", arguments: [agentScript.path], environment: env, select: false)
            owned = agent
        } catch {
            fatalError("agent composer fixture failed to start: \(error)")
        }
        guard let agent = owned else { return }
        var output = ""
        agent.terminal.onBytes = { output += String(decoding: $0, as: UTF8.self) }
        eventually("agent composer fixture was not ready") { output.contains("READY-AGENT") }
        eventually("agent composer fixture did not enable bracketed paste") { agent.terminal.getTerminal().bracketedPasteMode }
        let encoded = SessionPTYInput.encodeAgentTurn("fixture-turn", bracketedPaste: false)
        guard let bytes = encoded.bytes else { fatalError("agent composer fixture could not encode turn: \(encoded.error ?? "")") }
        agent.terminal.send(source: agent.terminal, data: bytes[...])
        eventually("agent composer fixture did not echo the CR-terminated turn") { output.contains("ECHO:fixture-turn") }
        require(agent.needsAttention, "agent composer fixture did not mark attention after BEL")
        require(SessionComposerModel.matches("ls").isEmpty, "ordinary text opened the slash menu")
        require(SessionBridgeTextView.action(keyCode: 36, modifiers: [], composing: false, editable: true) == .shell, "Return was not the shell chord")
        require(SessionBridgeTextView.action(keyCode: 36, modifiers: .command, composing: false, editable: true) == .resume, "⌘↩ was not the resume chord")
        require(SessionBridgeTextView.action(keyCode: 36, modifiers: [.command, .shift], composing: false, editable: true) == .newAgent, "⇧⌘↩ was not the new-session chord")
        require(SessionBridgeTextView.action(keyCode: 36, modifiers: .command, composing: true, editable: true) == nil, "marked text was submitted")
        require(SessionBridgeTextView.action(keyCode: 36, modifiers: [.command, .option], composing: false, editable: true) == nil, "an unrelated chord was captured")
    }
    static func captureOwnedWindow(_ window: NSWindow, to output: URL) throws {
        if consoleTestQuiet { return }
        var completed: Result<Void, Error>?
        let task = Task { @MainActor in
            do {
                let content = try await SCShareableContent.excludingDesktopWindows(true, onScreenWindowsOnly: false)
                guard let owned = content.windows.first(where: {
                    $0.windowID == CGWindowID(window.windowNumber) && $0.owningApplication?.processID == ProcessInfo.processInfo.processIdentifier
                }) else { throw CocoaError(.fileReadUnknown) }
                let config = SCStreamConfiguration()
                config.width = Int(window.frame.width * window.backingScaleFactor)
                config.height = Int(window.frame.height * window.backingScaleFactor)
                config.showsCursor = false
                let image = try await SCScreenshotManager.captureImage(contentFilter: SCContentFilter(desktopIndependentWindow: owned), configuration: config)
                try Task.checkCancellation()
                guard let png = NSBitmapImageRep(cgImage: image).representation(using: .png, properties: [:]) else { throw CocoaError(.fileWriteUnknown) }
                try png.write(to: output)
                completed = .success(())
            } catch { completed = .failure(error) }
        }
        defer { task.cancel() }
        eventually("owned native window composition capture timed out") { completed != nil }
        try completed!.get()
    }

}
