import Foundation

@main enum ConfigurationEditingTests {
    static func main() throws {
        var checks = 0
        func expect(_ condition: @autoclosure () -> Bool, _ message: String) { precondition(condition(), message); checks += 1 }
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("oberth-edit-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        for document in ConfigurationDocument.allCases {
            let file = ConfigurationEditorFile(document: document, directory: root)
            let source = try file.load()
            expect(source.contains("//"), "missing inline help")
            expect(!FileManager.default.fileExists(atPath: file.url.path), "opening created a file")
            try document.validate(Data(source.utf8))
            try file.save(source)
            let saved = try Data(contentsOf: file.url)
            expect(saved == Data(source.utf8), "comments were rewritten")
            do { try file.save("{"); preconditionFailure("invalid edit saved") } catch { checks += 1 }
            let afterInvalid = try Data(contentsOf: file.url)
            expect(afterInvalid == saved, "invalid edit changed disk")
            let external = Data("{\"schemaVersion\":1}\n".utf8)
            try external.write(to: file.url, options: .atomic)
            do { try file.save(source + " "); preconditionFailure("external edit overwritten") }
            catch ConfigurationEditingError.conflict { checks += 1 }
            let afterConflict = try Data(contentsOf: file.url)
            expect(afterConflict == external, "conflict changed disk")
            _ = try file.load(); try file.save(source)
            try FileManager.default.removeItem(at: file.url)
            do { try file.save(source); preconditionFailure("external deletion undone") }
            catch ConfigurationEditingError.conflict { checks += 1 }
        }
        let quoted = Data(##"{"schemaVersion":1, /* note */ "$schema":"https://example.invalid/a//b", "interfaceFont":"A /* literal */ Font", "colors":{"accent":"#123456",},}"##.utf8)
        let value = try ClientConfigurationIO.decode(ThemeConfiguration.self, data: quoted)
        expect(value.interfaceFont == "A /* literal */ Font", "parser stripped literal comment characters")
        for text in ["{ /* unterminated", "{\"schemaVersion\":1,\"unknown\":1}", "{\"schemaVersion\":1,\"dataScale\":NaN}"] {
            do { try ConfigurationDocument.theme.validate(Data(text.utf8)); preconditionFailure("invalid configuration accepted") } catch { checks += 1 }
        }
        do { try ConfigurationDocument.keybindings.validate(Data(#"{"schemaVersion":1,"bindings":{"find":{"key":"c"}}}"#.utf8)); preconditionFailure("native shortcut overwritten") } catch { checks += 1 }
        let oversized = Data(repeating: 32, count: ClientConfigurationIO.limit + 1)
        do { try ConfigurationDocument.theme.validate(oversized); preconditionFailure("size cap removed") } catch { checks += 1 }
        print("ConfigurationEditingTests: \(checks) checks passed")
    }
}
