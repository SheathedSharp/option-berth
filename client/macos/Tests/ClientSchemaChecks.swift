import Foundation

/// Companion to the reference JSON Schema validator. All inputs are synthetic;
/// no application preferences or user files are read by this executable.
@main enum ClientSchemaChecks {
    struct Probe: Decodable { let kind: String; let raw: String }
    static func main() throws {
        let data = FileHandle.standardInput.readDataToEndOfFile()
        guard data.count <= 4 * 1024 * 1024 else { throw ClientConfigurationError.invalid("schema test corpus too large") }
        let probes = try JSONDecoder().decode([Probe].self, from: data)
        let accepted = probes.map { probe -> Bool in
            do {
                let raw = Data(probe.raw.utf8)
                switch probe.kind {
                case "theme": _ = try ClientConfigurationIO.decode(ThemeConfiguration.self, data: raw)
                case "settings": _ = try ClientConfigurationIO.decode(ClientPreferencesConfiguration.self, data: raw)
                case "keybindings": _ = try ClientConfigurationIO.decode(ClientKeybindingsConfiguration.self, data: raw)
                default: return false
                }
                return true
            } catch { return false }
        }
        let keys: [String: [String]] = [
            "theme": ThemeConfiguration.keys.union(["$schema"]).sorted(),
            "settings": ClientPreferencesConfiguration.keys.union(["$schema"]).sorted(),
            "keybindings": ClientKeybindingsConfiguration.keys.union(["$schema"]).sorted()
        ]
        let result: [String: Any] = ["accepted": accepted, "keys": keys, "colors": ThemeConfiguration.paper.keys.sorted()]
        let json = try JSONSerialization.data(withJSONObject: result, options: [.sortedKeys])
        FileHandle.standardOutput.write(json)
    }
}
