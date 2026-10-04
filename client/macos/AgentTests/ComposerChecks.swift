import AppKit
import BerthAgent

@MainActor
extension AgentChecks {
    static func composerChecks() throws {
        let editor = AgentComposerTextView(frame: NSRect(x: 0, y: 0, width: 640, height: 180))
        editor.isRichText = false
        editor.allowsUndo = true
        var submissions = 0
        var nativeRequests = 0
        editor.onSubmit = { submissions += 1 }
        editor.onNative = { nativeRequests += 1 }
        let window = NSWindow(contentRect: editor.frame, styleMask: [.titled, .closable],
                              backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.title = "option-berth · isolated composer check"
        window.contentView = editor
        NSApp.setActivationPolicy(.regular)
        window.makeKeyAndOrderFront(nil)
        defer { window.close() }
        try check(window.makeFirstResponder(editor), "composer did not become first responder")
        func event(_ flags: NSEvent.ModifierFlags, keyCode: UInt16 = 36, text: String = "\r") -> NSEvent {
            NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: flags,
                timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber,
                context: nil, characters: text, charactersIgnoringModifiers: text,
                isARepeat: false, keyCode: keyCode)!
        }
        window.sendEvent(event(.command))
        try check(submissions == 1 && nativeRequests == 0, "Cmd+Return did not submit exactly once")
        window.sendEvent(event([.command, .shift]))
        try check(submissions == 1 && nativeRequests == 1, "Shift+Cmd+Return did not request native focus")
        editor.string = "first"
        editor.setSelectedRange(NSRange(location: 5, length: 0))
        window.sendEvent(event([]))
        try check(editor.string == "first\n" && submissions == 1, "ordinary Return must insert a newline")
        editor.setMarkedText("输入中", selectedRange: NSRange(location: 3, length: 0),
                             replacementRange: NSRange(location: NSNotFound, length: 0))
        try check(editor.hasMarkedText(), "marked-text fixture was not installed")
        window.sendEvent(event(.command))
        try check(submissions == 1, "IME composition accidentally submitted a prompt")
        editor.unmarkText()
        // This check runs synchronously, unlike separate user input events.
        // Close the fixture's earlier event group before testing one edit's undo.
        guard let undo = editor.undoManager else { throw FailedCheck(detail: "missing native undo manager") }
        while undo.groupingLevel > 0 { undo.endUndoGrouping() }
        undo.removeAllActions()
        undo.groupsByEvent = false
        editor.string = "native editing"
        editor.breakUndoCoalescing()
        undo.beginUndoGrouping()
        editor.selectAll(nil)
        editor.insertText("replacement", replacementRange: editor.selectedRange())
        undo.endUndoGrouping()
        editor.breakUndoCoalescing()
        undo.undo()
        try check(editor.string == "native editing", "native select/edit/undo was broken")
        for flags: NSEvent.ModifierFlags in [[.command, .option], [.command, .control], .shift, []] {
            try check(AgentComposerTextView.action(keyCode: 36, modifiers: flags,
                       composing: false, editable: true) == nil, "unrelated modifier was intercepted")
        }
        try check(AgentComposerTextView.action(keyCode: 76, modifiers: [.command, .numericPad],
                   composing: false, editable: true) == .submit, "keypad Enter was lost")
        try check(AgentComposerTextView.action(keyCode: 36, modifiers: [.command, .capsLock],
                   composing: false, editable: true) == .submit, "Caps Lock changed shortcut meaning")
        try check(AgentComposerTextView.action(keyCode: 36, modifiers: .command,
                   composing: true, editable: true) == nil, "marked text shortcut guard failed")
        editor.isEditable = false
        _ = editor.performKeyEquivalent(with: event(.command))
        try check(submissions == 1, "disabled composer submitted")
        editor.isEditable = true
        _ = window.makeFirstResponder(nil)
        _ = editor.performKeyEquivalent(with: event(.command))
        try check(submissions == 1 && nativeRequests == 1, "unfocused composer stole a shortcut")
        print("PASS: native NSWindow composer keys, multiline Return, marked-text guard, focus isolation, selection/undo, modifiers")
    }
}
