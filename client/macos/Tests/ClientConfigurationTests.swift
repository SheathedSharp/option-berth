import Foundation

@main struct ClientConfigurationTests {
    static func main() throws {
        var checks = 0
        func expect(_ condition: @autoclosure () -> Bool, _ message: String) {
            guard condition() else { fatalError(message) }; checks += 1
        }
        func theme(_ text: String) throws -> ThemeConfiguration {
            try ClientConfigurationIO.decode(ThemeConfiguration.self, data: Data(text.utf8))
        }
        func rejects(_ text: String) {
            do { _ = try theme(text); fatalError("accepted invalid theme") } catch { checks += 1 }
        }
        let defaults = try theme(#"{"schemaVersion":1}"#)
        expect(defaults.color("canvas") == 0xF8F4EE, "paper default")
        let custom = try theme(##"{"schemaVersion":1,"colors":{"accent":"#aBcD12"},"dataScale":1.25}"##)
        expect(custom.color("accent") == 0xABCD12, "override")
        expect(custom.color("ink") == defaults.color("ink"), "partial override")
        for invalid in [#"{}"#, #"[]"#, #"{"schemaVersion":2}"#, #"{"schemaVersion":true}"#,
                        #"{"schemaVersion":1,"appearance":"auto"}"#, #"{"schemaVersion":1,"colours":{}}"#,
                        ##"{"schemaVersion":1,"colors":{"accent":"ABCD12"}}"##,
                        ##"{"schemaVersion":1,"colors":{"accent":"#A#BCD12"}}"##,
                        ##"{"schemaVersion":1,"colors":{"unknown":"#ABCDEF"}}"##,
                        #"{"schemaVersion":1,"dataScale":0.2}"#, #"{"schemaVersion":1,"dataScale":true}"#,
                        #"{"schemaVersion":1,"interfaceFont":""}"#] { rejects(invalid) }
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("oberth-config-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let url = root.appendingPathComponent("theme.json")
        let file = ClientConfigurationFile<ThemeConfiguration>(url: url)
        expect(!file.reload() && !file.exists, "missing file defaults")
        try Data(##"{"schemaVersion":1,"colors":{"accent":"#123456"}}"##.utf8).write(to: url, options: .atomic)
        expect(file.reload() && file.value.color("accent") == 0x123456, "atomic file creation")
        try Data("{".utf8).write(to: url, options: .atomic)
        expect(file.reload() && file.problem != nil && file.value.color("accent") == 0x123456, "last good value")
        try Data(#"{"schemaVersion":1,"appearance":"dark"}"#.utf8).write(to: url, options: .atomic)
        expect(file.reload() && file.problem == nil && file.value.appearance == "dark", "recover after atomic replacement")
        try FileManager.default.removeItem(at: url)
        expect(file.reload() && !file.exists && file.value == defaults, "deletion restores defaults")
        try FileManager.default.createSymbolicLink(at: url, withDestinationURL: root)
        expect(file.reload() && file.problem != nil, "reject symlink")
        try FileManager.default.removeItem(at: url)
        try Data(repeating: 32, count: ClientConfigurationIO.limit + 1).write(to: url)
        file.reload()
        expect(file.problem != nil && file.value == defaults, "reject oversized file")
        expect(ClientConfigurationIO.directory(environment: ["BERTH_HOME":root.path], home: root).path == root.appendingPathComponent("config").path, "isolated config root")
        print("ClientConfigurationTests: \(checks) checks passed")
    }
}
