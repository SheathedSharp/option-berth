import SwiftUI

/// One declared service in the services table — hover wash, selection wash,
/// progressive row actions (visible on hover OR when selected).
///
/// Skills: operate (component states: default/hover/selected), polish (progressive
/// disclosure of actions), quieter (actions recede until needed), craft-floor
/// (2pt semantic rails live in the log pane, not thick costume borders here).
struct ServiceRow: View {
    let service: BerthService
    let isLive: Bool
    let isFocused: Bool
    let isWorking: Bool
    let notice: String?
    let onFocus: () -> Void
    let onStart: () -> Void
    let onStop: () -> Void
    let onRestart: () -> Void

    @State private var hovering = false

    private enum Col {
        static var spacing: CGFloat { Metrics.columnGap }
        static let dot: CGFloat = 14
        static let name: CGFloat = 128
        static var port: CGFloat { Metrics.portColumn }
        static let action: CGFloat = 120
    }

    private var showActions: Bool { hovering || isFocused }

    var body: some View {
        Button(action: onFocus) {
            HStack(spacing: Col.spacing) {
                StatusDot(tone: isLive ? Ink.live : Ink.dormant)
                    .frame(width: Col.dot, alignment: .leading)

                Text(service.name)
                    .font(Face.sans(Metrics.row, .medium))
                    .foregroundStyle(isLive ? Ink.ink : Ink.inkMuted)
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .frame(width: Col.name, alignment: .leading)

                Text(verbatim: service.shownPort.map { "\($0)" } ?? "—")
                    .font(Face.mono(Metrics.chrome))
                    .foregroundStyle(Ink.inkMuted)
                    .frame(width: Col.port, alignment: .leading)

                VStack(alignment: .leading, spacing: Metrics.stack) {
                    if let notice {
                        Text(notice)
                            .font(Face.sans(Metrics.chrome))
                            .foregroundStyle(Ink.ink)
                            .lineLimit(1)
                            .truncationMode(.middle)
                    } else {
                        Text(service.cmd.isEmpty ? "—" : service.cmd)
                            .font(Face.mono(Metrics.meta))
                            .foregroundStyle(Ink.inkFaint)
                            .lineLimit(1)
                            .truncationMode(.middle)
                    }
                    ServiceRowFacts(service: service)
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                actions
                    .frame(width: Col.action, alignment: .trailing)
                    .opacity(showActions || isWorking ? 1 : 0)
                    .allowsHitTesting(showActions || isWorking)
                    .animation(Motion.hover, value: showActions)
            }
            .padding(.horizontal, Metrics.gutter)
            .frame(height: Metrics.rowHeight)
            .background(rowWash)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .onHover { now in
            withAnimation(Motion.hover) { hovering = now }
        }
        .accessibilityAddTraits(isFocused ? .isSelected : [])
    }

    @ViewBuilder private var actions: some View {
        if isWorking {
            Text("…")
                .font(Face.sans(Metrics.chrome))
                .foregroundStyle(Ink.inkFaint)
        } else if isLive {
            HStack(spacing: Metrics.unit) {
                RowAction(title: "重启", tone: Ink.accent, action: onRestart)
                RowAction(title: "停止", action: onStop)
            }
        } else {
            RowAction(title: "启动", tone: Ink.accent, action: onStart)
        }
    }

    private var rowWash: Color {
        if isFocused { return Ink.accentSoft }
        if hovering { return Ink.sunken }
        return Ink.canvas
    }
}

/// Compact fact stack under the command — health / exit / manifest mismatch.
struct ServiceRowFacts: View {
    let service: BerthService

    var body: some View {
        VStack(alignment: .leading, spacing: 1) {
            if service.manifestRuntimeMismatch {
                fact("运行实例来自旧清单 · 重启以应用新命令", tone: Change.changed)
            }
            if let health = service.healthStatus {
                let label: String = {
                    switch health.status {
                    case "ok": return "就绪"
                    case "fail": return health.reason.isEmpty ? "健康失败" : "健康失败 · \(health.reason)"
                    default: return "健康未知"
                    }
                }()
                fact(label, tone: health.status == "ok" ? Ink.live : Change.changed)
            } else if service.health != nil && service.running {
                fact("等待健康检查", tone: Ink.inkFaint)
            }
            if let exit = service.lastExit {
                let label: String = {
                    switch exit.reason {
                    case "ready_timeout": return "就绪超时"
                    case "crashed": return "上次崩溃 · 退出码 \(exit.code)"
                    case "start_failed": return "上次启动失败"
                    case "port_occupied": return "上次端口被占用"
                    case "dependency_timeout": return "依赖等待超时"
                    case "dependency_not_ready": return "依赖未就绪"
                    default: return "上次退出"
                    }
                }()
                fact(label, tone: Change.changed)
            }
        }
    }

    private func fact(_ label: String, tone: Color) -> some View {
        Text(label)
            .font(Face.sans(Metrics.caption, .medium))
            .foregroundStyle(tone)
            .lineLimit(1)
            .truncationMode(.middle)
    }
}
