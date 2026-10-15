import Foundation

public enum ProviderSelection: Equatable, Sendable {
    case unset
    case chosen(String)
}

public enum ShellTarget: Equatable, Sendable {
    case none
    case atPrompt(id: UUID, title: String)
    case busy(id: UUID, title: String, command: String?)
    case untracked(id: UUID, title: String)
}

public enum StreamTargetState: Equatable, Sendable {
    case starting
    case idle
    case working
    case needsYou
    case ended(resumable: Bool)
}

public enum AgentTarget: Equatable, Sendable {
    case none
    case stream(id: UUID, title: String, state: StreamTargetState)
    case terminal(id: UUID, title: String, alive: Bool)
}

public enum ComposerChord: Equatable, Sendable {
    case enter
    case command
    case commandShift
}

public struct ComposerContext: Equatable, Sendable {
    public var text: String
    public var slash: SlashParse
    public var shell: ShellTarget
    public var provider: ProviderSelection
    public var target: AgentTarget

    public init(text: String, slash: SlashParse, shell: ShellTarget, provider: ProviderSelection, target: AgentTarget) {
        self.text = text
        self.slash = slash
        self.shell = shell
        self.provider = provider
        self.target = target
    }
}

public enum ComposerAction: Equatable, Sendable {
    case none
    case runSlash(SlashCommand, String)
    case shell(UUID, String)
    case shellBusy(String)
    case openShellThenSend(String)
    case agentSend(UUID, String)
    case agentQueue(UUID, String)
    case agentResumeThenSend(UUID, String)
    case agentPaste(UUID, String)
    case agentNew(String, firstTurn: String?)
    case pickProvider(then: ComposerChord)
    case openTarget(UUID)
    case refuse(String)
}

public struct RouteHintSegment: Equatable, Sendable {
    public let label: String
    public let available: Bool
    public init(label: String, available: Bool) {
        self.label = label
        self.available = available
    }
}

public struct RouteHints: Equatable, Sendable {
    public let enter: RouteHintSegment
    public let command: RouteHintSegment
    public let commandShift: RouteHintSegment

    public init(enter: RouteHintSegment, command: RouteHintSegment, commandShift: RouteHintSegment) {
        self.enter = enter
        self.command = command
        self.commandShift = commandShift
    }
}
