import SwiftUI
import BerthAgent
import BerthTerminal

/// Warp-style block stream: completed cards, plus an active PTY card only while
/// a command is running or an alternate-screen TUI is up. Idle = completed
/// blocks (or empty) + bottom composer — no 「提示符」 live card.
struct SessionBlockStream: View {
    @ObservedObject var session: TerminalSession
    var selectedID: UUID?
    var onSelect: (CommandBlock) -> Void
    var onSendToAgent: ((CommandBlock) -> Void)?
    /// Structured Agent sessions shown as status blocks in the same stream (not top tabs).
    var agentRows: [AgentRow] = []
    var onEnterAgent: ((UUID) -> Void)?
    var availableHeight: CGFloat
    var focusIntent: TerminalFocusIntent?
    var dimTerminalCaret = false
    var bridgePromptKeys = false
    var onPromptKey: ((NSEvent) -> Void)?

    private var completedBlocks: [CommandBlock] {
        session.commandBlocks.filter { !$0.isRunning }
    }

    private var runningBlock: CommandBlock? {
        session.commandBlocks.last(where: { $0.isRunning })
    }

    private var alternateScreen: Bool {
        session.activity.alternateScreen
    }

    /// Visible PTY card: TUI, or shells without integration (sole surface).
    /// With integration, ordinary running commands must NOT mount the live PTY
    /// card — that mount/unmount was the post-submit flash of 提示符/live.
    private var showsActiveCard: Bool {
        if alternateScreen { return true }
        if !session.shellIntegrationEnabled { return true }
        return false
    }

    /// Lightweight running chrome (no TerminalSurface) while a block is in flight.
    private var showsPendingRunningCard: Bool {
        session.shellIntegrationEnabled && runningBlock != nil && !alternateScreen
    }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 8) {
                    ForEach(completedBlocks) { block in
                        SessionCommandBlockCard(
                            block: block,
                            selected: selectedID == block.id,
                            onSelect: { onSelect(block) },
                            onSendToAgent: onSendToAgent.map { handler in { handler(block) } }
                        )
                        .id(block.id)
                    }

                    // Agent status blocks share the stream with Shell cards (product shape).
                    ForEach(agentRows) { row in
                        SessionAgentBlockCard(row: row, onEnter: onEnterAgent.map { handler in { handler(row.id) } })
                            .id("agent-\(row.id.uuidString)")
                    }

                    if showsPendingRunningCard, let running = runningBlock {
                        SessionPendingBlockCard(running: running)
                            .id("pending-block")
                    }

                    if showsActiveCard {
                        SessionActiveBlockCard(
                            running: runningBlock,
                            alternateScreen: alternateScreen,
                            shellIntegration: session.shellIntegrationEnabled,
                            ptyHeight: activePtyHeight,
                            focusIntent: focusIntent,
                            dimTerminalCaret: dimTerminalCaret,
                            bridgePromptKeys: bridgePromptKeys,
                            onPromptKey: onPromptKey,
                            session: session
                        )
                        .id("active-block")
                    }
                }
                .padding(.horizontal, Metrics.gutter)
                .padding(.top, 8)
                .padding(.bottom, 10)
            }
            .background(alignment: .bottom) {
                // Keep PTY hosted while idle so the process stays warm — not a card.
                // Use a real terminal geometry (not 1×1): shrinking to 1×1 reflows the
                // buffer and was a root cause of command-block output contamination.
                if !showsActiveCard {
                    TerminalSurface(session: session, focusIntent: nil,
                                    dimTerminalCaret: true,
                                    bridgePromptKeys: bridgePromptKeys,
                                    onPromptKey: onPromptKey)
                        .frame(width: 720, height: 360)
                        .opacity(0.01)
                        .allowsHitTesting(false)
                        .accessibilityHidden(true)
                        .fixedSize()
                }
            }
            .onAppear { scrollToActive(proxy) }
            .onChange(of: session.commandBlocks.count) { _, _ in scrollToActive(proxy) }
            .onChange(of: runningBlock?.id) { _, _ in scrollToActive(proxy) }
            .onChange(of: alternateScreen) { _, _ in scrollToActive(proxy) }
            .onChange(of: agentRows.count) { _, _ in scrollToActive(proxy) }
            .accessibilityIdentifier("console.blockStream")
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var activePtyHeight: CGFloat {
        let floor = availableHeight > 1 ? availableHeight : 320
        if alternateScreen {
            return max(280, floor - 24)
        }
        if runningBlock != nil {
            return max(160, min(260, floor * 0.48))
        }
        // No shell integration fallback — sole visible PTY card.
        return max(220, floor - 24)
    }

    private func scrollToActive(_ proxy: ScrollViewProxy) {
        let target: AnyHashable?
        if showsActiveCard {
            target = "active-block"
        } else if showsPendingRunningCard {
            target = "pending-block"
        } else if let lastAgent = agentRows.last {
            target = "agent-\(lastAgent.id.uuidString)"
        } else {
            target = completedBlocks.last?.id
        }
        guard let target else { return }
        DispatchQueue.main.async {
            withAnimation(.easeOut(duration: 0.12)) {
                proxy.scrollTo(target, anchor: .bottom)
            }
        }
    }
}


/// Running command without mounting the live PTY (avoids post-submit flash).
struct SessionPendingBlockCard: View {
    let running: CommandBlock

    var body: some View {
        HStack(spacing: 6) {
            Image(systemName: "hourglass")
                .font(.system(size: 10, weight: .semibold))
                .foregroundStyle(Ink.live)
            Text("$ \(running.command ?? "…")")
                .font(Face.mono(Metrics.meta))
                .foregroundStyle(Ink.ink)
                .lineLimit(2)
                .frame(maxWidth: .infinity, alignment: .leading)
            Text(SessionBlockTime.short(running.startedAt))
                .font(Face.mono(Metrics.caption))
                .foregroundStyle(Ink.inkFaint)
            Text("运行中")
                .font(Face.mono(Metrics.caption))
                .foregroundStyle(Ink.live)
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Ink.sunken.opacity(0.45))
        .clipShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
        .accessibilityIdentifier("console.pendingBlock")
        .accessibilityValue(running.command ?? "")
    }
}

/// Active card — only mounted while running / TUI / no-integration fallback.
struct SessionActiveBlockCard: View {
    var running: CommandBlock?
    var alternateScreen: Bool
    var shellIntegration: Bool
    var ptyHeight: CGFloat
    var focusIntent: TerminalFocusIntent?
    var dimTerminalCaret: Bool
    var bridgePromptKeys: Bool
    var onPromptKey: ((NSEvent) -> Void)?
    @ObservedObject var session: TerminalSession

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            header
            TerminalSurface(session: session, focusIntent: focusIntent,
                            dimTerminalCaret: dimTerminalCaret,
                            bridgePromptKeys: bridgePromptKeys,
                            onPromptKey: onPromptKey)
                .frame(maxWidth: .infinity)
                .frame(height: ptyHeight)
                .clipShape(RoundedRectangle(cornerRadius: 6, style: .continuous))
                .accessibilityIdentifier("console.activeBlock.pty")
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Ink.sunken.opacity(0.55))
        .overlay(
            RoundedRectangle(cornerRadius: 8, style: .continuous)
                .stroke(Ink.accent.opacity(0.45), lineWidth: 1)
        )
        .clipShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
        .accessibilityIdentifier("console.activeBlock")
        .accessibilityValue(running?.command ?? (alternateScreen ? "tui" : "shell"))
    }

    @ViewBuilder private var header: some View {
        HStack(spacing: 6) {
            Image(systemName: headerSymbol)
                .font(.system(size: 10, weight: .semibold))
                .foregroundStyle(Ink.live)
            Text(headerTitle)
                .font(Face.mono(Metrics.meta))
                .foregroundStyle(Ink.ink)
                .lineLimit(2)
                .frame(maxWidth: .infinity, alignment: .leading)
            if let running {
                Text(SessionBlockTime.short(running.startedAt))
                    .font(Face.mono(Metrics.caption))
                    .foregroundStyle(Ink.inkFaint)
            }
            Text(headerStatus)
                .font(Face.mono(Metrics.caption))
                .foregroundStyle(Ink.live)
        }
    }

    private var headerSymbol: String {
        if alternateScreen { return "rectangle.bottomhalf.inset.filled" }
        if running != nil { return "hourglass" }
        return "terminal"
    }

    private var headerTitle: String {
        if let running, let command = running.command, !command.isEmpty {
            return "$ \(command)"
        }
        if alternateScreen { return "交互界面" }
        return "Shell"
    }

    private var headerStatus: String {
        if alternateScreen { return "TUI" }
        if running != nil { return "运行中" }
        return "live"
    }
}

struct SessionCommandBlockCard: View {
    let block: CommandBlock
    var selected: Bool
    var onSelect: () -> Void
    var onSendToAgent: (() -> Void)?

    private var displayOutput: String? {
        guard let raw = block.capturedOutput else { return nil }
        let cleaned = ShellCommandCapture.finalizeAccumulated(raw)
        return cleaned.isEmpty ? nil : cleaned
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Image(systemName: statusSymbol)
                    .font(.system(size: 10, weight: .semibold))
                    .foregroundStyle(statusColor)
                Text("$ \(block.command ?? "命令文本不可用")")
                    .font(Face.mono(Metrics.meta))
                    .foregroundStyle(Ink.ink)
                    .lineLimit(2)
                    .frame(maxWidth: .infinity, alignment: .leading)
                Text(SessionBlockTime.short(block.startedAt))
                    .font(Face.mono(Metrics.caption))
                    .foregroundStyle(Ink.inkFaint)
                    .accessibilityLabel("发送于 \(SessionBlockTime.short(block.startedAt))")
                if let code = block.exitCode {
                    Text("exit \(code)")
                        .font(Face.mono(Metrics.caption))
                        .foregroundStyle(code == 0 ? Ink.inkFaint : Change.changed)
                } else if block.interrupted {
                    Text("中断")
                        .font(Face.mono(Metrics.caption))
                        .foregroundStyle(Change.changed)
                }
                if let onSendToAgent, block.command != nil {
                    Button(action: onSendToAgent) {
                        Text("发给 Agent")
                            .font(Face.sans(Metrics.caption, .medium))
                            .foregroundStyle(Ink.accent)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("console.commandBlock.sendToAgent")
                }
            }
            if let output = displayOutput {
                // verbatim: LocalizedStringKey collapses runs of spaces / tabs — breaks ls columns.
                Text(verbatim: truncate(output, limit: 48))
                    .font(Face.mono(Metrics.meta))
                    .foregroundStyle(Ink.inkMuted)
                    .lineSpacing(3)
                    .multilineTextAlignment(.leading)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
                    .padding(.top, 2)
                    .accessibilityIdentifier("console.commandBlock.output")
            }
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 8)
        .background(selected ? Ink.accentSoft : Ink.sunken.opacity(0.55))
        .overlay(
            RoundedRectangle(cornerRadius: 8, style: .continuous)
                .stroke(selected ? Ink.accent.opacity(0.55) : Ink.line, lineWidth: 1)
        )
        .clipShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
        .contentShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
        .onTapGesture(perform: onSelect)
        .accessibilityIdentifier("console.commandBlock." + block.id.uuidString)
        .accessibilityValue(block.command ?? "")
        .accessibilityAddTraits(.isButton)
    }

    private var statusSymbol: String {
        if block.interrupted { return "exclamationmark.triangle" }
        if block.exitCode == 0 { return "checkmark.circle" }
        if block.exitCode != nil { return "xmark.circle" }
        return "circle"
    }

    private var statusColor: Color {
        if block.interrupted { return Change.changed }
        if block.exitCode == 0 { return Ink.inkFaint }
        if block.exitCode != nil { return Change.changed }
        return Ink.inkMuted
    }

    private func truncate(_ text: String, limit: Int) -> String {
        let lines = text.split(separator: "\n", omittingEmptySubsequences: false)
        if lines.count <= limit { return text }
        return lines.prefix(limit).joined(separator: "\n") + "\n…"
    }
}


/// Agent turn/session as a status block in the unified stream (运行中 / 完成 / 失败).
struct SessionAgentBlockCard: View {
    let row: AgentRow
    var onEnter: (() -> Void)?

    var body: some View {
        Button(action: { onEnter?() }) {
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 6) {
                    Image(systemName: statusSymbol)
                        .font(.system(size: 10, weight: .semibold))
                        .foregroundStyle(statusColor)
                    Text(row.title)
                        .font(Face.sans(Metrics.meta, .semibold))
                        .foregroundStyle(Ink.ink)
                        .lineLimit(1)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    if let since = row.since {
                        Text(SessionBlockTime.short(since))
                            .font(Face.mono(Metrics.caption))
                            .foregroundStyle(Ink.inkFaint)
                    }
                    Text(statusLabel)
                        .font(Face.mono(Metrics.caption))
                        .foregroundStyle(statusColor)
                    Text("进入")
                        .font(Face.sans(Metrics.caption, .medium))
                        .foregroundStyle(Ink.accent)
                }
                if let activity = row.activity, !activity.isEmpty {
                    Text(activity)
                        .font(Face.mono(Metrics.caption))
                        .foregroundStyle(Ink.inkMuted)
                        .lineLimit(2)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                if row.queuedCount > 0 {
                    Text("排队 \(row.queuedCount)")
                        .font(Face.mono(Metrics.caption))
                        .foregroundStyle(Ink.inkFaint)
                }
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 8)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(row.isTarget ? Ink.accentSoft : Ink.sunken.opacity(0.55))
            .overlay(
                RoundedRectangle(cornerRadius: 8, style: .continuous)
                    .stroke(row.isTarget ? Ink.accent.opacity(0.45) : Ink.line, lineWidth: 1)
            )
            .clipShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
            .contentShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("console.agentBlock." + row.id.uuidString)
        .accessibilityValue(statusLabel)
        .accessibilityHint("打开 Agent 会话")
    }

    private var statusLabel: String {
        switch row.status {
        case .starting: return "启动中"
        case .idle: return "就绪"
        case .needsYou: return "等你"
        case .working: return "运行中"
        case .completed: return "完成"
        case .failed: return "失败"
        case .interrupted: return "已中断"
        case .lost: return "丢失"
        case .ended(let resumable): return resumable ? "已结束·可续" : "已结束"
        }
    }

    private var statusSymbol: String {
        switch row.status {
        case .starting, .working: return "hourglass"
        case .needsYou: return "hand.raised"
        case .completed, .idle: return "checkmark.circle"
        case .failed, .lost: return "xmark.circle"
        case .interrupted: return "exclamationmark.triangle"
        case .ended: return "stop.circle"
        }
    }

    private var statusColor: Color {
        switch row.status {
        case .starting, .working: return Ink.live
        case .needsYou: return Change.changed
        case .failed, .lost: return Change.removed
        case .completed, .idle, .ended, .interrupted: return Ink.inkFaint
        }
    }
}

enum SessionBlockTime {
    static func short(_ date: Date) -> String {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_GB")
        formatter.timeZone = TimeZone(identifier: "Asia/Shanghai") ?? .current
        formatter.dateFormat = "HH:mm"
        return formatter.string(from: date)
    }
}
