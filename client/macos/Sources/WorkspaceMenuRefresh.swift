import AppKit
import SwiftUI

/// SwiftUI materializes closed NSMenu contents lazily. Invalidate only our
/// command-containing menus when effective bindings/order change, so a keyboard
/// user need not open a menu before the new equivalent starts working.
///
/// ⌘N is special: the singleton window and the system New item both want it.
/// A disabled earlier item with the same equivalent does not fall through, so
/// the connect command must be the only owner of its chord.
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
        private var attempts = 0
        private var fileProxy: FileMenuProxy?
        private var monitor: Any?
        deinit { if let monitor { NSEvent.removeMonitor(monitor) } }
        func update(_ value: Signature) {
            if signature != value { signature = value; attempts = 0 }
            schedule()
        }
        private func schedule() {
            guard !scheduled else { return }
            scheduled = true
            DispatchQueue.main.async { [weak self] in
                guard let self else { return }
                self.scheduled = false
                let synced = self.apply()
                if !synced && self.attempts < 12 {
                    self.attempts += 1
                    self.schedule()
                }
            }
        }
        private func apply() -> Bool {
            guard let signature, let bar = NSApp.mainMenu else { return false }
            let shortcut = signature.bindings[.connect] ?? WorkspaceAction.connect.defaultShortcut
            installMonitor()
            attachFileProxy(shortcut: shortcut, allowed: signature.allowed)
            for item in bar.items {
                guard let menu = item.submenu, shouldMaterialize(item, menu) else { continue }
                // Public NSMenuDelegate entry point used by native tracking.
                // Do not change targets, native Edit/Window items, or events.
                menu.delegate?.menuNeedsUpdate?(menu)
                menu.update()
            }
            return WorkspaceMenuShortcutSync.install(shortcut, connectTitle: WorkspaceAction.connect.title,
                                                     in: bar, commandsAllowed: signature.allowed)
        }
        /// `sendEvent` asks the key window before the menu. The window's scene
        /// shortcut can swallow ⌘N. Perform the connect item itself so the
        /// shortcut cannot diverge from the menu, and leave every other chord alone.
        private func installMonitor() {
            guard monitor == nil else { return }
            monitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { [weak self] event in
                MainActor.assumeIsolated {
                    guard let self, let signature = self.signature else { return event }
                    let shortcut = signature.bindings[.connect] ?? WorkspaceAction.connect.defaultShortcut
                    let chars = event.charactersIgnoringModifiers?.lowercased() ?? ""
                    let mask = event.modifierFlags.intersection([.command, .shift, .option, .control])
                    guard chars == shortcut.key.lowercased(), mask == shortcut.modifierMask else { return event }
                    guard let bar = NSApp.mainMenu,
                          let item = WorkspaceMenuShortcutSync.items(in: bar).first(where: {
                              $0.title == WorkspaceAction.connect.title && !$0.isHidden && $0.isEnabled && $0.action != nil
                          }), let action = item.action else { return event }
                    return NSApp.sendAction(action, to: item.target, from: item) ? nil : event
                }
            }
        }
        private func shouldMaterialize(_ item: NSMenuItem, _ menu: NSMenu) -> Bool {
            if item.title == MenuBar.viewTitle || item.title == "Worktrees" { return true }
            if menu.items.contains(where: { $0.title == WorkspaceAction.connect.title || $0.title == WorkspaceAction.settings.title }) {
                return true
            }
            // File still has Close before SwiftUI replaces New. Refresh it even
            // when the connect title is not materialized yet.
            return WorkspaceMenuShortcutSync.isFileMenu(menu)
        }
        private func attachFileProxy(shortcut: WorkspaceShortcut, allowed: Bool) {
            guard let menu = NSApp.mainMenu?.items.compactMap(\.submenu).first(where: WorkspaceMenuShortcutSync.isFileMenu) else { return }
            if let fileProxy, menu.delegate === fileProxy {
                fileProxy.shortcut = shortcut
                fileProxy.commandsAllowed = allowed
                return
            }
            guard let owner = menu.delegate, !(owner is FileMenuProxy) else { return }
            let proxy = FileMenuProxy(owner: owner, shortcut: shortcut, commandsAllowed: allowed)
            fileProxy = proxy
            menu.delegate = proxy
        }
    }
}

enum WorkspaceMenuShortcutSync {
    static func matches(_ item: NSMenuItem, _ shortcut: WorkspaceShortcut) -> Bool {
        let equivalent = item.keyEquivalent.lowercased()
        guard !equivalent.isEmpty, equivalent == shortcut.key.lowercased() else { return false }
        let mask = item.keyEquivalentModifierMask.intersection([.command, .shift, .option, .control])
        return mask == shortcut.modifierMask
    }
    static func isFileMenu(_ menu: NSMenu) -> Bool {
        menu.items.contains { $0.action == #selector(NSWindow.performClose(_:)) }
            || menu.items.contains {
                $0.keyEquivalent.lowercased() == "w" && $0.keyEquivalentModifierMask.contains(.command)
                    && !$0.keyEquivalentModifierMask.contains(.option) && !$0.keyEquivalentModifierMask.contains(.control)
            }
    }
    /// Makes the connect command the only owner of its chord. A disabled or
    /// hidden predecessor with the same equivalent would swallow the key.
    @discardableResult
    static func install(_ shortcut: WorkspaceShortcut, connectTitle: String, in bar: NSMenu?, commandsAllowed: Bool) -> Bool {
        guard let bar else { return false }
        let all = items(in: bar)
        let candidates = all.filter { $0.title == connectTitle && !$0.isHidden }
        guard let owner = candidates.first(where: { $0.action != nil && $0.isEnabled })
                ?? candidates.first(where: { $0.action != nil })
                ?? candidates.first else { return false }
        owner.menu?.autoenablesItems = false
        owner.isEnabled = commandsAllowed
        for item in all where item !== owner && matches(item, shortcut) {
            item.keyEquivalent = ""
            item.keyEquivalentModifierMask = []
        }
        if !matches(owner, shortcut) {
            owner.keyEquivalent = shortcut.key
            owner.keyEquivalentModifierMask = shortcut.modifierMask
        }
        let blocked = all.contains { $0 !== owner && matches($0, shortcut) }
        // A title without an action is SwiftUI's unfinished New slot. Retry until
        // the button target is attached, otherwise the chord still calls nothing.
        return matches(owner, shortcut) && !blocked && owner.isEnabled == commandsAllowed && owner.action != nil
    }
    static func items(in menu: NSMenu) -> [NSMenuItem] {
        menu.items.flatMap { [$0] + ($0.submenu.map(items(in:)) ?? []) }
    }
}

/// Forwards SwiftUI's file-menu delegate, then keeps ⌘N (or the user's chord)
/// on the connect item. SwiftUI's own answer can still be `newDocument:`.
final class FileMenuProxy: NSObject, NSMenuDelegate {
    let owner: NSMenuDelegate
    var shortcut: WorkspaceShortcut
    var commandsAllowed: Bool
    init(owner: NSMenuDelegate, shortcut: WorkspaceShortcut, commandsAllowed: Bool) {
        self.owner = owner
        self.shortcut = shortcut
        self.commandsAllowed = commandsAllowed
    }
    func menuNeedsUpdate(_ menu: NSMenu) {
        MainActor.assumeIsolated {
            self.owner.menuNeedsUpdate?(menu)
            if menu.delegate !== self { menu.delegate = self }
            self.reinstall()
        }
    }
    func menuWillOpen(_ menu: NSMenu) {
        MainActor.assumeIsolated {
            self.owner.menuWillOpen?(menu)
            self.reinstall()
        }
    }
    func menuHasKeyEquivalent(_ menu: NSMenu, for event: NSEvent, target: AutoreleasingUnsafeMutablePointer<AnyObject?>, action: UnsafeMutablePointer<Selector?>) -> Bool {
        MainActor.assumeIsolated {
            if self.claims(event), let item = self.connectItem(in: menu), item.isEnabled, let itemAction = item.action {
                target.pointee = item.target
                action.pointee = itemAction
                return true
            }
            // Defining this method suppresses AppKit's own scan. Forward SwiftUI's
            // answer, and otherwise match enabled items in this menu ourselves.
            let selector = #selector(NSMenuDelegate.menuHasKeyEquivalent(_:for:target:action:))
            if self.owner.responds(to: selector) {
                return self.owner.menuHasKeyEquivalent?(menu, for: event, target: target, action: action) ?? false
            }
            return self.claimMatchingItem(in: menu, event: event, target: target, action: action)
        }
    }
    private func claimMatchingItem(in menu: NSMenu, event: NSEvent, target: AutoreleasingUnsafeMutablePointer<AnyObject?>, action: UnsafeMutablePointer<Selector?>) -> Bool {
        let chars = event.charactersIgnoringModifiers?.lowercased() ?? ""
        let mask = event.modifierFlags.intersection([.command, .shift, .option, .control])
        for item in menu.items {
            if let submenu = item.submenu, claimMatchingItem(in: submenu, event: event, target: target, action: action) { return true }
            let equivalent = item.keyEquivalent.lowercased()
            let itemMask = item.keyEquivalentModifierMask.intersection([.command, .shift, .option, .control])
            guard !equivalent.isEmpty, equivalent == chars, itemMask == mask, item.isEnabled, !item.isHidden, let itemAction = item.action else { continue }
            target.pointee = item.target
            action.pointee = itemAction
            return true
        }
        return false
    }
    private func reinstall() {
        _ = WorkspaceMenuShortcutSync.install(shortcut, connectTitle: WorkspaceAction.connect.title,
                                              in: NSApp.mainMenu, commandsAllowed: commandsAllowed)
    }
    private func claims(_ event: NSEvent) -> Bool {
        guard event.charactersIgnoringModifiers?.lowercased() == shortcut.key.lowercased() else { return false }
        let mask = event.modifierFlags.intersection([.command, .shift, .option, .control])
        return mask == shortcut.modifierMask
    }
    private func connectItem(in menu: NSMenu) -> NSMenuItem? {
        WorkspaceMenuShortcutSync.items(in: menu).first { $0.title == WorkspaceAction.connect.title && !$0.isHidden }
    }
}
