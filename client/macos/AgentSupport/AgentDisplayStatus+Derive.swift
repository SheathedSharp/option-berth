import Foundation

public enum AgentDisplayDeriver {
    public static func targetState(phase: StreamPhase, turns: [AgentTurn], requests: [AgentRequest]) -> StreamTargetState {
        switch phase {
        case .starting:
            return .starting
        case .ending:
            return .working
        case .ended(let reason):
            switch reason {
            case .closed: return .ended(resumable: true)
            case .providerExit: return .ended(resumable: false)
            case .lost: return .ended(resumable: false)
            }
        case .ready:
            guard let latest = turns.last else { return .idle }
            if latest.outcome == .running {
                if !requests.isEmpty { return .needsYou }
                return .working
            }
            return .idle
        }
    }

    public static func status(phase: StreamPhase, turns: [AgentTurn], requests: [AgentRequest]) -> AgentDisplayStatus {
        switch phase {
        case .starting:
            return .starting
        case .ending:
            return .working
        case .ended(let reason):
            switch reason {
            case .closed: return .ended(resumable: true)
            case .providerExit: return .ended(resumable: false)
            case .lost: return .lost
            }
        case .ready:
            guard let latest = turns.last else { return .idle }
            if latest.outcome == .running, !requests.isEmpty { return .needsYou }
            switch latest.outcome {
            case .running: return .working
            case .completed: return .completed
            case .failed: return .failed
            case .interrupted: return .interrupted
            case .lost: return .lost
            }
        }
    }

    public static func activity(phase: StreamPhase, turns: [AgentTurn], requests: [AgentRequest], now: Date) -> String? {
        switch status(phase: phase, turns: turns, requests: requests) {
        case .starting:
            return nil
        case .idle:
            return "等第一条消息"
        case .needsYou:
            if let request = requests.first {
                if let command = request.command { return requestSummary(command) }
                if let path = request.paths.first { return requestSummary(path) }
            }
            return "等你确认"
        case .working:
            return runningSummary(turn: turns.last, now: now)
        case .completed, .failed, .interrupted, .lost:
            return endedSummary(turn: turns.last, now: now)
        case .ended(let resumable):
            return resumable ? "可续接" : "不可续接"
        }
    }

    /// Inline body for the unified stream card — prefer message text, else live activity / request.
    public static func resultPreview(turn: AgentTurn?, activity: String?, requests: [AgentRequest]) -> String? {
        if let turn {
            let messages = turn.items.compactMap { item -> String? in
                if case .message(_, let text) = item {
                    let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
                    return trimmed.isEmpty ? nil : text
                }
                return nil
            }
            if let last = messages.last { return last }

            // While running with no message yet, surface the active tool/command line.
            if turn.outcome == .running, let item = turn.items.last(where: { runningItem($0) }) {
                switch item {
                case .command(_, let command, _, _, _, _, _):
                    return "$ \(command)"
                case .fileChange(_, let files, _):
                    let names = files.prefix(3).map(\.path).joined(separator: ", ")
                    let more = files.count > 3 ? " …" : ""
                    return names.isEmpty ? "修改文件" : "修改 \(names)\(more)"
                case .tool(_, let name, let preview, _):
                    let trimmed = preview.trimmingCharacters(in: .whitespacesAndNewlines)
                    return trimmed.isEmpty ? "调用 \(name)" : "\(name): \(trimmed)"
                case .reasoning(_, let summary):
                    let trimmed = summary.trimmingCharacters(in: .whitespacesAndNewlines)
                    return trimmed.isEmpty ? "思考中" : "思考：\(trimmed)"
                default:
                    break
                }
            }

            // Failed / interrupted with no message — keep a short failure line.
            if case .failed(_, let message) = turn.outcome, !message.isEmpty {
                return message
            }
        }
        if let request = requests.first {
            if let command = request.command { return "等你确认 · \(command)" }
            if let path = request.paths.first { return "等你确认 · \(path)" }
            return "等你确认"
        }
        return activity
    }

    private static func requestSummary(_ text: String) -> String {
        "等你确认 · \(text)"
    }

    private static func runningSummary(turn: AgentTurn?, now: Date) -> String? {
        guard let turn else { return nil }
        let elapsed = Int(now.timeIntervalSince(turn.sentAt))
        if let item = turn.items.last(where: { runningItem($0) }) {
            switch item {
            case .command(_, let command, _, _, _, _, _):
                return "运行 \(command) · \(elapsed)s"
            case .fileChange(_, let files, _):
                let count = files.count
                if let first = files.first?.path {
                    return count > 1 ? "修改 \(first) 等 \(count) 个文件 · \(elapsed)s" : "修改 \(first) · \(elapsed)s"
                }
            case .tool(_, let name, _, _):
                return "调用 \(name) · \(elapsed)s"
            case .message:
                return "回复中 · \(elapsed)s"
            case .reasoning:
                return "思考中 · \(elapsed)s"
            default:
                break
            }
        }
        return "工作中 · \(elapsed)s"
    }

    private static func runningItem(_ item: AgentItem) -> Bool {
        switch item {
        case .command(_, _, _, let status, _, _, _), .fileChange(_, _, let status),
             .tool(_, _, _, let status):
            return status == .running
        case .message, .reasoning, .plan, .notice:
            return false
        }
    }

    private static func endedSummary(turn: AgentTurn?, now: Date) -> String? {
        guard let turn, let endedAt = turn.endedAt ?? Optional(turn.sentAt) else { return nil }
        let minutes = max(1, Int(now.timeIntervalSince(endedAt) / 60))
        if let message = turn.items.compactMap({ item -> String? in
            if case .message(_, let text) = item { return text.split(separator: "\n").first.map(String.init) }
            return nil
        }).last {
            return "\(message) · \(minutes) 分钟前"
        }
        return "\(minutes) 分钟前"
    }
}
