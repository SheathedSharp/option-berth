import SwiftUI
import BerthTerminal

/// User reduction augments, never disables, the operating system preference.
struct ClientMotionPreferences: ViewModifier {
    @ObservedObject var settings: UISettings
    @Environment(\.accessibilityReduceMotion) private var systemReduced
    func body(content: Content) -> some View {
        let reduced = ClientMotionPolicy.reduced(system: systemReduced, preference: settings.configuration.preferences.reduceMotion)
        content.environment(\.clientReduceMotion, reduced)
            .transaction { transaction in
                if reduced { transaction.animation = nil; transaction.disablesAnimations = true }
            }
    }
}

/// Configuration changes affect future workspace creation only. Existing drafts,
/// provider selections, layouts, sessions and running PTYs are never rewritten.
@MainActor enum ClientSessionPreferences {
    static func apply(_ preferences: ClientPreferencesConfiguration, to sessions: TerminalSessions) {
        sessions.defaultProviderID = preferences.defaultAgent ?? "codex"
    }
}
