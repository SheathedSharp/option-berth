import AppKit
import SwiftUI
import Darwin
import BerthTerminal
@testable import BerthClient

extension ConsoleChecks {
    static func configurationChecks(parentRoot: URL) throws {
        let settings = UISettings.shared
        let registry = TerminalSessions.shared
        let root = parentRoot.appendingPathComponent("configured-worktree")
        let later = parentRoot.appendingPathComponent("later-worktree")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: later, withIntermediateDirectories: true)
        let directory = ClientConfigurationIO.directory()
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let configuration = directory.appendingPathComponent("settings.json")
        let binary = parentRoot.appendingPathComponent("fixture-oberth")
        // Only our controlled catalogue/plan is executed. The native Agent is a
        // test shell, never a provider model, account, approval bypass or network.
        try Data(#"""
#!/usr/bin/python3
import json, sys
args = sys.argv[1:]
if args == ['agent', 'list', '--json']:
    print(json.dumps({'providers': [dict(id=x, name=x.title()+' fixture', command=x, installed=True, native_prompt=True) for x in ['codex', 'claude', 'pi']]}))
elif args[:3] == ['agent', 'plan', 'claude'] and '--prompt-stdin' in args:
    sys.stdin.read()  # literal stdin from the composer; content varies per launch
    root = args[args.index('--worktree')+1]
    mode = args[args.index('--mode')+1]
    assert mode == 'native'
    print(json.dumps(dict(provider='claude', executable='/bin/sh', arguments=['-c', 'printf "CONFIGURED-AGENT\n"; while IFS= read -r line; do printf "AGENT:%s\n" "$line"; done'], worktree=root, mode=mode)))
else:
    raise SystemExit(97)
"""#.utf8).write(to: binary)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: binary.path)
        let original = ProcessInfo.processInfo.environment["BERTH_BIN"]
        setenv("BERTH_BIN", binary.path, 1)
        defer {
            if let original { setenv("BERTH_BIN", original, 1) } else { unsetenv("BERTH_BIN") }
            for session in registry.inWorktree(root.path) {
                TerminalWindows.shared.bringBack(session)
                if session.isActive { session.stop(force: true) }
            }
            eventually("configured test PTYs were not reaped") { registry.inWorktree(root.path).allSatisfy { !$0.isActive } }
            for session in registry.inWorktree(root.path) { registry.remove(session) }
            registry.forgetWorkspace(root.path); registry.forgetWorkspace(later.path)
            try? FileManager.default.removeItem(at: configuration)
            try? FileManager.default.removeItem(at: binary)
        }
        let old = registry.workspace(parentRoot.path)
        let oldProvider = old.providerID, oldDraft = old.draft
        try Data(#"{"schemaVersion":1,"defaultAgent":"claude","shellIntegration":true}"#.utf8).write(to: configuration, options: .atomic)
        eventually("console configuration was not loaded") { settings.configuration.preferences.defaultAgent == "claude" && settings.shellIntegration }
        ClientSessionPreferences.apply(settings.configuration.preferences, to: registry)
        let state = registry.workspace(root.path)
        require(state.providerID == "claude", "new console ignored the configured provider")
        require(old.providerID == oldProvider && old.draft == oldDraft, "configuration rewrote an existing console")
        let host = NSHostingView(rootView: ConfiguredConsole(root: root.path, settings: settings))
        host.sizingOptions = []
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 920, height: 540), styleMask: [.titled, .resizable], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false; window.contentView = host
        ConsoleChecks.mountTestWindow(window)
        ConsoleChecks.raiseTestWindow(NSApplication.shared, window)
        defer { window.contentView = nil; window.close() }
        eventually("configured console did not become ready") { ConsoleChecks.testWindowAcceptsInput(window) }
        pump(0.25)
        func press(_ id: String) {
            eventually("configured console did not mount action " + id) {
                find(NSButton.self, in: host).contains { $0.accessibilityIdentifier() == id }
            }
            guard let button = find(NSButton.self, in: host).first(where: { $0.accessibilityIdentifier() == id }) else { fatalError("missing native console action " + id) }
            button.performClick(nil)
        }
        func launchSurfaces() -> [NSView] {
            var roots = NSApp.windows.compactMap(\.contentView)
            if let content = window.contentView { roots.append(content) }
            return roots
        }
        func dismissCreationPopoverIfPresent() {
            let cancel = launchSurfaces()
                .flatMap { view in ConsoleChecks.find(NSButton.self, in: view) }
                .first { $0.accessibilityIdentifier() == "console.cancelLaunch" }
            guard let cancel else { return }
            cancel.performClick(nil)
            pump(0.1)
        }
        func openShell() {
            dismissCreationPopoverIfPresent()
            var picker: NSPopUpButton?
            for attempt in 0..<4 {
                press("console.newSession")
                pump(attempt == 0 ? 0.25 : 0.15)
                let end = Date().addingTimeInterval(5)
                while picker == nil, Date() < end {
                    pump(0.05)
                    picker = launchSurfaces().flatMap { ConsoleChecks.find(NSPopUpButton.self, in: $0) }
                        .first { $0.accessibilityIdentifier() == "console.launchTarget" }
                }
                if picker != nil { break }
                dismissCreationPopoverIfPresent()
            }
            require(picker != nil, "unified target picker missing")
            guard let picker, let item = picker.itemArray.first(where: { $0.representedObject as? String == "shell" }),
                  let action = picker.action else { fatalError("Shell target unavailable") }
            picker.select(item); NSApp.sendAction(action, to: picker.target, from: picker)
            pump(0.12)
            var launch: NSButton?
            eventually("unified Shell launch action missing") {
                launch = launchSurfaces().flatMap { ConsoleChecks.find(NSButton.self, in: $0) }
                    .first { $0.accessibilityIdentifier() == "console.launch" && $0.title == "打开 Shell" }
                return launch?.isEnabled == true
            }
            launch?.performClick(nil)
            pump(0.2)
        }
        eventually("opening the worktree session did not present a shell") {
            registry.inWorktree(root.path).contains { $0.kind == "terminal" && $0.isActive }
        }
        require(!registry.inWorktree(root.path).contains { $0.kind.hasPrefix("agent:") }, "shell-first open launched an agent")
        let shell = registry.inWorktree(root.path).first { $0.kind == "terminal" }!
        let process = shell.terminal.process
        eventually("shell surface was not attached") { shell.terminal.window === window && shell.terminal.bounds.width > 40 }
        pump(0.2)
        let shellBounds = shell.terminal.bounds
        press("console.focusComposer")
        var editor: NSTextView?
        eventually("shell did not focus the session composer") {
            editor = find(NSTextView.self, in: host).first { $0.accessibilityIdentifier() == "console.sessionComposer" }
            return editor != nil && window.firstResponder === editor
        }
        eventually("configured provider was not the composer default") {
            find(NSButton.self, in: host).contains { $0.accessibilityIdentifier() == "console.agentChip" && $0.title == "Claude fixture" }
        }
        require(shell.terminal.bounds == shellBounds, "session composer resized the live shell")
        require(registry.inWorktree(root.path).filter { $0.kind.hasPrefix("agent:") }.isEmpty, "focusing the composer launched an agent")
        guard let editor else { fatalError("session composer missing") }
        editor.insertText("configured fixture prompt", replacementRange: NSRange(location: NSNotFound, length: 0))
        eventually("native composer did not keep its draft") { state.draft == "configured fixture prompt" }
        func chord(_ flags: NSEvent.ModifierFlags) {
            let submit = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: flags,
                timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
                characters: "\r", charactersIgnoringModifiers: "\r", isARepeat: false, keyCode: 36)!
            if !window.performKeyEquivalent(with: submit) { window.sendEvent(submit) }
        }
        chord(.command)
        eventually("⌘↩ did not launch the first configured Agent through its plan") {
            registry.inWorktree(root.path).contains { $0.kind == "agent:claude:native" && $0.isActive }
        }
        let agent = registry.inWorktree(root.path).first { $0.kind == "agent:claude:native" }!
        let agentProcess = agent.terminal.process
        eventually("new agent session did not stay beside the shell") {
            state.draft.isEmpty && state.activeSelection == shell.id && state.resumeAgentID == agent.id
        }
        require(shell.terminal.process === process && shell.isActive, "agent launch replaced the shell")
        require(find(NSButton.self, in: host).contains { $0.accessibilityIdentifier() == "console.agentStatus." + agent.id.uuidString },
                "new agent was not listed in the status column")
        var agentOutput = ""
        agent.terminal.onBytes = { agentOutput += String(decoding: $0, as: UTF8.self) }
        window.makeFirstResponder(editor)
        editor.insertText("second-turn", replacementRange: NSRange(location: NSNotFound, length: 0))
        eventually("resume draft was not kept") { state.draft == "second-turn" }
        chord(.command)
        eventually("⌘↩ did not deliver a new turn to the selected agent stdin") { agentOutput.contains("AGENT:second-turn") }
        require(registry.inWorktree(root.path).filter { $0.kind.hasPrefix("agent:") }.count == 1, "⌘↩ opened another agent instead of resuming")
        require(agent.terminal.process === agentProcess && shell.terminal.process === process, "resume replaced a PTY")
        require(state.activeSelection == shell.id, "resume switched the canvas into the agent GUI")
        press("console.agentStatus." + agent.id.uuidString)
        require(state.resumeAgentID == agent.id && state.activeSelection == shell.id, "status preview did not set resume target while keeping the shell canvas")
        press("console.agentEnter." + agent.id.uuidString)
        ConsoleChecks.raiseTestWindow(NSApplication.shared, window)
        pump(0.12)
        eventually("status enter did not attach the same agent session") {
            state.activeSelection == agent.id && agent.terminal.process === agentProcess
                && (window.firstResponder === agent.terminal || ConsoleChecks.consoleTestQuiet)
        }
        let escape = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [],
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
            characters: "\u{1b}", charactersIgnoringModifiers: "\u{1b}", isARepeat: false, keyCode: 53)!
        NSApp.sendEvent(escape)
        eventually("Escape did not return to the live shell") {
            state.activeSelection == shell.id && window.firstResponder === editor
                && agent.isActive && agent.terminal.process === agentProcess
        }
        press("console.reenter." + agent.id.uuidString)
        ConsoleChecks.raiseTestWindow(NSApplication.shared, window)
        pump(0.12)
        eventually("re-enter did not attach the same agent session") {
            state.activeSelection == agent.id && agent.terminal.process === agentProcess && shell.isActive && shell.terminal.process === process
                && (window.firstResponder === agent.terminal || ConsoleChecks.consoleTestQuiet)
        }
        let escapeAgain = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [],
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
            characters: "\u{1b}", charactersIgnoringModifiers: "\u{1b}", isARepeat: false, keyCode: 53)!
        NSApp.sendEvent(escapeAgain)
        eventually("Escape after re-enter did not return to the live shell") {
            state.activeSelection == shell.id && window.firstResponder === editor
                && agent.isActive && agent.terminal.process === agentProcess
        }
        editor.insertText("another-agent", replacementRange: NSRange(location: NSNotFound, length: 0))
        chord([.command, .shift])
        eventually("⇧⌘↩ did not launch a second agent session") {
            registry.inWorktree(root.path).filter { $0.kind.hasPrefix("agent:") }.count == 2
        }
        pump(0.15)
        shell.terminal.send(source: shell.terminal, data: Array("printf 'CONFIGURED-SHELL\\n'\n".utf8)[...])
        eventually("file-enabled shell integration did not produce command metadata") { shell.commandBlocks.contains { $0.command?.contains("CONFIGURED-SHELL") == true && $0.endedAt != nil } }
        let bytes = try Data(contentsOf: configuration)
        require(String(decoding: bytes, as: UTF8.self).contains("claude"), "console modified the user's configuration")
        try Data(#"{"schemaVersion":1,"defaultAgent":"pi","shellIntegration":false}"#.utf8).write(to: configuration, options: .atomic)
        eventually("console did not consume updated future-session defaults") { registry.defaultProviderID == "pi" && !settings.shellIntegration }
        require(state.providerID == "claude" && registry.workspace(later.path).providerID == "pi", "default Agent change rewrote existing console or missed the next one")
        require(shell.terminal.process === process && shell.isActive && agent.isActive, "configuration restarted a running PTY")
        dismissCreationPopoverIfPresent()
        eventually("provider catalogue was not ready for a second shell") {
            find(NSButton.self, in: host).contains { $0.accessibilityIdentifier() == "console.agentChip" }
        }
        ConsoleChecks.raiseTestWindow(NSApplication.shared, window)
        pump(0.25)
        openShell()
        if registry.inWorktree(root.path).filter({ $0.kind == "terminal" }).count < 2 {
            press("console.newShell")
        }
        eventually("second explicit Shell action missing") { registry.inWorktree(root.path).filter { $0.kind == "terminal" }.count == 2 }
        let plain = registry.inWorktree(root.path).last { $0.kind == "terminal" }!
        var output = ""
        plain.terminal.onBytes = { output += String(decoding: $0, as: UTF8.self) }
        pump(0.15)
        plain.terminal.send(source: plain.terminal, data: Array("printf 'PLAIN-SHELL-DONE\\n'\n".utf8)[...])
        eventually("plain shell fixture command did not finish") { output.contains("PLAIN-SHELL-DONE") }
        require(plain.commandBlocks.isEmpty && shell.terminal.process === process, "new-shell setting modified existing integration or ignored opt-out")
        pump()
        print("PASS: shell-first open; composer resume vs new agent; status inspect; return and re-enter keep both PTYs; file defaults; zsh integration on/off")
        try captureOwnedWindow(window, to: directory.deletingLastPathComponent().appendingPathComponent("console-configured-composited-native.png"))
    }
}

private struct ConfiguredConsole: View {
    let root: String
    @ObservedObject var settings: UISettings
    var body: some View {
        WorkspaceConsole(root: root)
            .preferredColorScheme(settings.colorScheme)
            .modifier(ClientMotionPreferences(settings: settings))
            .onReceive(settings.$configuration) { ClientSessionPreferences.apply($0.preferences, to: .shared) }
    }
}
