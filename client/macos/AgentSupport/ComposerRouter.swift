import Foundation

public enum ComposerRouter {
    public static func route(_ chord: ComposerChord, _ context: ComposerContext) -> ComposerAction {
        let body = context.text.trimmingCharacters(in: .whitespacesAndNewlines)
        if case .command(let command, let argument) = context.slash {
            if let slashAction = slashRoute(chord: chord, command: command, argument: argument, context: context, body: body) {
                return slashAction
            }
            return .runSlash(command, argument)
        }
        if case .composing = context.slash, chord == .enter {
            return .none
        }
        switch chord {
        case .enter:
            return enterRoute(body: body, context: context)
        case .command:
            return commandRoute(body: body, context: context)
        case .commandShift:
            return commandShiftRoute(body: body, context: context)
        }
    }

    public static func hints(_ context: ComposerContext) -> RouteHints {
        let enterAction = route(.enter, context)
        let commandAction = route(.command, context)
        let shiftAction = route(.commandShift, context)
        return RouteHints(
            enter: describe(enterAction, chord: .enter, context: context),
            command: describe(commandAction, chord: .command, context: context),
            commandShift: describe(shiftAction, chord: .commandShift, context: context)
        )
    }

    private static func slashRoute(chord: ComposerChord, command: SlashCommand, argument: String,
                                   context: ComposerContext, body: String) -> ComposerAction? {
        switch command {
        case .agentSelect:
            if chord == .commandShift {
                return providerChosen(context) ? .agentNew(defaultProviderID(context), firstTurn: body.isEmpty ? nil : body) : .pickProvider(then: .commandShift)
            }
            if argument.isEmpty {
                return .pickProvider(then: chord == .commandShift ? .commandShift : .command)
            }
            return .runSlash(command, argument)
        case .agentNew:
            guard providerChosen(context) else { return .pickProvider(then: .commandShift) }
            return .agentNew(defaultProviderID(context), firstTurn: argument.isEmpty ? nil : argument)
        default:
            return .runSlash(command, argument)
        }
    }

    private static func enterRoute(body: String, context: ComposerContext) -> ComposerAction {
        guard !body.isEmpty else { return .none }
        switch context.shell {
        case .none:
            return .openShellThenSend(body)
        case .busy(_, let title, let command):
            let detail = command.map { "\(title) 正在运行 \($0)" } ?? "\(title) 正在运行命令"
            return .shellBusy(detail)
        case .atPrompt(let id, _), .untracked(let id, _):
            return .shell(id, body)
        }
    }

    private static func commandRoute(body: String, context: ComposerContext) -> ComposerAction {
        if body.isEmpty {
            return emptyCommandRoute(context: context)
        }
        return agentTextRoute(body: body, context: context, newSession: false)
    }

    private static func commandShiftRoute(body: String, context: ComposerContext) -> ComposerAction {
        guard providerChosen(context) else { return .pickProvider(then: .commandShift) }
        let provider = defaultProviderID(context)
        return .agentNew(provider, firstTurn: body.isEmpty ? nil : body)
    }

    private static func emptyCommandRoute(context: ComposerContext) -> ComposerAction {
        switch context.target {
        case .none:
            guard providerChosen(context) else { return .pickProvider(then: .command) }
            return .agentNew(defaultProviderID(context), firstTurn: nil)
        case .stream(let id, _, _), .terminal(let id, _, _):
            return .openTarget(id)
        }
    }

    private static func agentTextRoute(body: String, context: ComposerContext, newSession: Bool) -> ComposerAction {
        if newSession {
            guard providerChosen(context) else { return .pickProvider(then: .commandShift) }
            return .agentNew(defaultProviderID(context), firstTurn: body)
        }
        switch context.target {
        case .none:
            guard providerChosen(context) else { return .pickProvider(then: .command) }
            return .agentNew(defaultProviderID(context), firstTurn: body)
        case .stream(let id, _, let state):
            switch state {
            case .starting, .working, .needsYou:
                return .agentQueue(id, body)
            case .idle:
                return .agentSend(id, body)
            case .ended(let resumable):
                if resumable { return .agentResumeThenSend(id, body) }
                return .refuse("会话已结束，请用 ⇧⌘↩ 新建")
            }
        case .terminal:
            // Native Agent PTYs are not ⌘↩ paste targets (rejects Shell|Pi jump /
            // 「放进终端」). Open a structured stream session instead.
            guard providerChosen(context) else { return .pickProvider(then: .command) }
            return .agentNew(defaultProviderID(context), firstTurn: body)
        }
    }

    private static func providerChosen(_ context: ComposerContext) -> Bool {
        if case .chosen = context.provider { return true }
        return false
    }

    private static func defaultProviderID(_ context: ComposerContext) -> String {
        if case .chosen(let id) = context.provider { return id }
        return "codex"
    }

    private static func describe(_ action: ComposerAction, chord: ComposerChord, context: ComposerContext) -> RouteHintSegment {
        switch (chord, action) {
        case (.enter, .shell(_, _)):
            let title = shellTitle(context)
            return RouteHintSegment(label: "↩ \(title)", available: true)
        case (.enter, .shellBusy(let reason)):
            return RouteHintSegment(label: "↩ 不可用：\(reason)", available: false)
        case (.enter, .openShellThenSend):
            return RouteHintSegment(label: "↩ 先打开 Shell", available: true)
        case (.enter, .none):
            return RouteHintSegment(label: "↩", available: false)
        case (.command, .pickProvider):
            return RouteHintSegment(label: "⌘↩ 先选择 Agent", available: false)
        case (.command, .agentSend), (.command, .agentQueue), (.command, .agentResumeThenSend):
            return targetCommandHint(context, prefix: "⌘↩")
        case (.command, .agentNew):
            return RouteHintSegment(label: "⌘↩ 新建 \(providerLabel(context)) 会话", available: true)
        case (.command, .agentPaste(let id, _)):
            return RouteHintSegment(label: "⌘↩ 放进 \(targetTitle(context, id: id)) 的终端（不回车）", available: true)
        case (.command, .openTarget(let id)):
            return RouteHintSegment(label: "⌘↩ 打开 \(targetTitle(context, id: id))", available: true)
        case (.command, .refuse(let message)):
            return RouteHintSegment(label: "⌘↩ \(message)", available: false)
        case (.commandShift, .pickProvider):
            return RouteHintSegment(label: "⇧⌘↩ 先选择 Agent", available: false)
        case (.commandShift, .agentNew):
            return RouteHintSegment(label: "⇧⌘↩ 新建 \(providerLabel(context)) 会话", available: true)
        default:
            return RouteHintSegment(label: chordLabel(chord), available: action != .none)
        }
    }

    private static func targetCommandHint(_ context: ComposerContext, prefix: String) -> RouteHintSegment {
        switch context.target {
        case .none:
            return RouteHintSegment(label: "\(prefix) 新建 \(providerLabel(context)) 会话", available: providerChosen(context))
        case .stream(_, let title, let state):
            switch state {
            case .needsYou:
                return RouteHintSegment(label: "\(prefix) 排队（\(title) 在等你确认）", available: true)
            case .working, .starting:
                return RouteHintSegment(label: "\(prefix) 排在 \(title) 当前这一轮之后", available: true)
            case .idle:
                return RouteHintSegment(label: "\(prefix) 续接 \(title)", available: true)
            case .ended:
                return RouteHintSegment(label: "\(prefix) 续接 \(title)", available: true)
            }
        case .terminal:
            return RouteHintSegment(label: "\(prefix) 新建 \(providerLabel(context)) 会话", available: providerChosen(context))
        }
    }

    private static func shellTitle(_ context: ComposerContext) -> String {
        switch context.shell {
        case .none: return "Shell"
        case .atPrompt(_, let title), .busy(_, let title, _), .untracked(_, let title): return title
        }
    }

    private static func targetTitle(_ context: ComposerContext, id: UUID) -> String {
        switch context.target {
        case .stream(let targetID, let title, _) where targetID == id: return title
        case .terminal(let targetID, let title, _) where targetID == id: return title
        default: return "Agent"
        }
    }

    private static func providerLabel(_ context: ComposerContext) -> String {
        if case .chosen(let id) = context.provider {
            return ComposerProviders.name(id)
        }
        return "Agent"
    }

    private static func chordLabel(_ chord: ComposerChord) -> String {
        switch chord {
        case .enter: return "↩"
        case .command: return "⌘↩"
        case .commandShift: return "⇧⌘↩"
        }
    }
}

public enum ComposerProviders {
    public static let catalogue: [(id: String, name: String)] = [
        ("codex", "Codex"),
        ("claude", "Claude Code"),
        ("opencode", "OpenCode"),
        ("deepseek", "DeepSeek"),
        ("pi", "Pi"),
    ]

    public static func name(_ id: String) -> String {
        catalogue.first { $0.id == id }?.name ?? id
    }
}
