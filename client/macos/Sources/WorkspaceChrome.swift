import SwiftUI

/// Compact, always reachable workspace controls. These open existing features;
/// presenting the toolbar never starts a process or fetches release metadata.
struct WorkspaceToolbar: View {
    var frozen = false
    var sessionCount: Int
    var recoveryPending: Bool
    let openActions: () -> Void
    let openSessions: () -> Void
    let openRecovery: () -> Void
    let openUpdates: () -> Void
    let openSettings: () -> Void
    var openGuide: () -> Void = {}
    var body: some View {
        HStack(spacing: 8) {
            Wordmark(size: 12)
            Text("WORKSPACE").font(Face.mono(9)).tracking(1.2).foregroundStyle(Ink.inkFaint)
                .lineLimit(1).layoutPriority(-1)
            Spacer(minLength: 4)
            Button(action: openActions) {
                HStack(spacing: 7) {
                    Image(systemName: "magnifyingglass")
                    Text("命令").font(Face.sans(11))
                    Text("⇧⌘P").font(Face.mono(9)).foregroundStyle(Ink.inkFaint)
                }
            }.tourAnchor(.commands).accessibilityIdentifier("workspace.toolbar.commands").help("搜索工作区动作 / Command panel")
            toolbarButton("会话 \(sessionCount)", symbol: "rectangle.on.rectangle", id: "sessions", action: openSessions).tourAnchor(.sessions)
            toolbarButton(recoveryPending ? "恢复 · 待确认" : "恢复", symbol: "clock.arrow.circlepath", id: "recovery", action: openRecovery).tourAnchor(.recovery)
            if frozen {
                Image(systemName: "ellipsis.circle").font(.system(size: 13)).tourAnchor(.more)
            } else {
            Menu {
                Button("检查更新… / Check for updates…", action: openUpdates)
                Button("设置… / Settings…", action: openSettings)
                Button("使用指引… / Getting started…", action: openGuide)
            } label: { Image(systemName: "ellipsis.circle").font(.system(size: 13)) }
                .menuStyle(.borderlessButton).fixedSize().accessibilityLabel("更多工作区操作")
                .accessibilityIdentifier("workspace.toolbar.more").tourAnchor(.more)
            }
        }
        .controlSize(.small).buttonStyle(.plain)
        .padding(.horizontal, 14).frame(height: 42)
        .foregroundStyle(Ink.inkMuted).background(Ink.surface)
    }
    private func toolbarButton(_ title: String, symbol: String, id: String, action: @escaping () -> Void) -> some View {
        Button(action: action) { Label(title, systemImage: symbol).font(Face.sans(11)).lineLimit(1) }
            .accessibilityIdentifier("workspace.toolbar." + id)
            .padding(.horizontal, 5).padding(.vertical, 4)
    }
}

struct WorkspaceTab: View {
    let title: String
    let symbol: String
    let selected: Bool
    let action: () -> Void
    var body: some View {
        Button(action: action) {
            Label(title, systemImage: symbol)
                .font(Face.sans(11, selected ? .semibold : .regular))
                .foregroundStyle(selected ? Ink.accent : Ink.inkMuted)
                .padding(.horizontal, 11).padding(.vertical, 7)
                .background(selected ? Ink.accentSoft : Color.clear)
                .clipShape(RoundedRectangle(cornerRadius: 6))
                .contentShape(Rectangle())
        }.buttonStyle(.plain)
            .accessibilityAddTraits(selected ? .isSelected : [])
    }
}
