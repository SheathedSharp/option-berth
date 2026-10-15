import Foundation
import SwiftTerm

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
        _ = snapshot
        _ = now
        _ = isTaskMode
        return .unknown
    }

    public static func screenPreviewLines(from terminal: Terminal, maxNonEmpty: Int = 8) -> [String] {
        var collected: [String] = []
        var row = terminal.buffer.y
        while row >= 0, collected.count < maxNonEmpty {
            let text = terminal.getLine(row: row)?.translateToString(trimRight: true) ?? ""
            if !text.isEmpty { collected.append(text) }
            row -= 1
        }
        return collected.reversed()
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
