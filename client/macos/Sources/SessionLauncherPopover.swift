import AppKit
import SwiftUI
import BerthAgent

/// AppKit owns presentation and its fixed viewport. Letting a SwiftUI popover
/// continuously negotiate intrinsic size with an NSTextView caused a real
/// AttributeGraph cycle on first provider discovery. No terminal view moves.
struct SessionLauncherPopover: NSViewRepresentable {
    @Binding var isPresented: Bool
    let panel: SessionLaunchPanel
    let reduced: Bool
    let beforeOpen: () -> Void
    @ObservedObject private var settings = UISettings.shared
    func makeCoordinator() -> Coordinator { Coordinator(self) }
    func makeNSView(context: Context) -> ConsoleSessionButton {
        let button = ConsoleSessionButton(); button.title = "新会话"
        button.isBordered = false; button.bezelStyle = .inline
        context.coordinator.button = button
        button.activate = { [weak coordinator = context.coordinator] in coordinator?.toggle() }
        button.setAccessibilityIdentifier("console.newSession")
        return button
    }
    func updateNSView(_ button: ConsoleSessionButton, context: Context) {
        context.coordinator.parent = self
        let size = 11 * settings.interfaceScale
        button.font = settings.interfaceFontName == "__system__" ? NSFont.systemFont(ofSize: size)
            : (NSFont(name: settings.interfaceFontName, size: size) ?? NSFont.systemFont(ofSize: size))
        button.contentTintColor = NSColor(Ink.inkMuted)
        button.image = NSImage(systemSymbolName: "plus", accessibilityDescription: nil); button.imagePosition = .imageLeading
        button.sizeToFit()
        context.coordinator.host?.rootView = context.coordinator.content
        if !isPresented, context.coordinator.popover?.isShown == true {
            // Never change native window presentation during SwiftUI layout.
            DispatchQueue.main.async { [weak coordinator = context.coordinator] in
                guard let coordinator, !coordinator.parent.isPresented else { return }
                coordinator.closePresentation()
            }
        }
    }
    static func dismantleNSView(_ button: ConsoleSessionButton, coordinator: Coordinator) {
        coordinator.detaching = true; coordinator.closePresentation()
        button.activate = nil
    }
    final class Coordinator: NSObject, NSPopoverDelegate {
        var parent: SessionLauncherPopover
        weak var button: ConsoleSessionButton?
        var popover: NSPopover?
        var host: NSHostingView<AnyView>?
        var detaching = false
        init(_ parent: SessionLauncherPopover) { self.parent = parent }
        var content: AnyView {
            AnyView(parent.panel.frame(width: 460, height: 260, alignment: .top)
                .preferredColorScheme(parent.settings.colorScheme)
                .environment(\.clientReduceMotion, parent.reduced))
        }
        func toggle() {
            if parent.isPresented, popover?.isShown == true {
                parent.isPresented = false; closePresentation(); return
            }
            // A previous hide animation may still be finishing. A fresh click
            // starts a fresh presentation; it must not toggle that closing one.
            closePresentation()
            guard let button, button.window != nil, !detaching else { return }
            parent.beforeOpen(); parent.isPresented = true
            let host = NSHostingView(rootView: content)
            host.sizingOptions = []; host.frame = NSRect(x: 0, y: 0, width: 460, height: 260)
            let controller = NSViewController(); controller.view = host
            let popover = NSPopover(); popover.contentViewController = controller
            popover.contentSize = NSSize(width: 460, height: 260)
            popover.behavior = .transient; popover.animates = !parent.reduced; popover.delegate = self
            self.host = host; self.popover = popover
            popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
            // This is an explicit native click, outside any SwiftUI layout pass.
            // Only the newly opened composer receives this one-shot request.
            func composer(in view: NSView) -> AgentComposerTextView? {
                if let editor = view as? AgentComposerTextView { return editor }
                for child in view.subviews { if let editor = composer(in: child) { return editor } }
                return nil
            }
            if let editor = composer(in: host), editor.window?.makeFirstResponder(editor) == true { editor.focusOnAttach = false }
        }
        func closePresentation() {
            guard let previous = popover else { return }
            popover = nil; host = nil
            // Disown before closing. Its delayed animation/delegate callback
            // cannot reset the binding or release a subsequently opened host.
            previous.delegate = nil
            previous.close()
        }
        func popoverWillClose(_ notification: Notification) {
            guard let closing = notification.object as? NSPopover, closing === popover else { return }
            if !detaching { parent.isPresented = false }
        }
        func popoverDidClose(_ notification: Notification) {
            guard let closing = notification.object as? NSPopover, closing === popover else { return }
            host = nil; popover = nil
            if !detaching { parent.isPresented = false }
        }
    }
}
