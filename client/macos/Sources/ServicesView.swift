import AppKit
import SwiftUI

/// 服务板：一个项目声明了什么、现在跑着什么、它刚才说了什么。
///
/// 服务板回答「**说好要跑的东西起没起**」，并把运行实况放回对应的服务行；
/// 只有没有服务可归属的监听才单独列在页面底部。这个页面有按钮，是整个应用里
/// 少数几个会改变机器状态的地方。
///
/// 「在跑」的判据是运行注册表（`ServicesStore.live`），不是服务行里的 `running` ——
/// 后者是 daemon 从端口反推的，不监听端口的服务它看不见。
struct ServicesView: View {
    @ObservedObject var store: ServicesStore
    let group: BerthGroup
    /// 当前 worktree 的监听实况；正常监听显示在对应服务行，未声明的监听显示在页尾。
    let ports: [Port]
    /// `ImageRenderer` 画不出 `ScrollView` 里的东西。
    var scrolls: Bool = true

    // Logs need enough room for the fixed time/level columns and a readable message.
    // Services and Git have independent file-backed widths and drag preferences.
    @ClientDetailWidth(.services) private var detailWidth: Double
    private static let defaultDetailWidth = ClientDetailPane.defaultWidth
    private static let detailRange = ClientDetailPane.range
    private static let minimumMainWidth: CGFloat = 340

    @Environment(\.accessibilityReduceMotion) private var reduce
    @State private var following = true

    /// 查找停在第几条命中（`hits` 的下标）。**这是纯交互状态**，所以留在视图里；
    /// 「开着没有」「词是什么」在 store 上（见 `ServicesStore.logFindOpen`）。
    @State private var matchIndex = 0
    /// 「现在就滚过去」的信号。用递增的票而不是直接看 `matchIndex`：
    /// 停在第 0 条时再按一次「下一个」（只有一条命中）也得动。
    @State private var scrollTicket = 0
    /// 查找之前跟没跟着尾巴。关掉查找时按**他自己**那一档恢复 ——
    /// 查找会按停跟随，但「本来就没跟着」的人不该被我们按着跟上。
    @State private var followingBeforeFind = false
    @FocusState private var findFocused: Bool

    private enum Col {
        static let spacing: CGFloat = 12
        static let dot: CGFloat = 14
        static let name: CGFloat = 128
        static let port = Metrics.portColumn
        // Keep the listener evidence compact so the wider detail pane still fits
        // beside the service list at the default window size.
        static let process: CGFloat = 60
        static let action: CGFloat = 120
        /// 服务行最多露几行。再多就把日志挤没了 —— 而日志是这一页的正文。
        static let maxVisibleRows = 5
        /// 日志一行的近似高度（11pt 等宽 + 上下各 0.5pt）。离屏那条路拿它
        /// 估算「装得下几行」；长行会折行，所以只是个估算，宁可多算一行。
        static let logLine: CGFloat = 13
    }

    /// 日志那三列。**宽度是定值，不跟窗口走** —— 一列时间一列级别，扫的是一条竖线；
    /// 让它随窗口伸缩的话，同一条日志在不同窗口里断在不同的字上。
    private enum LogCol {
        /// 色条那一格：2pt 的条 + 1pt 的让位。两条记录之间的色条连不连得上，
        /// 决定「一屏扫下来是不是一条竖线」。
        static let rail: CGFloat = 3
        static let railWidth: CGFloat = 2
        /// `HH:MM:SS.mmm` = 12 个字符 × 10pt 等宽（约 6pt/字）+ 一格让位。
        /// 少给几个点的话，满毫秒那一种会和级别列**贴在一起**（实测过）。
        static let time: CGFloat = 82
        /// `ERROR` = 5 个字符，再加一格让位。
        static let level: CGFloat = 46
    }

    var body: some View {
        // 命中的行**只算一次**：页头那颗 `n/m` 和正文里的高亮用的是同一份 ——
        // 各算一遍的话，两个地方迟早会说出不一样的数。
        let hits = logMatches()
        VStack(spacing: 0) {
            GeometryReader { space in
                let range = detailRange(for: space.size.width)
                let width = resolvedDetailWidth(for: range)
                HStack(spacing: 0) {
                    servicePanel
                        .frame(width: max(0, space.size.width - width - SplitHandle.hitWidth),
                               alignment: .topLeading)
                    SplitHandle(width: $detailWidth, range: range, controlsTrailingPane: true)
                    logPanel(hits)
                        .frame(width: width, alignment: .topLeading)
                        .frame(maxHeight: .infinity, alignment: .topLeading)
                }
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .onAppear {
            normalizeDetailWidth()
            syncFocusToProject()
        }
        // ⌘F（菜单那一项）按下的那一下：露出查找栏、把光标送进框里、按停跟随。
        .onChange(of: store.logFindRequest) { _, _ in beginFind() }
        // 查找被 store 关掉的那条路（换服务、换项目）：跟随按原本那一档还回去，
        // 不然「查完一次」就等于把跟随永久关了 —— 那是个说不出口的状态。
        .onChange(of: store.logFindOpen) { _, open in
            guard !open, followingBeforeFind else { return }
            followingBeforeFind = false
            following = true
        }
    }

    private func detailRange(for availableWidth: CGFloat) -> ClosedRange<Double> {
        let maximum = min(Self.detailRange.upperBound,
                          Double(max(0, availableWidth - Self.minimumMainWidth - SplitHandle.hitWidth)))
        let minimum = min(Self.detailRange.lowerBound, maximum)
        return minimum...maximum
    }

    private func resolvedDetailWidth(for range: ClosedRange<Double>) -> CGFloat {
        if detailWidth < Self.detailRange.lowerBound {
            return CGFloat(min(Self.defaultDetailWidth, range.upperBound))
        }
        return CGFloat(min(max(detailWidth, range.lowerBound), range.upperBound))
    }

    private func normalizeDetailWidth() {
        if detailWidth < Self.detailRange.lowerBound {
            detailWidth = Self.defaultDetailWidth
        } else if detailWidth > Self.detailRange.upperBound {
            detailWidth = Self.detailRange.upperBound
        }
    }

    /// The list stays wide enough for commands; the log is an independently sized
    /// reading surface on the right so opening it never squeezes the service rows.
    private var servicePanel: some View {
        VStack(spacing: 0) {
            columnHeader
            Hairline()
            serviceList
            Hairline()
            if !group.machine.isEmpty {
                machineHeader
                Hairline()
                machineList
                Hairline()
            }
            if !unclaimedPorts.isEmpty {
                unclaimedHeader
                Hairline()
                unclaimedList
                Hairline()
            }
            Spacer(minLength: 0)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity,
               alignment: .topLeading)
        .background(Ink.canvas)
    }

    /// 进这一页的规矩：**不替用户选服务**。
    ///
    /// 从前这里会把焦点塞给第一个服务 —— 于是切进一个项目就先灌一段日志，
    /// 而那段日志属于谁、是不是这一次运行的，得自己猜。日志是这一页的正文没错，
    /// 但**看谁的**是点出来的：点上面那一行，它才出现。
    ///
    /// 跨项目要**清**焦点：`focused` 挂在 store 上（是全局的），不清的话切到 B 项目，
    /// 面板里还留着 A 的日志。
    private func syncFocusToProject() {
        guard let ref = store.focused, ref.group != group.name else { return }
        store.focus(nil)
    }

    // MARK: - 服务行

    private var columnHeader: some View {
        HStack(spacing: Col.spacing) {
            Color.clear.frame(width: Col.dot, height: 1)
            ColumnHead("service").frame(width: Col.name, alignment: .leading)
            ColumnHead("port").frame(width: Col.port, alignment: .leading)
            ColumnHead("command").frame(maxWidth: .infinity, alignment: .leading)
            Color.clear.frame(width: Col.action, height: 1)
        }
        .padding(.horizontal, Metrics.gutter)
        .padding(.vertical, 5)
        .background(Ink.surface)
    }

    private var serviceList: some View {
        // 高度按行数给，但不跟着无限长：日志才是这一页的正文。
        let visible = min(group.services.count, Col.maxVisibleRows)
        return Group {
            if scrolls {
                ScrollView { serviceRows }
            } else {
                serviceRows
            }
        }
        .frame(height: CGFloat(max(visible, 1)) * Metrics.rowHeight, alignment: .top)
        .clipped()
    }

    private var serviceRows: some View {
        VStack(spacing: 0) {
            ForEach(group.services) { service in
                serviceRow(service)
                Hairline()
            }
        }
    }

    private func serviceRow(_ service: BerthService) -> some View {
        let ref = ServiceRef(group: group.name, service: service.name)
        let run = store.live(service, in: group)
        let isLive = run != nil
        let isFocused = store.focused == ref

        return Button {
            store.focus(ref)
        } label: {
            HStack(spacing: Col.spacing) {
                StatusDot(tone: isLive ? Ink.live : Ink.dormant)
                    .frame(width: Col.dot, alignment: .leading)

                Text(service.name)
                    .font(Face.sans(12.5, .medium))
                    .foregroundStyle(isLive ? Ink.ink : Ink.inkMuted)
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .frame(width: Col.name, alignment: .leading)

                // `verbatim` 是必需的：`Text("\(port)")` 会按本地化给整数加千位分隔符，
                // 端口 18080 显示成「18,080」，而端口是标识符不是数量。
                // 监听实况那一版早就栽过一次，这里是同一个坑的第二遍 —— 加进 `--render-states`
                // 的第 07/08 屏就是为了让它再犯的时候立刻看得见。
                Text(verbatim: service.shownPort.map { "\($0)" } ?? "—")
                    .font(Face.mono(11.5))
                    .foregroundStyle(Ink.inkMuted)
                    .frame(width: Col.port, alignment: .leading)

                // 命令用等宽没错：它是一行要能照着念的东西。
                // 行尾那条 notice（「pid 31131」「已经在跑了」）比命令更相关，
                // 有的时候就顶上来 —— 它是刚发生的事，命令一直都在。
                VStack(alignment: .leading, spacing: 2) {
                    if let note = store.notice[ref] {
                        Text(note)
                            .font(Face.sans(11))
                            .foregroundStyle(Ink.ink)
                            .lineLimit(1)
                            .truncationMode(.middle)
                    } else {
                        Text(service.cmd.isEmpty ? "—" : service.cmd)
                            .font(Face.mono(10.5))
                            .foregroundStyle(Ink.inkFaint)
                            .lineLimit(1)
                            .truncationMode(.middle)
                    }
                    serviceFact(service)
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                Group {
                    if store.isWorking(group) {
                        Text("…").font(Face.sans(11)).foregroundStyle(Ink.inkFaint)
                    } else if isLive {
                        HStack(spacing: 5) {
                            RowAction(title: "重启", tone: Ink.accent) {
                                store.restart(group, only: [service.name])
                            }
                            RowAction(title: "停止") { store.stop(service, in: group) }
                        }
                    } else {
                        RowAction(title: "启动", tone: Ink.accent) {
                            store.start(group, only: [service.name])
                        }
                    }
                }
                .frame(width: Col.action, alignment: .trailing)
            }
            .padding(.horizontal, Metrics.gutter)
            .frame(height: Metrics.rowHeight)
            .background(isFocused ? Ink.accentSoft : Ink.canvas)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    @ViewBuilder
    private func serviceFact(_ service: BerthService) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            if service.manifestRuntimeMismatch {
                factText("运行实例来自旧清单 · 重启以应用新命令", tone: Change.changed)
            }
            if let health = service.healthStatus {
                let label: String = {
                    switch health.status {
                    case "ok": return "就绪"
                    case "fail": return health.reason.isEmpty ? "健康失败" : "健康失败 · \(health.reason)"
                    default: return "健康未知"
                    }
                }()
                factText(label, tone: health.status == "ok" ? Ink.live : Change.changed)
            } else if service.health != nil && service.running {
                factText("等待健康检查", tone: Ink.inkFaint)
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
                factText(label, tone: Change.changed)
            }
        }
    }

    private func factText(_ label: String, tone: Color) -> some View {
        Text(label)
            .font(Face.sans(9.5, .medium))
            .foregroundStyle(tone)
            .lineLimit(1)
            .truncationMode(.middle)
    }

    // MARK: - 依赖（机器上的，不归这一页管）

    /// 清单顶层 `machine:` 那一节：这个项目**依赖**的、机器上的服务（[0010]）。
    ///
    /// 它们不是这个项目的成员 —— `up` 不起、`down` 不停，所以这一节没有动作按钮，
    /// 也不进「启动全部 / 全部停止」的口径。它给的是实况那一半：那个端口上有没有人在听。
    /// 少了它，一份写着依赖的清单在界面上是看不出来的。
    private var machineHeader: some View {
        HStack(spacing: Col.spacing) {
            Color.clear.frame(width: Col.dot, height: 1)
            ColumnHead("machine").frame(width: Col.name, alignment: .leading)
            ColumnHead("port").frame(width: Col.port, alignment: .leading)
            Text("清单里的引用 —— 起停都不归这个项目")
                .font(Face.sans(10.5))
                .foregroundStyle(Ink.inkFaint)
                .lineLimit(1)
                .frame(maxWidth: .infinity, alignment: .leading)
            Color.clear.frame(width: Col.action, height: 1)
        }
        .padding(.horizontal, Metrics.gutter)
        .padding(.vertical, 5)
        .background(Ink.surface)
    }

    private var machineList: some View {
        // 同 serviceList：高度按行数给，但不跟着无限长 —— 日志才是这一页的正文。
        let visible = min(group.machine.count, Col.maxVisibleRows)
        return Group {
            if scrolls {
                ScrollView { machineRows }
            } else {
                machineRows
            }
        }
        .frame(height: CGFloat(max(visible, 1)) * Metrics.rowHeight, alignment: .top)
        .clipped()
    }

    private var machineRows: some View {
        VStack(spacing: 0) {
            ForEach(group.machine) { ref in
                HStack(spacing: Col.spacing) {
                    StatusDot(tone: ref.listening ? Ink.live : Ink.dormant)
                        .frame(width: Col.dot, alignment: .leading)

                    Text(ref.name)
                        .font(Face.sans(12.5, .medium))
                        .foregroundStyle(ref.listening ? Ink.ink : Ink.inkMuted)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .frame(width: Col.name, alignment: .leading)

                    // 端口是标识符，不是数量：`Text(verbatim:)` 挡掉千分位。
                    Text(verbatim: "\(ref.port)")
                        .font(Face.mono(11.5))
                        .foregroundStyle(Ink.inkMuted)
                        .frame(width: Col.port, alignment: .leading)

                    // 认到服务管理器单元就把单元名写出来：那是「这些端口是一家人的」
                    // 那个事实 —— 一条引用靠它把 mysql 的 3306 与 33060 一起算上。
                    Text(ref.unit ?? "这台机器上的服务")
                        .font(Face.mono(10.5))
                        .foregroundStyle(Ink.inkFaint)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .frame(maxWidth: .infinity, alignment: .leading)

                    Group {
                        if ref.listening {
                            Tag(text: "在听")
                        } else {
                            // 没听不是错误，是**依赖断了**：清单说要它，机器上没有。
                            // 所以用琥珀那枚（和「废纸篓残留」同档），不用红色。
                            Tag(text: "没听", tone: .warn)
                                .help("清单说这个项目依赖它，而这个端口上现在没人在听")
                        }
                    }
                    .frame(width: Col.action, alignment: .trailing)
                }
                .padding(.horizontal, Metrics.gutter)
                .frame(height: Metrics.rowHeight)
                Hairline()
            }
        }
    }

    // MARK: - 未声明监听

    /// 正常监听已经跟着服务行显示；这里仅保留无法归到清单服务的监听。
    /// 归属优先使用运行注册表的 pid，其次才使用清单声明的端口和 daemon 的状态。
    private var unclaimedPorts: [Port] {
        ports.filter { port in
            !group.services.contains { service in
                let run = store.live(service, in: group)
                if let run {
                    return run.pid == port.pid || run.ports.contains(port.port)
                }
                return service.running && (service.portActual == port.port || service.port == port.port)
            }
        }
    }

    private var unclaimedHeader: some View {
        HStack(spacing: Col.spacing) {
            Color.clear.frame(width: Col.dot, height: 1)
            ColumnHead("listener").frame(width: Col.port, alignment: .leading)
            ColumnHead("process").frame(width: Col.process, alignment: .leading)
            Text("未声明")
                .font(Face.sans(10.5))
                .foregroundStyle(Ink.inkFaint)
                .lineLimit(1)
                .frame(width: Col.port, alignment: .leading)
            Color.clear.frame(width: Col.action, height: 1)
        }
        .padding(.horizontal, Metrics.gutter)
        .padding(.vertical, 5)
        .background(Ink.surface)
    }

    private var unclaimedList: some View {
        let visible = min(unclaimedPorts.count, Col.maxVisibleRows)
        return Group {
            if scrolls {
                ScrollView { unclaimedRows }
            } else {
                unclaimedRows
            }
        }
        .frame(height: CGFloat(max(visible, 1)) * Metrics.rowHeight, alignment: .top)
        .clipped()
    }

    private var unclaimedRows: some View {
        VStack(spacing: 0) {
            ForEach(unclaimedPorts) { port in
                HStack(spacing: Col.spacing) {
                    StatusDot(tone: Change.changed)
                        .frame(width: Col.dot, alignment: .leading)
                    Text(verbatim: "\(port.port)")
                        .font(Face.mono(11.5, .medium))
                        .foregroundStyle(Ink.ink)
                        .frame(width: Col.port, alignment: .leading)
                    Text(port.displayName.isEmpty ? port.process : port.displayName)
                        .font(Face.mono(10.5))
                        .foregroundStyle(Ink.inkMuted)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .frame(width: Col.process, alignment: .leading)
                    Text(port.bindAddress)
                        .font(Face.mono(10.5))
                        .foregroundStyle(Ink.inkFaint)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .layoutPriority(1)
                        .frame(width: Col.port, alignment: .leading)
                    RowAction(title: "停止") {
                        store.kill(port) { _ in }
                    }
                    .frame(width: Col.action, alignment: .trailing)
                }
                .padding(.horizontal, Metrics.gutter)
                .frame(height: Metrics.rowHeight)
                Hairline()
            }
        }
    }

    // MARK: - 日志

    private func logPanel(_ hits: [Int]) -> some View {
        VStack(spacing: 0) {
            logHead
            Hairline()
            if store.logFindOpen {
                logFindBar(hits)
                Hairline()
            }
            logBody(hits)
        }
        .frame(maxHeight: .infinity, alignment: .top)
    }

    private var logHead: some View {
        let ref = store.focused
        let service = group.services.first { $0.name == ref?.service }
        let run = service.flatMap { store.live($0, in: group) }
        return VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                StatusDot(tone: run != nil ? Ink.live : Ink.dormant)
                Text(ref?.service ?? "日志")
                    .font(Face.sans(11.5, .medium))
                    .foregroundStyle(Ink.ink)
                    .lineLimit(1)
                Spacer(minLength: 4)
                if store.logPath != nil {
                    RowAction(title: "Finder") { revealLog() }
                }
                if ref != nil {
                    RowAction(title: "查找") { store.beginLogFind() }
                    RowAction(title: following ? "跟随中" : "已暂停",
                              tone: following ? Ink.accent : Ink.inkMuted) {
                        following.toggle()
                    }
                }
            }
            if let path = store.logPath {
                HStack(spacing: 6) {
                    Text(store.logIsFromRunStart ? "本次运行" : "最近")
                        .font(Face.sans(10))
                        .foregroundStyle(Ink.inkFaint)
                        .fixedSize()
                    Text(shortPath(path))
                        .font(Face.mono(9.5))
                        .foregroundStyle(Ink.inkFaint)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .help(path)
                }
            }
        }
        .padding(.horizontal, Metrics.gutter)
        .padding(.vertical, 8)
        .background(Ink.surface)
    }

    private func revealLog() {
        guard let path = store.logPath else { return }
        NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: path)])
    }

    // MARK: - 查找

    /// 查找栏。⌘F 露出来，Esc 收掉，⏎ / ⇧⏎ 在命中之间走。
    ///
    /// 「命中的一定看得见高亮」是这一块的恒等式：算命中是按**片段**算的
    /// （时间 / 级别 / 消息各找一遍），涂高亮也是逐片段涂 —— 于是不存在
    /// 「计数说有、眼睛找不到」那一格。（代价是跨列的词查不到，比如「07 INFO」。）
    private func logFindBar(_ hits: [Int]) -> some View {
        let position = matchPosition(hits)
        return HStack(spacing: 8) {
            LensMark(on: !store.logQuery.isEmpty && !hits.isEmpty)
            // 离屏那条路画不出真正的文本控件（和 `SettingsSheet` 的 `editable:false`、
            // `AddProjectSheet` 用只读 `Text` 顶 `TextEditor` 是同一个原因），
            // 所以那儿用同字体的 `Text` 顶上：图里要看的是这一行在不在、说的是什么。
            if scrolls {
                TextField("在日志里找…", text: Binding(
                    get: { store.logQuery },
                    set: { text in
                        store.setLogQuery(text)
                        // 词变了就从头数：停在「第 7 条」而词已经不是刚才那个，
                        // 那个位置没有任何意义。
                        matchIndex = 0
                        scrollTicket += 1
                    }
                ))
                .textFieldStyle(.plain)
                .font(Face.mono(11))
                .foregroundStyle(Ink.ink)
                .focused($findFocused)
                .onKeyPress(.escape) {
                    endFind()
                    return .handled
                }
                .onKeyPress(.return, phases: .down) { press in
                    // ⇧⏎ 往回走一条。`phases: .down` 是必须的：不带它那个重载是
                    // 「零参数的闭包」，拿不到修饰键。
                    step(press.modifiers.contains(.shift) ? -1 : 1, hits)
                    return .handled
                }
            } else {
                Text(store.logQuery.isEmpty ? "在日志里找…" : store.logQuery)
                    .font(Face.mono(11))
                    .foregroundStyle(store.logQuery.isEmpty ? Ink.inkFaint : Ink.ink)
                    .lineLimit(1)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            Spacer(minLength: 6)
            if !store.logQuery.isEmpty {
                if hits.isEmpty {
                    Text("没有匹配")
                        .font(Face.sans(10.5))
                        .foregroundStyle(Ink.inkFaint)
                        .fixedSize()
                } else {
                    // 计数是标识符，不是数量 —— `Text(verbatim:)` 挡掉千分位。
                    Text(verbatim: "\(position + 1)/\(hits.count)")
                        .font(Face.mono(10.5))
                        .foregroundStyle(Ink.inkMuted)
                        .fixedSize()
                }
            }
            StepMark(direction: .previous, enabled: !hits.isEmpty) { step(-1, hits) }
            StepMark(direction: .next, enabled: !hits.isEmpty) { step(1, hits) }
            RowAction(title: "×") { endFind() }
        }
        .padding(.horizontal, Metrics.gutter)
        .padding(.vertical, 5)
        .background(Ink.sunken)
    }

    /// 命中的行号（升序）。空词、以及没说要看谁的日志，都算没有命中。
    private func logMatches() -> [Int] {
        let needle = store.logQuery.trimmingCharacters(in: .whitespaces)
        guard !needle.isEmpty, store.focused != nil else { return [] }
        return store.logRecords.indices.filter { index in
            let record = store.logRecords[index]
            for fragment in [record.time, record.level?.label, record.message] {
                if let fragment, fragment.range(of: needle, options: [.caseInsensitive,
                                                                     .diacriticInsensitive]) != nil {
                    return true
                }
            }
            return false
        }
    }

    /// 当前停在第几条命中。命中的条数会变（日志在长、词在改），所以每次访问都夹一遍 ——
    /// 停在最后一条时又来了新行，不该把它甩到列表外面。
    private func matchPosition(_ hits: [Int]) -> Int {
        guard !hits.isEmpty else { return 0 }
        return min(max(matchIndex, 0), hits.count - 1)
    }

    /// ⏎ / ⇧⏎、还有那两颗箭头：在这一批命中里走一步（到头绕回另一端）。
    private func step(_ delta: Int, _ hits: [Int]) {
        guard !hits.isEmpty else { return }
        let count = hits.count
        matchIndex = ((matchPosition(hits) + delta) % count + count) % count
        scrollTicket += 1
    }

    /// ⌘F 那一下：露出查找栏、把光标送进框里，并且**把跟随按停** ——
    /// 匹配是要停下来读的；日志继续往下滚的话，刚跳过去的那一行下一秒就跑了。
    /// 关掉查找时恢复他原本那一档（见 `followingBeforeFind`）。
    private func beginFind() {
        guard store.focused != nil else { return }
        followingBeforeFind = following
        following = false
        findFocused = true
        scrollTicket += 1
    }

    /// 关掉查找栏（Esc / 那颗 ×）。**跟随不在这儿还**：`onChange(of: store.logFindOpen)`
    /// 那一条对「自己关的」和「换服务被关的」是同一条路，两处各写一遍迟早会走岔。
    private func endFind() {
        store.endLogFind()
        findFocused = false
    }

    /// 把查找词在**这一段**里的每一处涂上底。返回 nil = 这一段没有命中 ——
    /// 调用方就退回纯 `Text`（绝大多数行），高亮不许把 3000 行一起拖慢。
    private func highlight(_ text: String, current: Bool) -> AttributedString? {
        let needle = store.logQuery.trimmingCharacters(in: .whitespaces)
        guard !needle.isEmpty, !text.isEmpty else { return nil }
        var marked = AttributedString(text)
        var found = false
        var search = marked.startIndex..<marked.endIndex
        while let span = marked[search].range(of: needle, options: [.caseInsensitive,
                                                                   .diacriticInsensitive]) {
            marked[span].backgroundColor = current ? Ink.accent : Ink.accentSoft
            if current { marked[span].foregroundColor = .white }
            found = true
            guard span.upperBound < marked.endIndex else { break }
            search = span.upperBound..<marked.endIndex
        }
        return found ? marked : nil
    }

    @ViewBuilder private func logBody(_ hits: [Int]) -> some View {
        if store.focused == nil {
            logPlaceholder("点上面任意一个服务，它说过的话在这里")
        } else if store.logRecords.isEmpty {
            logPlaceholder("还没有输出")
        } else {
            logRoll(hits)
        }
    }

    private func logPlaceholder(_ text: String) -> some View {
        HStack {
            VStack(alignment: .leading, spacing: 6) {
                Text(text)
                    .font(Face.sans(11.5))
                    .foregroundStyle(Ink.inkFaint)
                if let path = store.logPath {
                    Text(shortPath(path))
                        .font(Face.mono(10))
                        .foregroundStyle(Ink.inkFaint)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, Metrics.gutter + 2)
        .padding(.top, 14)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(Ink.canvas)
    }

    private func logRoll(_ hits: [Int]) -> some View {
        let records = store.logRecords
        let hitLines = Set(hits)
        let currentLine = hits.isEmpty ? nil : hits[matchPosition(hits)]
        // These predicates used to be recomputed by every rendered row. With
        // a 3000-line tail that turned one log refresh into an O(n²) scan.
        // Compute them once for the buffer and pass the result down.
        let showsTime = records.contains { $0.time != nil }
        let showsLevel = records.contains { $0.level != nil }
        return Group {
            if scrolls {
                ScrollViewReader { proxy in
                    ScrollView {
                        linesStack(records[...], base: 0, hits: hitLines, current: currentLine,
                                   showsTime: showsTime, showsLevel: showsLevel)
                    }
                    // 打开就停在底部。日志是「最新的在下面」，
                    // 落在顶部等于让人从十分钟前开始读。
                    .defaultScrollAnchor(.bottom)
                    .onChange(of: records.count) { _, now in
                        // 查找开着就不跟着尾巴走：那是「停下来读」的另一种状态。
                        guard following, now > 0, !store.logFindOpen else { return }
                        proxy.scrollTo(now - 1, anchor: .bottom)
                    }
                    .onChange(of: scrollTicket) { _, _ in
                        guard let currentLine, store.logFindOpen else { return }
                        withAnimation(Motion.hover) {
                            proxy.scrollTo(currentLine, anchor: .center)
                        }
                    }
                }
            } else {
                // 离屏渲染这条路上没有 `ScrollView`，得自己把高度管住。
                //
                // 踩过的坑（和端口表那次同一个）：`VStack` 会拿**内容的理想高度**
                // 当自己的高度 —— 675 行日志 ≈ 8800pt，整页被撑开，页头和服务行
                // 全被顶出视野，图里只剩一片翻滚的日志。而且它和「真的画不出来」
                // 看起来一样。所以这里用 `GeometryReader` 报出实际空间，
                // 并且**只画装得下的那几行**。
                GeometryReader { space in
                    let room = max(1, Int(space.size.height / Col.logLine))
                    let window = logWindow(records, room: room, current: currentLine)
                    linesStack(window.slice, base: window.base, hits: hitLines, current: currentLine,
                               showsTime: showsTime, showsLevel: showsLevel)
                        // **底对齐**：真窗口里这一屏是滚到底的（跟随），
                        // 顶对齐会把最后那几行裁掉 —— 而最后那几行恰是这一页要看的。
                        .frame(width: space.size.width, height: space.size.height,
                               alignment: .bottom)
                        .clipped()
                }
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(Ink.canvas)
    }

    /// 离屏那条路要画哪一段：跟随模式下是尾巴；查找开着就画**当前命中那一屏** ——
    /// 真窗口里那一下是滚过去的，图里得看得见当前那一条。
    private func logWindow(_ records: [LogRecord], room: Int, current: Int?)
        -> (slice: ArraySlice<LogRecord>, base: Int) {
        if let current, store.logFindOpen {
            let start = max(0, min(current - room / 2, records.count - room - 1))
            let end = min(records.count, start + room + 1)
            return (records[start..<max(start + 1, end)], start)
        }
        let start = max(0, records.count - room - 1)
        return (records[start...], start)
    }

    private func linesStack(_ records: ArraySlice<LogRecord>, base: Int,
                            hits: Set<Int>, current: Int?,
                            showsTime: Bool, showsLevel: Bool) -> some View {
        Group {
            if store.logSkippedBytes > 0, base == 0 {
                Text(verbatim: "… 前面 \(store.logSkippedBytes / 1024) KiB 没有读进来（文件太大，只取了尾部）")
                    .font(Face.sans(10.5))
                    .foregroundStyle(Ink.inkFaint)
                    .padding(.horizontal, Metrics.gutter + 2)
                    .padding(.bottom, 4)
            }
            if scrolls {
                LazyVStack(alignment: .leading, spacing: 0) {
                    logRows(records, base: base, current: current,
                            showsTime: showsTime, showsLevel: showsLevel)
                }
            } else {
                VStack(alignment: .leading, spacing: 0) {
                    logRows(records, base: base, current: current,
                            showsTime: showsTime, showsLevel: showsLevel)
                }
            }
        }
        .padding(.vertical, 6)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    /// 一行日志：色条 / 时间 / 级别 / 消息。
    ///
    /// 时间和级别这两列**整份缓冲说了算**：一行时间戳都没有的日志（纯文本、进度条、
    /// 某个工具自创的格式），时间列就整个不出现 —— 空着两格七八十点宽，读的人只会
    /// 以为界面坏了。判据取整个缓冲而不取可视区：列宽要是随滚动忽宽忽窄，
    /// 眼睛就没法在上面停留。
    ///
    /// **斑马纹试过，不加**（2026-09-24）：认不出前缀的日志确实是一屏一样的字，
    /// 于是隔条记录铺一层 `Ink.surface` 底、出图看了一眼 —— 读起来像表格隔行填色
    /// （`tr:nth-child(even)` 那种），便宜、也没多读出什么。列和色条已经把
    /// 有结构的日志分开了；没结构的那一份，真实情况就是没有结构可依。
    @ViewBuilder
    private func logRows(_ records: ArraySlice<LogRecord>, base: Int, current: Int?,
                         showsTime: Bool, showsLevel: Bool) -> some View {
        ForEach(records.indices, id: \.self) { index in
            let line = base + index - records.startIndex
            logLine(records[index], line: line, isCurrent: line == current,
                    showsTime: showsTime, showsLevel: showsLevel)
                .id(line)
        }
    }

    /// One log record. The column flags are computed once per buffer by
    /// `logRoll`; keeping them out of this row avoids an O(n²) refresh.
    private func logLine(_ record: LogRecord, line: Int, isCurrent: Bool,
                         showsTime: Bool, showsLevel: Bool) -> some View {
        HStack(alignment: .top, spacing: 0) {
            if showsTime {
                fragment(record.time ?? "", tone: Ink.inkFaint, font: Face.log(10),
                         width: LogCol.time, isCurrent: isCurrent)
            }
            if showsLevel {
                fragment(record.level?.label ?? "", tone: levelTone(record.level),
                         font: Face.log(10, .medium), width: LogCol.level, isCurrent: isCurrent)
            }
            fragment(record.message.isEmpty ? " " : record.message,
                     tone: messageTone(record), font: Face.log(11),
                     width: nil, isCurrent: isCurrent, selectable: true)
        }
        // 色条撑满**整条记录**（折行、堆栈那几行都算），于是一屏扫下来是一条竖线。
        // 颜色在这儿指的是位置，不是装饰。
        //
        // 它必须是 overlay，不能是 HStack 里的一个 `Rectangle`：实测过，那样只拿到
        // 一行文字的高度（12pt），而记录折行时有 24pt —— 第二行就没有色条了。
        // overlay 拿的是它盖住的那个视图的尺寸，所以「一条记录一条色条」是排出来的。
        //
        // 顺序也量过：**先让位再画**（padding 在前、overlay 在后）。反过来的话
        // 色条会压到时间那一位的第一个数字上（实测 x 偏右 4pt，正好盖住 `09:41`
        // 的那个 `0`）。
        .padding(.leading, LogCol.rail)
        .overlay(alignment: .leading) {
            Rectangle()
                .fill(incidentTone(record) ?? Color.clear)
                .frame(width: LogCol.railWidth)
        }
        // 当前命中那一行整行给一层薄底：折行、带堆栈的记录里，光靠那几个字
        // 认不出「当前这一条」到哪儿为止。
        .background(isCurrent ? Ink.accentSoft : Color.clear)
        .padding(.horizontal, Metrics.gutter + 2)
        .padding(.vertical, 0.5)
    }

    /// 一格文本。命中就换成涂过高亮的 `AttributedString`，没命中就是纯 `Text`。
    @ViewBuilder private func fragment(_ text: String, tone: Color, font: Font,
                                       width: CGFloat?, isCurrent: Bool,
                                       selectable: Bool = false) -> some View {
        let marked = highlight(text, current: isCurrent)
        let cell = Group {
            if let marked {
                Text(marked)
            } else {
                Text(text)
            }
        }
        .font(font)
        .foregroundStyle(tone)
        .lineLimit(width == nil ? nil : 1)
        .fixedSize(horizontal: false, vertical: true)
        .frame(width: width, alignment: .leading)
        .frame(maxWidth: width == nil ? .infinity : nil, alignment: .leading)

        // 只有正文那一格可以选（时间 / 级别是元数据，选它没有意义）。
        // 两个分支的类型不同，所以不能写成一句三元。
        if selectable {
            cell.textSelection(.enabled)
        } else {
            cell
        }
    }

    /// 级别 → 颜色。**红和琥珀只落在级别列和那条 2pt 色条上**，正文仍是墨色。
    ///
    /// 「错误状态没有红色」那条规矩说的是界面自己的状态（`Ink` 里那句），
    /// 而这里是**数据**：绿加红删那三个色已经是同一个先例。日志里真正要一眼扫到的
    /// 就是那几行出错的 —— 用灰阶表达它，等于把这一屏最该看见的东西藏起来。
    private func levelTone(_ level: LogLevel?) -> Color {
        switch level {
        case .error: return Change.removed
        case .warn: return Change.changed
        case .info: return Ink.inkMuted
        case .debug: return Ink.inkFaint
        case nil: return Ink.inkFaint
        }
    }

    /// 这一行「有没有事」的颜色 —— 色条用它。**级别是主要判据，但访问日志不吃这一套**：
    /// 那些行自称 INFO，出事的其实是末尾那个状态码（404 / 503）。两个都看，
    /// 级别列则照实说打日志的人自称的那一档。
    private func incidentTone(_ record: LogRecord) -> Color? {
        switch record.level {
        case .error: return Change.removed
        case .warn: return Change.changed
        default: break
        }
        guard let status = record.http else { return nil }
        if status >= 500 { return Change.removed }
        if status >= 400 { return Change.changed }
        return nil
    }

    /// 正文的深浅：出事的那一行用墨色，其余是次级灰，续行（堆栈）最浅 ——
    /// 层级靠深浅，不靠加粗。
    private func messageTone(_ record: LogRecord) -> Color {
        if incidentTone(record) != nil { return Ink.ink }
        return record.continuation ? Ink.inkFaint : Ink.inkMuted
    }

    private func shortPath(_ path: String) -> String {
        path.replacingOccurrences(of: FileManager.default.homeDirectoryForCurrentUser.path, with: "~")
    }
}

/// 行内的小按钮：一个描边胶囊。
///
/// 服务板上每一行都有它，所以它得**轻**：不填色、不加粗，只靠一圈发丝边
/// 和它在悬停时收紧一点存在感。整页唯一填了色的是选中的那一行。
struct RowAction: View {
    let title: String
    var tone: Color = Ink.ink
    let action: () -> Void

    @State private var hovering = false

    var body: some View {
        Button(action: action) {
            Text(title)
                .font(Face.sans(10.5, .medium))
                .foregroundStyle(tone)
                .lineLimit(1)
                .padding(.horizontal, 7)
                .padding(.vertical, 2)
                .background(
                    RoundedRectangle(cornerRadius: 4, style: .continuous)
                        .strokeBorder(hovering ? tone.opacity(0.45) : Ink.lineStrong, lineWidth: 1)
                )
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .onHover { now in
            // 一天几十次的动作，动效只能「几乎察觉不到」。
            withAnimation(Motion.hover) { hovering = now }
        }
    }
}

/// 查找框头上那个放大镜。**手画的** —— 界面不混 SF Symbols。
///
/// 有命中时是强调色：框里那一串字到底找着东西没有，隔着半米也看得出来。
struct LensMark: View {
    var on: Bool = false

    var body: some View {
        Path { path in
            path.addEllipse(in: CGRect(x: 1.0, y: 1.0, width: 8.6, height: 8.6))
            path.move(to: CGPoint(x: 8.8, y: 8.8))
            path.addLine(to: CGPoint(x: 12.0, y: 12.0))
        }
        .stroke(on ? Ink.accent : Ink.inkFaint,
                style: StrokeStyle(lineWidth: 1.3, lineCap: .round))
        .frame(width: 14, height: 14)
    }
}

/// 查找栏里那两颗箭头（上一 / 下一条命中）。同样手画。
///
/// 笔画只有 6pt 宽，所以**命中区另给 18×18**：要让一行能点中，
/// 可点区不能只有看得见的那几条线（`SplitHandle` 那条 7pt 的教训）。
struct StepMark: View {
    enum Direction { case previous, next }

    let direction: Direction
    var enabled: Bool = true
    let action: () -> Void

    @State private var hovering = false

    private var tone: Color {
        guard enabled else { return Ink.dormant }
        return hovering ? Ink.accent : Ink.inkMuted
    }

    var body: some View {
        Button(action: action) {
            Path { path in
                switch direction {
                case .next:
                    path.move(to: CGPoint(x: 1, y: 4))
                    path.addLine(to: CGPoint(x: 5.5, y: 8.5))
                    path.addLine(to: CGPoint(x: 10, y: 4))
                case .previous:
                    path.move(to: CGPoint(x: 1, y: 8.5))
                    path.addLine(to: CGPoint(x: 5.5, y: 4))
                    path.addLine(to: CGPoint(x: 10, y: 8.5))
                }
            }
            .stroke(tone, style: StrokeStyle(lineWidth: 1.4, lineCap: .round, lineJoin: .round))
            .frame(width: 11, height: 12)
            .frame(width: 18, height: 18)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(!enabled)
        .onHover { now in
            withAnimation(Motion.hover) { hovering = now }
        }
    }
}
