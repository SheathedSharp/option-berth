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
        // Native Agent PTY (`target`) is intentionally ignored — not a ⌘↩ destination.
        _ = target
        let agentTarget: AgentTarget = {
            // Unified block canvas: ⌘↩ only targets structured streams (agent-session cards).
            // Native Agent PTYs are never composer paste/jump targets — that path showed
            // 「已放进 Pi 的终端」and switched Shell|Pi tabs.
            let stream: AgentStreamSession? = {
                if let streamResumeID,
                   let match = streamSessions.first(where: { $0.id == streamResumeID }) {
                    return match
                }
                return streamSessions.last
            }()
            if let stream {
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
