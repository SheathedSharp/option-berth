import Foundation

public enum SlashCommand: String, CaseIterable, Equatable, Sendable {
    /// Switch composer input mode to Shell (draft kept).
    case modeShell = "/mode-shell"
    /// Switch composer input mode to Agent (draft kept).
    case modeAgent = "/mode-agent"
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
    /// Services capability panel (session-primary v13). Alias token `/services` resolves here in SlashParser.
    case obServer = "/ob-server"
    /// Read-only Git review panel for this worktree.
    case git = "/git"
    /// Create a durable workplace under the current worktree.
    case workplaceNew = "/workplace-new"
    /// List workplaces for the current worktree.
    case workplaceList = "/workplace-list"
    /// Switch / recall a workplace by name or id (restores block stream).
    case workplace = "/workplace"
    /// Delete a workplace by name or id.
    case workplaceDelete = "/workplace-delete"

    public var title: String {
        switch self {
        case .modeShell: return "切换到 Shell 输入"
        case .modeAgent: return "切换到 Agent 输入"
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
        case .obServer: return "打开服务面板"
        case .git: return "打开 Git 面板"
        case .workplaceNew: return "新建 Workplace"
        case .workplaceList: return "列出 Workplace"
        case .workplace: return "切换 Workplace"
        case .workplaceDelete: return "删除 Workplace"
        }
    }

    public var detail: String {
        switch self {
        case .modeShell: return "等同 ⌘I → Shell，保留草稿"
        case .modeAgent: return "等同 ⌘I → Agent，保留草稿"
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
        case .obServer: return "需处理 · 在跑 · 日志（/services）"
        case .git: return "只读审查本 worktree"
        case .workplaceNew: return "持久化块流工作区（可选名称）"
        case .workplaceList: return "当前 worktree 下的全部 Workplace"
        case .workplace: return "按名称或 id 回溯块流"
        case .workplaceDelete: return "删除指定 Workplace"
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

    /// Shown when the user types bare `/` — Cursor/Warp-style short palette (5–6).
    /// Keep typing to filter the full catalogue.
    public static let pinned: [SlashCommand] = [
        .modeShell, .modeAgent, .agentSelect, .obServer, .git, .history,
    ]

    /// Token aliases that are not distinct enum cases (documented for users).
    public static let aliases: [String: SlashCommand] = [
        "/services": .obServer,
        "/shell-mode": .modeShell,
        "/agent-mode": .modeAgent,
        "/to-shell": .modeShell,
        "/to-agent": .modeAgent,
        "/wp-new": .workplaceNew,
        "/wp-list": .workplaceList,
        "/wp": .workplace,
        "/wp-delete": .workplaceDelete,
    ]

    public static func available(in context: SlashContext) -> [SlashCommand] {
        catalogue.filter { isAvailable($0, context: context) }
    }

    public static func pinnedAvailable(in context: SlashContext) -> [SlashCommand] {
        pinned.filter { isAvailable($0, context: context) }
    }

    public static func isAvailable(_ command: SlashCommand, context: SlashContext) -> Bool {
        switch command {
        case .modeShell, .modeAgent, .agentSelect, .agentNew, .shellNew, .rename, .history, .obServer, .git,
             .workplaceNew, .workplaceList, .workplace, .workplaceDelete:
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

    public static func resolveToken(_ token: String) -> SlashCommand? {
        if let exact = SlashCommand.allCases.first(where: { $0.rawValue == token }) { return exact }
        return aliases[token]
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
        if let exact = SlashCommandRegistry.resolveToken(token) {
            guard SlashCommandRegistry.isAvailable(exact, context: context) else { return .none }
            return .command(exact, argument: argument)
        }
        let available = SlashCommandRegistry.available(in: context)
        var prefixHits = available.filter { $0.rawValue.hasPrefix(token) }
        for (alias, command) in SlashCommandRegistry.aliases where alias.hasPrefix(token) {
            if available.contains(command), !prefixHits.contains(command) { prefixHits.append(command) }
        }
        if prefixHits.isEmpty { return .none }
        if token.count == 1 { return .composing(filter: "") }
        return .composing(filter: String(token.dropFirst()))
    }

    public static func matches(_ text: String, context: SlashContext) -> [SlashCommand] {
        let (token, _) = parts(text)
        guard token.hasPrefix("/"), !isPathToken(token) else { return [] }
        let available = SlashCommandRegistry.available(in: context)
        // Bare `/` → short pinned palette (not the full dump).
        if token == "/" {
            return SlashCommandRegistry.pinnedAvailable(in: context)
        }
        let query = token.dropFirst().lowercased()
        var result = available.filter {
            let name = String($0.rawValue.dropFirst()).lowercased()
            return name.hasPrefix(query)
                || $0.title.lowercased().contains(query)
                || $0.detail.lowercased().contains(query)
        }
        // Surface `/services` alias when the user types that prefix.
        if "services".hasPrefix(query) || query.hasPrefix("serv"),
           available.contains(.obServer),
           !result.contains(.obServer) {
            result.append(.obServer)
        }
        if "shell-mode".hasPrefix(query) || "to-shell".hasPrefix(query) || query == "mode",
           available.contains(.modeShell), !result.contains(.modeShell) {
            result.append(.modeShell)
        }
        if "agent-mode".hasPrefix(query) || "to-agent".hasPrefix(query),
           available.contains(.modeAgent), !result.contains(.modeAgent) {
            result.append(.modeAgent)
        }
        if "wp".hasPrefix(query) || "workplace".hasPrefix(query) || query.hasPrefix("wp") || query.hasPrefix("work"),
           available.contains(.workplaceList) {
            for cmd in [SlashCommand.workplaceList, .workplaceNew, .workplace, .workplaceDelete] where !result.contains(cmd) {
                result.append(cmd)
            }
        }
        // Keep the open palette scannable.
        if result.count > 8 {
            result = Array(result.prefix(8))
        }
        return result
    }
}
