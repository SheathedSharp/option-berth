import Foundation

/// The small, user-owned configuration shared by the settings page and the
/// optional `jev-attention` runner. It intentionally lives outside the daemon
/// RPC and outside `oberth.yaml`: a Jev key is a local integration secret, not
/// project state.
struct JevSettings: Codable, Equatable {
    static let schema = "oberth.jev-config/v1"
    static let defaultEndpoint = "https://openrouter.ai/api/alpha/decisions"
    static let defaultModel = "typesafe/jev-1.13"

    var schema: String = JevSettings.schema
    var enabled: Bool = false
    var provider: String = "openrouter"
    var endpoint: String = JevSettings.defaultEndpoint
    var model: String = JevSettings.defaultModel
    var apiKey: String = ""
    var timeoutMilliseconds: Int = 10_000

    static let defaults = JevSettings()

    /// A deterministic state for ImageRenderer. It never reads a user's key.
    static let preview = JevSettings(enabled: true, apiKey: "", timeoutMilliseconds: 10_000)

    init(schema: String = JevSettings.schema,
         enabled: Bool = false,
         provider: String = "openrouter",
         endpoint: String = JevSettings.defaultEndpoint,
         model: String = JevSettings.defaultModel,
         apiKey: String = "",
         timeoutMilliseconds: Int = 10_000) {
        self.schema = schema
        self.enabled = enabled
        self.provider = provider
        self.endpoint = endpoint
        self.model = model
        self.apiKey = apiKey
        self.timeoutMilliseconds = timeoutMilliseconds
    }

    var hasKey: Bool {
        !apiKey.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    var statusLabel: String {
        if !enabled { return "未启用" }
        return hasKey ? "已启用 · key 已保存" : "已启用 · 还没有 key"
    }

    var maskedKey: String {
        guard hasKey else { return "尚未配置" }
        let value = apiKey.trimmingCharacters(in: .whitespacesAndNewlines)
        if value.count <= 8 { return "••••••••" }
        return "\(value.prefix(4))••••\(value.suffix(4))"
    }

    private enum CodingKeys: String, CodingKey {
        case schema, enabled, provider, endpoint, model
        case apiKey = "api_key"
        case timeoutMilliseconds = "timeout_ms"
    }
}

enum JevSettingsStore {
    static let fileName = "jev.json"
    static let legacyFileName = "decide.key"

    static var directoryURL: URL {
        let environment = ProcessInfo.processInfo.environment
        if let override = environment["BERTH_HOME"], !override.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return URL(fileURLWithPath: override, isDirectory: true)
        }
        return URL(fileURLWithPath: NSHomeDirectory(), isDirectory: true)
            .appendingPathComponent(".option-berth", isDirectory: true)
    }

    static var fileURL: URL {
        directoryURL.appendingPathComponent(fileName, isDirectory: false)
    }

    static func load() -> JevSettings {
        let manager = FileManager.default
        let localExists = manager.fileExists(atPath: fileURL.path)
        if let attributes = try? manager.attributesOfItem(atPath: fileURL.path),
           let permissions = attributes[.posixPermissions] as? NSNumber,
           permissions.intValue & 0o077 != 0 {
            return .defaults
        }
        if let data = try? Data(contentsOf: fileURL),
           let value = try? JSONDecoder().decode(JevSettings.self, from: data) {
            return normalize(value)
        }
        if localExists {
            return .defaults
        }
        // Migrate the pre-settings key in memory only. The user still has to
        // press 保存配置 before it becomes the versioned jev.json file.
        let legacyURL = directoryURL.appendingPathComponent(legacyFileName, isDirectory: false)
        if let attributes = try? FileManager.default.attributesOfItem(atPath: legacyURL.path),
           let permissions = attributes[.posixPermissions] as? NSNumber,
           permissions.intValue & 0o077 == 0,
           let data = try? Data(contentsOf: legacyURL),
           let key = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines),
           !key.isEmpty {
            return JevSettings(enabled: true, apiKey: key)
        }
        return .defaults
    }

    private static func normalize(_ input: JevSettings) -> JevSettings {
        var value = input
        // Keep the UI tolerant of a future writer while normalizing the values
        // that the current Go reader accepts.
        value.schema = JevSettings.schema
        value.provider = value.provider.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        if value.provider.isEmpty { value.provider = "openrouter" }
        value.apiKey = value.apiKey.trimmingCharacters(in: .whitespacesAndNewlines)
        if value.endpoint.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            value.endpoint = JevSettings.defaultEndpoint
        }
        if value.model.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            value.model = JevSettings.defaultModel
        }
        if value.timeoutMilliseconds <= 0 { value.timeoutMilliseconds = 10_000 }
        return value
    }

    /// Save atomically with owner-only permissions. The directory itself is
    /// tightened as well, so a newly-created home cannot expose the key.
    static func save(_ input: JevSettings) throws {
        var value = input
        value.schema = JevSettings.schema
        value.provider = value.provider.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        if value.provider.isEmpty { value.provider = "openrouter" }
        value.endpoint = value.endpoint.trimmingCharacters(in: .whitespacesAndNewlines)
        value.model = value.model.trimmingCharacters(in: .whitespacesAndNewlines)
        value.apiKey = value.apiKey.trimmingCharacters(in: .whitespacesAndNewlines)
        if value.endpoint.isEmpty { value.endpoint = JevSettings.defaultEndpoint }
        if value.model.isEmpty { value.model = JevSettings.defaultModel }
        if value.timeoutMilliseconds <= 0 { value.timeoutMilliseconds = 10_000 }

        let manager = FileManager.default
        try manager.createDirectory(at: directoryURL, withIntermediateDirectories: true)
        try manager.setAttributes([.posixPermissions: 0o700], ofItemAtPath: directoryURL.path)

        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        let data = try encoder.encode(value)
        let temporary = directoryURL.appendingPathComponent(".jev-\(UUID().uuidString).tmp")
        defer { try? manager.removeItem(at: temporary) }
        try data.write(to: temporary, options: .withoutOverwriting)
        try manager.setAttributes([.posixPermissions: 0o600], ofItemAtPath: temporary.path)

        if manager.fileExists(atPath: fileURL.path) {
            _ = try manager.replaceItemAt(fileURL, withItemAt: temporary)
        } else {
            try manager.moveItem(at: temporary, to: fileURL)
        }
        try manager.setAttributes([.posixPermissions: 0o600], ofItemAtPath: fileURL.path)
    }
}
