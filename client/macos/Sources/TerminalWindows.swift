import AppKit
import BerthTerminal
import SwiftUI

/// Windows present registry-owned PTYs; closing a detached window returns its
/// presentation to the workspace and never starts/stops a process.
@MainActor
final class TerminalWindows: NSObject, ObservableObject, NSWindowDelegate {
    static let shared = TerminalWindows()
    @Published private(set) var windows: [UUID: NSWindow] = [:]
    private var sessions: [UUID: TerminalSession] = [:]
    func detach(_ session: TerminalSession) {
        if let window = windows[session.id] { window.makeKeyAndOrderFront(nil); return }
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 780, height: 460),
                              styleMask: [.titled, .closable, .miniaturizable, .resizable], backing: .buffered, defer: false)
        window.title = session.title
        window.isReleasedWhenClosed = false; window.delegate = self
        sessions[session.id] = session; windows[session.id] = window
        session.terminal.removeFromSuperview()
        window.contentView = NSHostingView(rootView: DetachedTerminalContent(session: session))
        window.minSize = NSSize(width: 400, height: 260)
        window.center(); window.makeKeyAndOrderFront(nil)
        DispatchQueue.main.async { window.makeFirstResponder(session.terminal) }
    }
    func bringBack(_ session: TerminalSession) { windows[session.id]?.close() }
    func windowWillClose(_ notification: Notification) {
        guard let window = notification.object as? NSWindow,
              let id = windows.first(where: { $0.value === window })?.key else { return }
        sessions[id]?.terminal.removeFromSuperview()
        window.contentView = nil
        sessions.removeValue(forKey: id); windows.removeValue(forKey: id)
    }
    private struct DetachedTerminalContent: View {
        @ObservedObject var session: TerminalSession
        var body: some View {
            VStack(spacing: 0) {
                HStack {
                    Text(session.title).font(Face.mono(11))
                    Text(session.state).font(Face.mono(10)).foregroundStyle(Ink.inkMuted)
                    Spacer()
                    Button("返回工作区") { TerminalWindows.shared.bringBack(session) }
                }.padding(10)
                Hairline()
                TerminalSurface(session: session, detached: true)
            }.background(Ink.canvas)
        }
    }
}

/// A separate container is created for each presentation, never a second PTY.
/// The detached-window flag prevents an embedded SwiftUI update stealing it.
@MainActor
final class TerminalHost: NSView {
    private(set) var session: TerminalSession?
    var detached = false
    func present(_ session: TerminalSession, detached: Bool) {
        self.session = session; self.detached = detached
        guard (TerminalWindows.shared.windows[session.id] != nil) == detached else {
            if session.terminal.superview === self { session.terminal.removeFromSuperview() }
            return
        }
        if session.terminal.superview !== self {
            session.terminal.removeFromSuperview()
            let terminal = session.terminal
            terminal.translatesAutoresizingMaskIntoConstraints = false
            addSubview(terminal)
            NSLayoutConstraint.activate([
                terminal.leadingAnchor.constraint(equalTo: leadingAnchor), terminal.trailingAnchor.constraint(equalTo: trailingAnchor),
                terminal.topAnchor.constraint(equalTo: topAnchor), terminal.bottomAnchor.constraint(equalTo: bottomAnchor)
            ])
        }
    }
    func releasePresentation() { if session?.terminal.superview === self { session?.terminal.removeFromSuperview() }; session = nil }
}
