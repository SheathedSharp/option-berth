import AppKit
import BerthAgent
import UserNotifications

/// Window-outside alerts for structured Agent sessions. Off by default; no transcript persistence.
@MainActor
final class ConsoleAgentAlerts: NSObject, UNUserNotificationCenterDelegate {
    static let shared = ConsoleAgentAlerts()

    private var enabled = false
    private var unreadIDs = Set<UUID>()
    private var operational: Bool { ProcessInfo.processInfo.environment["BERTH_CONSOLE_TEST"] != "1" }

    private override init() {
        super.init()
        guard operational else { return }
        UNUserNotificationCenter.current().delegate = self
        NotificationCenter.default.addObserver(forName: NSApplication.didBecomeActiveNotification,
                                               object: nil, queue: .main) { [weak self] _ in
            Task { @MainActor in self?.clearDockBadge() }
        }
    }

    func setEnabled(_ value: Bool) {
        enabled = value
        guard operational else { return }
        if value {
            UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { _, _ in }
        } else {
            unreadIDs.removeAll()
            clearDockBadge()
        }
    }

    func streamEvent(sessionID: UUID, title: String, line: String, detailVisible: Bool) {
        guard operational else { return }
        guard let kind = ConsoleAgentAttentionParser.parse(eventLine: line) else { return }
        if detailVisible {
            unreadIDs.remove(sessionID)
        } else {
            unreadIDs.insert(sessionID)
            deliverIfNeeded(title: title, kind: kind)
        }
        updateDockBadge()
    }

    func detailBecameVisible(_ id: UUID) {
        guard operational else { return }
        unreadIDs.remove(id)
        updateDockBadge()
    }

    private func deliverIfNeeded(title: String, kind: ConsoleAgentAttentionKind) {
        guard enabled, !NSApp.isActive else { return }
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = ConsoleAgentAttentionParser.notificationBody(for: kind)
        let request = UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil)
        UNUserNotificationCenter.current().add(request)
    }

    private func updateDockBadge() {
        guard enabled else { return }
        if unreadIDs.isEmpty { clearDockBadge() }
        else { NSApp.dockTile.badgeLabel = unreadIDs.count > 9 ? "9+" : String(unreadIDs.count) }
    }

    private func clearDockBadge() {
        NSApp.dockTile.badgeLabel = nil
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter,
                              willPresent notification: UNNotification) async -> UNNotificationPresentationOptions {
        enabled ? [.banner, .sound] : []
    }
}
