import Foundation

@main struct ClientConfigurationWatchTests {
    @MainActor static func main() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("oberth-config-watch-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let directory = root.appendingPathComponent("berth/config")
        let url = directory.appendingPathComponent("theme.json")
        var latest = ClientConfigurationSnapshot()
        var checks = 0
        let monitor = ClientConfigurationMonitor(directory: directory) {
            dispatchPrecondition(condition: .onQueue(.main))
            latest = $0
        }
        // Suspending yields the main executor to the production callback. A nested
        // synchronous Foundation run loop is not a main-queue drain contract.
        func eventually(_ message: String, _ check: () -> Bool) async throws {
            let deadline = Date().addingTimeInterval(5)
            while !check(), Date() < deadline { try await Task.sleep(nanoseconds: 5_000_000) }
            guard check() else { fatalError(message + "; problems: " + latest.problems.joined(separator: ", ")) }; checks += 1
        }
        func write(_ hex: String, atomic: Bool = true) throws {
            try Data("{\"schemaVersion\":1,\"colors\":{\"accent\":\"\(hex)\"}}".utf8).write(to: url, options: atomic ? .atomic : [])
        }
        var mainQueueDelivered = false
        DispatchQueue.main.async { mainQueueDelivered = true }
        try await eventually("test executor did not yield to the main queue") { mainQueueDelivered }
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        try write("#123456")
        try await eventually("initially absent directory was never discovered") { latest.theme.color("accent") == 0x123456 }
        try write("#ABCDEF", atomic: false)
        try await eventually("in-place save was not observed") { latest.theme.color("accent") == 0xABCDEF }
        try write("#456789")
        try await eventually("atomic replacement was not observed") { latest.theme.color("accent") == 0x456789 }
        try Data("{".utf8).write(to: url)
        try await eventually("invalid save discarded the last valid theme") { !latest.problems.isEmpty && latest.theme.color("accent") == 0x456789 }
        try write("#654321")
        try await eventually("valid repair did not clear errors") { latest.problems.isEmpty && latest.theme.color("accent") == 0x654321 }
        try FileManager.default.removeItem(at: directory)
        try await eventually("directory deletion did not restore defaults") { !latest.themeFilePresent && latest.theme == ThemeConfiguration() }
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        try write("#112233")
        try await eventually("directory recreation lost the watch") { latest.theme.color("accent") == 0x112233 }
        try FileManager.default.moveItem(at: root.appendingPathComponent("berth"), to: root.appendingPathComponent("retired"))
        try await eventually("parent rename retained stale config") { !latest.themeFilePresent }
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        try write("#223344")
        try await eventually("parent replacement lost the watch") { latest.theme.color("accent") == 0x223344 }
        withExtendedLifetime(monitor) {}
        print("ClientConfigurationWatchTests: \(checks) checks passed")
    }
}
