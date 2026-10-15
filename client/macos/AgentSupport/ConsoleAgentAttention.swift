import Foundation

public enum ConsoleAgentAttentionKind: Equatable, Sendable {
    case needsYou
    case turnEnded(summary: String)
}

public enum ConsoleAgentAttentionParser {
    public static func parse(eventLine: String) -> ConsoleAgentAttentionKind? {
        guard let data = eventLine.data(using: .utf8),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let type = object["type"] as? String else { return nil }
        switch type {
        case "request.opened":
            return .needsYou
        case "turn.completed":
            let outcome = object["outcome"] as? String ?? "completed"
            switch outcome {
            case "completed": return .turnEnded(summary: "本轮已完成")
            case "failed": return .turnEnded(summary: "本轮失败")
            case "interrupted": return .turnEnded(summary: "本轮已中断")
            case "lost": return .turnEnded(summary: "连接已断开")
            default: return .turnEnded(summary: "本轮结束")
            }
        default:
            return nil
        }
    }

    public static func notificationBody(for kind: ConsoleAgentAttentionKind) -> String {
        switch kind {
        case .needsYou: return "需要你确认"
        case .turnEnded(let summary): return summary
        }
    }
}
