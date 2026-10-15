import Foundation

public enum SlashCommand: String, CaseIterable, Equatable, Sendable {
    case agentSelect = "/agent-select"
    case agentNew = "/agent-new"
    case agent = "/agent"
    case interrupt = "/interrupt"
    case close = "/close"
    case native = "/native"
    case resume = "/resume"
    case shellNew = "/shell-new"
    case shell = "/shell"
    case focus = "/focus"
    case rename = "/rename"
    case history = "/history"

    public var title: String {
        switch self {
        case .agentSelect: return "选择默认 Agent"
        case .agentNew: return "新建 Agent 会话"
        case .agent: return "切换 ⌘↩ 目标"
        case .interrupt: return "中断当前轮次"
        case .close: return "结束目标会话"
        case .native: return "在原生终端中继续"
        case .resume: return "从会话文件续接"
        case .shellNew: return "新建 Shell"
        case .shell: return "切换当前 Shell"
        case .focus: return "把键盘交给终端"
        case .rename: return "重命名"
        case .history: return "搜索命令历史"
        }
    }

    public var detail: String {
        switch self {
        case .agentSelect: return "Codex、Claude Code 等"
        case .agentNew: return "等同 ⇧⌘↩"
        case .agent: return "本 worktree 的 Agent 会话"
        case .interrupt: return "中断目标正在跑的一轮"
        case .close: return "结束目标会话（需确认）"
        case .native: return "用会话文件打开原生 TUI"
        case .resume: return "显式选择会话文件"
        case .shellNew: return "再开一个 Shell"
        case .shell: return "按名称切换 Shell"
        case .focus: return "vim、交互程序"
        case .rename: return "Agent 详情或当前 Shell"
        case .history: return "跨会话搜索"
        }
    }
}

public struct SlashContext: Equatable, Sendable {
    public var hasAgents: Bool
    public var shellCount: Int
    public var targetWorking: Bool
    public var streamWorking: Bool
    public var targetResumable: Bool
    public var streamResumable: Bool
    public var providerSupportsResume: Bool

    public init(hasAgents: Bool, shellCount: Int, targetWorking: Bool, streamWorking: Bool = false,
                targetResumable: Bool, streamResumable: Bool = false, providerSupportsResume: Bool) {
        self.hasAgents = hasAgents
        self.shellCount = shellCount
        self.targetWorking = targetWorking
        self.streamWorking = streamWorking
        self.targetResumable = targetResumable
        self.streamResumable = streamResumable
        self.providerSupportsResume = providerSupportsResume
    }
}

public enum SlashParse: Equatable, Sendable {
    case none
    case composing(filter: String)
    case command(SlashCommand, argument: String)
}

public enum SlashCommandRegistry {
    public static let catalogue: [SlashCommand] = SlashCommand.allCases

    public static func available(in context: SlashContext) -> [SlashCommand] {
        catalogue.filter { isAvailable($0, context: context) }
    }

    public static func isAvailable(_ command: SlashCommand, context: SlashContext) -> Bool {
        switch command {
        case .agentSelect, .agentNew, .shellNew, .rename, .history:
            return true
        case .agent:
            return context.hasAgents
        case .interrupt:
            return context.targetWorking || context.streamWorking
        case .close:
            return context.hasAgents
        case .resume:
            return context.providerSupportsResume
        case .native:
            if context.streamResumable { return true }
            return context.targetResumable && context.providerSupportsResume
        case .shell:
            return context.shellCount > 1
        case .focus:
            return context.shellCount > 0
        }
    }
}

public enum SlashParser {
    /// Token and argument after trimming outer whitespace (composer field).
    public static func parts(_ text: String) -> (token: String, argument: String) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let pieces = trimmed.split(separator: " ", maxSplits: 1, omittingEmptySubsequences: true)
        let token = pieces.first.map(String.init) ?? ""
        let argument = pieces.count > 1 ? String(pieces[1]).trimmingCharacters(in: .whitespacesAndNewlines) : ""
        return (token, argument)
    }

    /// Second `/` inside the first token means a path, not a slash command.
    public static func isPathToken(_ token: String) -> Bool {
        guard token.hasPrefix("/"), token.count > 1 else { return false }
        return token.dropFirst().contains("/")
    }

    public static func parse(_ text: String, context: SlashContext) -> SlashParse {
        let (token, argument) = parts(text)
        guard token.hasPrefix("/") else { return .none }
        if isPathToken(token) { return .none }
        if token == "/" {
            return .composing(filter: "")
        }
        if let exact = SlashCommand.allCases.first(where: { $0.rawValue == token }) {
            guard SlashCommandRegistry.isAvailable(exact, context: context) else { return .none }
            return .command(exact, argument: argument)
        }
        let available = SlashCommandRegistry.available(in: context)
        let matches = available.filter { $0.rawValue.hasPrefix(token) }
        if matches.isEmpty { return .none }
        if token.count == 1 { return .composing(filter: "") }
        return .composing(filter: String(token.dropFirst()))
    }

    public static func matches(_ text: String, context: SlashContext) -> [SlashCommand] {
        let (token, _) = parts(text)
        guard token.hasPrefix("/"), !isPathToken(token) else { return [] }
        let available = SlashCommandRegistry.available(in: context)
        if token == "/" { return available }
        let query = token.dropFirst().lowercased()
        return available.filter {
            let name = String($0.rawValue.dropFirst()).lowercased()
            return name.hasPrefix(query)
                || $0.title.lowercased().contains(query)
                || $0.detail.lowercased().contains(query)
        }
    }
}
