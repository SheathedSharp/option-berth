import BerthAgent
import BerthTerminal
import Foundation

@MainActor
enum ComposerConsoleBridge {
    static func chord(_ value: SessionComposerModel.Chord) -> ComposerChord {
        switch value {
        case .plain: return .enter
        case .command: return .command
        case .commandShift: return .commandShift
        }
    }

    static func slashContext(agentCount: Int, shellCount: Int, target: TerminalSession?,
                           streamSessions: [AgentStreamSession] = [],
                           streamResumeID: UUID? = nil, provider: ExternalAgent? = nil) -> SlashContext {
        let working = target?.kind.hasPrefix("agent:") == true && target?.isActive == true && target?.isTaskAgent == true
            && target?.commandBlocks.last?.isRunning == true
        let resumable = target?.kind.hasPrefix("agent:") == true && target?.isActive == true
        let streamResumable = streamSessions.contains { stream in
            guard stream.snapshot.reference?.sessionFile != nil else { return false }
            guard case .ready = stream.snapshot.phase else { return false }
            if stream.snapshot.turns.last?.outcome == .running { return false }
            if stream.id == streamResumeID { return true }
            return !stream.snapshot.requests.isEmpty
                || stream.snapshot.turns.contains { $0.outcome != .running }
        }
        let streamWorking: Bool = {
            guard let streamResumeID,
                  let stream = streamSessions.first(where: { $0.id == streamResumeID }),
                  case .ready = stream.snapshot.phase else { return false }
            return stream.snapshot.turns.last?.outcome == .running
        }()
        let resumeCapable = provider?.supportsResume == true || provider?.supportsStream == true
        return SlashContext(hasAgents: agentCount > 0, shellCount: shellCount, targetWorking: working,
                            streamWorking: streamWorking, targetResumable: resumable,
                            streamResumable: streamResumable, providerSupportsResume: resumeCapable)
    }

    static func context(draft: String, shell: TerminalSession?, target: TerminalSession?,
                        streamSessions: [AgentStreamSession], streamResumeID: UUID?,
                        providerChosen: Bool, providerID: String, slashContext: SlashContext) -> ComposerContext {
        let slash = SlashParser.parse(draft, context: slashContext)
        let shellTarget: ShellTarget = {
            guard let shell else { return .none }
            if shell.shellForegroundBusy {
                let command = shell.commandBlocks.last(where: { $0.isRunning })?.command
                return .busy(id: shell.id, title: shell.title, command: command)
            }
            if shell.shellIntegrationEnabled {
                return .atPrompt(id: shell.id, title: shell.title)
            }
            return .untracked(id: shell.id, title: shell.title)
        }()
        let provider: ProviderSelection = providerChosen ? .chosen(providerID) : .unset
        let agentTarget: AgentTarget = {
            // docs/console.md「输入条与路由」: ⌘↩ sends to the current **Agent
            // target**, and for a terminal agent that means pasting into its
            // TUI (no Enter). So an explicitly targeted structured stream wins
            // first, then an explicitly selected native agent PTY, and only
            // then does the block-stream default apply.
            //
            // v15 briefly dropped the native branch entirely, which made the
            // documented ⌘↩-into-terminal-agent behaviour unreachable.
            if let streamResumeID,
               let match = streamSessions.first(where: { $0.id == streamResumeID }) {
                let state = AgentDisplayDeriver.targetState(phase: match.snapshot.phase,
                                                            turns: match.snapshot.turns,
                                                            requests: match.snapshot.requests)
                return .stream(id: match.id, title: match.title, state: state)
            }
            if let target, target.kind.hasPrefix("agent:") {
                return .terminal(id: target.id, title: target.title, alive: target.isActive)
            }
            if let stream = streamSessions.last {
                let state = AgentDisplayDeriver.targetState(phase: stream.snapshot.phase,
                                                            turns: stream.snapshot.turns,
                                                            requests: stream.snapshot.requests)
                return .stream(id: stream.id, title: stream.title, state: state)
            }
            return .none
        }()
        return ComposerContext(text: draft, slash: slash, shell: shellTarget, provider: provider, target: agentTarget)
    }

    static func agentStatusLabel(_ session: TerminalSession) -> String {
        if session.isTaskAgent {
            if let block = session.commandBlocks.last, block.isRunning { return "运行中" }
            if !session.isActive {
                if let code = session.commandBlocks.last?.exitCode { return "已退出 exit \(code)" }
                return "已退出"
            }
            return "运行中"
        }
        guard session.isActive else { return "已退出" }
        if let at = session.activity.lastOutputAt {
            let seconds = max(0, Int(Date().timeIntervalSince(at)))
            return "终端 · \(seconds) 秒前有输出"
        }
        return "终端"
    }
}
