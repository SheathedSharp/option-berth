import AppKit
import SwiftUI
import UniformTypeIdentifiers
import BerthTerminal

struct PaneWorkspaceView: View {
    let primary: TerminalSession
    @ObservedObject var workspace: ConsoleWorkspace
    let agent: Bool
    @ObservedObject private var registry = TerminalSessions.shared
    @State private var problem: String?
    private var layout: PaneLayout {
        let candidate = agent ? workspace.agentLayout : workspace.terminalLayout
        let allowed = Set(registry.inWorktree(primary.worktree).map(\.id))
        if candidate.root != nil, (try? candidate.validate(allowed: allowed)) != nil { return candidate }
        return PaneLayout(session: primary.id)
    }
    var body: some View {
        VStack(spacing: 0) {
            if let root = layout.root { node(root, in: layout) }
            if let problem { Text(problem).font(Face.sans(10)).padding(8).foregroundStyle(Ink.ink) }
        }
    }
    private func node(_ id: UUID, in layout: PaneLayout) -> AnyView {
        guard let value = layout.node(id) else { return AnyView(EmptyView()) }
        if let sessionID = value.session,
           let session = registry.inWorktree(primary.worktree).first(where: { $0.id == sessionID }) ?? (primary.id == sessionID ? primary : nil) {
            return AnyView(VStack(spacing: 0) {
                HStack(spacing: 8) {
                    Button {
                        update { $0.focus(sessionID) }
                        session.terminal.window?.makeFirstResponder(session.terminal)
                    } label: {
                        Image(systemName: layout.focused == sessionID ? "circle.inset.filled" : "circle")
                        Text(session.title).font(Face.mono(10)).lineLimit(1)
                    }.buttonStyle(.plain).onDrag { NSItemProvider(object: sessionID.uuidString as NSString) }
                    Spacer(minLength: 0)
                    splitMenu(session, layout: layout)
                    Button { TerminalWindows.shared.detach(session) } label: { Image(systemName: "arrow.up.right.square") }
                        .help("将同一 PTY 移到独立窗口 / Detach this PTY")
                    if layout.sessions.count > 1 {
                        Button { update { $0.remove(sessionID) } } label: { Image(systemName: "rectangle.badge.minus") }
                            .help("收起窗格但保留会话 / Hide pane without ending session")
                    }
                }.padding(6).background(layout.focused == sessionID ? Ink.accentSoft : Ink.surface)
                SessionPane(session: session, frozen: false)
            }.frame(minWidth: 140, minHeight: 100)
                .onDrop(of: [UTType.text], isTargeted: nil) { providers in
                    guard let provider = providers.first, provider.canLoadObject(ofClass: NSString.self) else { return false }
                    _ = provider.loadObject(ofClass: NSString.self) { object, _ in
                        guard let text = object as? String, text.utf8.count == 36, let moved = UUID(uuidString: text) else { return }
                        DispatchQueue.main.async {
                            guard registry.inWorktree(primary.worktree).contains(where: { $0.id == moved }) else { return }
                            update { try $0.move(moved, beside: sessionID, axis: .horizontal) }
                        }
                    }
                    return true
                })
        }
        guard let first = value.first, let second = value.second else { return AnyView(EmptyView()) }
        if value.axis == .vertical {
            return AnyView(VSplitView { node(first, in: layout); node(second, in: layout) }.id(id))
        }
        return AnyView(HSplitView { node(first, in: layout); node(second, in: layout) }.id(id))
    }
    private func splitMenu(_ target: TerminalSession, layout: PaneLayout) -> some View {
        Menu {
            ForEach([PaneLayout.Axis.horizontal, .vertical], id: \.self) { axis in
                Menu(axis == .horizontal ? "左右分屏" : "上下分屏") {
                    ForEach(registry.inWorktree(primary.worktree).filter { !layout.sessions.contains($0.id) }) { other in
                        Button(other.title) { update { try $0.split(other.id, beside: target.id, axis: axis) } }
                    }
                }
            }
            Button("只显示这个窗格") { update { $0 = PaneLayout(session: target.id) } }
        } label: { Image(systemName: "rectangle.split.2x1") }
            .menuStyle(.borderlessButton).fixedSize().help("分屏仅呈现已有会话，不广播输入")
    }
    private func update(_ mutation: (inout PaneLayout) throws -> Void) {
        var next = layout
        do {
            try mutation(&next)
            try next.validate(allowed: Set(registry.inWorktree(primary.worktree).map(\.id)))
            if agent { workspace.agentLayout = next } else { workspace.terminalLayout = next }
            problem = nil
        } catch { problem = error.localizedDescription }
    }
}
