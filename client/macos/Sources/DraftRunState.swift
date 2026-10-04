import Foundation

/// UI-only request state. A progress `done` frame is a candidate, not proof that
/// the CLI exited successfully. Nothing here writes a manifest or stores prompts.
struct DraftRunState {
    enum Phase: Equatable { case idle, running, ready, failed, cancelled }
    private(set) var phase: Phase = .idle
    private(set) var problem: String?
    private var request: UUID?
    private var candidate: String?
    private var reportedError: String?
    var isRunning: Bool { phase == .running }

    mutating func begin() -> UUID {
        let id = UUID()
        request = id; phase = .running
        candidate = nil; reportedError = nil; problem = nil
        return id
    }
    func accepts(_ id: UUID) -> Bool { request == id && isRunning }
    mutating func offer(_ yaml: String?, for id: UUID) {
        guard accepts(id) else { return }
        guard let yaml, !yaml.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            reject(Self.missingDraft, for: id); return
        }
        candidate = yaml
    }
    mutating func reject(_ message: String?, for id: UUID) {
        guard accepts(id) else { return }
        let message = message.flatMap { $0.isEmpty ? nil : $0 } ?? Self.missingDraft
        reportedError = message; problem = message; candidate = nil
    }
    /// Returns bytes for the editor only after a matching successful exit.
    mutating func finish(_ id: UUID, failure: String? = nil) -> String? {
        guard accepts(id) else { return nil }
        defer { candidate = nil }
        if let error = failure ?? reportedError {
            phase = .failed; problem = error; return nil
        }
        guard let candidate else {
            phase = .failed; problem = Self.missingDraft; return nil
        }
        phase = .ready; problem = nil
        return candidate
    }
    mutating func cancel() {
        request = nil; candidate = nil; reportedError = nil
        problem = nil; phase = .cancelled
    }
    private static let missingDraft = "agent 没有返回可用的 oberth.yaml 草稿。"
}
