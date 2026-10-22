import SwiftUI
import BerthTerminal

/// Warp-style block stream: completed cards + one active card that hosts the PTY.
/// There is no separate live-PTY viewport under the stream — interactive work
/// (running commands, idle prompt, alternate-screen TUIs) lives inside the
/// last/active block card.
struct SessionBlockStream: View {
    @ObservedObject var session: TerminalSession
    var selectedID: UUID?
    var onSelect: (CommandBlock) -> Void
    var onSendToAgent: ((CommandBlock) -> Void)?
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
                .padding(.horizontal, Metrics.gutter)
                .padding(.top, 8)
                .padding(.bottom, 10)
            }
            .onAppear { scrollToActive(proxy) }
            .onChange(of: session.commandBlocks.count) { _, _ in scrollToActive(proxy) }
            .onChange(of: runningBlock?.id) { _, _ in scrollToActive(proxy) }
            .onChange(of: alternateScreen) { _, _ in scrollToActive(proxy) }
            .accessibilityIdentifier("console.blockStream")
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var activePtyHeight: CGFloat {
        let floor = availableHeight > 1 ? availableHeight : 320
        if alternateScreen {
            // TUI owns the viewport — still one card, but tall.
            return max(280, floor - 24)
        }
        if runningBlock != nil {
            return max(160, min(260, floor * 0.48))
        }
        if completedBlocks.isEmpty {
            // Sole active card (fresh shell / no integration yet).
            return max(220, floor - 24)
        }
        // Idle prompt after completed blocks — compact live card.
        return max(110, min(168, floor * 0.32))
    }

    private func scrollToActive(_ proxy: ScrollViewProxy) {
        DispatchQueue.main.async {
            withAnimation(.easeOut(duration: 0.12)) {
                proxy.scrollTo("active-block", anchor: .bottom)
            }
        }
    }
}

/// The stream's last card: hosts HostedTerminalView. Same chrome language as
/// completed blocks so it never reads as a second pane.
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
                .stroke(alternateScreen || running != nil ? Ink.accent.opacity(0.45) : Ink.line, lineWidth: 1)
        )
        .clipShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
        .accessibilityIdentifier("console.activeBlock")
        .accessibilityValue(running?.command ?? (alternateScreen ? "tui" : "prompt"))
    }

    @ViewBuilder private var header: some View {
        HStack(spacing: 6) {
            Image(systemName: headerSymbol)
                .font(.system(size: 10, weight: .semibold))
                .foregroundStyle(headerColor)
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
                .foregroundStyle(headerColor)
        }
    }

    private var headerSymbol: String {
        if alternateScreen { return "rectangle.bottomhalf.inset.filled" }
        if running != nil { return "hourglass" }
        return "circle.fill"
    }

    private var headerColor: Color {
        if alternateScreen || running != nil { return Ink.live }
        return Ink.inkFaint
    }

    private var headerTitle: String {
        if let running, let command = running.command, !command.isEmpty {
            return "$ \(command)"
        }
        if alternateScreen { return "交互界面" }
        if shellIntegration { return "提示符" }
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
            if let output = block.capturedOutput?.trimmingCharacters(in: .whitespacesAndNewlines),
               !output.isEmpty {
                Text(truncate(output, limit: 12))
                    .font(Face.mono(Metrics.caption))
                    .foregroundStyle(Ink.inkMuted)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .textSelection(.enabled)
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


enum SessionBlockTime {
    /// Short local clock for block headers (Asia/Shanghai / box zone).
    static func short(_ date: Date) -> String {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_GB")
        formatter.timeZone = TimeZone(identifier: "Asia/Shanghai") ?? .current
        formatter.dateFormat = "HH:mm"
        return formatter.string(from: date)
    }
}
