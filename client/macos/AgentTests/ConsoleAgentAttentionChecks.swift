import BerthAgent
import Foundation

@MainActor
extension AgentChecks {
    static func consoleAgentAttentionChecks() throws {
        let need = #"{"v":1,"type":"request.opened","request":{"id":"r1"}}"#
        try check(ConsoleAgentAttentionParser.parse(eventLine: need) == .needsYou, "request.opened")
        let done = #"{"v":1,"type":"turn.completed","turn":"t1","outcome":"completed"}"#
        try check(ConsoleAgentAttentionParser.parse(eventLine: done) == .turnEnded(summary: "本轮已完成"), "completed turn")
        let lost = #"{"v":1,"type":"turn.completed","outcome":"lost"}"#
        try check(ConsoleAgentAttentionParser.parse(eventLine: lost) == .turnEnded(summary: "连接已断开"), "lost turn")
        try check(ConsoleAgentAttentionParser.parse(eventLine: #"{"type":"turn.started"}"#) == nil, "ignore other events")
        print("PASS: ConsoleAgentAttentionParser classifies stream alert events")
    }
}
