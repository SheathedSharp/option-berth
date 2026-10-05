import AppKit
import SwiftUI

/// Only the composer owns these shortcuts. The agent's native terminal never
/// installs them, so its Return, Escape, control keys and approvals stay native.
public final class AgentComposerTextView: NSTextView {
    public var onSubmit: (() -> Void)?
    public var onNative: (() -> Void)?
    public var focusOnAttach = false
    public override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        if focusOnAttach, let window, window.isKeyWindow, window.makeFirstResponder(self) { focusOnAttach = false }
    }

    public enum Action { case submit, native }
    public static func action(keyCode: UInt16, modifiers: NSEvent.ModifierFlags,
                              composing: Bool, editable: Bool) -> Action? {
        guard editable, !composing, keyCode == 36 || keyCode == 76 else { return nil }
        let keys = modifiers.intersection([.command, .shift, .control, .option])
        if keys == .command { return .submit }
        if keys == [.command, .shift] { return .native }
        return nil
    }

    private func handle(_ event: NSEvent) -> Bool {
        guard window?.firstResponder === self,
              let action = Self.action(keyCode: event.keyCode, modifiers: event.modifierFlags,
                                       composing: hasMarkedText(), editable: isEditable) else { return false }
        switch action {
        case .submit: onSubmit?()
        case .native: onNative?()
        }
        return true
    }
    public override func performKeyEquivalent(with event: NSEvent) -> Bool {
        if handle(event) { return true }
        return super.performKeyEquivalent(with: event)
    }
    public override func keyDown(with event: NSEvent) {
        if !handle(event) { super.keyDown(with: event) }
    }
}

public struct AgentComposer: NSViewRepresentable {
    @Binding private var text: String
    private let enabled: Bool
    private let font: NSFont
    private let foreground: NSColor
    private let focusOnAttach: Bool
    private let onSubmit: () -> Void
    private let onNative: () -> Void

    public init(text: Binding<String>, enabled: Bool, font: NSFont, foreground: NSColor, focusOnAttach: Bool = false,
                onSubmit: @escaping () -> Void, onNative: @escaping () -> Void) {
        _text = text
        self.enabled = enabled
        self.font = font
        self.foreground = foreground
        self.focusOnAttach = focusOnAttach
        self.onSubmit = onSubmit
        self.onNative = onNative
    }

    public func makeCoordinator() -> Coordinator { Coordinator(text: $text) }
    public func makeNSView(context: Context) -> NSScrollView {
        let scroll = NSScrollView()
        scroll.drawsBackground = false
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        let editor = AgentComposerTextView(frame: scroll.bounds)
        editor.focusOnAttach = focusOnAttach
        editor.isRichText = false
        editor.allowsUndo = true
        editor.isAutomaticQuoteSubstitutionEnabled = false
        editor.isAutomaticDashSubstitutionEnabled = false
        editor.isAutomaticTextReplacementEnabled = false
        editor.drawsBackground = false
        editor.textContainerInset = NSSize(width: 4, height: 4)
        editor.isVerticallyResizable = true
        editor.isHorizontallyResizable = false
        editor.autoresizingMask = [.width]
        editor.textContainer?.widthTracksTextView = true
        editor.delegate = context.coordinator
        editor.setAccessibilityLabel("给 coding agent 的新会话消息 / New agent-session message")
        scroll.documentView = editor
        update(editor, context: context)
        return scroll
    }
    public func updateNSView(_ scroll: NSScrollView, context: Context) {
        guard let editor = scroll.documentView as? AgentComposerTextView else { return }
        update(editor, context: context)
    }
    private func update(_ editor: AgentComposerTextView, context: Context) {
        context.coordinator.text = $text
        if editor.string != text && !editor.hasMarkedText() { editor.string = text }
        editor.isEditable = enabled
        editor.font = font
        editor.textColor = foreground
        editor.insertionPointColor = foreground
        editor.onSubmit = onSubmit
        editor.onNative = onNative
    }
    public final class Coordinator: NSObject, NSTextViewDelegate {
        var text: Binding<String>
        init(text: Binding<String>) { self.text = text }
        public func textDidChange(_ notification: Notification) {
            guard let editor = notification.object as? NSTextView else { return }
            text.wrappedValue = editor.string
        }
    }
}
