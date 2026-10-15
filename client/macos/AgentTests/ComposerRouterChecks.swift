import BerthAgent
import Foundation

@MainActor
extension AgentChecks {
    static func composerRouterChecks() throws {
        let shellID = UUID()
        let agentID = UUID()
        func ctx(text: String, slash: SlashParse = .none, shell: ShellTarget = .atPrompt(id: shellID, title: "Shell 1"),
                 provider: ProviderSelection = .chosen("codex"),
                 target: AgentTarget = .stream(id: agentID, title: "Codex · demo", state: .idle)) -> ComposerContext {
            ComposerContext(text: text, slash: slash, shell: shell, provider: provider, target: target)
        }

        let send = ComposerRouter.route(.command, ctx(text: "继续"))
        try check(send == .agentSend(agentID, "继续"), "idle stream should send")

        let queue = ComposerRouter.route(.command, ctx(text: "排队", target: .stream(id: agentID, title: "Codex", state: .working)))
        try check(queue == .agentQueue(agentID, "排队"), "working stream should queue")

        let needs = ComposerRouter.route(.command, ctx(text: "别回答", target: .stream(id: agentID, title: "Claude", state: .needsYou)))
        try check(needs == .agentQueue(agentID, "别回答"), "needsYou should queue not answer")

        let busy = ComposerRouter.route(.enter, ctx(text: "ls", shell: .busy(id: shellID, title: "Shell 1", command: "npm test")))
        try check(busy == .shellBusy("Shell 1 正在运行 npm test"), "busy shell should refuse enter")

        let path = ComposerRouter.route(.enter, ctx(text: "/usr/bin/true"))
        try check(path == .shell(shellID, "/usr/bin/true"), "path should go to shell")

        let pick = ComposerRouter.route(.command, ctx(text: "hi", provider: .unset, target: .none))
        try check(pick == .pickProvider(then: .command), "unset provider should open picker")

        let derived = AgentDisplayDeriver.targetState(phase: .ready, turns: [
            AgentTurn(id: "t1", input: "hi", sentAt: Date(), outcome: .running, items: [])
        ], requests: [AgentRequest(id: "r1", turn: "t1", kind: .command, command: "rm", cwd: nil,
                                   paths: [], reason: nil, preview: nil, decisions: [.allow, .deny])])
        try check(derived == .needsYou, "running turn with open request should need you")

        let hints = ComposerRouter.hints(ctx(text: "x", shell: .busy(id: shellID, title: "Shell 1", command: "npm test")))
        try check(!hints.enter.available && hints.enter.label.contains("npm test"), "hint should match busy shell")
        try check(hints.command.available, "command chord should stay available while shell busy")

        for chord in [ComposerChord.enter, .command, .commandShift] {
            let context = ctx(text: "task", target: .stream(id: agentID, title: "Codex · t", state: .working))
            let action = ComposerRouter.route(chord, context)
            let hint = ComposerRouter.hints(context)
            let segment = chord == .enter ? hint.enter : (chord == .command ? hint.command : hint.commandShift)
            try check(hintMatches(action, segment), "hint mismatch for \(chord): \(segment.label)")
        }
    }

    private static func hintMatches(_ action: ComposerAction, _ segment: RouteHintSegment) -> Bool {
        switch action {
        case .none: return !segment.available
        case .shellBusy: return !segment.available
        case .refuse: return !segment.available
        case .pickProvider: return !segment.available
        default: return segment.available
        }
    }
}
