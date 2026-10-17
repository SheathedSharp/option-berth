import SwiftUI

/// One declared service — hover/selection wash, progressive actions.
///
/// Layout that cannot z-fight:
/// - **Name column**: service name + at most one status fact (VStack, explicit line frames)
/// - **Port column**: single mono line
/// - **Command column**: single line only (notice replaces cmd when present — never
///   stacks a second string on top of it)
///
/// Earlier dual-line Command cells still collided under ImageRenderer height
/// compression; splitting meta onto the name column keeps both readable.
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
        /// Slightly wider name column so status fact does not crowd the port.
        static let name: CGFloat = 132
        static var port: CGFloat { Metrics.portColumn }
        /// Room for 重启+停止 pair at equal optical width.
        static let action: CGFloat = 112
        /// Shared min width so 启动 / 停止 / 重启 align as a column of chips.
        static let actionChip: CGFloat = 40

        static let primaryLine: CGFloat = 16
        static let secondaryLine: CGFloat = 14
        static let lineGap: CGFloat = 3
        static let verticalPad: CGFloat = 9
    }

    /// Name dual-line + padding. Fixed so the list can size without compressing cells.
    static var height: CGFloat {
        Col.verticalPad + Col.primaryLine + Col.lineGap + Col.secondaryLine + Col.verticalPad
    }

    private var showActions: Bool { hovering || isFocused }

    private var commandText: String {
        if let notice, !notice.isEmpty { return notice }
        return service.cmd.isEmpty ? "—" : service.cmd
    }

    private var commandIsNotice: Bool { notice.map { !$0.isEmpty } ?? false }

    /// One status fact for the name column — priority: mismatch → health fail → exit → ok → waiting.
    private var statusFact: (text: String, tone: Color)? {
        if service.manifestRuntimeMismatch {
            return ("清单已改 · 需重启", Change.changed)
        }
        if let health = service.healthStatus {
            switch health.status {
            case "ok":
                break
            case "fail":
                let label = health.reason.isEmpty ? "健康失败" : "健康失败 · \(health.reason)"
                return (label, Change.changed)
            default:
                return ("健康未知", Change.changed)
            }
        }
        if let exit = service.lastExit {
            let label: String = {
                switch exit.reason {
                case "ready_timeout": return "就绪超时"
                case "crashed": return "上次崩溃 · \(exit.code)"
                case "start_failed": return "上次启动失败"
                case "port_occupied": return "上次端口被占用"
                case "dependency_timeout": return "依赖等待超时"
                case "dependency_not_ready": return "依赖未就绪"
                default: return "上次退出"
                }
            }()
            return (label, Change.changed)
        }
        if let health = service.healthStatus, health.status == "ok" {
            return ("就绪", Ink.live)
        }
        if service.health != nil && service.running {
            return ("等待健康检查", Ink.inkFaint)
        }
        return nil
    }

    var body: some View {
        HStack(alignment: .center, spacing: Col.spacing) {
            StatusDot(tone: isLive ? Ink.live : Ink.dormant)
                .frame(width: Col.dot, alignment: .leading)

            nameCell
                .frame(width: Col.name, alignment: .leading)

            Text(verbatim: service.shownPort.map { "\($0)" } ?? "—")
                .font(Face.mono(Metrics.chrome))
                .foregroundStyle(Ink.inkMuted)
                .lineLimit(1)
                .frame(width: Col.port, alignment: .leading)

            // Single line only — never a second Text in this column.
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

    private var nameCell: some View {
        VStack(alignment: .leading, spacing: Col.lineGap) {
            Text(service.name)
                .font(Face.sans(Metrics.row, .medium))
                .foregroundStyle(isLive ? Ink.ink : Ink.inkMuted)
                .lineLimit(1)
                .truncationMode(.middle)
                .frame(maxWidth: .infinity, alignment: .leading)
                .frame(height: Col.primaryLine, alignment: .center)
                .clipped()

            Group {
                if let statusFact {
                    Text(statusFact.text)
                        .font(Face.sans(Metrics.caption, .medium))
                        .foregroundStyle(statusFact.tone)
                        .lineLimit(1)
                        .truncationMode(.middle)
                } else {
                    Text(" ")
                        .font(Face.sans(Metrics.caption, .medium))
                        .foregroundStyle(.clear)
                        .accessibilityHidden(true)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .frame(height: Col.secondaryLine, alignment: .center)
            .clipped()
        }
        .frame(width: Col.name, height: Col.primaryLine + Col.lineGap + Col.secondaryLine,
               alignment: .leading)
        .clipped()
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
                Ink.accentSoft
            } else if hovering {
                Ink.sunken
            } else {
                Ink.canvas
            }
            // 1pt selection rail — craft-floor allows 1px; marks focused row without costume.
            if isFocused {
                Rectangle()
                    .fill(Ink.accent)
                    .frame(width: 1)
            }
        }
    }
}
