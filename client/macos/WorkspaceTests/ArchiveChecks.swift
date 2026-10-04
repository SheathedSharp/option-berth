import Foundation
import Darwin
import BerthTerminal

extension WorkspaceChecks {
    static func archiveChecks() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("archive-check-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
        defer { try? FileManager.default.removeItem(at: root) }
        let directory = root.appendingPathComponent("private")
        let store = WorkspaceArchiveStore(directory: directory)
        require(tryRead(store) == nil, "absent archive not empty")
        let id = UUID()
        let workspace = SavedWorkspace(root: root.path, agentMode: false, providerID: "codex", nativeExpanded: false,
                                       terminalLayout: PaneLayout(session: id), agentLayout: PaneLayout())
        let record = SavedSession(id: id, worktree: root.path, title: "remembered shell", kind: "terminal")
        let archive = WorkspaceArchive(workspaces: [workspace], sessions: [record], savedAt: Date(timeIntervalSince1970: 1000))
        try store.write(archive)
        let loaded = try store.read()
        require(loaded == archive, "private snapshot did not roundtrip")
        let file = directory.appendingPathComponent("workspace-v1.json")
        let attrs = try FileManager.default.attributesOfItem(atPath: file.path)
        require((attrs[.posixPermissions] as? NSNumber)?.intValue == 0o600, "archive not private")
        let original = try Data(contentsOf: file)
        let invalid = WorkspaceArchive(workspaces: [workspace], sessions: [record, record])
        do { try store.write(invalid); fatalError("duplicate session IDs accepted") } catch {}
        let after = try Data(contentsOf: file)
        require(after == original, "refused write damaged previous snapshot")
        var doc = try JSONSerialization.jsonObject(with: original) as! [String: Any]
        doc["schemaVersion"] = 99
        try JSONSerialization.data(withJSONObject: doc).write(to: file)
        do { _ = try store.read(); fatalError("future schema accepted") } catch {}
        try store.write(archive)
        try FileManager.default.setAttributes([.posixPermissions: 0o644], ofItemAtPath: file.path)
        do { _ = try store.read(); fatalError("world-readable snapshot accepted") } catch {}
        try FileManager.default.removeItem(at: file)
        let outside = root.appendingPathComponent("outside")
        try original.write(to: outside)
        try FileManager.default.createSymbolicLink(at: file, withDestinationURL: outside)
        do { _ = try store.read(); fatalError("symlink snapshot followed") } catch {}
        try store.remove()
        require(FileManager.default.fileExists(atPath: outside.path), "delete followed a symlink")
        let oversized = Data(repeating: 32, count: WorkspaceArchiveStore.maximumBytes + 1)
        FileManager.default.createFile(atPath: file.path, contents: oversized, attributes: [.posixPermissions: 0o600])
        do { _ = try store.read(); fatalError("unbounded snapshot read") } catch {}
        try store.remove()
        require(tryRead(store) == nil, "snapshot not removed")
        print("PASS: private atomic metadata snapshot, schema/size/duplicate rejection, safe symlink handling and explicit deletion")
    }
    private static func tryRead(_ store: WorkspaceArchiveStore) -> WorkspaceArchive? { try? store.read() }
}
