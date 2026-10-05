import Foundation

@main enum ClientPreferencesTests {
    static func main() throws {
        var checks = 0
        func expect(_ condition: @autoclosure () -> Bool, _ message: String) {
            guard condition() else { fatalError(message) }; checks += 1
        }
        func decode(_ text: String) throws -> ClientPreferencesConfiguration {
            try ClientConfigurationIO.decode(ClientPreferencesConfiguration.self, data: Data(text.utf8))
        }
        let empty = try decode(#"{"schemaVersion":1}"#)
        expect(empty == ClientPreferencesConfiguration(), "empty document defaults")
        let all = try decode(#"{"schemaVersion":1,"reduceMotion":true,"shellIntegration":true,"sidebarWidth":220,"defaultAgent":"pi"}"#)
        expect(all.reduceMotion == true && all.shellIntegration == true && all.sidebarWidth == 220 && all.defaultAgent == "pi", "preferences lost")
        for agent in ["codex", "claude", "opencode", "deepseek", "pi"] {
            let value = try decode("{\"schemaVersion\":1,\"defaultAgent\":\"\(agent)\"}")
            expect(value.defaultAgent == agent, "valid provider rejected")
        }
        for width in [140, 172, 220, 320] {
            let value = try decode("{\"schemaVersion\":1,\"sidebarWidth\":\(width)}")
            expect(value.sidebarWidth == Double(width), "valid width rejected")
        }
        let optional = try decode(#"{"schemaVersion":1,"reduceMotion":null,"shellIntegration":false,"sidebarWidth":null,"defaultAgent":null}"#)
        expect(optional.reduceMotion == nil && optional.shellIntegration == false && optional.sidebarWidth == nil && optional.defaultAgent == nil, "null optional must mean unset")
        for text in [#"{}"#, #"{"schemaVersion":2}"#, #"{"schemaVersion":true}"#,
                     #"{"schemaVersion":1,"reduceMotion":1}"#, #"{"schemaVersion":1,"reduceMotion":"true"}"#,
                     #"{"schemaVersion":1,"shellIntegration":0}"#, #"{"schemaVersion":1,"shellIntegration":{}}"#,
                     #"{"schemaVersion":1,"sidebarWidth":139}"#, #"{"schemaVersion":1,"sidebarWidth":321}"#,
                     #"{"schemaVersion":1,"sidebarWidth":true}"#, #"{"schemaVersion":1,"sidebarWidth":"220"}"#,
                     #"{"schemaVersion":1,"defaultAgent":"custom-shell"}"#, #"{"schemaVersion":1,"defaultAgent":[]}"#,
                     #"{"schemaVersion":1,"reduceMotoin":true}"#] {
            do { _ = try decode(text); fatalError("invalid settings accepted") } catch { checks += 1 }
        }
        print("ClientPreferencesTests: \(checks) checks passed")
    }
}
