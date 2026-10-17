import SwiftUI

/// One declared service — hover/selection wash, progressive actions.
///
/// Stage craft v9 layout (operator-console data surface):
/// - **Name**: single primary line
/// - **Status**: calm chip (live / warn / quiet) — own optical column
/// - **Port**: tabular mono
/// - **Command**: single line (notice replaces cmd when present)
/// - **Actions**: progressive chips on hover/focus
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

    enum Col {
        static var spacing: CGFloat { Metrics.columnGap }
        static let dot: CGFloat = 14
        /// Name is primary only; status moved to its own column.
        static let name: CGFloat = 108
        /// Calm status chip column — lockstep with ServicesView.Col.status.
        static let status: CGFloat = 84
        static var port: CGFloat { Metrics.portColumn }
        static let action: CGFloat = 112
        static let actionChip: CGFloat = 40

        static let primaryLine: CGFloat = 17
        static let verticalPad: CGFloat = 10
    }

    static var height: CGFloat {
        Col.verticalPad + Col.primaryLine + Col.verticalPad
    }

    private var showActions: Bool { hovering || isFocused }

    private var commandText: String {
        if let notice, !notice.isEmpty { return notice }
        return service.cmd.isEmpty ? "—" : service.cmd
    }

    private var commandIsNotice: Bool { notice.map { !$0.isEmpty } ?? false }

    /// Status chip for the dedicated 状态 column.
    private var statusChip: (text: String, tone: Tag.Tone)? {
        if service.manifestRuntimeMismatch {
            return ("需重启", .warn)
        }
        if let health = service.healthStatus {
            switch health.status {
            case "ok":
                break
            case "fail":
                return ("健康失败", .warn)
            default:
                return ("健康未知", .warn)
            }
        }
        if let exit = service.lastExit {
            let label: String = {
                switch exit.reason {
                case "ready_timeout": return "就绪超时"
                case "crashed": return "已崩溃"
                case "start_failed": return "启动失败"
                case "port_occupied": return "端口占用"
                case "dependency_timeout": return "依赖超时"
                case "dependency_not_ready": return "依赖未就绪"
                default: return "已退出"
                }
            }()
            return (label, .warn)
        }
        if let health = service.healthStatus, health.status == "ok" {
            return ("就绪", .live)
        }
        if service.health != nil && service.running {
            return ("检查中", .quiet)
        }
        if isLive {
            return ("在跑", .live)
        }
        return ("未启动", .quiet)
    }

    var body: some View {
        HStack(alignment: .center, spacing: Col.spacing) {
            StatusDot(tone: isLive ? Ink.live : Ink.dormant)
                .frame(width: Col.dot, alignment: .leading)

            Text(service.name)
                .font(Face.sans(Metrics.row, .semibold))
                .foregroundStyle(isLive ? Ink.ink : Ink.inkMuted)
                .lineLimit(1)
                .truncationMode(.middle)
                .frame(width: Col.name, alignment: .leading)

            Group {
                if let statusChip {
                    Tag(text: statusChip.text, tone: statusChip.tone)
                } else {
                    Text("—")
                        .font(Face.sans(Metrics.meta))
                        .foregroundStyle(Ink.inkFaint)
                }
            }
            .frame(width: Col.status, alignment: .leading)

            Text(verbatim: service.shownPort.map { "\($0)" } ?? "—")
                .font(Face.mono(Metrics.chrome, .medium))
                .monospacedDigit()
                .foregroundStyle(Ink.inkMuted)
                .lineLimit(1)
                .frame(width: Col.port, alignment: .trailing)

            Text(commandText)
                .font(commandIsNotice ? Face.sans(Metrics.chrome) : Face.mono(Metrics.meta))
                .foregroundStyle(commandIsNotice ? Ink.ink : Ink.inkFaint)
                .lineLimit(1)
                .truncationMode(.middle)
                .frame(maxWidth: .infinity, alignment: .leading)
                .clipped()

            actions
                .frame(width: Col.action, alignment: .trailing)
                .layoutPriority(1)
                .opacity(showActions || isWorking ? 1 : 0)
                .allowsHitTesting(showActions || isWorking)
                .animation(Motion.hover, value: showActions)
        }
        .padding(.horizontal, Metrics.gutter)
        .frame(height: Self.height, alignment: .center)
        .background(rowWash)
        .clipped()
        .contentShape(Rectangle())
        .onTapGesture(perform: onFocus)
        .onHover { now in
            withAnimation(Motion.hover) { hovering = now }
        }
        .accessibilityAddTraits(isFocused ? .isSelected : [])
        .accessibilityAction(named: Text("选中服务"), onFocus)
    }

    @ViewBuilder private var actions: some View {
        if isWorking {
            Text("…")
                .font(Face.sans(Metrics.chrome))
                .foregroundStyle(Ink.inkFaint)
                .frame(minWidth: Col.actionChip, alignment: .trailing)
        } else if isLive {
            HStack(spacing: Metrics.unit) {
                RowAction(title: "重启", tone: Ink.accent, minWidth: Col.actionChip, action: onRestart)
                RowAction(title: "停止", minWidth: Col.actionChip, action: onStop)
            }
        } else {
            RowAction(title: "启动", tone: Ink.accent, minWidth: Col.actionChip, action: onStart)
        }
    }

    private var rowWash: some View {
        ZStack(alignment: .leading) {
            if isFocused {
                // v11: whisper blush wash + thin terracotta rail — never alarm flood.
                Ink.accentSoft.opacity(0.78)
            } else if hovering {
                Ink.surface.opacity(0.85)
            } else {
                Color.clear
            }
            if isFocused {
                Rectangle()
                    .fill(Ink.accent)
                    .frame(width: 2)
            }
        }
    }
}
