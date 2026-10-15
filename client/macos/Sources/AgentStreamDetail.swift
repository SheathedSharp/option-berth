import BerthAgent
import SwiftUI

struct AgentStreamDetail: View {
    let session: AgentStreamSession
    let queuedCount: Int
    var queuedPreview: String?
    var onWithdrawQueued: (() -> Void)?
    let onInterrupt: () -> Void
    let onClose: () -> Void
    let onRespond: (String, AgentDecision) -> Void
    var onAdoptCommand: (String) -> Void = { _ in }
    var onOpenGitPath: (String) -> Void = { _ in }
    var onContinueInNative: () -> Void = {}

    var body: some View {
        VStack(alignment: .leading, spacing: Metrics.inline) {
            header
            Hairline()
            ScrollView {
                VStack(alignment: .leading, spacing: Metrics.gutter) {
                    ForEach(session.snapshot.turns, id: \.id) { turn in
                        turnBlock(turn)
                    }
                    ForEach(session.snapshot.requests) { request in
                        requestCard(request)
                    }
                }
                .padding(Metrics.gutter)
            }
            if queuedCount > 0 {
                HStack(spacing: Metrics.inline) {
                    if let queuedPreview {
                        Text("排队 · \(queuedPreview)")
                            .font(Face.sans(Metrics.caption))
                            .foregroundStyle(Ink.inkMuted)
                            .lineLimit(2)
                    } else {
                        Text("排队 \(queuedCount) 条，本轮结束后自动发送")
                            .font(Face.sans(Metrics.caption))
                            .foregroundStyle(Ink.inkMuted)
                    }
                    Spacer(minLength: 0)
                    if queuedPreview != nil, let onWithdrawQueued {
                        ConsoleToolbarAction(title: "撤回", identifier: "console.streamQueueWithdraw." + session.id.uuidString,
                                             perform: onWithdrawQueued)
                    }
                }
                .padding(.horizontal, Metrics.gutter)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(Ink.canvas)
        .accessibilityIdentifier("console.streamDetail." + session.id.uuidString)
    }

    private var header: some View {
        let status = AgentDisplayDeriver.status(phase: session.snapshot.phase, turns: session.snapshot.turns,
                                                requests: session.snapshot.requests)
        let activity = AgentDisplayDeriver.activity(phase: session.snapshot.phase, turns: session.snapshot.turns,
                                                  requests: session.snapshot.requests, now: Date()) ?? ""
        return HStack(alignment: .top, spacing: Metrics.inline) {
            VStack(alignment: .leading, spacing: 2) {
                Text(session.title).font(Face.sans(Metrics.row, .semibold)).lineLimit(2)
                Text(activity).font(Face.sans(Metrics.caption)).foregroundStyle(statusColor(status))
            }
            Spacer(minLength: 0)
            if status == .working || status == .needsYou {
                ConsoleToolbarAction(title: "中断", identifier: "console.streamInterrupt." + session.id.uuidString,
                                     perform: onInterrupt)
            }
            ConsoleToolbarAction(title: "结束", identifier: "console.streamClose." + session.id.uuidString,
                                 perform: onClose)
        }
        .padding(.horizontal, Metrics.gutter).padding(.vertical, 8)
    }

    private func turnBlock(_ turn: AgentTurn) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("你").font(Face.sans(Metrics.caption, .semibold)).foregroundStyle(Ink.inkMuted)
            Text(turn.input).font(Face.mono(Metrics.caption)).textSelection(.enabled)
            ForEach(turn.items) { item in
                itemLine(item)
            }
            if let diff = turn.diff, !diff.files.isEmpty {
                VStack(alignment: .leading, spacing: 4) {
                    Text(fileDiffSummary(diff)).font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkMuted)
                    ForEach(diff.files, id: \.path) { file in
                        ConsoleToolbarAction(title: "在 Git 中查看 · \(file.path)",
                                             identifier: "console.streamGit." + file.path,
                                             perform: { onOpenGitPath(file.path) })
                            .font(Face.sans(Metrics.caption))
                    }
                }
            }
        }
    }

    private func itemLine(_ item: AgentItem) -> some View {
        Group {
            switch item {
            case .message(_, let text):
                Text(text).font(Face.mono(Metrics.caption)).textSelection(.enabled)
            case .reasoning(_, let summary):
                Text("思考：\(summary)").font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkFaint)
            case .command(_, let command, _, let status, let exit, let duration, _):
                HStack(spacing: 8) {
                    Text("$ \(command) · \(commandStatus(status, exit: exit, duration: duration))")
                        .font(Face.mono(Metrics.caption))
                    Spacer(minLength: 0)
                    ConsoleToolbarAction(title: "放到输入条", identifier: "console.streamAdoptCommand",
                                         perform: { onAdoptCommand(command) })
                        .font(Face.sans(Metrics.caption))
                }
            case .fileChange(_, let files, let status):
                Text("文件 · \(files.count) 个 · \(itemStatusLabel(status))").font(Face.mono(Metrics.caption))
            case .tool(_, let name, _, let status):
                Text("工具 \(name) · \(itemStatusLabel(status))").font(Face.mono(Metrics.caption))
            case .plan(_, let steps):
                Text("计划 · \(steps.filter(\.done).count)/\(steps.count)").font(Face.mono(Metrics.caption))
            case .notice(_, _, let code, let text):
                if code == "unsupported_request" || code == "provider_question" {
                    providerQuestionCard(code: code, text: text)
                } else {
                    Text(text).font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkFaint)
                }
            }
        }
    }

    private func providerQuestionCard(code: String, text: String) -> some View {
        let title = code == "provider_question" ? "需要你在原生终端回答" : "结构化模式不支持此请求"
        return VStack(alignment: .leading, spacing: 6) {
            Text(title).font(Face.sans(Metrics.caption, .semibold)).foregroundStyle(Change.changed)
            Text(text).font(Face.mono(Metrics.caption)).textSelection(.enabled)
            ConsoleToolbarAction(title: "在原生终端中继续",
                                 identifier: "console.streamContinueNative." + session.id.uuidString,
                                 perform: onContinueInNative)
                .font(Face.sans(Metrics.caption))
        }
        .padding(8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .overlay(RoundedRectangle(cornerRadius: 4).stroke(Change.changed.opacity(0.55), lineWidth: 1))
        .background(Ink.sunken)
        .clipShape(RoundedRectangle(cornerRadius: 4))
    }

    private func requestCard(_ request: AgentRequest) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("等你确认").font(Face.sans(Metrics.caption, .semibold)).foregroundStyle(Change.changed)
            if let command = request.command {
                Text(command).font(Face.mono(Metrics.caption)).textSelection(.enabled)
            }
            HStack(spacing: 8) {
                if request.decisions.contains(.allow) {
                    approvalButton("允许", decision: .allow, request: request)
                }
                if request.decisions.contains(.allowSession) {
                    approvalButton("本会话允许", decision: .allowSession, request: request)
                }
                if request.decisions.contains(.deny) {
                    approvalButton("拒绝", decision: .deny, request: request)
                }
            }
        }
        .padding(8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Ink.sunken)
        .clipShape(RoundedRectangle(cornerRadius: 4))
        .accessibilityIdentifier("console.streamRequest." + request.id)
    }

    private func approvalButton(_ title: String, decision: AgentDecision, request: AgentRequest) -> some View {
        ConsoleToolbarAction(title: title,
                             identifier: "console.streamApprove.\(request.id).\(decision.rawValue)",
                             perform: { onRespond(request.id, decision) })
            .disabled(request.answering != nil)
    }

    private func itemStatusLabel(_ status: ItemStatus) -> String {
        switch status {
        case .running: return "运行中"
        case .completed: return "完成"
        case .failed: return "失败"
        case .declined: return "已拒绝"
        }
    }

    private func commandStatus(_ status: ItemStatus, exit: Int?, duration: Int?) -> String {
        var parts: [String] = [itemStatusLabel(status)]
        if let exit { parts.append("exit \(exit)") }
        if let duration { parts.append("\(duration)ms") }
        return parts.joined(separator: " · ")
    }

    private func fileDiffSummary(_ diff: TurnDiff) -> String {
        let names = diff.files.prefix(3).map { "\($0.path) +\($0.added) −\($0.removed)" }
        let suffix = diff.truncated ? "（已截断）" : ""
        return names.joined(separator: ", ") + suffix
    }

    private func statusColor(_ status: AgentDisplayStatus) -> Color {
        switch status {
        case .needsYou: return Change.changed
        case .working: return Ink.live
        case .failed, .lost: return Change.removed
        default: return Ink.inkMuted
        }
    }
}
