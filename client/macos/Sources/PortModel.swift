import Foundation

/// A listener reported by `state.snapshot`.
///
/// Ports are evidence attached to a worktree or run. The client keeps the
/// daemon's attribution fields and never invents a machine-wide category.
struct Port: Decodable, Identifiable, Sendable {
    let port: Int
    let bindAddress: String
    let pid: Int
    let process: String
    let displayName: String
    let url: String?
    let cwd: String?
    let cwdInTrash: Bool
    let cwdGone: Bool
    let projectRoot: String?
    let group: String?
    let command: String?

    var id: String { "\(port):\(bindAddress)" }

    var isOnLan: Bool {
        !bindAddress.hasPrefix("127.") && bindAddress != "::1" && bindAddress != "localhost"
    }

    var projectName: String? {
        if let group, !group.isEmpty { return group }
        guard let root = projectRoot, !root.isEmpty else { return nil }
        return URL(fileURLWithPath: root).lastPathComponent
    }

    private enum CodingKeys: String, CodingKey {
        case port, pid, process, url, cwd, group, command
        case bindAddress = "bind_address"
        case displayName = "display_name"
        case cwdInTrash = "cwd_in_trash"
        case cwdGone = "cwd_gone"
        case projectRoot = "project_root"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        port = (try? c.decode(Int.self, forKey: .port)) ?? 0
        pid = (try? c.decode(Int.self, forKey: .pid)) ?? 0
        bindAddress = Port.string(c, .bindAddress) ?? ""
        process = Port.string(c, .process) ?? ""
        displayName = Port.string(c, .displayName) ?? process
        url = Port.string(c, .url)
        cwd = Port.string(c, .cwd)
        cwdInTrash = (try? c.decode(Bool.self, forKey: .cwdInTrash)) ?? false
        cwdGone = (try? c.decode(Bool.self, forKey: .cwdGone)) ?? false
        projectRoot = Port.string(c, .projectRoot)
        group = Port.string(c, .group)
        command = Port.string(c, .command)
    }

    private static func string(_ c: KeyedDecodingContainer<CodingKeys>, _ key: CodingKeys) -> String? {
        try? c.decodeIfPresent(String.self, forKey: key)
    }
}
