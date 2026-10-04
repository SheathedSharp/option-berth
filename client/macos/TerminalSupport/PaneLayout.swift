import Foundation

/// A small flat tree avoids recursively decoding untrusted restoration data.
/// Session IDs are references to the one PTY registry, never process identities.
public struct PaneLayout: Codable, Equatable {
    public enum Axis: String, Codable { case horizontal, vertical }
    public struct Node: Codable, Equatable, Identifiable {
        public let id: UUID
        public var session: UUID?
        public var axis: Axis?
        public var first: UUID?
        public var second: UUID?
        public var fraction: Double
        init(session: UUID) { id = UUID(); self.session = session; fraction = 0.5 }
        init(id: UUID, axis: Axis, first: UUID, second: UUID) {
            self.id = id; self.axis = axis; self.first = first; self.second = second; fraction = 0.5
        }
    }
    public private(set) var root: UUID?
    public private(set) var nodes: [Node] = []
    public private(set) var focused: UUID?
    public static let maximumPanes = 16
    public init() {}
    public init(session: UUID) { let node = Node(session: session); nodes = [node]; root = node.id; focused = session }
    public var sessions: [UUID] { nodes.compactMap(\.session) }
    public func node(_ id: UUID) -> Node? { nodes.first { $0.id == id } }

    public func validate(allowed: Set<UUID>? = nil) throws {
        guard nodes.count <= Self.maximumPanes * 2 - 1, Set(nodes.map(\.id)).count == nodes.count else { throw LayoutFailure.invalid }
        if nodes.isEmpty { guard root == nil && focused == nil else { throw LayoutFailure.invalid }; return }
        guard let root, let focused, sessions.contains(focused), Set(sessions).count == sessions.count,
              allowed.map({ Set(sessions).isSubset(of: $0) }) ?? true else { throw LayoutFailure.invalid }
        let index = Dictionary(uniqueKeysWithValues: nodes.map { ($0.id, $0) })
        var visited = Set<UUID>()
        func visit(_ id: UUID, depth: Int) throws {
            guard depth <= Self.maximumPanes, visited.insert(id).inserted, let node = index[id],
                  node.fraction.isFinite, (0.1...0.9).contains(node.fraction) else { throw LayoutFailure.invalid }
            if node.session != nil {
                guard node.axis == nil && node.first == nil && node.second == nil else { throw LayoutFailure.invalid }
            } else {
                guard node.axis != nil, let first = node.first, let second = node.second else { throw LayoutFailure.invalid }
                try visit(first, depth: depth + 1); try visit(second, depth: depth + 1)
            }
        }
        try visit(root, depth: 1)
        guard visited.count == nodes.count else { throw LayoutFailure.invalid }
    }
    public mutating func focus(_ session: UUID) { if sessions.contains(session) { focused = session } }
    public mutating func show(_ session: UUID) {
        if sessions.contains(session) { focus(session); return }
        guard let focused, let index = nodes.firstIndex(where: { $0.session == focused }) else { self = PaneLayout(session: session); return }
        nodes[index].session = session; self.focused = session
    }
    public mutating func split(_ session: UUID, beside target: UUID, axis: Axis) throws {
        guard sessions.count < Self.maximumPanes, !sessions.contains(session), let i = nodes.firstIndex(where: { $0.session == target }) else { throw LayoutFailure.invalid }
        var next = self
        let a = Node(session: target), b = Node(session: session)
        next.nodes[i] = Node(id: nodes[i].id, axis: axis, first: a.id, second: b.id)
        next.nodes += [a, b]; next.focused = session
        try next.validate(); self = next
    }
    public mutating func remove(_ session: UUID) {
        guard let leaf = nodes.first(where: { $0.session == session }) else { return }
        if root == leaf.id { self = PaneLayout(); return }
        guard let parent = nodes.first(where: { $0.first == leaf.id || $0.second == leaf.id }),
              let sibling = parent.first == leaf.id ? parent.second : parent.first else { return }
        if root == parent.id { root = sibling }
        else if let i = nodes.firstIndex(where: { $0.first == parent.id || $0.second == parent.id }) {
            if nodes[i].first == parent.id { nodes[i].first = sibling } else { nodes[i].second = sibling }
        }
        nodes.removeAll { $0.id == leaf.id || $0.id == parent.id }
        if focused == session { focused = sessions.first }
    }
    public mutating func move(_ session: UUID, beside target: UUID, axis: Axis) throws {
        guard session != target, sessions.contains(session), sessions.contains(target) else { throw LayoutFailure.invalid }
        var next = self
        next.remove(session); try next.split(session, beside: target, axis: axis); self = next
    }
    public mutating func setFraction(_ value: Double, at id: UUID) throws {
        guard value.isFinite, let i = nodes.firstIndex(where: { $0.id == id && $0.axis != nil }) else { throw LayoutFailure.invalid }
        nodes[i].fraction = min(0.9, max(0.1, value))
    }
    private enum CodingKeys: CodingKey { case root, nodes, focused }
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        root = try c.decodeIfPresent(UUID.self, forKey: .root)
        nodes = try c.decode([Node].self, forKey: .nodes)
        focused = try c.decodeIfPresent(UUID.self, forKey: .focused)
        try validate()
    }
}
public enum LayoutFailure: LocalizedError {
    case invalid
    public var errorDescription: String? { "窗格布局无效、重复或超出上限 / Invalid, duplicate or oversized pane layout" }
}
