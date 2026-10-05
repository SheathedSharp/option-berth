import AppKit

/// Native responder state is inspected, never intercepted. Terminals implement
/// NSTextInputClient directly and must receive the same IME protection as editors.
@MainActor enum WorkspaceInputContext {
    static func hasMarkedText(in window: NSWindow?) -> Bool {
        (window?.firstResponder as? NSTextInputClient)?.hasMarkedText() == true
    }
    static func allowsNavigation(in window: NSWindow?, action: WorkspaceAction? = nil) -> Bool {
        guard let window, !(window is NSPanel), NSApp.modalWindow == nil,
              window.sheetParent == nil, window.attachedSheet == nil,
              !hasMarkedText(in: window) else { return false }
        // A detached session can search its own text, but cannot redirect the
        // selected project or open another workspace's command panel.
        if TerminalWindows.shared.windows.values.contains(where: { $0 === window }) {
            return action == .find
        }
        return true
    }
}
