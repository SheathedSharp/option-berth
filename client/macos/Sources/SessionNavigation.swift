import AppKit
import BerthTerminal
import SwiftUI

/// The compact session rail keeps the terminal as the primary surface while
/// making every shell and external agent reachable without opening a sheet.
struct SessionRailItem: View {
    let session: TerminalSession
    let selected: Bool
    let activate: () -> Void

    var body: some View {
        ZStack(alignment: .bottomTrailing) {
            ConsoleSessionTab(session: session, selected: selected, compact: true, activate: activate)
                .frame(width: 30, height: 28)
                .background(selected ? Ink.accentSoft : Color.clear)
                .clipShape(RoundedRectangle(cornerRadius: 6))
            Circle()
                .fill(session.isActive ? Ink.live : Ink.dormant)
                .frame(width: 5, height: 5)
                .offset(x: -4, y: -4)
        }
        .frame(width: 30, height: 28)
        .help(session.title + " · " + session.state)
        .accessibilityIdentifier("console.session." + session.id.uuidString)
        .accessibilityLabel(session.title)
        .accessibilityValue((selected ? "选中，" : "") + session.state)
    }
}

/// A non-modal switcher for fast session navigation. It owns no PTY and only
/// calls the same activation closure used by the rail and the command palette.
struct SessionSwitcherPalette: View {
    let root: String
    let sessions: [TerminalSession]
    @Binding var query: String
    let selected: TerminalSession
    let activate: (TerminalSession) -> Void
    let close: () -> Void
    @FocusState private var focused: Bool

    private var matches: [TerminalSession] {
        let term = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !term.isEmpty else { return sessions }
        return sessions.filter {
            SessionNavigationModel.matches(query: term, title: $0.title,
                                           kind: $0.kind, state: $0.state)
                || $0.worktree.localizedCaseInsensitiveContains(term)
        }
    }

    private var shells: [TerminalSession] { matches.filter { $0.kind == "terminal" } }
    private var agents: [TerminalSession] { matches.filter { $0.kind != "terminal" } }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: Metrics.inline) {
                Image(systemName: "rectangle.on.rectangle")
                    .foregroundStyle(Ink.accent)
                Text("切换会话").font(Face.sans(Metrics.heading, .semibold))
                Spacer(minLength: 0)
                Text("Esc 返回 terminal").font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkFaint)
                Button(action: close) { Image(systemName: "xmark") }
                    .buttonStyle(.plain).foregroundStyle(Ink.inkMuted)
                    .frame(minWidth: Metrics.hit, minHeight: Metrics.hit)
            }.padding(.horizontal, Metrics.gutter).padding(.vertical, Metrics.headerPad)
            Hairline()
            HStack(spacing: Metrics.inline) {
                Image(systemName: "magnifyingglass").foregroundStyle(Ink.inkFaint)
                TextField("搜索名称、agent、状态或路径", text: $query)
                    .textFieldStyle(.plain).font(Face.sans(Metrics.chrome)).focused($focused)
                    .accessibilityIdentifier("console.sessionSearch")
                Text("⌘Tab").font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkFaint)
            }.padding(.horizontal, Metrics.gutter).padding(.vertical, Metrics.headerPad).background(Ink.sunken)
            Hairline()
            ScrollView {
                LazyVStack(alignment: .leading, spacing: Metrics.inline) {
                    section("Shell", sessions: shells)
                    section("Agent", sessions: agents)
                    if matches.isEmpty {
                        Text("没有匹配会话 / No matching sessions")
                            .font(Face.sans(Metrics.chrome)).foregroundStyle(Ink.inkMuted).padding(Metrics.gutter)
                    }
                }.padding(Metrics.gutter)
            }
            HStack(spacing: Metrics.inline) {
                Text(root).font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkFaint)
                    .lineLimit(1).truncationMode(.middle)
                Spacer(minLength: 0)
                Text("⌘⌥Tab 打开 · ⌘Tab 循环")
                    .font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkFaint)
            }.padding(.horizontal, Metrics.gutter).padding(.vertical, Metrics.headerPad)
            .background(Ink.surface)
        }
        .background(Ink.canvas)
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(Ink.lineStrong, lineWidth: 1))
        .clipShape(RoundedRectangle(cornerRadius: 10))
        .shadow(color: .black.opacity(0.22), radius: 20, y: 8)
        .onAppear { focused = true }
        .accessibilityIdentifier("console.sessionPalette")
            // SwiftUI's modifier does not reliably appear as an NSView. The
            // harness walks NSView.accessibilityIdentifier, so this marker is
            // the observation point only; it does not receive keys.
            .background(SessionPaletteMarker().frame(width: 1, height: 1))
    }

    @ViewBuilder private func section(_ title: String, sessions: [TerminalSession]) -> some View {
        if !sessions.isEmpty {
            VStack(alignment: .leading, spacing: Metrics.rowGap) {
                Text(title.uppercased()).font(Face.mono(Metrics.caption, .semibold)).foregroundStyle(Ink.inkFaint)
                ForEach(sessions) { session in
                    Button { activate(session) } label: {
                        HStack(spacing: Metrics.inline) {
                            Image(systemName: session.kind == "terminal" ? "terminal" : "sparkle")
                                .foregroundStyle(session.id == selected.id ? Ink.accent : Ink.inkMuted)
                                .frame(width: 16)
                            VStack(alignment: .leading, spacing: Metrics.stack) {
                                Text(session.title).font(Face.sans(Metrics.chrome, session.id == selected.id ? .semibold : .regular))
                                    .foregroundStyle(Ink.ink).lineLimit(1)
                                Text(session.kind == "terminal" ? session.state : providerLabel(session))
                                    .font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkMuted).lineLimit(1)
                            }
                            Spacer(minLength: 0)
                            if session.isActive { Circle().fill(Ink.live).frame(width: 5, height: 5) }
                            if session.id == selected.id { Image(systemName: "checkmark").foregroundStyle(Ink.accent) }
                        }.padding(.horizontal, Metrics.inline).padding(.vertical, Metrics.tabPadV)
                        .background(session.id == selected.id ? Ink.accentSoft : Color.clear)
                        .clipShape(RoundedRectangle(cornerRadius: 4))
                    }.buttonStyle(.plain)
                        .accessibilityIdentifier("console.session." + session.id.uuidString)
                }
            }
        }
    }

    private func providerLabel(_ session: TerminalSession) -> String {
        let pieces = session.kind.split(separator: ":").map(String.init)
        return pieces.count > 1 ? pieces[1] + " · " + session.state : session.state
    }
}

/// Session shortcuts for one console window.
///
/// Primary path: one local monitor. AppKit calls it from `NSApp.sendEvent`
/// before dispatch, which is how both real keystrokes and the native harness
/// enter the app. The monitor reads the view's window at event time and
/// accepts the event only when that window has a positive `windowNumber` and
/// the event is addressed to it (`event.window` identity, or the same positive
/// window number when the window object is still unresolved). Zero is not a
/// window and never matches. A detached terminal is a different window, so its
/// keys fall through.
///
/// Fallback: `performKeyEquivalent` on this same view, using the same
/// recognizer and the same positive window number. It covers a direct
/// key-equivalent call that never entered `sendEvent`. An event the monitor
/// already claimed is consumed here but not acted on twice. Hidden
/// key-equivalent buttons are absent: several `\t` buttons compete, and Tab
/// equivalents are not a second policy.
struct SessionShortcutDelivery: NSViewRepresentable {
    let open: () -> Void
    let cycle: (Int) -> Void
    let escape: () -> Void
    let focusComposer: () -> Void
    let newAgent: () -> Void
    let canCycle: () -> Bool
    let canEscape: () -> Bool
    let canFocusComposer: () -> Bool
    let canNewAgent: () -> Bool

    func makeNSView(context: Context) -> DeliveryView { DeliveryView() }

    func updateNSView(_ view: DeliveryView, context: Context) {
        view.apply(open: open, cycle: cycle, escape: escape, focusComposer: focusComposer, newAgent: newAgent,
                   canCycle: canCycle, canEscape: canEscape, canFocusComposer: canFocusComposer, canNewAgent: canNewAgent)
    }

    static func dismantleNSView(_ view: DeliveryView, coordinator: ()) { view.removeMonitor() }

    final class DeliveryView: NSView {
        private var monitor: Any?
        private var consumed: NSEvent?
        private var openAction: (() -> Void)?
        private var cycleAction: ((Int) -> Void)?
        private var escapeAction: (() -> Void)?
        private var focusComposerAction: (() -> Void)?
        private var newAgentAction: (() -> Void)?
        private var canCycle: (() -> Bool)?
        private var canEscape: (() -> Bool)?
        private var canFocusComposer: (() -> Bool)?
        private var canNewAgent: (() -> Bool)?

        override init(frame frameRect: NSRect) {
            super.init(frame: frameRect)
            setAccessibilityElement(false)
            installMonitor()
        }

        required init?(coder: NSCoder) { fatalError("init(coder:) is not supported") }

        deinit { removeMonitor() }

        override func hitTest(_ point: NSPoint) -> NSView? { nil }
        override var acceptsFirstResponder: Bool { false }

        func apply(open: @escaping () -> Void, cycle: @escaping (Int) -> Void,
                   escape: @escaping () -> Void, focusComposer: @escaping () -> Void, newAgent: @escaping () -> Void,
                   canCycle: @escaping () -> Bool, canEscape: @escaping () -> Bool,
                   canFocusComposer: @escaping () -> Bool, canNewAgent: @escaping () -> Bool) {
            openAction = open
            cycleAction = cycle
            escapeAction = escape
            focusComposerAction = focusComposer
            newAgentAction = newAgent
            self.canCycle = canCycle
            self.canEscape = canEscape
            self.canFocusComposer = canFocusComposer
            self.canNewAgent = canNewAgent
        }

        func removeMonitor() {
            if let monitor { NSEvent.removeMonitor(monitor) }
            monitor = nil
        }

        /// Command-key traversal on the key window. Escape reaches this only
        /// when AppKit includes it in that walk; otherwise the monitor handles
        /// it. The same `consumed` event is not acted on twice.
        override func performKeyEquivalent(with event: NSEvent) -> Bool {
            guard fallbackAccepts(event), let shortcut = recognize(event) else {
                return super.performKeyEquivalent(with: event)
            }
            if claim(event) { run(shortcut) }
            return true
        }

        private func installMonitor() {
            guard monitor == nil else { return }
            monitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { [weak self] event in
                guard let self else { return event }
                guard self.monitorAccepts(event), let shortcut = self.recognize(event) else { return event }
                if self.claim(event) { self.run(shortcut) }
                return nil
            }
        }

        private func monitorAccepts(_ event: NSEvent) -> Bool {
            guard let host = window, host.windowNumber > 0 else { return false }
            if event.window === host { return true }
            return event.window == nil && event.windowNumber == host.windowNumber
        }

        private func fallbackAccepts(_ event: NSEvent) -> Bool {
            guard let host = window, host.isKeyWindow else { return false }
            return monitorAccepts(event)
        }

        private func claim(_ event: NSEvent) -> Bool {
            if consumed === event { return false }
            consumed = event
            return true
        }

        private func recognize(_ event: NSEvent) -> SessionShortcut? {
            let flags = event.modifierFlags
            // Control is accepted with Command. The system reserves Command-Tab;
            // the harness still synthesizes Command, and Control remains a chord
            // that can reach this window.
            let command = flags.contains(.command) || flags.contains(.control)
            if event.keyCode == Self.tab, command, flags.contains(.option), openAction != nil { return .open }
            if event.keyCode == Self.tab, command, canCycle?() == true {
                return .cycle(flags.contains(.shift) ? -1 : 1)
            }
            // ⌘↩ focuses the bridge. ⇧⌘↩ creates an agent PTY. The bridge field
            // owns those chords while it is first responder. Control-Return stays with the PTY.
            let keys = flags.intersection([.command, .shift, .control, .option])
            if event.keyCode == Self.returnKey || event.keyCode == Self.keypadEnter {
                if keys == [.command, .shift], canNewAgent?() == true { return .newAgent }
                if keys == .command, canFocusComposer?() == true { return .focusComposer }
            }
            if event.keyCode == Self.escape, canEscape?() == true { return .escape }
            return nil
        }

        private func run(_ shortcut: SessionShortcut) {
            switch shortcut {
            case .open: openAction?()
            case .cycle(let offset): cycleAction?(offset)
            case .escape: escapeAction?()
            case .focusComposer: focusComposerAction?()
            case .newAgent: newAgentAction?()
            }
        }

        private static let tab: UInt16 = 48
        private static let escape: UInt16 = 53
        private static let returnKey: UInt16 = 36
        private static let keypadEnter: UInt16 = 76
    }
}

private enum SessionShortcut { case open, cycle(Int), escape, focusComposer, newAgent }

private struct SessionPaletteMarker: NSViewRepresentable {
    func makeNSView(context: Context) -> NSView {
        let view = NSView(frame: .zero)
        view.setAccessibilityIdentifier("console.sessionPalette")
        return view
    }

    func updateNSView(_ view: NSView, context: Context) {}
}

enum SessionNavigationModel {
    enum Group: Equatable { case shell, agent }
    static func group(for kind: String) -> Group { kind == "terminal" ? .shell : .agent }
    static func matches(query: String, title: String, kind: String, state: String) -> Bool {
        let term = query.trimmingCharacters(in: .whitespacesAndNewlines)
        return term.isEmpty || (title + " " + kind + " " + state).localizedCaseInsensitiveContains(term)
    }
}
