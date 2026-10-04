import AppKit
import SwiftUI
import UniformTypeIdentifiers
import BerthTerminal

struct PaneWorkspaceView: View {
    let primary: TerminalSession
    @ObservedObject var workspace: ConsoleWorkspace
    let agent: Bool
    var frozen = false
    var frozenSessions: [TerminalSession] = []
    @ObservedObject private var registry = TerminalSessions.shared
    @State private var problem: String?
    private var scoped: [TerminalSession] { frozen ? frozenSessions : registry.inWorktree(primary.worktree) }
    // The editable tree may contain historical references whose replacement
    // processes have not been explicitly started. Never overwrite it with the
    // smaller set that can currently be displayed as live terminal views.
    private var sourceLayout: PaneLayout {
        let candidate = agent ? workspace.agentLayout : workspace.terminalLayout
        if candidate.root != nil, (try? candidate.validate()) != nil { return candidate }
        return PaneLayout(session: primary.id)
    }
    private var layout: PaneLayout {
        var visible = sourceLayout
        let available = Set(scoped.map(\.id))
        for id in visible.sessions where !available.contains(id) { visible.remove(id) }
        return visible.root == nil ? PaneLayout(session: primary.id) : visible
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
           let session = scoped.first(where: { $0.id == sessionID }) ?? (primary.id == sessionID ? primary : nil) {
            let pane = VStack(spacing: 0) {
                HStack(spacing: 8) {
                    Button {
                        update { $0.focus(sessionID) }
                        session.terminal.window?.makeFirstResponder(session.terminal)
                    } label: {
                        Image(systemName: layout.focused == sessionID ? "circle.inset.filled" : "circle")
                        Text(session.title).font(Face.mono(10)).lineLimit(1)
                    }.buttonStyle(.plain)
                    .modifier(PaneDragSource(id: sessionID, enabled: !frozen))
                    Text(frozen ? "布局示例" : session.state).font(Face.mono(9)).foregroundStyle(Ink.inkFaint).lineLimit(1).layoutPriority(-1)
                    Spacer(minLength: 0)
                    Button { showTerminalFind(session.terminal) } label: { Image(systemName: "magnifyingglass") }
                        .help("查找此窗格 / Find in pane")
                    if frozen { Image(systemName: "rectangle.split.2x1") }
                    else { splitMenu(session, layout: layout) }
                    Button { TerminalWindows.shared.detach(session) } label: { Image(systemName: "arrow.up.right.square") }
                        .help("将同一 PTY 移到独立窗口 / Detach this PTY")
                    if layout.sessions.count > 1 {
                        Button { update { $0.remove(sessionID) } } label: { Image(systemName: "rectangle.badge.minus") }
                            .help("收起窗格但保留会话 / Hide pane without ending session")
                    }
                    Button {
                        if session.isActive { confirmSessionStop(session) }
                        else { TerminalWindows.shared.bringBack(session); registry.remove(session) }
                    } label: { Image(systemName: session.isActive ? "stop.fill" : "xmark") }
                        .accessibilityLabel((session.isActive ? "结束 " : "关闭 ") + session.title)
                        .help("仅操作当前会话 / This session only")
                }.padding(.horizontal, 8).padding(.vertical, 7)
                    .background(layout.focused == sessionID ? Ink.accentSoft : Ink.surface).disabled(frozen)
                Hairline()
                TerminalPaneBody(session: session, frozen: frozen)
            }.frame(minWidth: 140, minHeight: 100)
            if frozen { return AnyView(pane) }
            return AnyView(pane
                .onDrop(of: [UTType.text], isTargeted: nil) { providers in
                    guard !frozen, let provider = providers.first, provider.canLoadObject(ofClass: NSString.self) else { return false }
                    _ = provider.loadObject(ofClass: NSString.self) { object, _ in
                        guard let text = object as? String, text.utf8.count == 36, let moved = UUID(uuidString: text) else { return }
                        DispatchQueue.main.async {
                            guard scoped.contains(where: { $0.id == moved }) else { return }
                            update { try $0.move(moved, beside: sessionID, axis: .horizontal) }
                        }
                    }
                    return true
                })
        }
        guard let first = value.first, let second = value.second else { return AnyView(EmptyView()) }
        if frozen {
            if value.axis == .vertical {
                return AnyView(VStack(spacing: 0) { node(first, in: layout); Hairline(); node(second, in: layout) })
            }
            return AnyView(HStack(spacing: 0) { node(first, in: layout); Hairline(axis: .vertical); node(second, in: layout) })
        }
        if value.axis == .vertical {
            return AnyView(VSplitView { node(first, in: layout); node(second, in: layout) }.id(id))
        }
        return AnyView(HSplitView { node(first, in: layout); node(second, in: layout) }.id(id))
    }
    private func splitMenu(_ target: TerminalSession, layout: PaneLayout) -> some View {
        Menu {
            ForEach([PaneLayout.Axis.horizontal, .vertical], id: \.self) { axis in
                Menu(axis == .horizontal ? "左右分屏" : "上下分屏") {
                    ForEach(scoped.filter { !layout.sessions.contains($0.id) }) { other in
                        Button(other.title) { update { try $0.split(other.id, beside: target.id, axis: axis) } }
                    }
                }
            }
            Button("只显示这个窗格") { update { $0 = PaneLayout(session: target.id) } }
        } label: { Image(systemName: "rectangle.split.2x1") }
            .menuStyle(.borderlessButton).fixedSize().help("分屏仅呈现已有会话，不广播输入")
    }
    // Shared by actual pane actions and the regression harness. Existing
    // historical references may remain; new references must be registry-owned
    // in this worktree. Invalid edits cannot partially mutate the source tree.
    func applyLayoutChange(_ mutation: (inout PaneLayout) throws -> Void) throws {
        var next = sourceLayout
        let allowed = Set(next.sessions).union(scoped.map(\.id))
        try mutation(&next)
        try next.validate(allowed: allowed)
        if agent { workspace.agentLayout = next } else { workspace.terminalLayout = next }
    }
    private func update(_ mutation: (inout PaneLayout) throws -> Void) {
        do { try applyLayoutChange(mutation); problem = nil }
        catch { problem = error.localizedDescription }
    }
}


private struct PaneDragSource: ViewModifier {
    let id: UUID
    let enabled: Bool
    @ViewBuilder func body(content: Content) -> some View {
        if enabled { content.onDrag { NSItemProvider(object: id.uuidString as NSString) } }
        else { content }
    }
}
