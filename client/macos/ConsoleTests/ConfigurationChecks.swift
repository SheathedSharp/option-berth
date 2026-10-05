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
    assert sys.stdin.read() == 'configured fixture prompt'
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
        NSApp.activate(ignoringOtherApps: true); window.makeKeyAndOrderFront(nil)
        defer { window.contentView = nil; window.close() }
        eventually("configured console did not become key") { window.isKeyWindow }
        func press(_ id: String) {
            eventually("configured console did not mount action " + id) {
                find(NSButton.self, in: host).contains { $0.accessibilityIdentifier() == id }
            }
            guard let button = find(NSButton.self, in: host).first(where: { $0.accessibilityIdentifier() == id }) else { fatalError("missing native console action " + id) }
            button.performClick(nil)
        }
        press("console.newAgent")
        eventually("configured provider did not reach the native Agent picker") {
            find(NSPopUpButton.self, in: host).contains { $0.titleOfSelectedItem == "Claude fixture" }
        }
        require(registry.inWorktree(root.path).isEmpty, "opening the Agent composer launched a process")
        guard let editor = find(NSTextView.self, in: host).first(where: { $0.isEditable }) else { fatalError("configured Agent composer missing") }
        window.makeFirstResponder(editor)
        editor.insertText("configured fixture prompt", replacementRange: NSRange(location: NSNotFound, length: 0))
        eventually("native composer did not keep its draft") { state.draft == "configured fixture prompt" }
        let submit = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: .command,
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
            characters: "\r", charactersIgnoringModifiers: "\r", isARepeat: false, keyCode: 36)!
        if !window.performKeyEquivalent(with: submit) { window.sendEvent(submit) }
        eventually("Cmd+Enter did not launch the configured Agent through its plan") {
            registry.inWorktree(root.path).contains { $0.kind == "agent:claude:native" && $0.isActive }
        }
        let agent = registry.inWorktree(root.path).first { $0.kind == "agent:claude:native" }!
        eventually("Agent launch did not hand off to its own native terminal") { window.firstResponder === agent.terminal && state.draft.isEmpty }
        press("console.newShell")
        eventually("configured Shell action did not create a PTY") { registry.inWorktree(root.path).contains { $0.kind == "terminal" } }
        let shell = registry.inWorktree(root.path).first { $0.kind == "terminal" }!
        let process = shell.terminal.process
        pump(0.15)
        shell.terminal.send(source: shell.terminal, data: Array("printf 'CONFIGURED-SHELL\\n'\n".utf8)[...])
        eventually("file-enabled shell integration did not produce command metadata") { shell.commandBlocks.contains { $0.command?.contains("CONFIGURED-SHELL") == true && $0.endedAt != nil } }
        let bytes = try Data(contentsOf: configuration)
        require(String(decoding: bytes, as: UTF8.self).contains("claude"), "console modified the user's configuration")
        try Data(#"{"schemaVersion":1,"defaultAgent":"pi","shellIntegration":false}"#.utf8).write(to: configuration, options: .atomic)
        eventually("console did not consume updated future-session defaults") { registry.defaultProviderID == "pi" && !settings.shellIntegration }
        require(state.providerID == "claude" && registry.workspace(later.path).providerID == "pi", "default Agent change rewrote existing console or missed the next one")
        require(shell.terminal.process === process && shell.isActive && agent.isActive, "configuration restarted a running PTY")
        press("console.newShell")
        eventually("second explicit Shell action missing") { registry.inWorktree(root.path).filter { $0.kind == "terminal" }.count == 2 }
        let plain = registry.inWorktree(root.path).last { $0.kind == "terminal" }!
        var output = ""
        plain.terminal.onBytes = { output += String(decoding: $0, as: UTF8.self) }
        pump(0.15)
        plain.terminal.send(source: plain.terminal, data: Array("printf 'PLAIN-SHELL-DONE\\n'\n".utf8)[...])
        eventually("plain shell fixture command did not finish") { output.contains("PLAIN-SHELL-DONE") }
        require(plain.commandBlocks.isEmpty && shell.terminal.process === process, "new-shell setting modified existing integration or ignored opt-out")
        pump()
        if let image = host.bitmapImageRepForCachingDisplay(in: host.bounds) {
            host.cacheDisplay(in: host.bounds, to: image)
            try image.representation(using: .png, properties: [:])!.write(to: directory.deletingLastPathComponent().appendingPathComponent("console-configured-native.png"))
        }
        print("PASS: file defaults -> native Agent picker -> Cmd+Enter plan -> native PTY; new zsh integration on/off; existing provider/draft/process identity preserved")
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
