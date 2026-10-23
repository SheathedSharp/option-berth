import SwiftUI
import BerthAgent
import BerthTerminal

/// Warp-style block stream: completed cards, plus an active PTY card only while
/// a command is running or an alternate-screen TUI is up. Idle = completed
/// blocks (or empty) + bottom composer — no 「提示符」 live card.
///
/// Shell command cards and Agent turn cards share one chronological scroll
/// with the same card chrome: status icon · prompt · kind tag · time (± exit).
struct SessionBlockStream: View {
    @ObservedObject var session: TerminalSession
    var selectedID: UUID?
    var onSelect: (CommandBlock) -> Void
    var onSendToAgent: ((CommandBlock) -> Void)?
    /// Agent turn / queued cards shown as peers in the same stream (not top tabs).
    var agentRows: [AgentRow] = []
    /// Focus / expand a turn in-stream (same chrome); do not navigate away.
    var onEnterAgent: ((AgentRow) -> Void)?
    /// Card id to expand + scroll to (tap selection in the unified stream).
    var focusedAgentCardID: UUID? = nil
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

    /// Peer cards (completed shell + agent turns) sorted by start time so they
    /// interleave instead of dumping all agents under all shells.
    private var peerItems: [SessionStreamPeer] {
        let shells = completedBlocks.map { SessionStreamPeer.shell($0) }
        let agents = agentRows.map { SessionStreamPeer.agent($0) }
        return (shells + agents).sorted { lhs, rhs in
            if lhs.sortDate != rhs.sortDate { return lhs.sortDate < rhs.sortDate }
            return lhs.stableKey < rhs.stableKey
        }
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
                    ForEach(peerItems) { peer in
                        switch peer {
                        case .shell(let block):
                            SessionCommandBlockCard(
                                block: block,
                                selected: selectedID == block.id,
                                onSelect: { onSelect(block) }
                            )
                            .id(block.id)
                        case .agent(let row):
                            SessionAgentBlockCard(
                                row: row,
                                expanded: focusedAgentCardID == row.id || row.isTarget,
                                onEnter: onEnterAgent.map { handler in { handler(row) } }
                            )
                            .id("agent-\(row.id.uuidString)")
                        }
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
            .onChange(of: focusedAgentCardID) { _, id in
                guard let id else { return }
                DispatchQueue.main.async {
                    withAnimation(.easeOut(duration: 0.12)) {
                        proxy.scrollTo("agent-\(id.uuidString)", anchor: .center)
                    }
                }
            }
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
        } else if let last = peerItems.last {
            target = last.scrollID
        } else {
            target = nil
        }
        guard let target else { return }
        DispatchQueue.main.async {
            withAnimation(.easeOut(duration: 0.12)) {
                proxy.scrollTo(target, anchor: .bottom)
            }
        }
    }
}

/// Chronological peer in the unified block stream.
private enum SessionStreamPeer: Identifiable {
    case shell(CommandBlock)
    case agent(AgentRow)

    var id: String {
        switch self {
        case .shell(let block): return "shell-\(block.id.uuidString)"
        case .agent(let row): return "agent-\(row.id.uuidString)"
        }
    }

    var scrollID: AnyHashable {
        switch self {
        case .shell(let block): return block.id
        case .agent(let row): return "agent-\(row.id.uuidString)"
        }
    }

    var sortDate: Date {
        switch self {
        case .shell(let block): return block.startedAt
        // No turn yet → pin to end of the peer list (just-created session).
        case .agent(let row): return row.since ?? .distantFuture
        }
    }

    var stableKey: String { id }
}


/// Shared status icon language for Shell + Agent blocks:
/// 运行中 / 排队中 / 已完成 / 有异常.
enum SessionBlockStatusChrome {
    enum Kind {
        case running
        case queued
        case completed
        case abnormal
    }

    static func kind(for block: CommandBlock) -> Kind {
        if block.isRunning { return .running }
        if block.interrupted { return .abnormal }
        if let code = block.exitCode { return code == 0 ? .completed : .abnormal }
        return .completed
    }

    static func kind(for status: AgentDisplayStatus) -> Kind {
        switch status {
        case .starting, .working: return .running
        case .queued: return .queued
        case .completed, .idle, .ended: return .completed
        case .failed, .interrupted, .lost, .needsYou: return .abnormal
        }
    }

    static func symbol(_ kind: Kind) -> String {
        switch kind {
        case .running: return "hourglass"
        case .queued: return "clock"
        case .completed: return "checkmark.circle"
        case .abnormal: return "exclamationmark.triangle"
        }
    }

    static func color(_ kind: Kind) -> Color {
        switch kind {
        case .running: return Ink.live
        case .queued: return Ink.inkMuted
        case .completed: return Ink.inkFaint
        case .abnormal: return Change.changed
        }
    }

    /// Agent prompt prefix — distinct from Shell `$`.
    static let agentPromptPrefix = "›"
}

/// Running command without mounting the live PTY (avoids post-submit flash).
struct SessionPendingBlockCard: View {
    let running: CommandBlock

    var body: some View {
        HStack(spacing: 6) {
            let kind = SessionBlockStatusChrome.Kind.running
            Image(systemName: SessionBlockStatusChrome.symbol(kind))
                .font(.system(size: 10, weight: .semibold))
                .foregroundStyle(SessionBlockStatusChrome.color(kind))
                .accessibilityLabel("运行中")
            Text("$ \(running.command ?? "…")")
                .font(Face.mono(Metrics.meta))
                .foregroundStyle(Ink.ink)
                .lineLimit(2)
                .frame(maxWidth: .infinity, alignment: .leading)
            Tag(text: "shell", tone: .quiet)
            Text(SessionBlockTime.short(running.startedAt))
                .font(Face.mono(Metrics.caption))
                .foregroundStyle(Ink.inkFaint)
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
            Tag(text: "shell", tone: .quiet)
            if let running {
                Text(SessionBlockTime.short(running.startedAt))
                    .font(Face.mono(Metrics.caption))
                    .foregroundStyle(Ink.inkFaint)
            }
        }
    }

    private var headerSymbol: String {
        if alternateScreen { return "rectangle.bottomhalf.inset.filled" }
        if running != nil { return SessionBlockStatusChrome.symbol(.running) }
        return "terminal"
    }

    private var headerTitle: String {
        if let running, let command = running.command, !command.isEmpty {
            return "$ \(command)"
        }
        if alternateScreen { return "交互界面" }
        return "Shell"
    }
}

struct SessionCommandBlockCard: View {
    let block: CommandBlock
    var selected: Bool
    var onSelect: () -> Void

    private var displayOutput: String? {
        guard let raw = block.capturedOutput else { return nil }
        let cleaned = ShellCommandCapture.finalizeAccumulated(raw)
        return cleaned.isEmpty ? nil : cleaned
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                let kind = SessionBlockStatusChrome.kind(for: block)
                Image(systemName: SessionBlockStatusChrome.symbol(kind))
                    .font(.system(size: 10, weight: .semibold))
                    .foregroundStyle(SessionBlockStatusChrome.color(kind))
                    .accessibilityLabel(statusAccessibilityLabel(kind))
                Text("$ \(block.command ?? "命令文本不可用")")
                    .font(Face.mono(Metrics.meta))
                    .foregroundStyle(Ink.ink)
                    .lineLimit(2)
                    .frame(maxWidth: .infinity, alignment: .leading)
                Tag(text: "shell", tone: .quiet)
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

    private func statusAccessibilityLabel(_ kind: SessionBlockStatusChrome.Kind) -> String {
        switch kind {
        case .running: return "运行中"
        case .queued: return "排队中"
        case .completed: return "已完成"
        case .abnormal: return "有异常"
        }
    }

    private func truncate(_ text: String, limit: Int) -> String {
        let lines = text.split(separator: "\n", omittingEmptySubsequences: false)
        if lines.count <= limit { return text }
        return lines.prefix(limit).joined(separator: "\n") + "\n…"
    }
}


/// Agent turn as a peer of the shell card: same chrome, `›` prompt prefix.
struct SessionAgentBlockCard: View {
    let row: AgentRow
    /// Selected / tapped in the unified stream — expand result, keep isomorphic chrome.
    var expanded: Bool = false
    var onEnter: (() -> Void)?

    private var highlighted: Bool { expanded || row.isTarget }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                let kind = SessionBlockStatusChrome.kind(for: row.status)
                Image(systemName: SessionBlockStatusChrome.symbol(kind))
                    .font(.system(size: 10, weight: .semibold))
                    .foregroundStyle(SessionBlockStatusChrome.color(kind))
                    .accessibilityLabel(statusAccessibilityLabel(kind))
                Text(headerPrompt)
                    .font(Face.mono(Metrics.meta))
                    .foregroundStyle(Ink.ink)
                    .lineLimit(expanded ? 6 : 2)
                    .frame(maxWidth: .infinity, alignment: .leading)
                Tag(text: "agent-session", tone: .quiet)
                if let since = row.since {
                    Text(SessionBlockTime.short(since))
                        .font(Face.mono(Metrics.caption))
                        .foregroundStyle(Ink.inkFaint)
                        .accessibilityLabel("发送于 \(SessionBlockTime.short(since))")
                }
                if let code = row.exitCode {
                    Text("exit \(code)")
                        .font(Face.mono(Metrics.caption))
                        .foregroundStyle(code == 0 ? Ink.inkFaint : Change.changed)
                }
            }
            if let result = displayResult {
                Text(verbatim: truncate(result, limit: expanded ? 400 : 48))
                    .font(Face.mono(Metrics.meta))
                    .foregroundStyle(Ink.inkMuted)
                    .lineSpacing(3)
                    .multilineTextAlignment(.leading)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
                    .padding(.top, 2)
                    .accessibilityIdentifier("console.agentBlock.result")
            }
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(highlighted ? Ink.accentSoft : Ink.sunken.opacity(0.55))
        .overlay(
            RoundedRectangle(cornerRadius: 8, style: .continuous)
                .stroke(highlighted ? Ink.accent.opacity(0.45) : Ink.line, lineWidth: 1)
        )
        .clipShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
        .contentShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
        .onTapGesture { onEnter?() }
        .accessibilityIdentifier("console.agentBlock." + row.id.uuidString)
        .accessibilityValue(statusAccessibilityLabel(SessionBlockStatusChrome.kind(for: row.status)))
        .accessibilityHint("在块流中聚焦此回合")
        .accessibilityAddTraits(.isButton)
    }

    private var headerPrompt: String {
        let prefix = SessionBlockStatusChrome.agentPromptPrefix
        if let prompt = row.userPrompt, !prompt.isEmpty {
            return "\(prefix) \(prompt)"
        }
        // No turn yet — fall back to session title so the card still reads as a peer.
        return "\(prefix) \(row.title)"
    }

    private var displayResult: String? {
        if let text = row.resultText?.trimmingCharacters(in: .whitespacesAndNewlines), !text.isEmpty {
            return row.resultText
        }
        if let activity = row.activity?.trimmingCharacters(in: .whitespacesAndNewlines), !activity.isEmpty {
            return row.activity
        }
        return nil
    }

    private func statusAccessibilityLabel(_ kind: SessionBlockStatusChrome.Kind) -> String {
        switch kind {
        case .running: return "运行中"
        case .queued: return "排队中"
        case .completed: return "已完成"
        case .abnormal: return "有异常"
        }
    }

    private func truncate(_ text: String, limit: Int) -> String {
        let lines = text.split(separator: "\n", omittingEmptySubsequences: false)
        if lines.count <= limit { return text }
        return lines.prefix(limit).joined(separator: "\n") + "\n…"
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
