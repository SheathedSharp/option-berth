import Foundation
import BerthTerminal

extension WorkspaceChecks {
    static func layoutChecks() throws {
        let a = UUID(), b = UUID(), c = UUID()
        var tree = PaneLayout(session: a)
        try tree.split(b, beside: a, axis: .horizontal)
        try tree.split(c, beside: b, axis: .vertical)
        require(Set(tree.sessions) == Set([a,b,c]) && tree.nodes.count == 5, "recursive split failed")
        let before = tree
        do { try tree.split(c, beside: a, axis: .horizontal); fatalError("duplicate pane accepted") } catch {}
        require(tree == before, "failed split mutated tree")
        try tree.move(a, beside: c, axis: .vertical)
        try tree.validate(allowed: Set([a,b,c]))
        tree.remove(c); tree.remove(b)
        require(tree.sessions == [a] && tree.nodes.count == 1, "prune retained orphan nodes")
        let roundtrip = try JSONDecoder().decode(PaneLayout.self, from: JSONEncoder().encode(tree))
        require(tree == roundtrip, "layout restoration changed identity")
        for _ in 1..<16 { try tree.split(UUID(), beside: a, axis: .horizontal) }
        require(tree.sessions.count == 16 && tree.nodes.count == 31, "pane bound failed")
        do { try tree.split(UUID(), beside: a, axis: .vertical); fatalError("oversized tree accepted") } catch {}
        var invalid = try JSONSerialization.jsonObject(with: JSONEncoder().encode(before)) as! [String: Any]
        invalid["root"] = UUID().uuidString
        do { _ = try JSONDecoder().decode(PaneLayout.self, from: JSONSerialization.data(withJSONObject: invalid)); fatalError("dangling root accepted") } catch {}
        invalid = try JSONSerialization.jsonObject(with: JSONEncoder().encode(before)) as! [String: Any]
        var nodes = invalid["nodes"] as! [[String:Any]]
        nodes[0]["first"] = nodes[0]["id"]; invalid["nodes"] = nodes
        do { _ = try JSONDecoder().decode(PaneLayout.self, from: JSONSerialization.data(withJSONObject: invalid)); fatalError("cycle accepted") } catch {}
        print("PASS: recursive split/move/prune, 16-pane bound, atomic refusal, flat Codable cycle validation")
    }
}
