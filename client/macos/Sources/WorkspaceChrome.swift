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
        HStack(spacing: Metrics.inline) {
            Wordmark(size: Metrics.chrome)
            Spacer(minLength: Metrics.unit)
            Button(action: openActions) {
                HStack(spacing: Metrics.tabPadV + 1) {
                    Image(systemName: "magnifyingglass")
                    Text("命令").font(Face.sans(Metrics.chrome))
                    Text("⇧⌘P").font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkFaint)
                }
            }.tourAnchor(.commands).accessibilityIdentifier("workspace.toolbar.commands").help("搜索工作区动作 / Command panel")
            toolbarButton("会话 \(sessionCount)", symbol: "rectangle.on.rectangle", id: "sessions", action: openSessions).tourAnchor(.sessions)
            toolbarButton(recoveryPending ? "恢复 · 待确认" : "恢复", symbol: "clock.arrow.circlepath", id: "recovery", action: openRecovery).tourAnchor(.recovery)
            if frozen {
                Image(systemName: "ellipsis.circle").font(.system(size: 12)).frame(minWidth: Metrics.hit, minHeight: Metrics.hit).tourAnchor(.more)
            } else {
            Menu {
                Button("检查更新… / Check for updates…", action: openUpdates)
                Button("设置… / Settings…", action: openSettings)
                Button("使用指引… / Getting started…", action: openGuide)
            } label: { Image(systemName: "ellipsis.circle").font(.system(size: 12)).frame(minWidth: Metrics.hit, minHeight: Metrics.hit) }
                .menuStyle(.borderlessButton).fixedSize().accessibilityLabel("更多工作区操作")
                .accessibilityIdentifier("workspace.toolbar.more").tourAnchor(.more)
            }
        }
        .controlSize(.small).buttonStyle(.plain)
        .padding(.horizontal, Metrics.gutter).frame(height: Metrics.chromeHeight)
        .foregroundStyle(Ink.inkMuted).background(Ink.surface)
    }
    private func toolbarButton(_ title: String, symbol: String, id: String, action: @escaping () -> Void) -> some View {
        Button(action: action) { Label(title, systemImage: symbol).font(Face.sans(Metrics.chrome)).lineLimit(1) }
            .accessibilityIdentifier("workspace.toolbar." + id)
            .padding(.horizontal, Metrics.unit).padding(.vertical, 2)
            .frame(minHeight: Metrics.hit)
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
                .font(Face.sans(Metrics.chrome, selected ? .semibold : .regular))
                .foregroundStyle(selected ? Ink.accent : Ink.inkMuted)
                .padding(.horizontal, Metrics.tabPadH).padding(.vertical, Metrics.tabPadV)
                .frame(minHeight: Metrics.hit)
                .background(selected ? Ink.accentSoft : Color.clear)
                .clipShape(RoundedRectangle(cornerRadius: 6))
                .contentShape(Rectangle())
        }.buttonStyle(.plain)
            .accessibilityAddTraits(selected ? .isSelected : [])
    }
}
