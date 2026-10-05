import AppKit
import SwiftUI

/// SwiftUI materializes closed NSMenu contents lazily. Invalidate only our
/// command-containing menus when effective bindings/order change, so a keyboard
/// user need not open a menu before the new equivalent starts working.
struct WorkspaceMenuRefresh: NSViewRepresentable {
    let bindings: [WorkspaceAction: WorkspaceShortcut]
    let worktrees: [String]
    let allowed: Bool
    struct Signature: Equatable {
        let bindings: [WorkspaceAction: WorkspaceShortcut]
        let worktrees: [String]
        let allowed: Bool
    }
    func makeCoordinator() -> Coordinator { Coordinator() }
    func makeNSView(context: Context) -> NSView { NSView(frame: .zero) }
    func updateNSView(_ view: NSView, context: Context) {
        context.coordinator.update(Signature(bindings: bindings, worktrees: worktrees, allowed: allowed))
    }
    @MainActor final class Coordinator {
        private var signature: Signature?
        private var scheduled = false
        func update(_ value: Signature) {
            guard signature != value else { return }
            signature = value
            guard !scheduled else { return }
            scheduled = true
            DispatchQueue.main.async { [weak self] in
                guard let self else { return }
                self.scheduled = false
                for item in NSApp.mainMenu?.items ?? [] {
                    guard let menu = item.submenu else { continue }
                    let ownsCommands = item.title == MenuBar.viewTitle || item.title == "Worktrees"
                        || menu.items.contains { $0.title == WorkspaceAction.connect.title || $0.title == WorkspaceAction.settings.title }
                    guard ownsCommands else { continue }
                    // Public NSMenuDelegate entry point used by native tracking.
                    // Do not change targets, native Edit/Window items, or events.
                    menu.delegate?.menuNeedsUpdate?(menu)
                    menu.update()
                }
            }
        }
    }
}
