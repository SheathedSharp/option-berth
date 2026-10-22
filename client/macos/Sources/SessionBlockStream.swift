import SwiftUI
import BerthTerminal

/// Warp-like command block cards above the live PTY (v15).
/// Completed / running shell-integration blocks render as selectable cards;
/// the live HostedTerminalView remains the active unfinished region below.
struct SessionBlockStream: View {
    @ObservedObject var session: TerminalSession
    var selectedID: UUID?
    var onSelect: (CommandBlock) -> Void
    var onSendToAgent: ((CommandBlock) -> Void)?

    private var blocks: [CommandBlock] { session.commandBlocks }

    var body: some View {
        if blocks.isEmpty {
            EmptyView()
        } else {
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 8) {
                        ForEach(blocks) { block in
                            SessionCommandBlockCard(
                                block: block,
                                selected: selectedID == block.id,
                                onSelect: { onSelect(block) },
                                onSendToAgent: block.isRunning ? nil : onSendToAgent.map { handler in { handler(block) } }
                            )
                            .id(block.id)
                        }
                    }
                    .padding(.horizontal, Metrics.gutter)
                    .padding(.top, 8)
                    .padding(.bottom, 4)
                }
                .frame(maxHeight: blockStreamMaxHeight)
                .onChange(of: blocks.count) { _, _ in
                    if let last = blocks.last?.id {
                        withAnimation(.easeOut(duration: 0.15)) {
                            proxy.scrollTo(last, anchor: .bottom)
                        }
                    }
                }
                .accessibilityIdentifier("console.blockStream")
            }
        }
    }

    private var blockStreamMaxHeight: CGFloat {
        // Cap so live PTY / prompt always keeps room; stream scrolls inside.
        let n = blocks.count
        if n <= 2 { return 160 }
        if n <= 5 { return 240 }
        return 320
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
                if let code = block.exitCode {
                    Text("exit \(code)")
                        .font(Face.mono(Metrics.caption))
                        .foregroundStyle(code == 0 ? Ink.inkFaint : Change.changed)
                } else if block.isRunning {
                    Text("运行中")
                        .font(Face.mono(Metrics.caption))
                        .foregroundStyle(Ink.live)
                } else if block.interrupted {
                    Text("中断")
                        .font(Face.mono(Metrics.caption))
                        .foregroundStyle(Change.changed)
                }
                if let onSendToAgent, !block.isRunning, block.command != nil {
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
        if block.isRunning { return "hourglass" }
        if block.interrupted { return "exclamationmark.triangle" }
        if block.exitCode == 0 { return "checkmark.circle" }
        if block.exitCode != nil { return "xmark.circle" }
        return "circle"
    }

    private var statusColor: Color {
        if block.isRunning { return Ink.live }
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
