import AppKit
import SwiftUI

/// A palette selection belongs to this workspace window. SwiftUI's onDismiss
/// can precede AppKit's key-window handoff; deliver from that native lifecycle,
/// not from a timeout, a global keyboard hook, or an unrelated window.
@MainActor final class WorkspaceCommandDelivery: ObservableObject {
    private weak var owner: NSView?
    private weak var window: NSWindow?
    private var observers: [NSObjectProtocol] = []
    private var pending: (WorkspaceAction, (WorkspaceAction) -> Void)?
    deinit { observers.forEach(NotificationCenter.default.removeObserver) }
    func attach(_ view: NSView) {
        guard let next = view.window else { detach(view); return }
        guard owner !== view || window !== next else { return }
        observers.forEach(NotificationCenter.default.removeObserver); observers.removeAll()
        owner = view; window = next
        for name in [NSWindow.didBecomeKeyNotification, NSWindow.didEndSheetNotification] {
            observers.append(NotificationCenter.default.addObserver(forName: name, object: next, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.deliver() }
            })
        }
        deliver()
    }
    func detach(_ view: NSView) {
        guard owner === view else { return }
        cancel(); owner = nil; window = nil
        observers.forEach(NotificationCenter.default.removeObserver); observers.removeAll()
    }
    func submit(_ action: WorkspaceAction, perform: @escaping (WorkspaceAction) -> Void) {
        pending = (action, perform); deliver()
    }
    func cancel() { pending = nil }
    private func deliver() {
        guard let window, window.isKeyWindow, NSApp.keyWindow === window,
              window.attachedSheet == nil, window.sheetParent == nil, NSApp.modalWindow == nil,
              let (action, perform) = pending else { return }
        pending = nil
        perform(action) // The shared router still validates IME and operation state.
    }
}

struct WorkspaceCommandDeliveryAnchor: NSViewRepresentable {
    let delivery: WorkspaceCommandDelivery
    func makeNSView(context: Context) -> Anchor { Anchor(delivery: delivery) }
    func updateNSView(_ view: Anchor, context: Context) { delivery.attach(view) }
    static func dismantleNSView(_ view: Anchor, coordinator: ()) { view.delivery.detach(view) }
    final class Anchor: NSView {
        let delivery: WorkspaceCommandDelivery
        init(delivery: WorkspaceCommandDelivery) { self.delivery = delivery; super.init(frame: .zero) }
        required init?(coder: NSCoder) { fatalError("init(coder:) is not supported") }
        override func viewDidMoveToWindow() { super.viewDidMoveToWindow(); delivery.attach(self) }
        override func hitTest(_ point: NSPoint) -> NSView? { nil }
    }
}
