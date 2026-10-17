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
        HStack(spacing: Metrics.unit) {
            Wordmark(size: Metrics.chrome)
            Spacer(minLength: Metrics.unit)
            chromeChip(symbol: "magnifyingglass", title: "命令", shortcut: "⇧⌘P",
                       id: "commands", action: openActions)
                .tourAnchor(.commands)
                .help("搜索工作区动作 / Command panel")
            chromeChip(symbol: "rectangle.on.rectangle",
                       title: "会话 \(sessionCount)", id: "sessions", action: openSessions)
                .tourAnchor(.sessions)
            chromeChip(symbol: "clock.arrow.circlepath",
                       title: recoveryPending ? "恢复 · 待确认" : "恢复",
                       id: "recovery", action: openRecovery)
                .tourAnchor(.recovery)
            if frozen {
                Image(systemName: "ellipsis.circle")
                    .font(.system(size: 12, weight: .regular))
                    .frame(minWidth: Metrics.hit, minHeight: Metrics.hit)
                    .tourAnchor(.more)
            } else {
                Menu {
                    Button("检查更新… / Check for updates…", action: openUpdates)
                    Button("设置… / Settings…", action: openSettings)
                    Button("使用指引… / Getting started…", action: openGuide)
                } label: {
                    Image(systemName: "ellipsis.circle")
                        .font(.system(size: 12, weight: .regular))
                        .frame(minWidth: Metrics.hit, minHeight: Metrics.hit)
                        .contentShape(Rectangle())
                }
                .menuStyle(.borderlessButton)
                .fixedSize()
                .accessibilityLabel("更多工作区操作")
                .accessibilityIdentifier("workspace.toolbar.more")
                .tourAnchor(.more)
            }
        }
        .controlSize(.small)
        .buttonStyle(.plain)
        .foregroundStyle(Ink.inkMuted)
        .padding(.horizontal, Metrics.gutter)
        .frame(height: Metrics.chromeHeight)
        .background(Ink.canvas)
    }

    private func chromeChip(symbol: String, title: String, shortcut: String? = nil,
                            id: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: Metrics.tabPadV + 1) {
                Image(systemName: symbol)
                    .font(.system(size: 11, weight: .medium))
                Text(title)
                    .font(Face.sans(Metrics.chrome))
                    .lineLimit(1)
                if let shortcut {
                    Text(shortcut)
                        .font(Face.mono(Metrics.caption))
                        .foregroundStyle(Ink.inkFaint)
                }
            }
            .padding(.horizontal, Metrics.controlPadH)
            .padding(.vertical, Metrics.controlPadV)
            .frame(minHeight: Metrics.hit)
            .contentShape(RoundedRectangle(cornerRadius: 6, style: .continuous))
        }
        .accessibilityIdentifier("workspace.toolbar." + id)
    }
}

/// Project-scope segmented control: Services / Git / Sessions.
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
                .padding(.horizontal, Metrics.tabPadH)
                .padding(.vertical, Metrics.tabPadV)
                .frame(minHeight: Metrics.hit)
                .background(selected ? Ink.accentSoft : Color.clear)
                .clipShape(RoundedRectangle(cornerRadius: 6, style: .continuous))
                .contentShape(RoundedRectangle(cornerRadius: 6, style: .continuous))
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}
