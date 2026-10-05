import AppKit
import Foundation
@testable import BerthClient

extension CommandFixture {
    func gitFindChecks(window: NSWindow, configuration: URL) async throws {
        guard let root = window.contentView else { throw Failure(message: "workspace content missing") }
        try await eventually("combined Git search was not rendered") {
            descendants(root).contains { ($0 as? NSTextField)?.placeholderString == "筛选文件或原路径" }
        }
        let field = descendants(root).compactMap { $0 as? NSTextField }.first { $0.placeholderString == "筛选文件或原路径" }!
        try Data(#"{"schemaVersion":1,"bindings":{"find":{"key":"k","option":true}}}"#.utf8).write(to: configuration, options: .atomic)
        try await eventually("custom Git Find equivalent was not installed") {
            shortcuts.shortcut(.find).key == "k" && items(NSApp.mainMenu).contains {
                $0.title == WorkspaceAction.find.title && $0.keyEquivalent == "k"
            }
        }
        window.makeFirstResponder(nil)
        try await rejected("f", "a hard-coded Git Find key survived customization")
        try expect(field.currentEditor() == nil, "retired Find key moved native search focus")
        let count = performed.count
        key("k", modifiers: [.command, .option])
        try await eventually("custom Find did not reach this worktree's native search") {
            field.currentEditor() != nil && performed.count == count + 1 && performed.last == .find
        }
        guard let editor = field.currentEditor() as? NSTextView else { throw Failure(message: "Git native field editor missing") }
        editor.setMarkedText("中文", selectedRange: NSRange(location: 2, length: 0), replacementRange: NSRange(location: NSNotFound, length: 0))
        try await rejected("1", "combined Git search composition changed worktree")
        editor.unmarkText()
        window.makeFirstResponder(nil)
        try FileManager.default.removeItem(at: configuration)
        try await eventually("default Find equivalent did not return after deletion") {
            shortcuts.shortcut(.find).key == "f" && items(NSApp.mainMenu).contains {
                $0.title == WorkspaceAction.find.title && $0.keyEquivalent == "f"
            }
        }
        key("f")
        try await eventually("restored Find did not focus the real Git search field") { field.currentEditor() != nil }
    }
}
