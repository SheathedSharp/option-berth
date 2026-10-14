import Foundation

/// Observable PTY facts for inferred agent UI. Not a second runtime ledger.
public struct SessionActivitySnapshot: Equatable {
    public var lastOutputAt: Date?
    public var lastBellAt: Date?
    public var lastNotifyAt: Date?
    public var alternateScreen = false
    public var outputTail = ""
    public init() {}
}

public enum AgentInferredState: String, Equatable {
    case exited
    case working
    case waitingInput
    case unknown

    public var label: String {
        switch self {
        case .exited: return "已退出"
        case .working: return "工作中（推断）"
        case .waitingInput: return "等待输入（推断）"
        case .unknown: return "未知"
        }
    }
}

public enum AgentActivityInference {
    private static let recentOutput: TimeInterval = 2.5
    private static let bellWindow: TimeInterval = 8

    public static func infer(isActive: Bool, isTaskMode: Bool, snapshot: SessionActivitySnapshot, now: Date = Date()) -> AgentInferredState {
        guard isActive else { return .exited }
        if isTaskMode { return .unknown }
        if snapshot.alternateScreen { return .working }
        if let notify = snapshot.lastNotifyAt, now.timeIntervalSince(notify) <= bellWindow { return .waitingInput }
        if let bell = snapshot.lastBellAt, now.timeIntervalSince(bell) <= bellWindow { return .waitingInput }
        if let output = snapshot.lastOutputAt, now.timeIntervalSince(output) <= recentOutput { return .working }
        if snapshot.lastOutputAt != nil { return .waitingInput }
        return .unknown
    }

    public static func appendTail(_ existing: String, bytes: ArraySlice<UInt8>, limit: Int = 2048) -> String {
        let chunk = String(decoding: bytes, as: UTF8.self)
        let merged = existing + chunk
        guard merged.count > limit else { return merged }
        return String(merged.suffix(limit))
    }

    public static func previewLines(_ tail: String, count: Int = 8) -> [String] {
        tail.split(separator: "\n", omittingEmptySubsequences: false).suffix(count).map(String.init)
    }
}
