import BerthAgent
import Foundation

extension Notification.Name {
    static let agentStreamRegistryChanged = Notification.Name("agentStreamRegistryChanged")
    static let agentStreamEndedExternally = Notification.Name("agentStreamEndedExternally")
}

/// App-wide index of running `oberth agent stream` children for quit prompts and session caps.
@MainActor
enum AgentStreamRegistry {
    struct Entry: Identifiable {
        let id: UUID
        let worktree: String
        let title: String
        let process: AgentStreamProcess
    }

    private static var entries: [UUID: Entry] = [:]

    static var all: [Entry] { Array(entries.values) }

    static var activeBridgeCount: Int {
        entries.values.filter { $0.process.isRunning }.count
    }

    static func track(id: UUID, worktree: String, title: String, process: AgentStreamProcess) {
        entries[id] = Entry(id: id, worktree: worktree, title: title, process: process)
        NotificationCenter.default.post(name: .agentStreamRegistryChanged, object: nil)
    }

    static func untrack(id: UUID) {
        entries.removeValue(forKey: id)
        NotificationCenter.default.post(name: .agentStreamRegistryChanged, object: nil)
    }

    static func end(_ id: UUID) {
        guard let entry = entries[id] else { return }
        try? entry.process.closeSession()
        _ = entry.process.waitUntilExit()
        untrack(id: id)
        NotificationCenter.default.post(name: .agentStreamEndedExternally, object: nil, userInfo: ["id": id])
    }
}
