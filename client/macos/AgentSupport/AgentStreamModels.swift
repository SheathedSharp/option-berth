import Foundation

public enum StreamPhase: Equatable, Sendable {
    case starting
    case ready
    case ending
    case ended(EndReason)
}

public enum EndReason: Equatable, Sendable {
    case closed
    case providerExit(code: Int)
    case lost
}

public enum TurnOutcome: Equatable, Sendable {
    case running
    case completed
    case failed(code: String, message: String)
    case interrupted
    case lost
}

public enum ItemStatus: Equatable, Sendable {
    case running
    case completed
    case failed
    case declined
}

public struct FileStat: Equatable, Sendable {
    public let path: String
    public let added: Int
    public let removed: Int
    public init(path: String, added: Int, removed: Int) {
        self.path = path
        self.added = added
        self.removed = removed
    }
}

public struct PlanStep: Equatable, Sendable {
    public let title: String
    public let done: Bool
    public init(title: String, done: Bool) {
        self.title = title
        self.done = done
    }
}

public enum NoticeLevel: Equatable, Sendable {
    case info, warn, error
}

public enum AgentItem: Equatable, Sendable, Identifiable {
    case message(id: String, text: String)
    case reasoning(id: String, summary: String)
    case command(id: String, command: String, cwd: String?, status: ItemStatus,
                 exitCode: Int?, durationMs: Int?, outputTail: String)
    case fileChange(id: String, files: [FileStat], status: ItemStatus)
    case tool(id: String, name: String, inputPreview: String, status: ItemStatus)
    case plan(id: String, steps: [PlanStep])
    case notice(id: String, level: NoticeLevel, code: String, text: String)

    public var id: String {
        switch self {
        case .message(let id, _), .reasoning(let id, _), .command(let id, _, _, _, _, _, _),
             .fileChange(let id, _, _), .tool(let id, _, _, _), .plan(let id, _),
             .notice(let id, _, _, _):
            return id
        }
    }
}

public struct AgentTurn: Equatable, Sendable, Identifiable {
    public let id: String
    public let input: String
    public let sentAt: Date
    public var outcome: TurnOutcome
    public var items: [AgentItem]
    public var diff: TurnDiff?
    public var endedAt: Date?

    public init(id: String, input: String, sentAt: Date, outcome: TurnOutcome,
                items: [AgentItem], diff: TurnDiff? = nil, endedAt: Date? = nil) {
        self.id = id
        self.input = input
        self.sentAt = sentAt
        self.outcome = outcome
        self.items = items
        self.diff = diff
        self.endedAt = endedAt
    }
}

public struct TurnDiff: Equatable, Sendable {
    public let files: [FileStat]
    public let patch: String
    public let truncated: Bool
    public init(files: [FileStat], patch: String, truncated: Bool) {
        self.files = files
        self.patch = patch
        self.truncated = truncated
    }
}

public enum AgentRequestKind: Equatable, Sendable {
    case command, fileChange, permission, tool
}

public enum AgentDecision: String, Equatable, Sendable {
    case allow
    case allowSession = "allow_session"
    case deny
}

public struct AgentRequest: Equatable, Sendable, Identifiable {
    public let id: String
    public let turn: String
    public let kind: AgentRequestKind
    public let command: String?
    public let cwd: String?
    public let paths: [String]
    public let reason: String?
    public let preview: String?
    public let decisions: [AgentDecision]
    public var answering: AgentDecision?

    public init(id: String, turn: String, kind: AgentRequestKind, command: String?, cwd: String?,
                paths: [String], reason: String?, preview: String?, decisions: [AgentDecision],
                answering: AgentDecision? = nil) {
        self.id = id
        self.turn = turn
        self.kind = kind
        self.command = command
        self.cwd = cwd
        self.paths = paths
        self.reason = reason
        self.preview = preview
        self.decisions = decisions
        self.answering = answering
    }
}

public struct ProviderReference: Equatable, Sendable {
    public let sessionID: String
    public let sessionFile: String?
    public init(sessionID: String, sessionFile: String?) {
        self.sessionID = sessionID
        self.sessionFile = sessionFile
    }
}

public struct AgentStreamSnapshot: Equatable, Sendable {
    public var phase: StreamPhase
    public var turns: [AgentTurn]
    public var requests: [AgentRequest]
    public var reference: ProviderReference?
    public var turnCapDropped: Bool
    public var itemCapDropped: Bool
    /// Fatal bridge error from NDJSON `error` events (e.g. `unsupported`).
    public var bridgeErrorCode: String?
    public var bridgeErrorMessage: String?

    public init(phase: StreamPhase = .starting, turns: [AgentTurn] = [], requests: [AgentRequest] = [],
                reference: ProviderReference? = nil, turnCapDropped: Bool = false, itemCapDropped: Bool = false,
                bridgeErrorCode: String? = nil, bridgeErrorMessage: String? = nil) {
        self.phase = phase
        self.turns = turns
        self.requests = requests
        self.reference = reference
        self.turnCapDropped = turnCapDropped
        self.itemCapDropped = itemCapDropped
        self.bridgeErrorCode = bridgeErrorCode
        self.bridgeErrorMessage = bridgeErrorMessage
    }
}

public enum AgentTransport: Equatable, Sendable {
    case stream
    case terminal
}

public enum AgentDisplayStatus: Equatable, Sendable {
    case starting
    case idle
    case needsYou
    case working
    case queued
    case completed
    case failed
    case interrupted
    case lost
    case ended(resumable: Bool)
}

public struct AgentRow: Equatable, Sendable, Identifiable {
    /// Unique card identity (per turn / queued item in the block stream).
    public let id: UUID
    /// Session to open on enter / target for ⌘↩ (may differ from `id` when one session has many turns).
    public let sessionID: UUID
    public let provider: String
    public let transport: AgentTransport
    public let title: String
    public let status: AgentDisplayStatus
    public let activity: String?
    public let since: Date?
    public let unread: Bool
    public let isTarget: Bool
    public let queuedCount: Int
    /// User turn text shown in the stream card header (sibling of `$ cmd`).
    public let userPrompt: String?
    /// Inline result / activity body for the stream card (sibling of shell output).
    public let resultText: String?
    /// Analogous exit code when the turn/provider exposes one; otherwise nil (shell specialty).
    public let exitCode: Int?

    public init(id: UUID, sessionID: UUID? = nil, provider: String, transport: AgentTransport, title: String,
                status: AgentDisplayStatus, activity: String?, since: Date?, unread: Bool,
                isTarget: Bool, queuedCount: Int, userPrompt: String? = nil, resultText: String? = nil,
                exitCode: Int? = nil) {
        self.id = id
        self.sessionID = sessionID ?? id
        self.provider = provider
        self.transport = transport
        self.title = title
        self.status = status
        self.activity = activity
        self.since = since
        self.unread = unread
        self.isTarget = isTarget
        self.queuedCount = queuedCount
        self.userPrompt = userPrompt
        self.resultText = resultText
        self.exitCode = exitCode
    }
}
