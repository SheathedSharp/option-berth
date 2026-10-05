import AppKit
import SwiftUI
import BerthTerminal
@testable import BerthClient

extension CommandFixture {
    func run(home: String, berth: String) async throws {
        NSApp.setActivationPolicy(.regular); NSApp.activate(ignoringOtherApps: true)
        try expect(services.projects.count == 2, "fixture worktrees were not declared")
        try await eventually("native command menu not installed") {
            items(NSApp.mainMenu).contains { $0.title == "1 · alpha" && $0.keyEquivalent == "1" } && NSApp.keyWindow != nil
        }
        let window = NSApp.keyWindow!
        key("2")
        try await eventually("Cmd+2 did not select the second worktree") { views.scope == .services("beta") }
        key("g", modifiers: [.command, .option])
        try await eventually("Git mnemonic did not use the native menu") { views.scope == .code("beta") }
        key("1")
        try await eventually("worktree selection reset the Git module") { views.scope == .code("alpha") }
        views.projectQuery = "SECOND"
        // Closed AppKit menus materialize labels when tracking starts. Verify
        // the keyboard route before opening the menu, then its visible label.
        try await Task.sleep(nanoseconds: 50_000_000)
        key("1")
        try await eventually("filtered Cmd+1 selected a hidden project") { views.scope == .code("beta") }
        try trackMenu("Worktrees", window: window)
        try expect(items(NSApp.mainMenu).contains { $0.title == "1 · beta" }, "opened menu does not match the filtered rail")
        try await rejected("9", "out-of-range worktree command executed")
        views.projectQuery = ""
        key("t", modifiers: [.command, .option])
        try await eventually("Console mnemonic failed") { views.scope == .terminal("beta") }
        key("s", modifiers: [.command, .option])
        try await eventually("Services mnemonic failed") { views.scope == .services("beta") }
        key("b")
        try await eventually("sidebar mnemonic failed") { !views.railVisible }
        key("b")
        try await eventually("sidebar mnemonic failed to restore rail") { views.railVisible }

        let directory = URL(fileURLWithPath: berth).appendingPathComponent("config")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let file = directory.appendingPathComponent("keybindings.json")
        try Data(#"{"schemaVersion":1,"bindings":{"code":{"key":"j","option":true}}}"#.utf8).write(to: file, options: .atomic)
        try await eventually("file keybinding was not observed") { shortcuts.shortcut(.code).key == "j" }
        try await Task.sleep(nanoseconds: 50_000_000)
        try await rejected("g", "retired default remained active", modifiers: [.command, .option])
        key("j", modifiers: [.command, .option])
        try await eventually("custom key failed to activate Git") { views.scope == .code("beta") }
        try trackMenu(MenuBar.viewTitle, window: window)
        try expect(items(NSApp.mainMenu).contains { $0.title == WorkspaceAction.code.title && $0.keyEquivalent == "j" }, "opened menu lost the custom shortcut")
        print("Native check: inputChecks"); fflush(stdout)
        try await inputChecks(window: window, home: home)
        print("Native check: paletteChecks"); fflush(stdout)
        try await paletteChecks(window: window)
        print("Native check: connectionChecks"); fflush(stdout)
        try await connectionChecks(window: window)
        try expect(TerminalSessions.shared.sessions.isEmpty, "command navigation started a PTY")
        try FileManager.default.removeItem(at: file)
        try await eventually("deleting keybindings did not restore defaults") { shortcuts.shortcut(.code).key == "g" }
        // The model publishes before SwiftUI commits its native menu tree.
        // Send exactly one key after the native equivalent (not merely the
        // decoded setting) is ready. Never open a menu to make the test pass.
        try await eventually("native default equivalent was not restored") {
            items(NSApp.mainMenu).contains {
                $0.title == WorkspaceAction.code.title && $0.keyEquivalent == "g"
                    && $0.keyEquivalentModifierMask.contains([.command, .option])
            }
        }
        key("g", modifiers: [.command, .option])
        try await eventually("restored default shortcut did not route") { views.scope == .code("beta") }
        try trackMenu(MenuBar.viewTitle, window: window)
        try expect(items(NSApp.mainMenu).contains { $0.title == WorkspaceAction.code.title && $0.keyEquivalent == "g" }, "opened menu did not restore default equivalent")
        print("Native check: gitFindChecks"); fflush(stdout)
        try await gitFindChecks(window: window, configuration: file)
        window.contentView?.layoutSubtreeIfNeeded()
        if let host = window.contentView, let bitmap = host.bitmapImageRepForCachingDisplay(in: host.bounds) {
            host.cacheDisplay(in: host.bounds, to: bitmap)
            if let png = bitmap.representation(using: .png, properties: [:]) {
                try png.write(to: URL(fileURLWithPath: berth).appendingPathComponent("commands-native.png"))
            }
        }
    }

    func inputChecks(window: NSWindow, home: String) async throws {
        let editor = NSTextView(frame: NSRect(x: 20, y: 10, width: 240, height: 60))
        editor.isEditable = true; editor.allowsUndo = true
        window.contentView!.addSubview(editor); window.makeFirstResponder(editor)
        editor.setMarkedText("拼音", selectedRange: NSRange(location: 2, length: 0), replacementRange: NSRange(location: NSNotFound, length: 0))
        try expect(editor.hasMarkedText(), "native editor did not enter composition")
        try await rejected("1", "worktree key consumed editor composition")
        editor.unmarkText(); editor.string = "native edit"; editor.selectAll(nil)
        key("c")
        try expect(NSPasteboard.general.string(forType: .string) == "native edit", "native Copy shortcut was lost")
        editor.string = ""; key("v")
        try await eventually("native Paste shortcut was lost") { editor.string == "native edit" }
        key("z")
        try await eventually("native Undo shortcut was lost") { editor.string.isEmpty }
        editor.removeFromSuperview()

        let session = TerminalSession(worktree: home, title: "IME fixture")
        session.terminal.frame = NSRect(x: 20, y: 10, width: 280, height: 70)
        window.contentView!.addSubview(session.terminal); window.makeFirstResponder(session.terminal)
        session.terminal.setMarkedText("中文输入", selectedRange: NSRange(location: 4, length: 0), replacementRange: NSRange(location: NSNotFound, length: 0))
        try expect(session.terminal.hasMarkedText(), "terminal did not enter native composition")
        try await rejected("1", "worktree key consumed terminal composition")
        session.terminal.unmarkText(); session.terminal.removeFromSuperview(); window.makeFirstResponder(nil)
        try expect(!session.isActive, "IME test launched a process")

        TerminalWindows.shared.detach(session)
        defer { TerminalWindows.shared.bringBack(session) }
        try await eventually("detached terminal did not become key") { TerminalWindows.shared.windows[session.id]?.isKeyWindow == true }
        try await rejected("1", "detached terminal redirected the selected worktree")
        key("p", modifiers: [.command, .shift])
        try expect(!views.showingActions, "detached terminal opened the workspace palette")
        key("f")
        try await eventually("detached terminal lost its native Find action") { performed.last == .find }
        TerminalWindows.shared.bringBack(session)
        window.makeKeyAndOrderFront(nil)
        try await eventually("returning from detached terminal lost the workspace") { window.isKeyWindow && TerminalWindows.shared.windows[session.id] == nil }

        let sheet = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 300, height: 100), styleMask: [.titled], backing: .buffered, defer: false)
        sheet.isReleasedWhenClosed = false
        window.beginSheet(sheet, completionHandler: nil)
        try await eventually("native sheet did not attach") { window.attachedSheet === sheet }
        try await rejected("1", "workspace navigation escaped a native sheet")
        window.endSheet(sheet); sheet.orderOut(nil)
        window.makeKeyAndOrderFront(nil)
        try await eventually("native sheet did not detach") { window.attachedSheet == nil && window.isKeyWindow }
    }

    func paletteChecks(window: NSWindow) async throws {
        let count = performed.count
        key("p", modifiers: [.command, .shift])
        try await eventually("command palette did not open") { views.showingActions && window.attachedSheet != nil }
        guard let sheet = window.attachedSheet, let root = sheet.contentView,
              let field = descendants(root).compactMap({ $0 as? NSTextField }).first(where: { $0.placeholderString == "搜索服务、Git、终端或操作" }) else {
            throw Failure(message: "native palette search field not found")
        }
        sheet.makeFirstResponder(field)
        field.stringValue = "services"
        if let editor = field.currentEditor() as? NSTextView {
            editor.string = "services"
            NotificationCenter.default.post(name: NSText.didChangeNotification, object: editor)
        }
        field.delegate?.controlTextDidChange?(Notification(name: NSControl.textDidChangeNotification, object: field))
        try await Task.sleep(nanoseconds: 50_000_000)
        let event = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [], timestamp: ProcessInfo.processInfo.systemUptime,
            windowNumber: sheet.windowNumber, context: nil, characters: "\r", charactersIgnoringModifiers: "\r", isARepeat: false, keyCode: 36)!
        if !sheet.performKeyEquivalent(with: event) { sheet.sendEvent(event) }
        try await eventually("palette selection was lost before native dismissal") {
            !views.showingActions && window.attachedSheet == nil && views.scope == .services("beta") && performed.count == count + 1
        }
        try expect(performed.last == .services, "palette dispatched another action")
    }

    func connectionChecks(window: NSWindow) async throws {
        // Cancel the genuine directory chooser while its native modal loop runs.
        // Never select a directory or start a manifest operation.
        var observedPanel = false
        var correctPanel = false
        let before = views.scope
        let deadline = Date().addingTimeInterval(5)
        let timer = Timer(timeInterval: 0.02, repeats: true) { [weak self] timer in
            MainActor.assumeIsolated {
                if let panel = NSApp.modalWindow as? NSOpenPanel {
                    observedPanel = true
                    correctPanel = panel.canChooseDirectories && !panel.canChooseFiles && !panel.allowsMultipleSelection
                    _ = self?.key("1")
                    panel.cancel(nil); timer.invalidate()
                } else if Date() >= deadline {
                    fputs("CommandChecks: directory chooser deadline exceeded\n", stderr); exit(1)
                }
            }
        }
        RunLoop.main.add(timer, forMode: .common); RunLoop.main.add(timer, forMode: .modalPanel)
        defer { timer.invalidate() }
        key("n")
        try await eventually("Cmd+N did not open and cancel the native directory chooser") { observedPanel && !views.projectOperationPending }
        try expect(correctPanel && views.scope == before, "directory chooser allowed file selection or concurrent navigation")
        // runModal returning and clearing the operation are not the native
        // key-window handoff. Wait for this exact owner, without forcing focus
        // or dispatching the next command into the dismissed panel.
        try await eventually("cancelled chooser did not return focus to its owner") {
            NSApp.modalWindow == nil && NSApp.keyWindow === window && window.isKeyWindow
        }
    }
}

// Native menu tracking calls its delegate and materializes SwiftUI's deferred
// menu content. The timer cancels this test-owned menu without selecting an item.
extension CommandFixture {
    func trackMenu(_ title: String, window: NSWindow) throws {
        guard let menu = NSApp.mainMenu?.items.first(where: { $0.title == title })?.submenu,
              let view = window.contentView else { throw Failure(message: "missing native menu " + title) }
        // Initial menu population may take longer than one timer interval. A
        // one-shot cancel can run before tracking starts and strand popUp().
        // Cancel only this test menu, with the same bounded five-second limit.
        let deadline = Date().addingTimeInterval(5)
        let timer = Timer(timeInterval: 0.04, repeats: true) { _ in
            MainActor.assumeIsolated {
                if Date() >= deadline {
                    fputs("CommandChecks: menu tracking cancellation deadline exceeded\n", stderr); exit(1)
                }
                menu.cancelTrackingWithoutAnimation()
            }
        }
        RunLoop.main.add(timer, forMode: .eventTracking)
        defer { timer.invalidate() }
        menu.popUp(positioning: nil, at: NSPoint(x: 30, y: 30), in: view)
    }
}
