import Foundation

/// Detects when a provider session file is already bound to a running structured stream in this app.
public enum AgentSessionFileGuard {
    public struct Binding: Equatable {
        public let sessionID: UUID
        public let sessionFile: String?
        public let active: Bool

        public init(sessionID: UUID, sessionFile: String?, active: Bool) {
            self.sessionID = sessionID
            self.sessionFile = sessionFile
            self.active = active
        }
    }

    public static func canonicalSessionFilePath(_ path: String) -> String {
        URL(fileURLWithPath: path).standardized.path
    }

    public static func runningHolder(
        among bindings: [Binding],
        for candidatePath: String,
        excluding: UUID? = nil
    ) -> UUID? {
        let target = canonicalSessionFilePath(candidatePath)
        for binding in bindings {
            if let excluding, binding.sessionID == excluding { continue }
            guard binding.active, let file = binding.sessionFile else { continue }
            if canonicalSessionFilePath(file) == target { return binding.sessionID }
        }
        return nil
    }
}
