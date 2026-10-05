import AppKit

/// Read native responder state without intercepting keyboard events. This also
/// covers terminal views that implement NSTextInputClient without NSTextView.
@MainActor enum WorkspaceInputContext {
    static func hasMarkedText(in window: NSWindow?) -> Bool {
        (window?.firstResponder as? NSTextInputClient)?.hasMarkedText() == true
    }
    static func allowsNavigation(in window: NSWindow?) -> Bool {
        guard let window, NSApp.modalWindow == nil, window.sheetParent == nil,
              window.attachedSheet == nil else { return false }
        return !hasMarkedText(in: window)
    }
}
