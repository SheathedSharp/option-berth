import Foundation
#if canImport(Darwin)
import Darwin
#else
import Glibc
#endif

/// Personal configuration is data, never executable code or a second runtime model.
protocol ClientConfigurationDocument: Decodable, Equatable {
    init()
    static var keys: Set<String> { get }
    func validate() throws
}

enum ClientConfigurationError: LocalizedError {
    case invalid(String)
    var errorDescription: String? {
        switch self { case .invalid(let reason): return reason }
    }
}

struct ThemeConfiguration: ClientConfigurationDocument {
    var schemaVersion = 1
    var appearance: String?
    var colors: [String: String]?
    var interfaceFont: String?
    var dataFont: String?
    var interfaceScale: Double?
    var dataScale: Double?
    var logScale: Double?
    static let keys: Set<String> = ["schemaVersion", "appearance", "colors", "interfaceFont", "dataFont", "interfaceScale", "dataScale", "logScale"]
    static let paper: [String: UInt32] = [
        "canvas": 0xF8F4EE, "surface": 0xEFE7DC, "sunken": 0xE5DACD,
        "ink": 0x292521, "inkMuted": 0x6B6259, "inkFaint": 0x968A7D,
        "dormant": 0xC5B8A9, "line": 0xDED3C7, "lineStrong": 0xC9B9A8,
        "accent": 0xC56A4A, "accentSoft": 0xF4DED3, "live": 0x5E7D68,
        "diff.added": 0x1B7F4B, "diff.addedBackground": 0xE9F5ED,
        "diff.removed": 0xB3261E, "diff.removedBackground": 0xFDECEA,
        "diff.changed": 0x9A5B00, "diff.changedBackground": 0xFBF3E4,
        "terminal.background": 0xF8F4EE, "terminal.foreground": 0x292521
    ]
    static func hex(_ text: String) -> UInt32? {
        let bytes = Array(text.utf8)
        guard bytes.count == 7, bytes[0] == 35,
              bytes.dropFirst().allSatisfy({ (48...57).contains($0) || (65...70).contains($0) || (97...102).contains($0) }) else { return nil }
        return UInt32(text.dropFirst(), radix: 16)
    }
    func validate() throws {
        guard schemaVersion == 1 else { throw ClientConfigurationError.invalid("theme.schemaVersion must be 1") }
        if let appearance, !["light", "dark"].contains(appearance) { throw ClientConfigurationError.invalid("theme.appearance must be light or dark") }
        for (key, value) in colors ?? [:] {
            guard Self.paper[key] != nil, Self.hex(value) != nil else { throw ClientConfigurationError.invalid("theme.colors contains an unknown token or invalid #RRGGBB value") }
        }
        for font in [interfaceFont, dataFont].compactMap({ $0 }) {
            guard !font.isEmpty, font.utf8.count <= 128, !font.unicodeScalars.contains(where: CharacterSet.controlCharacters.contains) else { throw ClientConfigurationError.invalid("theme font must contain 1...128 bytes without control characters") }
        }
        for scale in [interfaceScale, dataScale, logScale].compactMap({ $0 }) {
            guard scale.isFinite, (0.75...2).contains(scale) else { throw ClientConfigurationError.invalid("theme scale must be between 0.75 and 2") }
        }
    }
    func color(_ token: String) -> UInt32? { colors?[token].flatMap(Self.hex) ?? Self.paper[token] }
}

struct ClientPreferencesConfiguration: ClientConfigurationDocument {
    var schemaVersion = 1
    var reduceMotion: Bool?
    var shellIntegration: Bool?
    var sidebarWidth: Double?
    var defaultAgent: String?
    static let keys: Set<String> = ["schemaVersion", "reduceMotion", "shellIntegration", "sidebarWidth", "defaultAgent"]
    func validate() throws {
        guard schemaVersion == 1 else { throw ClientConfigurationError.invalid("settings.schemaVersion must be 1") }
        if let sidebarWidth, !sidebarWidth.isFinite || !(140...320).contains(sidebarWidth) { throw ClientConfigurationError.invalid("settings.sidebarWidth must be between 140 and 320") }
        if let defaultAgent, !["codex", "claude", "opencode", "deepseek", "pi"].contains(defaultAgent) { throw ClientConfigurationError.invalid("settings.defaultAgent is not a supported provider ID") }
    }
}

struct ClientKeybinding: Decodable, Equatable, Hashable {
    var key: String
    var shift: Bool?
    var option: Bool?
    var control: Bool?
}
extension ClientKeybinding {
    private struct Key: CodingKey {
        let stringValue: String
        var intValue: Int? { nil }
        init(_ value: String) { stringValue = value }
        init?(stringValue: String) { self.init(stringValue) }
        init?(intValue: Int) { return nil }
    }
    init(from decoder: Decoder) throws {
        let fields = try decoder.container(keyedBy: Key.self)
        guard fields.allKeys.allSatisfy({ ["key", "shift", "option", "control"].contains($0.stringValue) }) else {
            throw ClientConfigurationError.invalid("keybindings contain an unknown modifier field")
        }
        key = try fields.decode(String.self, forKey: Key("key"))
        shift = try fields.decodeIfPresent(Bool.self, forKey: Key("shift"))
        option = try fields.decodeIfPresent(Bool.self, forKey: Key("option"))
        control = try fields.decodeIfPresent(Bool.self, forKey: Key("control"))
    }
}
struct ClientKeybindingsConfiguration: ClientConfigurationDocument {
    var schemaVersion = 1
    var bindings: [String: ClientKeybinding]?
    static let keys: Set<String> = ["schemaVersion", "bindings"]
    func validate() throws {
        guard schemaVersion == 1 else { throw ClientConfigurationError.invalid("keybindings.schemaVersion must be 1") }
        guard (bindings?.count ?? 0) <= 64 else { throw ClientConfigurationError.invalid("too many keybindings") }
        for (command, binding) in bindings ?? [:] {
            guard !command.isEmpty, command.utf8.count <= 80, binding.key.utf8.count == 1,
                  "abcdefghijklmnopqrstuvwxyz0123456789,./;[]=-".contains(binding.key) else { throw ClientConfigurationError.invalid("keybindings contain an invalid command ID or key") }
        }
        // Effective defaults, native reserved keys and command IDs are validated
        // by the command catalogue before the entire document is applied.
    }
}

enum ClientConfigurationIO {
    static let limit = 65_536
    static func directory(environment: [String: String] = ProcessInfo.processInfo.environment,
                          home: URL = FileManager.default.homeDirectoryForCurrentUser) -> URL {
        let berth = environment["BERTH_HOME"].flatMap { $0.isEmpty ? nil : $0 }
        return (berth.map { URL(fileURLWithPath: $0, isDirectory: true) }
            ?? home.appendingPathComponent(".option-berth", isDirectory: true)).appendingPathComponent("config", isDirectory: true)
    }
    static func decode<Value: ClientConfigurationDocument>(_ type: Value.Type, data: Data) throws -> Value {
        guard data.count <= limit else { throw ClientConfigurationError.invalid("configuration exceeds 64 KiB") }
        guard let object = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              Set(object.keys).isSubset(of: Value.keys) else { throw ClientConfigurationError.invalid("configuration must be an object with known keys") }
        let value = try JSONDecoder().decode(type, from: data)
        try value.validate()
        return value
    }
    static func read(_ url: URL) throws -> Data? {
        // O_NONBLOCK avoids hanging on a FIFO; fstat validates the opened object,
        // not an earlier pathname. The extra byte detects growth during reading.
        let fd = open(url.path, O_RDONLY | O_CLOEXEC | O_NOFOLLOW | O_NONBLOCK)
        guard fd >= 0 else {
            if errno == ENOENT { return nil }
            throw ClientConfigurationError.invalid("configuration could not be opened as a regular file")
        }
        let handle = FileHandle(fileDescriptor: fd, closeOnDealloc: true)
        defer { try? handle.close() }
        var info = stat()
        guard fstat(fd, &info) == 0, (info.st_mode & mode_t(S_IFMT)) == mode_t(S_IFREG),
              info.st_size <= limit else { throw ClientConfigurationError.invalid("configuration must be a regular file of at most 64 KiB") }
        let data = try handle.read(upToCount: limit + 1) ?? Data()
        guard data.count <= limit else { throw ClientConfigurationError.invalid("configuration exceeds 64 KiB") }
        return data
    }
}

/// Reloads are transactional per document. A failed edit leaves the last valid
/// value in memory. Deleting the file intentionally restores built-in defaults.
final class ClientConfigurationFile<Value: ClientConfigurationDocument> {
    let url: URL
    private(set) var value = Value()
    private(set) var problem: String?
    private(set) var exists = false
    init(url: URL) { self.url = url }
    @discardableResult func reload() -> Bool {
        let before = value; let oldProblem = problem; let oldExists = exists
        do {
            if let data = try ClientConfigurationIO.read(url) {
                exists = true
                value = try ClientConfigurationIO.decode(Value.self, data: data)
            } else { exists = false; value = Value() }
            problem = nil
        } catch {
            // Do not expose file contents or arbitrary decoder snippets in UI/logs.
            problem = "\(url.lastPathComponent): invalid or unreadable configuration; keeping the last valid value"
        }
        return before != value || oldProblem != problem || oldExists != exists
    }
}
