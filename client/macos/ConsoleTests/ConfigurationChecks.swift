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
            press("console.newShell")
            pump(0.2)
        }
        func backToShell() {
            press("console.session." + shell.id.uuidString)
            pump(0.15)
        }
        eventually("opening the worktree session did not present a shell") {
            registry.inWorktree(root.path).contains { $0.kind == "terminal" && $0.isActive }
        }
        require(!registry.inWorktree(root.path).contains { $0.kind.hasPrefix("agent:") }, "shell-first open launched an agent")
        let shell = registry.inWorktree(root.path).first { $0.kind == "terminal" }!
        let process = shell.terminal.process
        eventually("shell surface was not attached") { shell.terminal.window === window && shell.terminal.bounds.width > 40 }
        pump(0.2)
        var shellBounds = shell.terminal.bounds
        press("console.focusComposer")
        var editor: NSTextView?
        eventually("shell did not focus the session composer") {
            editor = find(NSTextView.self, in: host).first { $0.accessibilityIdentifier() == "console.sessionComposer" }
            return editor != nil && window.firstResponder === editor
        }
        func chord(_ flags: NSEvent.ModifierFlags) {
            guard let composerEditor = editor else { fatalError("session composer missing") }
            let submit = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: flags,
                timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
                characters: "\r", charactersIgnoringModifiers: "\r", isARepeat: false, keyCode: 36)!
            if window.firstResponder === composerEditor {
                if !composerEditor.performKeyEquivalent(with: submit) { composerEditor.keyDown(with: submit) }
            } else if !window.performKeyEquivalent(with: submit) {
                window.sendEvent(submit)
            }
        }
        require(shell.terminal.caretColor == .clear, "composer-focused shell should hide the terminal caret")
        state.draft = ""
        window.makeFirstResponder(shell.terminal)
        pump(0.08)
        let typed = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [],
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
            characters: "z", charactersIgnoringModifiers: "z", isARepeat: false, keyCode: 6)!
        NSApp.sendEvent(typed)
        eventually("typing in the shell surface did not bridge into the composer") { state.draft == "z" }
        backToShell()
        state.resumeAgentID = nil
        press("console.focusComposer")
        pump(0.1)
        window.makeFirstResponder(editor)
        state.draft = "/bin/sleep 60"
        chord([])
        pump(0.25)
        eventually("composer Enter did not start a foreground shell command") { shell.shellForegroundBusy }
        require(shell.terminal.caretColor != .clear, "foreground command should show the terminal caret")
        shell.terminal.send(source: shell.terminal, data: [0x03][...])
        eventually("shell interrupt did not end the foreground command") { !shell.shellForegroundBusy }
        press("console.focusComposer")
        pump(0.15)
        eventually("shell prompt did not hide the terminal caret again") { shell.terminal.caretColor == .clear }
        shellBounds = shell.terminal.bounds
        var shellFF: [UInt8] = []
        shell.terminal.onOutboundBytes = { shellFF.append(contentsOf: $0) }
        defer { shell.terminal.onOutboundBytes = nil }
        guard let composerField = editor else { fatalError("session composer missing for control-L test") }
        window.makeFirstResponder(composerField)
        state.draft = ""
        pump(0.08)
        let controlL = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: .control,
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
            characters: "l", charactersIgnoringModifiers: "l", isARepeat: false, keyCode: 37)!
        if !composerField.performKeyEquivalent(with: controlL) {
            composerField.keyDown(with: controlL)
        }
        eventually("composer control-L did not forward FF to the shell PTY") { shellFF.contains(0x0c) }
        shellFF.removeAll()
        state.draft = "ech"
        pump(0.05)
        let tab = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [],
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
            characters: "\t", charactersIgnoringModifiers: "\t", isARepeat: false, keyCode: 48)!
        if !composerField.performKeyEquivalent(with: tab) {
            composerField.keyDown(with: tab)
        }
        eventually("composer Tab did not forward draft plus HT to the shell PTY") {
            shellFF.suffix(4) == Array("ech\t".utf8)
        }
        eventually("shell tab completion did not read the prompt line back into the composer") {
            state.draft.hasPrefix("ech") && state.draft.count >= 3
        }
        state.draft = ""
        composerField.string = ""
        pump(0.05)
        eventually("configured provider was not the composer default") {
            state.providerID == "claude" && state.providerChosen
        }
        require(shell.terminal.bounds == shellBounds, "session composer resized the live shell")
        require(registry.inWorktree(root.path).filter { $0.kind.hasPrefix("agent:") }.isEmpty, "focusing the composer launched an agent")
        guard let composerEditor = editor else { fatalError("session composer missing") }
        composerEditor.insertText("configured fixture prompt", replacementRange: NSRange(location: NSNotFound, length: 0))
        eventually("native composer did not keep its draft") { state.draft == "configured fixture prompt" }
        pump(0.75)
        press("console.focusComposer")
        pump(0.08)
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
        window.makeFirstResponder(composerEditor)
        composerEditor.insertText("second-turn", replacementRange: NSRange(location: NSNotFound, length: 0))
        eventually("resume draft was not kept") { state.draft == "second-turn" }
        chord(.command)
        eventually("⌘↩ did not paste into the terminal agent") {
            state.activeSelection == agent.id && state.draft.isEmpty && state.resumeAgentID == agent.id
        }
        agent.terminal.send(source: agent.terminal, data: Array("\n".utf8)[...])
        eventually("⌘↩ paste plus Enter did not reach the agent stdin") { agentOutput.contains("AGENT:second-turn") }
        require(registry.inWorktree(root.path).filter { $0.kind.hasPrefix("agent:") }.count == 1, "⌘↩ opened another agent instead of resuming")
        require(agent.terminal.process === agentProcess && shell.terminal.process === process, "resume replaced a PTY")
        backToShell()
        press("console.focusComposer")
        eventually("return to shell did not restore the shell canvas") { state.activeSelection == shell.id }
        press("console.agentStatus." + agent.id.uuidString)
        ConsoleChecks.raiseTestWindow(NSApplication.shared, window)
        pump(0.12)
        require(state.resumeAgentID == agent.id, "agent row did not set resume target")
        eventually("agent row did not attach the same agent session") {
            state.activeSelection == agent.id && agent.terminal.process === agentProcess
                && (window.firstResponder === agent.terminal || ConsoleChecks.consoleTestQuiet)
        }
        let escape = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [],
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
            characters: "\u{1b}", charactersIgnoringModifiers: "\u{1b}", isARepeat: false, keyCode: 53)!
        NSApp.sendEvent(escape)
        pump(0.05)
        require(state.activeSelection == agent.id && agent.isActive && agent.terminal.process === agentProcess,
                "Escape must stay in the agent TUI for the provider to handle")
        backToShell()
        eventually("return to shell after agent view did not restore the shell canvas") {
            state.activeSelection == shell.id
        }
        press("console.focusComposer")
        pump(0.15)
        press("console.agentStatus." + agent.id.uuidString)
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
        pump(0.05)
        require(state.activeSelection == agent.id && agent.isActive && agent.terminal.process === agentProcess,
                "Escape after re-enter must stay in the agent TUI")
        backToShell()
        eventually("second return to shell did not restore the shell canvas") { state.activeSelection == shell.id }
        press("console.focusComposer")
        pump(0.15)
        composerEditor.insertText("another-agent", replacementRange: NSRange(location: NSNotFound, length: 0))
        chord([.command, .shift])
        eventually("⇧⌘↩ did not launch a second agent session") {
            registry.inWorktree(root.path).filter { $0.kind.hasPrefix("agent:") }.count == 2
        }
        pump(0.15)
        shell.terminal.send(source: shell.terminal, data: Array("printf 'CONFIGURED-SHELL\\n'\n".utf8)[...])
        eventually("file-enabled shell integration did not produce command metadata") { shell.commandBlocks.contains { $0.command?.contains("CONFIGURED-SHELL") == true && $0.endedAt != nil } }
        eventually("finished shell command should offer send-to-agent") {
            find(NSButton.self, in: host).contains { $0.accessibilityIdentifier() == "console.commandBlock.sendToAgent" }
        }
        guard let sendToAgent = find(NSButton.self, in: host).first(where: {
            $0.accessibilityIdentifier() == "console.commandBlock.sendToAgent"
        }) else { fatalError("command block missing send-to-agent control") }
        sendToAgent.performClick(nil)
        eventually("send-to-agent should attach shell output to the composer") {
            find(NSView.self, in: host).contains {
                $0.accessibilityIdentifier() == "console.composerAttachment"
                    && ($0.accessibilityValue() as? String)?.contains("CONFIGURED-SHELL") == true
            }
        }
        let bytes = try Data(contentsOf: configuration)
        require(String(decoding: bytes, as: UTF8.self).contains("claude"), "console modified the user's configuration")
        try Data(#"{"schemaVersion":1,"defaultAgent":"pi","shellIntegration":false}"#.utf8).write(to: configuration, options: .atomic)
        eventually("console did not consume updated future-session defaults") { registry.defaultProviderID == "pi" && !settings.shellIntegration }
        require(state.providerID == "claude" && registry.workspace(later.path).providerID == "pi", "default Agent change rewrote existing console or missed the next one")
        require(shell.terminal.process === process && shell.isActive && agent.isActive, "configuration restarted a running PTY")
        dismissCreationPopoverIfPresent()
        ConsoleChecks.raiseTestWindow(NSApplication.shared, window)
        pump(0.5)
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
