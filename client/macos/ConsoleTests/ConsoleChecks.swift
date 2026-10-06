import AppKit
import SwiftUI
import ScreenCaptureKit
import BerthTerminal
@testable import BerthClient

@main @MainActor enum ConsoleChecks {
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
        let app = NSApplication.shared; app.setActivationPolicy(.accessory); app.finishLaunching()
        let root = URL(fileURLWithPath: home).appendingPathComponent("console-fixture")
        let other = root.appendingPathComponent("other")
        try FileManager.default.createDirectory(at: other, withIntermediateDirectories: true)
        // Initial discovery is a synthetic local catalogue too, not whichever
        // daily oberth happens to be installed on the developer's machine.
        let catalogue = root.appendingPathComponent("fixture-catalogue")
        try Data(#"""
#!/bin/sh
[ "$#" = 3 ] && [ "$1" = agent ] && [ "$2" = list ] && [ "$3" = --json ] || exit 97
/bin/sleep 0.5
printf '%s\n' '{"providers":[{"id":"codex","name":"Codex fixture","command":"codex","installed":true,"native_prompt":true}]}'
"""#.utf8).write(to: catalogue)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: catalogue.path)
        setenv("BERTH_BIN", catalogue.path, 1)
        // A synthetic prompt prevents machine/user identity in visual evidence.
        setenv("PROMPT", "fixture> ", 1); setenv("PS1", "fixture> ", 1)
        let registry = TerminalSessions.shared
        let env = ["HOME":root.path, "PATH":"/usr/bin:/bin"]
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
        window.isReleasedWhenClosed = false; window.contentView = host; window.makeKeyAndOrderFront(nil)
        defer { window.contentView = nil; window.close() }
        app.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
        eventually("the native test window never became key") { window.isKeyWindow }
        pump(0.25)
        func choose(_ session: TerminalSession) {
            guard let button = find(NSButton.self, in: host).first(where: { $0.accessibilityIdentifier() == "console.session." + session.id.uuidString }) else { fatalError("native shared session button missing") }
            button.performClick(nil)
            let end = Date().addingTimeInterval(5)
            while window.firstResponder !== session.terminal, Date() < end { pump(0.01) }
            if window.firstResponder !== session.terminal {
                let presentations = find(TerminalHost.self, in: host).map { host in
                    "session=\(host.session?.title ?? "nil") active=\(host.session?.id == workspace.activeSelection) intent=\(host.focusIntent?.sessionID == session.id) consumed=\(host.focusIntent?.consumed ?? false) attached=\(host.session?.terminal.superview === host)"
                }.joined(separator: "; ")
                fatalError("native focus: selected=\(session.title) workspace=\(workspace.activeSelection == session.id) key=\(window.isKeyWindow) responder=\(String(describing: window.firstResponder)) hosts: " + presentations)
            }
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
        choose(shell); type("shell-only")
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
        let shellBounds = shell.terminal.bounds, agentBounds = agent.terminal.bounds
        let shellHost = shell.terminal.superview, agentHost = agent.terminal.superview
        guard let create = find(NSButton.self, in: host).first(where: { $0.accessibilityIdentifier() == "console.newSession" }) else { fatalError("unified creation entry missing") }
        for attempt in 0..<4 {
            create.performClick(nil)
            var cancel: NSButton?
            eventually("native creation popover did not open/reopen") {
                cancel = NSApp.windows.filter(\.isVisible).compactMap(\.contentView)
                    .flatMap { find(NSButton.self, in: $0) }.first { $0.accessibilityIdentifier() == "console.cancelLaunch" }
                return cancel != nil
            }
            let targetPicker = NSApp.windows.filter(\.isVisible).compactMap(\.contentView)
                .flatMap { find(NSPopUpButton.self, in: $0) }.first { $0.accessibilityIdentifier() == "console.launchTarget" }
            require(targetPicker?.isEnabled == true, "provider discovery blocked the Shell selector")
            pump(0.03)
            require(shell.terminal.bounds == shellBounds && agent.terminal.bounds == agentBounds, "creation resized live PTYs")
            require(shell.terminal.superview === shellHost && agent.terminal.superview === agentHost, "creation remounted native hosts")
            require(registry.sessions.count == 3 && workspace.activeLayout.root == treeRoot, "creation changed runtime ownership")
            if attempt == 0 {
                eventually("synthetic provider was not loaded in the popover") {
                    NSApp.windows.filter(\.isVisible).compactMap(\.contentView).flatMap { find(NSPopUpButton.self, in: $0) }.contains { $0.titleOfSelectedItem == "Codex fixture" }
                }
                pump(0.3) // Capture settled native presentation, not the show animation.
                try captureOwnedWindow(window, to: URL(fileURLWithPath: home).appendingPathComponent("session-launcher-composited-native.png"))
            }
            cancel?.performClick(nil)
            eventually("cancel did not return focus to selected native PTY") { window.firstResponder === agent.terminal && window.isKeyWindow }
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
        TerminalWindows.shared.bringBack(agent); window.makeKeyAndOrderFront(nil)
        eventually("returning the detached terminal did not restore focus") { agent.terminal.window === window && window.firstResponder === agent.terminal }
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
        print("PASS: real native shared tabs, mixed layout identity, cwd/draft retention, independent key input, one-shot focus, detached return and background launch isolation")
    }
    static func captureOwnedWindow(_ window: NSWindow, to output: URL) throws {
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
