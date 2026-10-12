import Foundation

// 历史图车道。算法来自 Xit 的 CommitHistory.generateConnections / generateLines
// （https://github.com/Uncommon/Xit ，Copyright 2013 David Catmull，Apache-2.0，
// 许可与改动说明见 client/macos/ThirdParty）。
//
// 只消费 `oberth git graph` 已经返回的提交和完整 parent hash，顺序必须是
// newest-first。这里不读取 Git，也不把日志里相邻的两行当成一条边。
// 没有任何连线碰到的提交仍在下一空列放点；Xit 会让这个点空着，窗口里的提交就会看不见。

struct GitGraphNode: Equatable, Sendable {
    var hash: String
    var parents: [String]
}

struct GitGraphLayout: Equatable, Sendable {
    struct Line: Equatable, Sendable {
        var childIndex: Int?
        var parentIndex: Int?
        var colorIndex: Int
    }

    struct Row: Equatable, Sendable {
        var lines: [Line]
        /// Column of this commit's node. Color follows the connection, not the column:
        /// a later branch may reuse the column and must not inherit the color.
        var dotColumn: Int
        var dotColor: Int

        var parentDepartures: Int { lines.filter { $0.childIndex == nil }.count }
    }

    var rows: [Row]

    var columnCount: Int {
        rows.reduce(0) { partial, row in
            var count = max(partial, row.dotColumn + 1)
            for line in row.lines {
                if let child = line.childIndex { count = max(count, child + 1) }
                if let parent = line.parentIndex { count = max(count, parent + 1) }
            }
            return count
        }
    }

    static func layout(_ commits: [GitGraphNode]) -> GitGraphLayout {
        var connections: [Connection] = []
        var nextColor = 0
        var perRow: [[Connection]] = []
        perRow.reserveCapacity(commits.count)

        for commit in commits {
            let incoming = connections.firstIndex { $0.parent == commit.hash }
            let incomingColor = incoming.map { connections[$0].colorIndex }
            if let firstParent = commit.parents.first {
                let color: Int
                if let incomingColor {
                    color = incomingColor
                } else {
                    color = nextColor
                    nextColor += 1
                }
                let insertAt = incoming.map { $0 + 1 } ?? connections.endIndex
                connections.insert(Connection(parent: firstParent, child: commit.hash, colorIndex: color), at: insertAt)
                for parent in commit.parents.dropFirst() {
                    connections.append(Connection(parent: parent, child: commit.hash, colorIndex: nextColor))
                    nextColor += 1
                }
            }
            perRow.append(connections)
            let hash = commit.hash
            connections.removeAll { $0.parent == hash }
        }

        var rows: [Row] = []
        rows.reserveCapacity(commits.count)
        for (commit, connections) in zip(commits, perRow) {
            rows.append(lines(for: commit.hash, connections: connections))
        }
        return GitGraphLayout(rows: rows)
    }

    private struct Connection {
        var parent: String
        var child: String
        var colorIndex: Int
    }

    private static func lines(for hash: String, connections: [Connection]) -> Row {
        var nextChild = 0
        let outlets = unique(connections.compactMap { $0.parent == hash ? nil : $0.parent })
        var parentLines: [String: (child: Int, color: Int)] = [:]
        var generated: [Line] = []
        generated.reserveCapacity(connections.count)
        var dotColumn: Int?
        var dotColor: Int?

        for connection in connections {
            let commitIsParent = connection.parent == hash
            let commitIsChild = connection.child == hash
            let parentIndex = commitIsParent ? nil : outlets.firstIndex(of: connection.parent)
            var childIndex: Int? = commitIsChild ? nil : nextChild
            var colorIndex = connection.colorIndex
            if dotColumn == nil && (commitIsParent || commitIsChild) {
                dotColumn = nextChild
                dotColor = colorIndex
            }
            if let existing = parentLines[connection.parent] {
                if !commitIsChild {
                    childIndex = existing.child
                    colorIndex = existing.color
                } else if !commitIsParent {
                    nextChild += 1
                }
            } else {
                if !commitIsChild {
                    parentLines[connection.parent] = (nextChild, colorIndex)
                }
                if !commitIsParent {
                    nextChild += 1
                }
            }
            generated.append(Line(childIndex: childIndex, parentIndex: parentIndex, colorIndex: colorIndex))
        }

        if dotColumn == nil {
            let fallbackColor = (generated.map(\.colorIndex).max() ?? -1) + 1
            dotColumn = nextChild
            dotColor = fallbackColor
        }
        return Row(lines: generated, dotColumn: dotColumn ?? 0, dotColor: dotColor ?? 0)
    }

    private static func unique(_ values: [String]) -> [String] {
        var seen = Set<String>()
        return values.filter { seen.insert($0).inserted }
    }
}
