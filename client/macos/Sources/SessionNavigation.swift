import AppKit
import BerthTerminal
import SwiftUI

/// The compact session rail keeps the terminal as the primary surface while
/// making every shell and external agent reachable without opening a sheet.
struct SessionRailItem: View {
    let session: TerminalSession
    let selected: Bool
    let activate: () -> Void

    private var isTerminal: Bool { session.kind == "terminal" }
    private var symbol: String { isTerminal ? "terminal" : "sparkle" }

    var body: some View {
        Button(action: activate) {
            ZStack(alignment: .bottomTrailing) {
                Image(systemName: symbol)
                    .font(.system(size: 13, weight: selected ? .semibold : .regular))
                    .foregroundStyle(selected ? Ink.accent : Ink.inkMuted)
                    .frame(width: 30, height: 28)
                    .background(selected ? Ink.accentSoft : Color.clear)
                    .clipShape(RoundedRectangle(cornerRadius: 6))
                Circle()
                    .fill(session.isActive ? Ink.live : Ink.dormant)
                    .frame(width: 5, height: 5)
                    .offset(x: -4, y: -4)
            }
        }
        .buttonStyle(.plain)
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
            HStack(spacing: 8) {
                Image(systemName: "rectangle.on.rectangle")
                    .foregroundStyle(Ink.accent)
                Text("切换会话").font(Face.sans(13, .semibold))
                Spacer(minLength: 0)
                Text("Esc 返回 terminal").font(Face.mono(9)).foregroundStyle(Ink.inkFaint)
                Button(action: close) { Image(systemName: "xmark") }
                    .buttonStyle(.plain).foregroundStyle(Ink.inkMuted)
            }.padding(.horizontal, 13).padding(.vertical, 10)
            Hairline()
            HStack(spacing: 7) {
                Image(systemName: "magnifyingglass").foregroundStyle(Ink.inkFaint)
                TextField("搜索名称、agent、状态或路径", text: $query)
                    .textFieldStyle(.plain).font(Face.sans(11)).focused($focused)
                    .accessibilityIdentifier("console.sessionSearch")
                Text("⌘Tab").font(Face.mono(9)).foregroundStyle(Ink.inkFaint)
            }.padding(10).background(Ink.sunken)
            Hairline()
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 12) {
                    section("Shell", sessions: shells)
                    section("Agent", sessions: agents)
                    if matches.isEmpty {
                        Text("没有匹配会话 / No matching sessions")
                            .font(Face.sans(11)).foregroundStyle(Ink.inkMuted).padding(14)
                    }
                }.padding(10)
            }
            HStack(spacing: 8) {
                Text(root).font(Face.mono(9)).foregroundStyle(Ink.inkFaint)
                    .lineLimit(1).truncationMode(.middle)
                Spacer(minLength: 0)
                Text("⌘⌥Tab 打开 · ⌘Tab 循环")
                    .font(Face.mono(9)).foregroundStyle(Ink.inkFaint)
            }.padding(.horizontal, 11).padding(.vertical, 8)
            .background(Ink.surface)
        }
        .background(Ink.canvas)
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(Ink.lineStrong, lineWidth: 1))
        .clipShape(RoundedRectangle(cornerRadius: 10))
        .shadow(color: .black.opacity(0.22), radius: 20, y: 8)
        .onAppear { focused = true }
        .accessibilityIdentifier("console.sessionPalette")
    }

    @ViewBuilder private func section(_ title: String, sessions: [TerminalSession]) -> some View {
        if !sessions.isEmpty {
            VStack(alignment: .leading, spacing: 4) {
                Text(title.uppercased()).font(Face.mono(9, .semibold)).foregroundStyle(Ink.inkFaint)
                ForEach(sessions) { session in
                    Button { activate(session) } label: {
                        HStack(spacing: 9) {
                            Image(systemName: session.kind == "terminal" ? "terminal" : "sparkle")
                                .foregroundStyle(session.id == selected.id ? Ink.accent : Ink.inkMuted)
                                .frame(width: 17)
                            VStack(alignment: .leading, spacing: 2) {
                                Text(session.title).font(Face.sans(11, session.id == selected.id ? .semibold : .regular))
                                    .foregroundStyle(Ink.ink).lineLimit(1)
                                Text(session.kind == "terminal" ? session.state : providerLabel(session))
                                    .font(Face.mono(9)).foregroundStyle(Ink.inkMuted).lineLimit(1)
                            }
                            Spacer(minLength: 0)
                            if session.isActive { Circle().fill(Ink.live).frame(width: 5, height: 5) }
                            if session.id == selected.id { Image(systemName: "checkmark").foregroundStyle(Ink.accent) }
                        }.padding(.horizontal, 8).padding(.vertical, 7)
                        .background(session.id == selected.id ? Ink.accentSoft : Color.clear)
                        .clipShape(RoundedRectangle(cornerRadius: 6))
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

enum SessionNavigationModel {
    enum Group: Equatable { case shell, agent }
    static func group(for kind: String) -> Group { kind == "terminal" ? .shell : .agent }
    static func matches(query: String, title: String, kind: String, state: String) -> Bool {
        let term = query.trimmingCharacters(in: .whitespacesAndNewlines)
        return term.isEmpty || (title + " " + kind + " " + state).localizedCaseInsensitiveContains(term)
    }
}

@MainActor
final class SessionKeyRouter: ObservableObject {
    private var monitor: Any?
    private var windowNumber: Int?
    private var root = ""
    private weak var workspace: ConsoleWorkspace?
    private var openAction: (() -> Void)?
    private var cycleAction: ((Int) -> Void)?
    private var escapeAction: (() -> Void)?
    private var canCycle: (() -> Bool)?
    private var canEscape: (() -> Bool)?

    func configure(root: String, workspace: ConsoleWorkspace,
                   open: @escaping () -> Void, cycle: @escaping (Int) -> Void,
                   escape: @escaping () -> Void, canCycle: @escaping () -> Bool,
                   canEscape: @escaping () -> Bool) {
        self.root = root
        self.workspace = workspace
        self.openAction = open
        self.cycleAction = cycle
        self.escapeAction = escape
        self.canCycle = canCycle
        self.canEscape = canEscape
    }

    func install(windowNumber: Int?) {
        guard monitor == nil, let windowNumber else { return }
        self.windowNumber = windowNumber
        monitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { [weak self] event in
            guard let self else { return event }
            return self.handle(event)
        }
    }

    func remove() {
        if let monitor { NSEvent.removeMonitor(monitor) }
        monitor = nil; windowNumber = nil
        openAction = nil; cycleAction = nil; escapeAction = nil; canCycle = nil; canEscape = nil
        workspace = nil
    }

    private func handle(_ event: NSEvent) -> NSEvent? {
        guard let windowNumber, event.window?.windowNumber == windowNumber else { return event }
        let flags = event.modifierFlags
        let command = flags.contains(.command) || flags.contains(.control)
        if event.keyCode == 48, command, flags.contains(.option) {
            openAction?(); return nil
        }
        if event.keyCode == 48, command, canCycle?() == true {
            cycleAction?(flags.contains(.shift) ? -1 : 1); return nil
        }
        if event.keyCode == 53, canEscape?() == true {
            escapeAction?(); return nil
        }
        return event
    }
}
